package actor

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"time"

	"max/internal/app"
	"max/internal/domain"
)

const drainBatchCap = 20

// Claimed describes a run claimed inside ClaimBatch and ready to be spawned outside the DB transaction.
type Claimed = app.Claimed

// Capacity conveys available engine process slots and subscription quota admission at dispatch time.
type Capacity struct {
	Free  map[domain.EngineName]int
	Admit func(domain.EngineName, domain.Priority) domain.Admission
}

// ClaimBatch atomically claims ready agents, drains up to 20 wakeups of a single conversation per agent,
// creates running rows, and updates active_run_id + teams.running in one BEGIN IMMEDIATE transaction.
func (s *Service) ClaimBatch(ctx context.Context, cap Capacity, now time.Time) ([]Claimed, error) {
	free := maps.Clone(cap.Free)
	totalFree := 0
	mask := 0
	for _, eng := range []domain.EngineName{domain.EngineAgy, domain.EngineClaude, domain.EngineCodex} {
		n := free[eng]
		if n <= 0 {
			continue
		}
		if cap.Admit != nil && cap.Admit(eng, domain.PrioInteractive) == domain.DeferLimited {
			continue
		}
		totalFree += n
		mask |= domain.EngineBit(eng)
	}
	if totalFree <= 0 || mask == 0 {
		return nil, nil
	}

	nowMs := now.UnixMilli()
	var out []Claimed

	err := s.store.WithTx(ctx, func(tx app.Tx) error {
		cands, err := tx.ListCandidateAgents(ctx, nowMs, mask, totalFree*3)
		if err != nil {
			return err
		}
		teamClaimed := make(map[domain.ID]int)

		for _, cand := range cands {
			if totalFree <= 0 {
				break
			}
			if cand.TeamRunning+teamClaimed[cand.TeamID] >= cand.TeamMaxRun {
				continue
			}

			top, ok, err := tx.PeekTopDueWakeup(ctx, cand.ID, nowMs)
			if err != nil {
				return err
			}
			if !ok {
				if err := tx.RecomputeAgentReady(ctx, cand.ID, nowMs); err != nil {
					return err
				}
				continue
			}
			if cand.CooldownUntil > nowMs && top.Priority < domain.PrioInteractive {
				continue
			}

			origConvID := top.ConversationID
			var convID domain.ID
			if origConvID != nil && *origConvID != "" {
				convID = *origConvID
			} else {
				sysConv, err := tx.EnsureSystemConversation(ctx, cand.ID, s.ids.New())
				if err != nil {
					return err
				}
				convID = sysConv
			}

			conv, found, err := tx.GetConversation(ctx, convID)
			if err != nil {
				return err
			}
			if !found {
				if err := tx.InsertConversation(ctx, app.ConversationRecord{
					ID: convID, AgentID: cand.ID, Sessions: `{}`,
				}); err != nil {
					return err
				}
				conv = app.ConversationRecord{ID: convID, AgentID: cand.ID, Sessions: `{}`}
			}

			eng, sessID, changed, ok := chooseEngine(cand.EnginesJSON, conv, top.Priority, free, cap.Admit)
			if !ok {
				continue
			}

			runID := s.ids.New()
			drained, err := tx.DrainDueWakeupsForConv(ctx, cand.ID, origConvID, convID, runID, nowMs, drainBatchCap)
			if err != nil {
				return err
			}
			if len(drained) == 0 {
				continue
			}

			maxAttempt := 1
			wakeups := make([]Wakeup, 0, len(drained))
			for _, w := range drained {
				if w.Attempts > maxAttempt {
					maxAttempt = w.Attempts
				}
				wakeups = append(wakeups, Wakeup{
					ID:             w.ID,
					Kind:           w.Kind,
					Payload:        json.RawMessage(w.Payload),
					CoalescedCount: w.CoalescedCount,
					Attempts:       w.Attempts,
					CauseRunID:     w.CauseRunID,
					Hops:           w.Hops,
				})
			}

			if err := tx.InsertRun(ctx, app.RunRecord{
				ID:             runID,
				AgentID:        cand.ID,
				ConversationID: convID,
				TaskID:         conv.TaskID,
				Status:         domain.RunRunning,
				Priority:       top.Priority,
				Engine:         eng,
				Model:          chooseModel(cand.ModelsJSON, eng),
				Attempt:        maxAttempt,
				ConfigRev:      cand.ConfigRev,
				StartedAt:      nowMs,
			}); err != nil {
				return fmt.Errorf("insert claimed run: %w", err)
			}
			if err := tx.SetAgentActiveRun(ctx, cand.ID, &runID); err != nil {
				return err
			}
			if err := tx.AdjustTeamRunning(ctx, cand.TeamID, 1); err != nil {
				return err
			}
			if err := tx.RecomputeAgentReady(ctx, cand.ID, nowMs); err != nil {
				return err
			}

			free[eng]--
			totalFree--
			teamClaimed[cand.TeamID]++

			out = append(out, Claimed{
				RunID:          runID,
				AgentID:        cand.ID,
				ConversationID: convID,
				TaskID:         conv.TaskID,
				Engine:         eng,
				Priority:       top.Priority,
				Wakeups:        wakeups,
				SessionID:      sessID,
				EngineChanged:  changed,
				ConfigRev:      cand.ConfigRev,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func chooseEngine(
	enginesJSON string,
	conv app.ConversationRecord,
	prio domain.Priority,
	free map[domain.EngineName]int,
	admit func(domain.EngineName, domain.Priority) domain.Admission,
) (picked domain.EngineName, sessionID string, changed bool, ok bool) {
	var declared []domain.EngineName
	if err := json.Unmarshal([]byte(enginesJSON), &declared); err != nil || len(declared) == 0 {
		declared = []domain.EngineName{domain.EngineAgy}
	}
	sessions := make(map[string]string)
	_ = json.Unmarshal([]byte(conv.Sessions), &sessions)

	canUse := func(e domain.EngineName) bool {
		if free[e] <= 0 {
			return false
		}
		if admit != nil && admit(e, prio) != domain.AdmitNow {
			return false
		}
		return true
	}

	if conv.LastEngine != nil && *conv.LastEngine != "" {
		last := domain.EngineName(*conv.LastEngine)
		for _, d := range declared {
			if d == last && canUse(last) {
				return last, sessions[string(last)], false, true
			}
		}
	}

	for _, eng := range declared {
		if !canUse(eng) {
			continue
		}
		priorOther := false
		if conv.LastEngine != nil && *conv.LastEngine != "" && *conv.LastEngine != string(eng) {
			priorOther = true
		} else {
			for k, v := range sessions {
				if k != string(eng) && v != "" {
					priorOther = true
					break
				}
			}
		}
		if priorOther {
			return eng, "", true, true
		}
		return eng, sessions[string(eng)], false, true
	}
	return "", "", false, false
}

func chooseModel(modelsJSON string, eng domain.EngineName) string {
	var m map[string]string
	if err := json.Unmarshal([]byte(modelsJSON), &m); err == nil {
		if v := m[string(eng)]; v != "" {
			return v
		}
	}
	return "default"
}
