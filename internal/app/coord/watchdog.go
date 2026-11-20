package coord

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/domain"
)

const (
	staleChildAge   = 7 * 24 * time.Hour
	maxIdleRunsTask = 3
)

// DidRunProgress reports whether a task run made tangible progress per Spec 8.7:
// changing task status, writing a file under artifacts/, or creating at least one child task.
func DidRunProgress(statusChanged bool, touchedPaths []string, childrenAdded int) bool {
	if statusChanged || childrenAdded > 0 {
		return true
	}
	for _, p := range touchedPaths {
		norm := filepath.ToSlash(p)
		if strings.HasPrefix(norm, "artifacts/") || strings.Contains(norm, "/artifacts/") {
			return true
		}
	}
	return false
}

// Watchdog scans open child tasks for 7-day staleness or overdue due_at deadlines and wakes their delegators at most once per UTC day.
func (s *Service) Watchdog(ctx context.Context, now time.Time) (int, error) {
	nowMs := now.UnixMilli()
	staleBeforeMs := now.Add(-staleChildAge).UnixMilli()
	dayKey := now.UTC().Format("2006-01-02")

	candidates, err := s.deps.Store.Read().ListWatchdogCandidateTasks(ctx, staleBeforeMs, nowMs)
	if err != nil {
		return 0, err
	}
	if len(candidates) == 0 || s.deps.Actors == nil {
		return 0, nil
	}

	woken := 0
	err = s.deps.Store.WithTx(ctx, func(tx app.Tx) error {
		for _, ch := range candidates {
			if ch.ParentID == nil || domain.TaskStatus(ch.Status).Final() {
				continue
			}
			isOverdue := ch.DueAt != nil && *ch.DueAt <= nowMs
			isStale := ch.UpdatedAt <= staleBeforeMs
			if !isOverdue && !isStale {
				continue
			}

			reason := "stale_child"
			if isOverdue {
				reason = "overdue_child"
			}
			idemKey := fmt.Sprintf("watchdog:%s:%s:%s", reason, ch.ID, dayKey)
			seen, err := tx.HasWakeupIdemKey(ctx, idemKey)
			if err != nil {
				return err
			}
			if seen {
				continue
			}

			delegator := ch.CreatedByAgent
			if delegator == nil {
				if pTk, ok, perr := tx.GetTask(ctx, *ch.ParentID); perr == nil && ok {
					delegator = pTk.AssigneeAgentID
				}
			}
			if delegator == nil {
				continue
			}

			convID, err := tx.EnsureTaskConversation(ctx, *delegator, *ch.ParentID, s.deps.IDs.New())
			if err != nil {
				return err
			}
			_, coalesced, err := s.deps.Actors.Wake(ctx, tx, actor.WakeInput{
				AgentID:        *delegator,
				Kind:           domain.WakeSystem,
				Priority:       domain.PrioRoutine,
				ConversationID: &convID,
				IdempotencyKey: &idemKey,
				Payload: marshalPayload(map[string]any{
					"task_id":       *ch.ParentID,
					"child_task_id": ch.ID,
					"child_status":  ch.Status,
					"reason":        reason,
				}),
			})
			if err != nil {
				return err
			}
			if !coalesced {
				woken++
			}
		}
		return nil
	})
	return woken, err
}

// NoteRunOutcome updates tasks.idle_runs after a run; 3 consecutive non-progressing runs mark the task blocked/stalled and escalate.
func (s *Service) NoteRunOutcome(ctx context.Context, tx app.Tx, taskID domain.ID, progressed bool) error {
	tk, ok, err := tx.GetTask(ctx, taskID)
	if err != nil || !ok {
		return err
	}
	if domain.TaskStatus(tk.Status).Final() {
		return nil
	}
	nowMs := s.deps.Clock.Now().UnixMilli()
	if progressed {
		if tk.IdleRuns == 0 {
			return nil
		}
		return tx.UpdateTaskIdleRuns(ctx, taskID, 0, nowMs)
	}

	nextIdle := tk.IdleRuns + 1
	if err := tx.UpdateTaskIdleRuns(ctx, taskID, nextIdle, nowMs); err != nil {
		return err
	}
	if nextIdle < maxIdleRunsTask {
		return nil
	}

	stalled := domain.BlockedStalled
	if err := tx.UpdateTaskStatus(ctx, taskID, domain.TaskBlocked, &stalled, nowMs); err != nil {
		return err
	}
	reasonText := fmt.Sprintf("Task không tiến triển sau %d run liên tiếp (blocked/stalled)", nextIdle)
	if err := tx.InsertTaskEvent(ctx, app.TaskEventRecord{
		ID:        s.deps.IDs.New(),
		TaskID:    taskID,
		Kind:      "escalate",
		ActorKind: "system",
		Body:      reasonText,
		CreatedAt: nowMs,
	}); err != nil {
		return err
	}

	var managerID *domain.ID
	hops := 0
	fromID := ""
	if tk.AssigneeAgentID != nil {
		fromID = *tk.AssigneeAgentID
		if ag, aok, aerr := tx.GetAgent(ctx, fromID); aerr == nil && aok && ag.ManagerAgentID != nil && *ag.ManagerAgentID != "" {
			m := *ag.ManagerAgentID
			managerID = &m
		}
		hops, _ = tx.GetMaxWakeupHopsForTask(ctx, fromID, taskID)
	}

	if managerID != nil && hops < domain.MaxCausalHops {
		return s.wakeTaskAgentTx(ctx, tx, *managerID, taskID, domain.WakeTaskUpdate, nil, hops+1, map[string]any{
			"task_id":        taskID,
			"reason":         "stalled",
			"escalated_from": fromID,
		})
	}

	refKind := "task"
	refID := taskID
	return tx.InsertInboxItem(ctx, app.InboxItemRecord{
		ID:        s.deps.IDs.New(),
		Kind:      "escalation",
		Title:     fmt.Sprintf("Task treo (stalled): %s", tk.Title),
		Body:      reasonText,
		RefKind:   &refKind,
		RefID:     &refID,
		AgentID:   optionalID(fromID),
		Urgency:   "high",
		CreatedAt: nowMs,
	})
}
