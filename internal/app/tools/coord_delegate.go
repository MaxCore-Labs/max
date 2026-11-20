package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/app/coord"
	"max/internal/domain"
	"max/internal/platform"
)

// Delegate creates a child task assigned to target agent after validating anti-storm coordination rules.
func (s *Service) Delegate(
	ctx context.Context,
	c Caller,
	to, title, description, doneCriteria string,
	blockedBy []string,
	needsReview bool,
	dueAt *time.Time,
) Result {
	if ok, res := s.checkPolicy(c, "delegate", domain.ToolClassC, ""); !ok {
		return res
	}
	rd := s.store.Read()
	sender, ok, err := rd.GetAgent(ctx, c.AgentID)
	if err != nil || !ok {
		return Result{Text: "delegate failed: caller agent not found", IsError: true}
	}
	target, tok, err := s.resolveAgent(ctx, to)
	if err != nil || !tok {
		return Result{Text: fmt.Sprintf("delegate failed: target agent %q not found", to), IsError: true}
	}
	senderTeam, _, _ := rd.GetTeam(ctx, sender.TeamID)
	targetTeam, _, _ := rd.GetTeam(ctx, target.TeamID)

	parentID := s.resolveCurrentTaskID(ctx, c)
	parentDepth := 0
	origin := c.Origin
	if origin == "" {
		origin = domain.OriginOwner
	}
	if parentID != nil {
		if pTk, pok, perr := rd.GetTask(ctx, *parentID); perr == nil && pok {
			parentDepth = pTk.Depth
			origin = pTk.RootOrigin
		}
	}

	hops := s.effectiveHops(ctx, c)
	chk := domain.CoordCheck{
		Depth:           parentDepth,
		Hops:            hops,
		Run:             s.getRunCounters(c.RunID),
		SenderTeam:      senderTeam.Slug,
		TargetTeam:      targetTeam.Slug,
		TargetIsManager: sender.ManagerAgentID != nil && *sender.ManagerAgentID == target.ID,
		SenderIsChief:   sender.Slug == "chief-of-staff" || c.Policy.Scope.Slug == "chief-of-staff",
	}
	if err := domain.CheckDelegate(chk); err != nil {
		if errors.Is(err, domain.ErrHops) {
			s.reportHopsCapInbox(ctx, c, hops)
		}
		return Result{Text: fmt.Sprintf("delegate blocked by coordination rule: %v", err), IsError: true}
	}

	callerID := c.AgentID
	runID := c.RunID
	childID, err := s.ensureCoord().Create(ctx, coord.NewTask{
		Title:          title,
		Description:    description,
		DoneCriteria:   doneCriteria,
		ParentID:       parentID,
		Assignee:       &target.ID,
		BlockedBy:      blockedBy,
		NeedsReview:    needsReview,
		DueAt:          dueAt,
		Origin:         origin,
		CreatedByAgent: &callerID,
		CauseRunID:     &runID,
		Hops:           hops + 1,
	})
	if err != nil {
		if errors.Is(err, actor.ErrMailboxFull) {
			return Result{Text: fmt.Sprintf("hộp thư đầy (mailbox full for %s): task not created", target.Slug), IsError: true}
		}
		return Result{Text: fmt.Sprintf("delegate failed: %v", err), IsError: true}
	}

	s.incDelegateCount(c.RunID)
	if c.ConversationID != "" && parentID != nil {
		wfBytes, _ := json.Marshal(map[string]string{"kind": "children", "task_id": *parentID})
		wf := string(wfBytes)
		_ = s.store.WithTx(ctx, func(tx app.Tx) error {
			return tx.SetConversationWaitingFor(ctx, c.ConversationID, &wf)
		})
	}
	return Result{Text: fmt.Sprintf("Delegated child task %s to %s. End your turn if waiting on child completion.", childID, target.Slug)}
}

// MessageAgent records a task message in task_events and sends a low-priority reference-only wakeup.
func (s *Service) MessageAgent(ctx context.Context, c Caller, to, body string) Result {
	if ok, res := s.checkPolicy(c, "message_agent", domain.ToolClassC, ""); !ok {
		return res
	}
	clean := platform.Redact(strings.TrimSpace(body))
	if clean == "" {
		return Result{Text: "message_agent requires non-empty body", IsError: true}
	}
	taskIDPtr := s.resolveCurrentTaskID(ctx, c)
	if taskIDPtr == nil {
		return Result{Text: "message_agent requires an active task context", IsError: true}
	}
	target, tok, err := s.resolveAgent(ctx, to)
	if err != nil || !tok {
		return Result{Text: fmt.Sprintf("message_agent failed: target agent %q not found", to), IsError: true}
	}

	now := s.clock.Now()
	sinceMs := now.Add(-time.Hour).UnixMilli()
	pairCount, err := s.store.Read().CountPairMessagesSince(ctx, *taskIDPtr, c.AgentID, target.ID, sinceMs)
	if err != nil {
		return Result{Text: fmt.Sprintf("message_agent rate check: %v", err), IsError: true}
	}
	hops := s.effectiveHops(ctx, c)
	if err := domain.CheckMessage(domain.CoordCheck{
		Hops:             hops,
		Run:              s.getRunCounters(c.RunID),
		PairMsgsLastHour: pairCount,
	}); err != nil {
		if errors.Is(err, domain.ErrHops) {
			s.reportHopsCapInbox(ctx, c, hops)
		}
		return Result{Text: fmt.Sprintf("message_agent blocked by coordination rule: %v", err), IsError: true}
	}

	evID := s.ids.New()
	nowMs := now.UnixMilli()
	err = s.store.WithTx(ctx, func(tx app.Tx) error {
		senderID := c.AgentID
		targetID := target.ID
		if err := tx.InsertTaskEvent(ctx, app.TaskEventRecord{
			ID:            evID,
			TaskID:        *taskIDPtr,
			Kind:          "message",
			ActorKind:     "agent",
			ActorID:       &senderID,
			TargetAgentID: &targetID,
			Body:          clean,
			CreatedAt:     nowMs,
		}); err != nil {
			return err
		}
		if s.actors == nil {
			return nil
		}
		convID, err := tx.EnsureTaskConversation(ctx, target.ID, *taskIDPtr, s.ids.New())
		if err != nil {
			return err
		}
		refPayload, _ := json.Marshal(map[string]any{
			"task_id":       *taskIDPtr,
			"event_id":      evID,
			"from_agent_id": c.AgentID,
		})
		runID := c.RunID
		_, _, err = s.actors.Wake(ctx, tx, actor.WakeInput{
			AgentID:        target.ID,
			Kind:           domain.WakeTaskUpdate,
			Priority:       domain.PrioRoutine,
			ConversationID: &convID,
			Payload:        refPayload,
			CauseRunID:     &runID,
			Hops:           hops + 1,
		})
		return err
	})
	if err != nil {
		if errors.Is(err, actor.ErrMailboxFull) {
			return Result{Text: fmt.Sprintf("hộp thư đầy (mailbox full for %s)", target.Slug), IsError: true}
		}
		return Result{Text: fmt.Sprintf("message_agent failed: %v", err), IsError: true}
	}
	s.incMessageCount(c.RunID)
	return Result{Text: fmt.Sprintf("Message recorded on task %s for %s (event %s).", *taskIDPtr, target.Slug, evID)}
}

// RequestReview submits the caller's active task for review.
func (s *Service) RequestReview(ctx context.Context, c Caller) Result {
	if ok, res := s.checkPolicy(c, "request_review", domain.ToolClassC, ""); !ok {
		return res
	}
	taskIDPtr := s.resolveCurrentTaskID(ctx, c)
	if taskIDPtr == nil {
		return Result{Text: "request_review requires an active task context", IsError: true}
	}
	if err := s.ensureCoord().SetStatus(ctx, *taskIDPtr, domain.TaskInReview, nil, coord.Actor{Kind: "agent", ID: c.AgentID}); err != nil {
		return Result{Text: fmt.Sprintf("request_review failed: %v", err), IsError: true}
	}
	return Result{Text: fmt.Sprintf("Task %s submitted for review. End your turn.", *taskIDPtr)}
}
