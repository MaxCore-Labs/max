package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"sort"
	"strings"
	"time"

	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/domain"
)

const (
	maxRoutineFailures = 3
	heartbeatJitterPct = 0.10
)

// FireRoutines scans routines/*.md, reports malformed files or missing inbound declarations once,
// skips active runs with a run_event, and creates a dedicated task + wakeup for due routines.
func (s *Scheduler) FireRoutines(ctx context.Context, now time.Time) error {
	if s.vault == nil {
		return nil
	}
	entries, err := s.vault.ListDir(ctx, "routines")
	if err != nil {
		return fmt.Errorf("list routines dir: %w", err)
	}
	sort.Strings(entries)
	nowMs := now.UnixMilli()

	for _, name := range entries {
		if !strings.HasSuffix(name, ".md") || name[0] == '.' || name[0] == '_' {
			continue
		}
		slug := strings.TrimSuffix(name, ".md")
		rel := "routines/" + name
		raw, rErr := s.vault.Read(ctx, rel)
		if rErr != nil {
			continue
		}

		st, hasState, err := s.store.Read().GetRoutineState(ctx, slug)
		if err != nil {
			return err
		}

		rf, pErr := domain.ParseRoutine(slug, raw)
		if pErr != nil {
			if !hasState || !st.BadFileReported {
				_ = s.reportBadRoutineFile(ctx, slug, rel, st, pErr.Error(), nowMs)
			}
			continue
		}

		if rf.Origin != "inbound" && looksLikeInboundRoutine(rf) {
			_ = s.reportInboundWarning(ctx, rf, nowMs)
		}

		if !rf.Enabled || (hasState && st.AutoDisabled) {
			continue
		}

		if err := s.evalAndFireRoutine(ctx, rf, st, hasState, now, nowMs); err != nil {
			return err
		}
	}
	return nil
}

func (s *Scheduler) evalAndFireRoutine(
	ctx context.Context,
	rf domain.RoutineFile,
	st app.RoutineStateRecord,
	hasState bool,
	now time.Time,
	nowMs int64,
) error {
	var dueSlot time.Time
	if rf.Schedule != "" {
		var lastFired time.Time
		if hasState && st.LastScheduledMin != nil {
			lastFired = time.Unix((*st.LastScheduledMin)*60, 0).In(s.tz)
		}
		slots, err := domain.DueSlots(rf.Schedule, s.tz, lastFired, now, rf.CatchUp)
		if err != nil || len(slots) == 0 {
			return err
		}
		dueSlot = slots[0]
	} else if rf.At != nil {
		if (hasState && st.LastFiredAt != nil) || now.Before(*rf.At) {
			return nil
		}
		dueSlot = *rf.At
	}

	ag, ok, err := s.store.Read().GetAgentBySlug(ctx, rf.Agent)
	if err != nil || !ok || ag.Status != domain.AgentActive {
		return err
	}

	slotMin := dueSlot.Unix() / 60

	// Check if previous routine task is still non-final -> skip with run_events(type="skip")
	if hasState && st.ActiveTaskID != nil && *st.ActiveTaskID != "" {
		prevTk, pok, perr := s.store.Read().GetTask(ctx, *st.ActiveTaskID)
		if perr == nil && pok && !domain.TaskStatus(prevTk.Status).Final() {
			return s.store.WithTx(ctx, func(tx app.Tx) error {
				st.Slug = rf.Slug
				st.LastScheduledMin = &slotMin
				st.UpdatedAt = nowMs
				if err := tx.UpsertRoutineState(ctx, st); err != nil {
					return err
				}
				return tx.InsertRunEvent(ctx, app.RunEventRecord{
					RunID:     "routine:" + rf.Slug,
					AgentID:   ag.ID,
					Type:      "skip",
					DataJSON:  fmt.Sprintf(`{"slug":%q,"active_task_id":%q}`, rf.Slug, prevTk.ID),
					CreatedAt: nowMs,
				})
			})
		}
	}

	rootOrigin := domain.OriginRoutine
	if rf.Origin == "inbound" {
		rootOrigin = domain.OriginExternal
	}
	idemKey := fmt.Sprintf("routine:%s:%d", rf.Slug, slotMin)

	return s.store.WithTx(ctx, func(tx app.Tx) error {
		seen, err := tx.HasWakeupIdemKey(ctx, idemKey)
		if err != nil || seen {
			return err
		}
		taskID := s.ids.New()
		agID := ag.ID
		if err := tx.InsertTask(ctx, app.TaskRecord{
			ID:              taskID,
			RootID:          taskID,
			RootOrigin:      rootOrigin,
			Depth:           0,
			Title:           fmt.Sprintf("Routine: %s", rf.Slug),
			Description:     strings.TrimSpace(rf.Body),
			Status:          string(domain.TaskTodo),
			AssigneeAgentID: &agID,
			CreatedAt:       nowMs,
			UpdatedAt:       nowMs,
		}); err != nil {
			return err
		}

		convID, err := tx.EnsureTaskConversation(ctx, ag.ID, taskID, s.ids.New())
		if err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{"routine": rf.Slug, "task_id": taskID})
		if s.actors != nil {
			if _, _, err := s.actors.Wake(ctx, tx, actor.WakeInput{
				AgentID:        ag.ID,
				Kind:           domain.WakeRoutine,
				Priority:       domain.PrioRoutine,
				ConversationID: &convID,
				Payload:        payload,
				IdempotencyKey: &idemKey,
			}); err != nil {
				return err
			}
		}

		st.Slug = rf.Slug
		st.LastScheduledMin = &slotMin
		st.LastFiredAt = &nowMs
		st.ActiveTaskID = &taskID
		st.BadFileReported = false
		st.UpdatedAt = nowMs
		return tx.UpsertRoutineState(ctx, st)
	})
}

// RecordRoutineFailure increments consecutive_failures and auto-disables the routine in SQLite (without mutating the vault file) on the 3rd consecutive failure.
func (s *Scheduler) RecordRoutineFailure(ctx context.Context, slug string, now time.Time) error {
	nowMs := now.UnixMilli()
	return s.store.WithTx(ctx, func(tx app.Tx) error {
		st, ok, err := tx.GetRoutineState(ctx, slug)
		if err != nil {
			return err
		}
		if !ok {
			st = app.RoutineStateRecord{Slug: slug}
		}
		st.ConsecutiveFailures++
		st.ActiveTaskID = nil
		st.UpdatedAt = nowMs
		justDisabled := false
		if st.ConsecutiveFailures >= maxRoutineFailures && !st.AutoDisabled {
			st.AutoDisabled = true
			justDisabled = true
		}
		if err := tx.UpsertRoutineState(ctx, st); err != nil {
			return err
		}
		if justDisabled {
			refKind := "routine"
			refID := slug
			dedupe := "routine:disabled:" + slug
			return tx.InsertInboxItem(ctx, app.InboxItemRecord{
				ID:        s.ids.New(),
				Kind:      "error",
				Title:     fmt.Sprintf("Routine %s đã tự tắt sau 3 lần lỗi liên tiếp", slug),
				Body:      fmt.Sprintf("Routine %s thất bại %d lần liên tiếp và đã được tạm tắt trong DB.", slug, st.ConsecutiveFailures),
				RefKind:   &refKind,
				RefID:     &refID,
				Urgency:   "high",
				DedupeKey: &dedupe,
				CreatedAt: nowMs,
			})
		}
		return nil
	})
}

// ComputeNextHeartbeat calculates the next heartbeat timestamp jittered within ±10% of the schedule period.
func ComputeNextHeartbeat(schedule string, tz *time.Location, from time.Time, seed string) (time.Time, error) {
	next, period, err := domain.NextCronPeriod(schedule, tz, from)
	if err != nil {
		return time.Time{}, err
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(seed))
	_, _ = fmt.Fprintf(h, ":%d", rand.Uint64())
	u := float64(h.Sum64()%1_000_000) / 1_000_000.0 // [0, 1)
	offsetSec := (u*2.0 - 1.0) * heartbeatJitterPct * period.Seconds()
	return next.Add(time.Duration(offsetSec * float64(time.Second))), nil
}

// FireHeartbeats checks due agent heartbeats, evaluates Go SQLite preconditions (S5), creates an auto-closing task, and schedules the next jittered slot.
func (s *Scheduler) FireHeartbeats(ctx context.Context, now time.Time) error {
	nowMs := now.UnixMilli()
	due, err := s.store.Read().ListDueHeartbeatAgents(ctx, nowMs, 200)
	if err != nil || len(due) == 0 {
		return err
	}

	for _, ag := range due {
		cronExpr := strings.TrimSpace(ag.Heartbeat)
		if cronExpr == "" {
			cronExpr = "0 * * * *"
		}
		nextTime, cErr := ComputeNextHeartbeat(cronExpr, s.tz, now, ag.ID)
		if cErr != nil {
			continue
		}
		nextMs := nextTime.UnixMilli()

		hasWork, wErr := s.store.Read().HasHeartbeatWork(ctx, ag.ID, ag.LastRunAt)
		if wErr != nil {
			return wErr
		}

		err := s.store.WithTx(ctx, func(tx app.Tx) error {
			if err := tx.SetAgentHeartbeatSchedule(ctx, ag.ID, cronExpr, &nextMs); err != nil {
				return err
			}
			if !hasWork {
				return nil
			}
			taskID := s.ids.New()
			agID := ag.ID
			if err := tx.InsertTask(ctx, app.TaskRecord{
				ID:              taskID,
				RootID:          taskID,
				RootOrigin:      domain.OriginAgent,
				Depth:           0,
				Title:           fmt.Sprintf("Heartbeat: %s", ag.Slug),
				Description:     "Periodic heartbeat check (auto-closed on completion).",
				Status:          string(domain.TaskTodo),
				AssigneeAgentID: &agID,
				CreatedAt:       nowMs,
				UpdatedAt:       nowMs,
			}); err != nil {
				return err
			}
			convID, err := tx.EnsureTaskConversation(ctx, ag.ID, taskID, s.ids.New())
			if err != nil {
				return err
			}
			if s.actors != nil {
				payload, _ := json.Marshal(map[string]any{"heartbeat": true, "task_id": taskID})
				_, _, err = s.actors.Wake(ctx, tx, actor.WakeInput{
					AgentID:        ag.ID,
					Kind:           domain.WakeHeartbeat,
					Priority:       domain.PrioMaintenance,
					ConversationID: &convID,
					Payload:        payload,
				})
				return err
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func looksLikeInboundRoutine(rf domain.RoutineFile) bool {
	for _, c := range rf.Connections {
		lc := strings.ToLower(c)
		if strings.Contains(lc, "mail") || strings.Contains(lc, "inbox") || strings.Contains(lc, "imap") || strings.Contains(lc, "webhook") {
			return true
		}
	}
	return false
}

func (s *Scheduler) reportBadRoutineFile(ctx context.Context, slug, rel string, st app.RoutineStateRecord, msg string, nowMs int64) error {
	return s.store.WithTx(ctx, func(tx app.Tx) error {
		st.Slug = slug
		st.BadFileReported = true
		st.UpdatedAt = nowMs
		if err := tx.UpsertRoutineState(ctx, st); err != nil {
			return err
		}
		refKind := "routine"
		refID := slug
		dedupe := "routine:bad:" + slug
		return tx.InsertInboxItem(ctx, app.InboxItemRecord{
			ID:        s.ids.New(),
			Kind:      "error",
			Title:     fmt.Sprintf("File routine lỗi: %s", rel),
			Body:      msg,
			RefKind:   &refKind,
			RefID:     &refID,
			Urgency:   "high",
			DedupeKey: &dedupe,
			CreatedAt: nowMs,
		})
	})
}

func (s *Scheduler) reportInboundWarning(ctx context.Context, rf domain.RoutineFile, nowMs int64) error {
	return s.store.WithTx(ctx, func(tx app.Tx) error {
		refKind := "routine"
		refID := rf.Slug
		dedupe := "routine:inbound-warn:" + rf.Slug
		return tx.InsertInboxItem(ctx, app.InboxItemRecord{
			ID:        s.ids.New(),
			Kind:      "warning",
			Title:     fmt.Sprintf("Cảnh báo nguồn dữ liệu ngoài: routine %s", rf.Slug),
			Body:      "Routine đọc hộp thư/kết nối ngoài nhưng chưa khai 'origin: inbound'.",
			RefKind:   &refKind,
			RefID:     &refID,
			Urgency:   "normal",
			DedupeKey: &dedupe,
			CreatedAt: nowMs,
		})
	})
}
