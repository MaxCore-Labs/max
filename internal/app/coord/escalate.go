package coord

import (
	"context"
	"fmt"
	"strings"

	"max/internal/app"
	"max/internal/domain"
	"max/internal/platform"
)

// Escalate walks up the manager_agent_id chain from agent `from`; if at the root (or causal hops exhausted), routes to owner Inbox.
func (s *Service) Escalate(ctx context.Context, taskID domain.ID, from domain.ID, reason string) error {
	cleanReason := platform.Redact(strings.TrimSpace(reason))
	if cleanReason == "" {
		cleanReason = "escalated"
	}
	nowMs := s.deps.Clock.Now().UnixMilli()
	err := s.deps.Store.WithTx(ctx, func(tx app.Tx) error {
		return s.escalateTx(ctx, tx, taskID, from, cleanReason, nowMs)
	})
	if err == nil {
		s.publishTask(taskID, "task.escalated")
	}
	return err
}

func (s *Service) escalateTx(ctx context.Context, tx app.Tx, taskID, from domain.ID, cleanReason string, nowMs int64) error {
	tk, ok, err := tx.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("task %q not found", taskID)
	}
	if domain.TaskStatus(tk.Status).Final() {
		return nil
	}

	var managerID *domain.ID
	hops := 0
	if strings.TrimSpace(from) != "" {
		if ag, aok, aerr := tx.GetAgent(ctx, from); aerr == nil && aok {
			if ag.ManagerAgentID != nil && strings.TrimSpace(*ag.ManagerAgentID) != "" && *ag.ManagerAgentID != from {
				m := *ag.ManagerAgentID
				managerID = &m
			}
		}
		hops, _ = tx.GetMaxWakeupHopsForTask(ctx, from, taskID)
	}

	if managerID != nil && hops < domain.MaxCausalHops {
		if err := tx.UpdateTaskAssignee(ctx, taskID, managerID, domain.TaskTodo, nil, nowMs); err != nil {
			return err
		}
		if err := tx.InsertTaskEvent(ctx, app.TaskEventRecord{
			ID:            s.deps.IDs.New(),
			TaskID:        taskID,
			Kind:          "escalate",
			ActorKind:     "agent",
			ActorID:       optionalID(from),
			TargetAgentID: managerID,
			Body:          cleanReason,
			CreatedAt:     nowMs,
		}); err != nil {
			return err
		}
		return s.wakeTaskAgentTx(ctx, tx, *managerID, taskID, domain.WakeTaskAssigned, nil, hops+1, map[string]any{
			"task_id":        taskID,
			"escalated_from": from,
			"reason":         cleanReason,
		})
	}

	if err := tx.UpdateTaskAssignee(ctx, taskID, nil, domain.TaskWaitingOwner, nil, nowMs); err != nil {
		return err
	}
	if err := tx.InsertTaskEvent(ctx, app.TaskEventRecord{
		ID:        s.deps.IDs.New(),
		TaskID:    taskID,
		Kind:      "escalate",
		ActorKind: defaultActorKind(actorKindForID(from)),
		ActorID:   optionalID(from),
		Body:      cleanReason,
		CreatedAt: nowMs,
	}); err != nil {
		return err
	}
	refKind := "task"
	refID := taskID
	return tx.InsertInboxItem(ctx, app.InboxItemRecord{
		ID:        s.deps.IDs.New(),
		Kind:      "escalation",
		Title:     fmt.Sprintf("Chuyển cấp task: %s", tk.Title),
		Body:      cleanReason,
		RefKind:   &refKind,
		RefID:     &refID,
		AgentID:   optionalID(from),
		Urgency:   "high",
		CreatedAt: nowMs,
	})
}

func actorKindForID(id domain.ID) string {
	if strings.TrimSpace(id) == "" {
		return "system"
	}
	return "agent"
}
