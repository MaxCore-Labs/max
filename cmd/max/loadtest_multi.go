package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"time"

	"max/internal/adapter/sqlite"
	"max/internal/app"
	"max/internal/domain"
)

func calcPercentiles(durs []time.Duration) (p50, p95, p99 time.Duration) {
	if len(durs) == 0 {
		return 0, 0, 0
	}
	cp := slices.Clone(durs)
	slices.Sort(cp)
	at := func(q float64) time.Duration {
		idx := int(float64(len(cp)-1) * q)
		return cp[idx]
	}
	return at(0.50), at(0.95), at(0.99)
}

func seedMultiLoadtest(ctx context.Context, st *sqlite.Store, cfg loadtestFlags) error {
	numTeams := max(5, cfg.slots*2)
	nowMs := time.Now().UnixMilli()
	idGen := app.UUIDv7Gen{}
	templates := []string{"researcher", "writer", "reviewer", "marketer", "monitor"}
	kinds := []domain.WakeKind{
		domain.WakeTaskAssigned,
		domain.WakeTaskUpdate,
		domain.WakeChildDone,
		domain.WakeRoutine,
		domain.WakeMessage,
	}
	prios := []domain.Priority{
		domain.PrioInteractive,
		domain.PrioTask,
		domain.PrioRoutine,
		domain.PrioMaintenance,
	}

	err := st.WithTx(ctx, func(tx app.Tx) error {
		for t := range numTeams {
			if err := tx.InsertTeam(ctx, app.TeamRecord{
				ID:         fmt.Sprintf("team-%03d", t),
				Slug:       fmt.Sprintf("team-%03d", t),
				Name:       fmt.Sprintf("Team %d", t),
				MaxRunning: max(4, cfg.slots),
				Status:     "active",
			}); err != nil {
				return err
			}
		}

		for a := range cfg.agents {
			agID := fmt.Sprintf("agent-%05d", a)
			taskID := fmt.Sprintf("task-%05d", a)
			convID := fmt.Sprintf("conv-%05d", a)
			tpl := templates[a%len(templates)]
			if err := tx.InsertAgent(ctx, app.AgentRecord{
				ID:           agID,
				Slug:         agID,
				Name:         agID,
				TemplateSlug: &tpl,
				TeamID:       fmt.Sprintf("team-%03d", a%numTeams),
				EnginesJSON:  `["agy"]`,
				EngineMask:   4,
				Autonomy:     domain.AutonomyAuto,
				Browser:      domain.BrowserNone,
				ConfigRev:    "lt-multi-1",
				Status:       domain.AgentActive,
			}); err != nil {
				return err
			}
			if err := tx.InsertTask(ctx, app.TaskRecord{
				ID:              taskID,
				RootID:          taskID,
				RootOrigin:      domain.OriginOwner,
				Depth:           a % 4,
				Title:           fmt.Sprintf("Multi Task %d", a),
				Description:     "Multi-agent coordination benchmark task",
				Status:          string(domain.TaskTodo),
				AssigneeAgentID: &agID,
				CreatedAt:       nowMs,
				UpdatedAt:       nowMs,
			}); err != nil {
				return err
			}
			if err := tx.InsertConversation(ctx, app.ConversationRecord{
				ID:       convID,
				AgentID:  agID,
				TaskID:   &taskID,
				Sessions: `{}`,
			}); err != nil {
				return err
			}
		}

		touchedAgents := make(map[string]struct{}, cfg.agents)
		for w := range cfg.wakeups {
			agIdx := w % cfg.agents
			agID := fmt.Sprintf("agent-%05d", agIdx)
			convID := fmt.Sprintf("conv-%05d", agIdx)
			kind := kinds[w%len(kinds)]
			prio := prios[rand.IntN(len(prios))]
			if err := tx.InsertWakeupRaw(ctx, app.WakeupRecord{
				ID:             idGen.New(),
				AgentID:        agID,
				ConversationID: &convID,
				Kind:           kind,
				Priority:       prio,
				Payload:        `{"scenario":"multi"}`,
				Status:         "queued",
				Hops:           w % 5,
				CreatedAt:      nowMs + int64(w),
			}); err != nil {
				return err
			}
			touchedAgents[agID] = struct{}{}
		}
		for agID := range touchedAgents {
			if err := tx.RecomputeAgentReady(ctx, agID, nowMs+int64(cfg.wakeups)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return st.ExecRaw(ctx, `ANALYZE`)
}
