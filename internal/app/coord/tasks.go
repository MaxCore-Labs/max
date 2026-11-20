package coord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/domain"
	"max/internal/platform"
)

// Actor identifies who performed a task action ("owner", "agent", or "system").
type Actor struct {
	Kind string
	ID   domain.ID
}

// Deps bundles dependencies injected into coord.Service.
type Deps struct {
	Store     app.Store
	Actors    *actor.Service
	Clock     app.Clock
	IDs       app.IDGen
	CancelRun func(runID domain.ID)
	Publish   func(topic, typ string, data any)
}

// Service coordinates multi-agent tasks, dependencies, reviews, escalations, and watchdogs.
type Service struct {
	deps Deps
}

// NewService constructs a coordination Service.
func NewService(deps Deps) *Service {
	if deps.Clock == nil {
		deps.Clock = app.SystemClock{}
	}
	if deps.IDs == nil {
		deps.IDs = app.UUIDv7Gen{}
	}
	return &Service{deps: deps}
}

// NewTask describes parameters for creating a root or child task.
type NewTask struct {
	ID                         domain.ID
	Title, Description         string
	DoneCriteria               string
	GoalID, ParentID           *domain.ID
	Assignee                   *domain.ID
	BlockedBy                  []domain.ID
	NeedsReview                bool
	DueAt                      *time.Time
	Origin                     domain.RootOrigin
	CreatedByAgent, CauseRunID *domain.ID
	Hops                       int
}

// Create inserts a new task with optional blockers, validating depth and cycle rules, and wakes the assignee if unblocked.
func (s *Service) Create(ctx context.Context, n NewTask) (domain.ID, error) {
	title := strings.TrimSpace(n.Title)
	if title == "" {
		return "", errors.New("task title is required")
	}
	taskID := strings.TrimSpace(n.ID)
	if taskID == "" {
		taskID = s.deps.IDs.New()
	}
	nowMs := s.deps.Clock.Now().UnixMilli()

	err := s.deps.Store.WithTx(ctx, func(tx app.Tx) error {
		rootID := taskID
		origin := n.Origin
		if origin == "" {
			origin = domain.OriginOwner
		}
		depth := 0
		goalID := n.GoalID

		if n.ParentID != nil && strings.TrimSpace(*n.ParentID) != "" {
			parent, ok, err := tx.GetTask(ctx, *n.ParentID)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("parent task %q not found", *n.ParentID)
			}
			if parent.Depth >= domain.MaxTaskDepth {
				return fmt.Errorf("%w: parent depth %d", domain.ErrDepth, parent.Depth)
			}
			rootID = parent.RootID
			origin = parent.RootOrigin
			depth = parent.Depth + 1
			if goalID == nil {
				goalID = parent.GoalID
			}
		}

		edges, err := tx.ListAllBlockerEdges(ctx)
		if err != nil {
			return err
		}
		openBlockers := 0
		hasCancelledBlocker := false
		for _, bid := range n.BlockedBy {
			if domain.HasCycle(edges, taskID, bid) {
				return fmt.Errorf("dependency cycle detected (%s blocked by %s)", taskID, bid)
			}
			bTk, ok, err := tx.GetTask(ctx, bid)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("blocker task %q not found", bid)
			}
			edges[taskID] = append(edges[taskID], bid)
			switch domain.TaskStatus(bTk.Status) {
			case domain.TaskDone:
			case domain.TaskCancelled:
				hasCancelledBlocker = true
			default:
				openBlockers++
			}
		}

		initStatus, initReason := domain.InitialStatus(openBlockers)
		if hasCancelledBlocker {
			r := domain.BlockedNeedsOwner
			initStatus = domain.TaskBlocked
			initReason = &r
		}
		var reasonStr *string
		if initReason != nil {
			rs := string(*initReason)
			reasonStr = &rs
		}
		var dueAtMs *int64
		if n.DueAt != nil {
			ms := n.DueAt.UnixMilli()
			dueAtMs = &ms
		}

		if err := tx.InsertTask(ctx, app.TaskRecord{
			ID:              taskID,
			GoalID:          goalID,
			ParentID:        n.ParentID,
			RootID:          rootID,
			RootOrigin:      origin,
			Depth:           depth,
			Title:           title,
			Description:     n.Description,
			DoneCriteria:    n.DoneCriteria,
			Status:          string(initStatus),
			BlockedReason:   reasonStr,
			AssigneeAgentID: n.Assignee,
			CreatedByAgent:  n.CreatedByAgent,
			NeedsReview:     n.NeedsReview,
			DueAt:           dueAtMs,
			CreatedAt:       nowMs,
			UpdatedAt:       nowMs,
		}); err != nil {
			return err
		}

		for _, bid := range n.BlockedBy {
			if err := tx.InsertTaskBlocker(ctx, taskID, bid); err != nil {
				return err
			}
		}

		actorKind := "owner"
		if n.CreatedByAgent != nil {
			actorKind = "agent"
		}
		if err := tx.InsertTaskEvent(ctx, app.TaskEventRecord{
			ID:        s.deps.IDs.New(),
			TaskID:    taskID,
			Kind:      "created",
			ActorKind: actorKind,
			ActorID:   n.CreatedByAgent,
			Body:      fmt.Sprintf("status=%s", initStatus),
			CreatedAt: nowMs,
		}); err != nil {
			return err
		}

		if initStatus == domain.TaskTodo && n.Assignee != nil && strings.TrimSpace(*n.Assignee) != "" {
			return s.wakeTaskAgentTx(ctx, tx, *n.Assignee, taskID, domain.WakeTaskAssigned, n.CauseRunID, n.Hops, map[string]any{
				"task_id": taskID,
				"title":   title,
			})
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	s.publishTask(taskID, "task.created")
	return taskID, nil
}

// AddBlocker adds a dependency edge taskID blocked by blockedBy after rejecting cycles.
func (s *Service) AddBlocker(ctx context.Context, taskID, blockedBy domain.ID) error {
	nowMs := s.deps.Clock.Now().UnixMilli()
	return s.deps.Store.WithTx(ctx, func(tx app.Tx) error {
		tk, ok, err := tx.GetTask(ctx, taskID)
		if err != nil || !ok {
			if err != nil {
				return err
			}
			return fmt.Errorf("task %q not found", taskID)
		}
		bTk, bok, err := tx.GetTask(ctx, blockedBy)
		if err != nil || !bok {
			if err != nil {
				return err
			}
			return fmt.Errorf("blocker task %q not found", blockedBy)
		}
		edges, err := tx.ListAllBlockerEdges(ctx)
		if err != nil {
			return err
		}
		if domain.HasCycle(edges, taskID, blockedBy) {
			return fmt.Errorf("dependency cycle detected (%s blocked by %s)", taskID, blockedBy)
		}
		if err := tx.InsertTaskBlocker(ctx, taskID, blockedBy); err != nil {
			return err
		}
		if domain.TaskStatus(bTk.Status) != domain.TaskDone && !domain.TaskStatus(tk.Status).Final() {
			r := domain.BlockedDependency
			if domain.TaskStatus(bTk.Status) == domain.TaskCancelled {
				r = domain.BlockedNeedsOwner
			}
			return tx.UpdateTaskStatus(ctx, taskID, domain.TaskBlocked, &r, nowMs)
		}
		return nil
	})
}

// Checkout atomically claims taskID for runID if assigned to agentID and currently todo/in_progress without an active holder.
func (s *Service) Checkout(ctx context.Context, tx app.Tx, taskID, agentID, runID domain.ID) (bool, error) {
	return tx.CheckoutTaskAtomic(ctx, taskID, agentID, runID, s.deps.Clock.Now().UnixMilli())
}

// Comment appends a redacted comment event to taskID and wakes ONLY the task assignee (never @mentions).
func (s *Service) Comment(ctx context.Context, taskID domain.ID, actor Actor, body string) error {
	clean := platform.Redact(strings.TrimSpace(body))
	if clean == "" {
		return errors.New("comment body cannot be empty")
	}
	nowMs := s.deps.Clock.Now().UnixMilli()
	err := s.deps.Store.WithTx(ctx, func(tx app.Tx) error {
		tk, ok, err := tx.GetTask(ctx, taskID)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("task %q not found", taskID)
		}
		evID := s.deps.IDs.New()
		if err := tx.InsertTaskEvent(ctx, app.TaskEventRecord{
			ID:        evID,
			TaskID:    taskID,
			Kind:      "comment",
			ActorKind: defaultActorKind(actor.Kind),
			ActorID:   optionalID(actor.ID),
			Body:      clean,
			CreatedAt: nowMs,
		}); err != nil {
			return err
		}
		if tk.AssigneeAgentID != nil && (actor.Kind != "agent" || actor.ID != *tk.AssigneeAgentID) {
			return s.wakeTaskAgentTx(ctx, tx, *tk.AssigneeAgentID, taskID, domain.WakeTaskUpdate, nil, 0, map[string]any{
				"task_id":  taskID,
				"event_id": evID,
				"kind":     "comment",
			})
		}
		return nil
	})
	if err == nil {
		s.publishTask(taskID, "task.comment")
	}
	return err
}

func (s *Service) publishTask(taskID domain.ID, typ string) {
	if s.deps.Publish != nil {
		s.deps.Publish("task:"+taskID, typ, map[string]string{"taskId": taskID})
	}
}

func optionalID(id domain.ID) *domain.ID {
	if strings.TrimSpace(id) == "" {
		return nil
	}
	cp := id
	return &cp
}

func defaultActorKind(k string) string {
	if strings.TrimSpace(k) == "" {
		return "system"
	}
	return k
}

func marshalPayload(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}
