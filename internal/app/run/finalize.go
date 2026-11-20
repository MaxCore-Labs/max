package run

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/app/coord"
	"max/internal/domain"
)

func (s *Service) finalizeWithRetry(ctx context.Context, runID domain.ID, o Outcome) error {
	var lastErr error
	backoff := s.deps.FinalizeBackoff
	for attempt := range finalizeMaxRetries {
		if attempt > 0 {
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
			backoff *= 2
		}
		if err := s.Finalize(ctx, runID, o); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return fmt.Errorf("finalize run %q after %d attempts: %w", runID, finalizeMaxRetries, lastErr)
}

// Finalize atomically persists a terminal run outcome, transitions claimed wakeups, clears checkouts,
// updates conversation sessions/turns, and enqueues follow-up continue or Inbox items in one WithTx.
func (s *Service) Finalize(ctx context.Context, runID domain.ID, o Outcome) error {
	now := s.deps.Clock.Now()
	nowMs := now.UnixMilli()

	var vaultCommit string
	if s.deps.Vault != nil && len(o.TouchedPaths) > 0 && (o.Status == domain.RunSucceeded || o.Status == domain.RunParked || o.Status == domain.RunTurnLimit) {
		sha, err := s.deps.Vault.CommitPaths(ctx, o.TouchedPaths, fmt.Sprintf("vault(run): %s", runID))
		if err == nil {
			vaultCommit = sha
		}
	}

	var (
		postTxRemember bool
		postTxAgentID  domain.ID
		postTxSummary  string
	)
	err := s.deps.Store.WithTx(ctx, func(tx app.Tx) error {
		runRec, ok, err := tx.GetRun(ctx, runID)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("run %q not found", runID)
		}
		if runRec.Status.Terminal() && runRec.EndedAt != nil {
			return nil
		}

		ag, ok, err := tx.GetAgent(ctx, runRec.AgentID)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("agent %q not found", runRec.AgentID)
		}

		conv, convOK, err := tx.GetConversation(ctx, runRec.ConversationID)
		if err != nil {
			return err
		}

		status := o.Status
		if status == domain.RunSucceeded && convOK && conv.WaitingFor != nil && *conv.WaitingFor != "" {
			status = domain.RunParked
		}

		if o.SessionRebuilt {
			if err := tx.InsertRunEvent(ctx, app.RunEventRecord{
				RunID:     runID,
				AgentID:   runRec.AgentID,
				Type:      "session_rebuilt",
				DataJSON:  `{"reason":"cold_resume"}`,
				CreatedAt: nowMs,
			}); err != nil {
				return err
			}
		}

		claimedWakeups, err := tx.ListClaimedWakeupsByRun(ctx, runID)
		if err != nil {
			return err
		}
		maxAttempts := runRec.Attempt
		for _, w := range claimedWakeups {
			if w.Attempts > maxAttempts {
				maxAttempts = w.Attempts
			}
		}

		var idleRuns int
		if runRec.TaskID != nil && *runRec.TaskID != "" {
			if tk, found, err := tx.GetTask(ctx, *runRec.TaskID); err == nil && found {
				idleRuns = tk.IdleRuns
			}
		}

		totalConvTurns := conv.TurnsUsed + o.Turns
		requeue, contWake, blockTask, blockReason := domain.NextStepAfter(status, totalConvTurns, idleRuns, maxAttempts)

		if convOK {
			sessions := make(map[string]string)
			_ = json.Unmarshal([]byte(conv.Sessions), &sessions)
			if o.SessionID != "" {
				sessions[string(runRec.Engine)] = o.SessionID
			}
			sessBytes, _ := json.Marshal(sessions)
			engStr := string(runRec.Engine)
			if err := tx.UpdateConversationAfterRun(ctx, conv.ID, string(sessBytes), &engStr, o.Turns); err != nil {
				return err
			}
		}

		if err := tx.ClearTaskCheckout(ctx, runID, nowMs); err != nil {
			return err
		}

		if status == domain.RunSucceeded && runRec.TaskID != nil && *runRec.TaskID != "" {
			isAutoTask := false
			for _, w := range claimedWakeups {
				if w.Kind == domain.WakeHeartbeat || w.Kind == domain.WakeRoutine {
					isAutoTask = true
					break
				}
			}
			if isAutoTask {
				if tk, found, _ := tx.GetTask(ctx, *runRec.TaskID); found &&
					(tk.Status == string(domain.TaskTodo) || tk.Status == string(domain.TaskInProgress)) {
					if err := tx.UpdateTaskStatus(ctx, *runRec.TaskID, domain.TaskDone, nil, nowMs); err != nil {
						return err
					}
				}
			}
		}

		if s.deps.Coord != nil && runRec.TaskID != nil && *runRec.TaskID != "" &&
			(status == domain.RunSucceeded || status == domain.RunParked || status == domain.RunTurnLimit) {
			stChanged, chAdded, _ := tx.HasTaskProgressedSince(ctx, *runRec.TaskID, runRec.StartedAt)
			progressed := coord.DidRunProgress(stChanged, o.TouchedPaths, chAdded)
			if err := s.deps.Coord.NoteRunOutcome(ctx, tx, *runRec.TaskID, progressed); err != nil {
				return err
			}
		}

		switch {
		case status == domain.RunQuotaStopped:
			if err := tx.RequeueWakeupsForRun(ctx, runID, true); err != nil {
				return err
			}
			until := now.Add(domain.NextProbe(0))
			if o.LimitResetAt != nil && o.LimitResetAt.After(now) {
				until = *o.LimitResetAt
			}
			if err := tx.UpsertQuotaLimit(ctx, runRec.Engine, until.UnixMilli(), nowMs); err != nil {
				return err
			}
		case requeue:
			if err := tx.RequeueWakeupsForRun(ctx, runID, false); err != nil {
				return err
			}
		case status == domain.RunInterrupted || status == domain.RunFailed:
			if err := tx.FinishWakeupsForRun(ctx, runID, "dropped"); err != nil {
				return err
			}
		default:
			if err := tx.FinishWakeupsForRun(ctx, runID, "done"); err != nil {
				return err
			}
		}

		if blockTask {
			if runRec.TaskID != nil && *runRec.TaskID != "" {
				if err := tx.BlockTask(ctx, *runRec.TaskID, blockReason, nowMs); err != nil {
					return err
				}
			}
			refKind := "run"
			refID := runID
			convID := runRec.ConversationID
			agentID := runRec.AgentID
			dedupe := fmt.Sprintf("run-blocked:%s:%s", runID, blockReason)
			if err := tx.InsertInboxItem(ctx, app.InboxItemRecord{
				ID:             s.deps.IDs.New(),
				Kind:           "error",
				Title:          fmt.Sprintf("Công việc bị dừng (%s)", blockReason),
				Body:           fmt.Sprintf("Run %s của agent %s bị dừng với lý do %s.", runID, ag.Slug, blockReason),
				RefKind:        &refKind,
				RefID:          &refID,
				ConversationID: &convID,
				AgentID:        &agentID,
				Urgency:        "high",
				DedupeKey:      &dedupe,
				CreatedAt:      nowMs,
			}); err != nil {
				return err
			}
		}

		if contWake && s.deps.Actors != nil {
			convID := runRec.ConversationID
			if _, _, err := s.deps.Actors.Wake(ctx, tx, actor.WakeInput{
				AgentID:        runRec.AgentID,
				Kind:           domain.WakeContinue,
				Priority:       runRec.Priority,
				ConversationID: &convID,
				CauseRunID:     &runID,
			}); err != nil {
				return err
			}
		}

		touchedBytes, _ := json.Marshal(o.TouchedPaths)
		var errPtr, sumPtr, shaPtr *string
		if o.Err != "" {
			e := o.Err
			errPtr = &e
		}
		if o.Summary != "" {
			sm := o.Summary
			sumPtr = &sm
		}
		if vaultCommit != "" {
			sc := vaultCommit
			shaPtr = &sc
		}
		endedAt := nowMs
		if err := tx.UpdateRunFinal(ctx, app.RunRecord{
			ID:               runID,
			Status:           status,
			Turns:            o.Turns,
			InputTokens:      o.InputTokens,
			OutputTokens:     o.OutputTokens,
			TouchedPathsJSON: string(touchedBytes),
			Error:            errPtr,
			Summary:          sumPtr,
			VaultCommit:      shaPtr,
			EndedAt:          &endedAt,
		}); err != nil {
			return err
		}

		if convOK && conv.ThreadID != nil && *conv.ThreadID != "" && o.Summary != "" {
			if _, thOK, _ := tx.GetThread(ctx, *conv.ThreadID); thOK {
				rID := runID
				if err := tx.InsertMessage(ctx, app.MessageRecord{
					ID:              s.deps.IDs.New(),
					ThreadID:        *conv.ThreadID,
					Role:            "assistant",
					Content:         o.Summary,
					AttachmentsJSON: "[]",
					RunID:           &rID,
					CreatedAt:       nowMs,
				}); err != nil {
					return err
				}
				if err := tx.TouchThread(ctx, *conv.ThreadID, nowMs); err != nil {
					return err
				}
			}
		}

		if err := tx.SetAgentActiveRun(ctx, runRec.AgentID, nil); err != nil {
			return err
		}
		if o.ScopeViolations > 0 {
			totalV, paused, vErr := tx.RecordScopeViolations(ctx, runRec.AgentID, 1, nowMs)
			if vErr != nil {
				return vErr
			}
			refKind := "agent"
			refID := runRec.AgentID
			dedupe := fmt.Sprintf("scope-violation:%s:%d", runRec.AgentID, totalV)
			title := fmt.Sprintf("Hoàn tác ghi ngoài phạm vi của %s (lần %d)", ag.Slug, totalV)
			if paused {
				title = fmt.Sprintf("Tạm dừng agent %s sau 3 lần ghi ngoài phạm vi", ag.Slug)
			}
			_ = tx.InsertInboxItem(ctx, app.InboxItemRecord{
				ID:        s.deps.IDs.New(),
				Kind:      "error",
				Title:     title,
				Body:      fmt.Sprintf("Run %s có %d thay đổi ngoài phạm vi đã bị hoàn tác.", runID, o.ScopeViolations),
				RefKind:   &refKind,
				RefID:     &refID,
				AgentID:   &refID,
				Urgency:   "high",
				DedupeKey: &dedupe,
				CreatedAt: nowMs,
			})
		}
		if err := tx.SetAgentLastRunAt(ctx, runRec.AgentID, nowMs); err != nil {
			return err
		}
		if runRec.Priority < domain.PrioInteractive {
			cd := defaultCooldown
			if s.deps.Cooldown != nil {
				cd = *s.deps.Cooldown
			}
			if cd > 0 {
				if err := tx.SetAgentCooldown(ctx, runRec.AgentID, now.Add(cd).UnixMilli()); err != nil {
					return err
				}
			}
		}
		if err := tx.AdjustTeamRunning(ctx, ag.TeamID, -1); err != nil {
			return err
		}
		needRem, cleanSum, mErr := s.recordMemoryInFinalizeTx(ctx, tx, runRec, o.Summary, nowMs)
		if mErr != nil {
			return mErr
		}
		postTxRemember = needRem
		postTxAgentID = runRec.AgentID
		postTxSummary = cleanSum
		return tx.RecomputeAgentReady(ctx, runRec.AgentID, nowMs)
	})
	if err != nil {
		return err
	}
	if postTxRemember && s.deps.Memory != nil {
		if rErr := s.deps.Memory.Remember(ctx, postTxAgentID, postTxSummary, nil); rErr != nil {
			_ = s.deps.Store.WithTx(ctx, func(tx app.Tx) error {
				return enqueueMemoryOutbox(ctx, tx, runID, postTxAgentID, postTxSummary, nowMs)
			})
		}
	}
	return nil
}
