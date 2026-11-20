package coord

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"max/internal/app"
	"max/internal/domain"
	"max/internal/platform"
)

const maxAgentReviewRounds = 2

// PickReviewer selects an active reviewer agent in the task's team distinct from the assignee; returns nil when the owner must review.
func (s *Service) PickReviewer(ctx context.Context, taskID domain.ID) (*domain.ID, error) {
	return pickReviewerWithReader(ctx, s.deps.Store.Read(), taskID)
}

func pickReviewerWithReader(ctx context.Context, rd app.Reader, taskID domain.ID) (*domain.ID, error) {
	tk, ok, err := rd.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("task %q not found", taskID)
	}
	refAgent := tk.AssigneeAgentID
	if refAgent == nil {
		refAgent = tk.CreatedByAgent
	}
	if refAgent == nil {
		return nil, nil
	}
	ag, aok, err := rd.GetAgent(ctx, *refAgent)
	if err != nil || !aok {
		return nil, err
	}
	teamAgents, err := rd.ListTeamAgents(ctx, ag.TeamID)
	if err != nil {
		return nil, err
	}
	for _, cand := range teamAgents {
		if tk.AssigneeAgentID != nil && cand.ID == *tk.AssigneeAgentID {
			continue
		}
		isReviewer := (cand.TemplateSlug != nil && *cand.TemplateSlug == "reviewer") ||
			strings.HasPrefix(cand.Slug, "reviewer")
		if isReviewer {
			id := cand.ID
			return &id, nil
		}
	}
	return nil, nil
}

// SubmitForReview moves taskID into review, assigning a peer reviewer for the first two rounds or routing to the owner Inbox thereafter.
func (s *Service) SubmitForReview(ctx context.Context, taskID domain.ID, by domain.ID) error {
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
		if cur.Final() {
			return fmt.Errorf("cannot submit final task %q (%s) for review", taskID, cur)
		}

		var reviewerID *domain.ID
		targetStatus := domain.TaskInReview
		if tk.ReviewRounds >= maxAgentReviewRounds {
			targetStatus = domain.TaskWaitingOwner
		} else {
			reviewerID, err = pickReviewerWithReader(ctx, tx, taskID)
			if err != nil {
				return err
			}
		}

		if err := tx.UpdateTaskReviewState(ctx, taskID, targetStatus, reviewerID, tk.ReviewRounds, nowMs); err != nil {
			return err
		}
		if err := tx.InsertTaskEvent(ctx, app.TaskEventRecord{
			ID:        s.deps.IDs.New(),
			TaskID:    taskID,
			Kind:      "review",
			ActorKind: "agent",
			ActorID:   optionalID(by),
			Body:      fmt.Sprintf("submitted for review (round %d)", tk.ReviewRounds+1),
			CreatedAt: nowMs,
		}); err != nil {
			return err
		}

		if reviewerID != nil {
			hops, _ := tx.GetMaxWakeupHopsForTask(ctx, by, taskID)
			return s.wakeTaskAgentTx(ctx, tx, *reviewerID, taskID, domain.WakeTaskAssigned, nil, min(hops+1, domain.MaxCausalHops), map[string]any{
				"task_id": taskID,
				"action":  "review_requested",
				"round":   tk.ReviewRounds + 1,
			})
		}

		refKind := "task"
		refID := taskID
		return tx.InsertInboxItem(ctx, app.InboxItemRecord{
			ID:        s.deps.IDs.New(),
			Kind:      "review",
			Title:     fmt.Sprintf("Cần duyệt kết quả task: %s", tk.Title),
			Body:      fmt.Sprintf("Task %s đã qua %d vòng sửa hoặc không có reviewer độc lập.", taskID, tk.ReviewRounds),
			RefKind:   &refKind,
			RefID:     &refID,
			AgentID:   optionalID(by),
			Urgency:   "normal",
			CreatedAt: nowMs,
		})
	})
	if err == nil {
		s.publishTask(taskID, "task.in_review")
	}
	return err
}

// Review records an approve or changes decision from reviewer (empty reviewer = owner).
func (s *Service) Review(ctx context.Context, taskID, reviewer domain.ID, approve bool, notes string) error {
	cleanNotes := platform.Redact(strings.TrimSpace(notes))
	if !approve && cleanNotes == "" {
		return errors.New("review requesting changes must include notes")
	}
	nowMs := s.deps.Clock.Now().UnixMilli()
	act := Actor{Kind: "owner"}
	if strings.TrimSpace(reviewer) != "" {
		act = Actor{Kind: "agent", ID: reviewer}
	}

	err := s.deps.Store.WithTx(ctx, func(tx app.Tx) error {
		tk, ok, err := tx.GetTask(ctx, taskID)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("task %q not found", taskID)
		}
		cur := domain.TaskStatus(tk.Status)
		if cur != domain.TaskInReview && cur != domain.TaskWaitingOwner {
			return fmt.Errorf("task %q is in status %q, not awaiting review", taskID, cur)
		}

		verdictLabel := "changes"
		if approve {
			verdictLabel = "approve"
		}
		if err := tx.InsertTaskEvent(ctx, app.TaskEventRecord{
			ID:        s.deps.IDs.New(),
			TaskID:    taskID,
			Kind:      "review",
			ActorKind: act.Kind,
			ActorID:   optionalID(act.ID),
			Body:      fmt.Sprintf("[%s] %s", verdictLabel, cleanNotes),
			CreatedAt: nowMs,
		}); err != nil {
			return err
		}

		if approve {
			return s.completeTaskTx(ctx, tx, tk, act, nowMs)
		}

		newRounds := tk.ReviewRounds + 1
		if err := tx.UpdateTaskReviewState(ctx, taskID, domain.TaskInProgress, tk.ReviewerAgentID, newRounds, nowMs); err != nil {
			return err
		}
		if tk.AssigneeAgentID != nil {
			hops := 0
			if act.ID != "" {
				hops, _ = tx.GetMaxWakeupHopsForTask(ctx, act.ID, taskID)
			}
			return s.wakeTaskAgentTx(ctx, tx, *tk.AssigneeAgentID, taskID, domain.WakeTaskUpdate, nil, min(hops+1, domain.MaxCausalHops), map[string]any{
				"task_id": taskID,
				"action":  "review_changes",
				"round":   newRounds,
			})
		}
		return nil
	})
	if err == nil {
		s.publishTask(taskID, "task.reviewed")
	}
	return err
}
