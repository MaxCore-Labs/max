package coord

import (
	"context"
	"fmt"
	"slices"

	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/domain"
)

const mailboxHardCap = 200

// SetStatus transitions taskID to a new TaskStatus, atomically unblocking dependents and waking parents when done.
func (s *Service) SetStatus(
	ctx context.Context,
	taskID domain.ID,
	to domain.TaskStatus,
	reason *domain.BlockedReason,
	act Actor,
) error {
	if to == domain.TaskCancelled {
		return s.Cancel(ctx, taskID, act)
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
		cur := domain.TaskStatus(tk.Status)
		if cur == to {
			if to == domain.TaskBlocked && reason != nil {
				return tx.UpdateTaskStatus(ctx, taskID, to, reason, nowMs)
			}
			return nil
		}
		if !domain.CanTransition(cur, to) {
			return fmt.Errorf("invalid task transition %s -> %s", cur, to)
		}
		if to == domain.TaskDone {
			return s.completeTaskTx(ctx, tx, tk, act, nowMs)
		}
		if to == domain.TaskBlocked && reason == nil {
			r := domain.BlockedNeedsOwner
			reason = &r
		}
		if err := tx.UpdateTaskStatus(ctx, taskID, to, reason, nowMs); err != nil {
			return err
		}
		return tx.InsertTaskEvent(ctx, app.TaskEventRecord{
			ID:        s.deps.IDs.New(),
			TaskID:    taskID,
			Kind:      "status",
			ActorKind: defaultActorKind(act.Kind),
			ActorID:   optionalID(act.ID),
			Body:      fmt.Sprintf("%s -> %s", cur, to),
			CreatedAt: nowMs,
		})
	})
	if err == nil {
		s.publishTask(taskID, "task.updated")
	}
	return err
}

// Cancel cancels taskID and its entire non-final subtree, expiring pending approvals, cancelling active runs,
// and escalating any external dependent tasks to blocked(needs_owner).
func (s *Service) Cancel(ctx context.Context, taskID domain.ID, act Actor) error {
	nowMs := s.deps.Clock.Now().UnixMilli()
	var runsToCancel []domain.ID

	err := s.deps.Store.WithTx(ctx, func(tx app.Tx) error {
		rootTk, ok, err := tx.GetTask(ctx, taskID)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("task %q not found", taskID)
		}
		subtree, err := collectSubtreeTx(ctx, tx, rootTk)
		if err != nil {
			return err
		}

		subtreeSet := make(map[domain.ID]bool, len(subtree))
		var cancelledIDs []domain.ID
		for _, tk := range subtree {
			subtreeSet[tk.ID] = true
			cur := domain.TaskStatus(tk.Status)
			if cur.Final() {
				continue
			}
			if err := tx.UpdateTaskStatus(ctx, tk.ID, domain.TaskCancelled, nil, nowMs); err != nil {
				return err
			}
			if err := tx.InsertTaskEvent(ctx, app.TaskEventRecord{
				ID:        s.deps.IDs.New(),
				TaskID:    tk.ID,
				Kind:      "status",
				ActorKind: defaultActorKind(act.Kind),
				ActorID:   optionalID(act.ID),
				Body:      fmt.Sprintf("%s -> cancelled", cur),
				CreatedAt: nowMs,
			}); err != nil {
				return err
			}
			cancelledIDs = append(cancelledIDs, tk.ID)
		}
		if len(cancelledIDs) == 0 {
			return nil
		}

		if _, err := tx.ExpirePendingApprovalsByTasks(ctx, cancelledIDs, nowMs); err != nil {
			return err
		}
		activeRuns, err := tx.ListActiveRunsByTasks(ctx, cancelledIDs)
		if err != nil {
			return err
		}
		for _, r := range activeRuns {
			if !slices.Contains(runsToCancel, r.ID) {
				runsToCancel = append(runsToCancel, r.ID)
			}
		}

		// Escalate dependents outside the cancelled subtree that were blocked by any cancelled task.
		for _, cid := range cancelledIDs {
			deps, err := tx.ListDependents(ctx, cid)
			if err != nil {
				return err
			}
			for _, dep := range deps {
				if subtreeSet[dep.ID] || domain.TaskStatus(dep.Status).Final() {
					continue
				}
				r := domain.BlockedNeedsOwner
				if err := tx.UpdateTaskStatus(ctx, dep.ID, domain.TaskBlocked, &r, nowMs); err != nil {
					return err
				}
				if err := tx.InsertTaskEvent(ctx, app.TaskEventRecord{
					ID:        s.deps.IDs.New(),
					TaskID:    dep.ID,
					Kind:      "status",
					ActorKind: "system",
					Body:      fmt.Sprintf("blocker %s cancelled -> blocked(needs_owner)", cid),
					CreatedAt: nowMs,
				}); err != nil {
					return err
				}
				target := dep.CreatedByAgent
				if target == nil {
					target = dep.AssigneeAgentID
				}
				if target != nil {
					if err := s.wakeTaskAgentTx(ctx, tx, *target, dep.ID, domain.WakeTaskUpdate, nil, 0, map[string]any{
						"task_id": dep.ID,
						"reason":  "blocker_cancelled",
						"blocker": cid,
					}); err != nil {
						return err
					}
				} else {
					refKind := "task"
					refID := dep.ID
					if err := tx.InsertInboxItem(ctx, app.InboxItemRecord{
						ID:        s.deps.IDs.New(),
						Kind:      "escalation",
						Title:     fmt.Sprintf("Task bị chặn do %s đã hủy", cid),
						Body:      dep.Title,
						RefKind:   &refKind,
						RefID:     &refID,
						Urgency:   "high",
						CreatedAt: nowMs,
					}); err != nil {
						return err
					}
				}
			}
		}

		if rootTk.ParentID != nil {
			if err := s.wakeParentIfAllChildrenFinalTx(ctx, tx, *rootTk.ParentID, rootTk.ID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	if s.deps.CancelRun != nil {
		for _, rid := range runsToCancel {
			s.deps.CancelRun(rid)
		}
	}
	s.publishTask(taskID, "task.cancelled")
	return nil
}

func collectSubtreeTx(ctx context.Context, tx app.Tx, root app.TaskRecord) ([]app.TaskRecord, error) {
	out := []app.TaskRecord{root}
	queue := []domain.ID{root.ID}
	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]
		children, err := tx.ListChildTasks(ctx, curr)
		if err != nil {
			return nil, err
		}
		for _, ch := range children {
			out = append(out, ch)
			queue = append(queue, ch.ID)
		}
	}
	return out, nil
}

func (s *Service) completeTaskTx(ctx context.Context, tx app.Tx, tk app.TaskRecord, act Actor, nowMs int64) error {
	if err := tx.UpdateTaskStatus(ctx, tk.ID, domain.TaskDone, nil, nowMs); err != nil {
		return err
	}
	if err := tx.InsertTaskEvent(ctx, app.TaskEventRecord{
		ID:        s.deps.IDs.New(),
		TaskID:    tk.ID,
		Kind:      "status",
		ActorKind: defaultActorKind(act.Kind),
		ActorID:   optionalID(act.ID),
		Body:      fmt.Sprintf("%s -> done", tk.Status),
		CreatedAt: nowMs,
	}); err != nil {
		return err
	}

	// Unblock dependents whose blocked_reason == dependency when all blockers are now done.
	deps, err := tx.ListDependents(ctx, tk.ID)
	if err != nil {
		return err
	}
	for _, dep := range deps {
		if domain.TaskStatus(dep.Status) != domain.TaskBlocked || dep.BlockedReason == nil {
			continue
		}
		if !domain.AutoUnblockable(domain.BlockedReason(*dep.BlockedReason)) {
			continue
		}
		blockerIDs, err := tx.ListTaskBlockers(ctx, dep.ID)
		if err != nil {
			return err
		}
		allDone := true
		for _, bid := range blockerIDs {
			bTk, ok, err := tx.GetTask(ctx, bid)
			if err != nil {
				return err
			}
			if !ok || domain.TaskStatus(bTk.Status) != domain.TaskDone {
				allDone = false
				break
			}
		}
		if !allDone {
			continue
		}
		if err := tx.UpdateTaskStatus(ctx, dep.ID, domain.TaskTodo, nil, nowMs); err != nil {
			return err
		}
		if err := tx.InsertTaskEvent(ctx, app.TaskEventRecord{
			ID:        s.deps.IDs.New(),
			TaskID:    dep.ID,
			Kind:      "status",
			ActorKind: "system",
			Body:      "all blockers done -> todo",
			CreatedAt: nowMs,
		}); err != nil {
			return err
		}
		if dep.AssigneeAgentID != nil {
			if err := s.wakeTaskAgentTx(ctx, tx, *dep.AssigneeAgentID, dep.ID, domain.WakeTaskAssigned, nil, 0, map[string]any{
				"task_id":   dep.ID,
				"unblocked": true,
			}); err != nil {
				return err
			}
		}
	}

	if tk.ParentID != nil {
		if err := s.wakeParentIfAllChildrenFinalTx(ctx, tx, *tk.ParentID, tk.ID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) wakeParentIfAllChildrenFinalTx(ctx context.Context, tx app.Tx, parentID, lastChildID domain.ID) error {
	parent, ok, err := tx.GetTask(ctx, parentID)
	if err != nil || !ok || domain.TaskStatus(parent.Status).Final() {
		return err
	}
	children, err := tx.ListChildTasks(ctx, parentID)
	if err != nil || len(children) == 0 {
		return err
	}
	for _, ch := range children {
		if !domain.TaskStatus(ch.Status).Final() {
			return nil
		}
	}
	target := parent.AssigneeAgentID
	if target == nil {
		target = parent.CreatedByAgent
	}
	if target == nil {
		return nil
	}
	convID, err := tx.EnsureTaskConversation(ctx, *target, parentID, s.deps.IDs.New())
	if err != nil {
		return err
	}
	if err := tx.SetConversationWaitingFor(ctx, convID, nil); err != nil {
		return err
	}
	return s.wakeTaskAgentWithConvTx(ctx, tx, *target, convID, domain.WakeChildDone, nil, 0, map[string]any{
		"task_id":       parentID,
		"last_child_id": lastChildID,
	})
}

func (s *Service) wakeTaskAgentTx(
	ctx context.Context,
	tx app.Tx,
	agentID, taskID domain.ID,
	kind domain.WakeKind,
	causeRunID *domain.ID,
	hops int,
	payload map[string]any,
) error {
	convID, err := tx.EnsureTaskConversation(ctx, agentID, taskID, s.deps.IDs.New())
	if err != nil {
		return err
	}
	return s.wakeTaskAgentWithConvTx(ctx, tx, agentID, convID, kind, causeRunID, hops, payload)
}

func (s *Service) wakeTaskAgentWithConvTx(
	ctx context.Context,
	tx app.Tx,
	agentID, convID domain.ID,
	kind domain.WakeKind,
	causeRunID *domain.ID,
	hops int,
	payload map[string]any,
) error {
	if s.deps.Actors == nil {
		return nil
	}
	if !kind.Coalescible() && kind == domain.WakeTaskAssigned {
		cnt, err := tx.CountQueuedWakeups(ctx, agentID)
		if err != nil {
			return err
		}
		if cnt >= mailboxHardCap {
			return actor.ErrMailboxFull
		}
	}
	_, _, err := s.deps.Actors.Wake(ctx, tx, actor.WakeInput{
		AgentID:        agentID,
		Kind:           kind,
		Priority:       domain.PrioTask,
		ConversationID: &convID,
		Payload:        marshalPayload(payload),
		CauseRunID:     causeRunID,
		Hops:           hops,
	})
	return err
}
