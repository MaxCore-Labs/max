package limits_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/app/limits"
	runapp "max/internal/app/run"
	"max/internal/domain"
	"max/internal/testutil/testdb"
)

type mutableClock struct{ now time.Time }

func (c *mutableClock) Now() time.Time { return c.now }

func ptrInt64(v int64) *int64 { return &v }

func TestRunsPerHourCap(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	now := time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC)
	clk := &mutableClock{now: now}
	ids := app.UUIDv7Gen{}
	actors := actor.NewService(st, clk, ids)
	q := limits.NewQuota(st, clk, limits.QuotaConfig{TZ: "Asia/Ho_Chi_Minh"})

	err := st.WithTx(ctx, func(tx app.Tx) error {
		if err := tx.InsertTeam(ctx, app.TeamRecord{ID: "t1", Slug: "core", Name: "Core", MaxRunning: 4, Status: "active"}); err != nil {
			return err
		}
		if err := tx.InsertAgent(ctx, app.AgentRecord{
			ID: "a1", Slug: "worker", Name: "Worker", TeamID: "t1",
			EnginesJSON: `["agy"]`, EngineMask: 4, Autonomy: domain.AutonomyAuto,
			Browser: domain.BrowserNone, ConfigRev: "1", Status: domain.AgentActive,
		}); err != nil {
			return err
		}
		convID, err := tx.EnsureSystemConversation(ctx, "a1", "conv-a1")
		if err != nil {
			return err
		}
		// Seed 12 completed runs within the last hour
		for i := range 12 {
			start := now.Add(-time.Duration(50-i) * time.Minute).UnixMilli()
			end := start + 1000
			if err := tx.InsertRun(ctx, app.RunRecord{
				ID: fmt.Sprintf("r-hr-%02d", i), AgentID: "a1", ConversationID: convID,
				Status: domain.RunSucceeded, Priority: domain.PrioTask, Engine: domain.EngineAgy,
				Model: "default", StartedAt: start, EndedAt: &end,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	ok, reason := q.AgentAllowed(ctx, "a1", "t1", now)
	if ok || reason == "" {
		t.Fatalf("AgentAllowed after 12 runs/hr = (%v, %q), want (false, non-empty)", ok, reason)
	}

	// Queue a PrioTask wakeup -> must NOT be claimed
	_ = st.WithTx(ctx, func(tx app.Tx) error {
		_, _, err := actors.Wake(ctx, tx, actor.WakeInput{AgentID: "a1", Kind: domain.WakeTaskAssigned, Priority: domain.PrioTask})
		return err
	})
	capCfg := actor.Capacity{
		Free:         map[domain.EngineName]int{domain.EngineAgy: 2},
		AgentAllowed: q.AgentAllowed,
	}
	claimed, err := actors.ClaimBatch(ctx, capCfg, now)
	if err != nil || len(claimed) != 0 {
		t.Fatalf("expected PrioTask blocked by 12 runs/hr cap, got %+v (err=%v)", claimed, err)
	}

	// Queue a PrioInteractive wakeup -> MUST still be claimed!
	_ = st.WithTx(ctx, func(tx app.Tx) error {
		_, _, err := actors.Wake(ctx, tx, actor.WakeInput{AgentID: "a1", Kind: domain.WakeMessage, Priority: domain.PrioInteractive})
		return err
	})
	claimedInt, err := actors.ClaimBatch(ctx, capCfg, now)
	if err != nil || len(claimedInt) != 1 || claimedInt[0].Priority != domain.PrioInteractive {
		t.Fatalf("expected PrioInteractive allowed despite 12 runs/hr cap, got %+v (err=%v)", claimedInt, err)
	}
}

func TestAgentDailyTokenCap(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	hcm, _ := time.LoadLocation("Asia/Ho_Chi_Minh")
	// 2026-11-20 23:30 ICT
	now := time.Date(2026, 11, 20, 23, 30, 0, 0, hcm)
	clk := &mutableClock{now: now}
	q := limits.NewQuota(st, clk, limits.QuotaConfig{TZ: "Asia/Ho_Chi_Minh"})

	_ = st.WithTx(ctx, func(tx app.Tx) error {
		_ = tx.InsertTeam(ctx, app.TeamRecord{ID: "t1", Slug: "core", Name: "Core", MaxRunning: 4, Status: "active"})
		return tx.InsertAgent(ctx, app.AgentRecord{
			ID: "a-cap", Slug: "capped", Name: "Capped", TeamID: "t1",
			EnginesJSON: `["agy"]`, EngineMask: 4, Autonomy: domain.AutonomyAuto,
			Browser: domain.BrowserNone, QuotaTokensDay: ptrInt64(10000),
			ConfigRev: "1", Status: domain.AgentActive,
		})
	})

	// Exceed daily cap (12,000 > 10,000)
	if err := st.WithTx(ctx, func(tx app.Tx) error {
		return q.Record(ctx, tx, domain.EngineAgy, "a-cap", 8000, 4000, now)
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	// Second record on same day must not create duplicate notification
	_ = st.WithTx(ctx, func(tx app.Tx) error {
		return q.Record(ctx, tx, domain.EngineAgy, "a-cap", 100, 100, now)
	})

	ag, _, _ := st.Read().GetAgent(ctx, "a-cap")
	if ag.Status != domain.AgentQuotaPaused {
		t.Fatalf("agent status=%q, want quota_paused", ag.Status)
	}
	inbox, _ := st.Read().ListInboxItems(ctx, 10)
	if len(inbox) != 1 {
		t.Fatalf("expected exactly 1 Inbox notification on quota_paused, got %d", len(inbox))
	}
	if ok, _ := q.AgentAllowed(ctx, "a-cap", "t1", now); ok {
		t.Fatal("expected AgentAllowed=false while quota_paused")
	}

	// Advance past midnight in Asia/Ho_Chi_Minh (2026-11-21 00:05 ICT) -> reactivates to active!
	clk.now = time.Date(2026, 11, 21, 0, 5, 0, 0, hcm)
	if err := q.RefreshDailyQuotas(ctx, clk.now); err != nil {
		t.Fatalf("RefreshDailyQuotas: %v", err)
	}
	agNext, _, _ := st.Read().GetAgent(ctx, "a-cap")
	if agNext.Status != domain.AgentActive {
		t.Fatalf("expected status='active' on new MAX_TZ day, got %q", agNext.Status)
	}
	if ok, reason := q.AgentAllowed(ctx, "a-cap", "t1", clk.now); !ok {
		t.Fatalf("expected AgentAllowed=true on new day, got false (%s)", reason)
	}
}

func TestTeamDailyTokenCap(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	now := time.Date(2026, 11, 20, 15, 0, 0, 0, time.UTC)
	clk := &mutableClock{now: now}
	q := limits.NewQuota(st, clk, limits.QuotaConfig{TZ: "UTC"})

	_ = st.WithTx(ctx, func(tx app.Tx) error {
		_ = tx.InsertTeam(ctx, app.TeamRecord{
			ID: "t-mkt", Slug: "marketing", Name: "Marketing", MaxRunning: 4,
			QuotaTokensDay: ptrInt64(20000), Status: "active",
		})
		_ = tx.InsertAgent(ctx, app.AgentRecord{
			ID: "a1", Slug: "m1", Name: "M1", TeamID: "t-mkt",
			EnginesJSON: `["agy"]`, EngineMask: 4, Autonomy: domain.AutonomyAuto,
			Browser: domain.BrowserNone, ConfigRev: "1", Status: domain.AgentActive,
		})
		return tx.InsertAgent(ctx, app.AgentRecord{
			ID: "a2", Slug: "m2", Name: "M2", TeamID: "t-mkt",
			EnginesJSON: `["agy"]`, EngineMask: 4, Autonomy: domain.AutonomyAuto,
			Browser: domain.BrowserNone, ConfigRev: "1", Status: domain.AgentActive,
		})
	})

	// Agent a1 consumes 21,000 tokens -> team cap (20,000) exceeded for sibling a2 as well
	if err := st.WithTx(ctx, func(tx app.Tx) error {
		return q.Record(ctx, tx, domain.EngineAgy, "a1", 15000, 6000, now)
	}); err != nil {
		t.Fatal(err)
	}

	ok, reason := q.AgentAllowed(ctx, "a2", "t-mkt", now)
	if ok || reason == "" {
		t.Fatalf("expected sibling a2 blocked by team daily token cap, got ok=%v reason=%q", ok, reason)
	}
}

func TestNoisyAgentDoesNotStarve(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	clk := &mutableClock{now: now}
	ids := app.UUIDv7Gen{}
	actors := actor.NewService(st, clk, ids)
	zeroDur := time.Duration(0)
	runSvc := runapp.NewService(runapp.Deps{Store: st, Actors: actors, Cooldown: &zeroDur, Clock: clk, IDs: ids})

	err := st.WithTx(ctx, func(tx app.Tx) error {
		if err := tx.InsertTeam(ctx, app.TeamRecord{ID: "t1", Slug: "core", Name: "Core", MaxRunning: 10, Status: "active"}); err != nil {
			return err
		}
		if err := tx.InsertAgent(ctx, app.AgentRecord{
			ID: "noisy", Slug: "noisy", Name: "Noisy", TeamID: "t1",
			EnginesJSON: `["agy"]`, EngineMask: 4, Autonomy: domain.AutonomyAuto,
			Browser: domain.BrowserNone, ConfigRev: "1", Status: domain.AgentActive,
		}); err != nil {
			return err
		}
		noisyConv, _ := tx.EnsureSystemConversation(ctx, "noisy", "conv-noisy")
		// Seed 500 distinct non-coalescible wakeups on noisy agent FIRST (earliest created_at)
		for w := range 500 {
			if err := tx.InsertWakeupRaw(ctx, app.WakeupRecord{
				ID: fmt.Sprintf("nw-%04d", w), AgentID: "noisy", ConversationID: &noisyConv,
				Kind: domain.WakeTaskAssigned, Priority: domain.PrioTask, Payload: `{}`,
				Status: "queued", CreatedAt: int64(w + 1),
			}); err != nil {
				return err
			}
		}
		if err := tx.RecomputeAgentReady(ctx, "noisy", now.UnixMilli()); err != nil {
			return err
		}

		// Seed 20 quiet agents AFTER noisy agent (higher created_at)
		for i := range 20 {
			agID := fmt.Sprintf("quiet-%02d", i)
			if err := tx.InsertAgent(ctx, app.AgentRecord{
				ID: agID, Slug: agID, Name: agID, TeamID: "t1",
				EnginesJSON: `["agy"]`, EngineMask: 4, Autonomy: domain.AutonomyAuto,
				Browser: domain.BrowserNone, ConfigRev: "1", Status: domain.AgentActive,
			}); err != nil {
				return err
			}
			qConv, _ := tx.EnsureSystemConversation(ctx, agID, "conv-"+agID)
			if err := tx.InsertWakeupRaw(ctx, app.WakeupRecord{
				ID: "qw-" + agID, AgentID: agID, ConversationID: &qConv,
				Kind: domain.WakeTaskAssigned, Priority: domain.PrioTask, Payload: `{}`,
				Status: "queued", CreatedAt: int64(1000 + i),
			}); err != nil {
				return err
			}
			if err := tx.RecomputeAgentReady(ctx, agID, now.UnixMilli()); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	noisyRuns := 0
	quietDone := make(map[string]bool)

	for round := range 15 {
		clk.now = clk.now.Add(time.Second)
		claimed, err := actors.ClaimBatch(ctx, actor.Capacity{Free: map[domain.EngineName]int{domain.EngineAgy: 3}}, clk.now)
		if err != nil {
			t.Fatalf("round %d ClaimBatch: %v", round, err)
		}
		if len(claimed) == 0 {
			break
		}
		for _, c := range claimed {
			if c.AgentID == "noisy" {
				noisyRuns++
				if noisyRuns == 4 && len(quietDone) < 20 {
					t.Fatalf("noisy agent started 4th run while only %d/20 quiet agents have run", len(quietDone))
				}
			} else {
				quietDone[c.AgentID] = true
			}
			if err := runSvc.Finalize(ctx, c.RunID, runapp.Outcome{Status: domain.RunSucceeded, Turns: 1}); err != nil {
				t.Fatalf("Finalize: %v", err)
			}
		}
		if len(quietDone) == 20 {
			break
		}
	}

	if len(quietDone) != 20 {
		t.Fatalf("expected all 20 quiet agents served, got %d", len(quietDone))
	}
}

func TestNoStarvationWhenCapacity(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	now := time.Date(2026, 11, 20, 11, 0, 0, 0, time.UTC)
	clk := &mutableClock{now: now}
	ids := app.UUIDv7Gen{}
	actors := actor.NewService(st, clk, ids)
	zeroDur := time.Duration(0)
	runSvc := runapp.NewService(runapp.Deps{Store: st, Actors: actors, Cooldown: &zeroDur, Clock: clk, IDs: ids})

	// 9 ready agents with 3 free slots -> every ready agent must be claimed within <= 3 ClaimBatch rounds (I11).
	_ = st.WithTx(ctx, func(tx app.Tx) error {
		_ = tx.InsertTeam(ctx, app.TeamRecord{ID: "t1", Slug: "core", Name: "Core", MaxRunning: 9, Status: "active"})
		for i := range 9 {
			agID := fmt.Sprintf("ag-%d", i)
			_ = tx.InsertAgent(ctx, app.AgentRecord{
				ID: agID, Slug: agID, Name: agID, TeamID: "t1",
				EnginesJSON: `["agy"]`, EngineMask: 4, Autonomy: domain.AutonomyAuto,
				Browser: domain.BrowserNone, ConfigRev: "1", Status: domain.AgentActive,
			})
			for w := range 5 {
				_, _, _ = actors.Wake(ctx, tx, actor.WakeInput{
					AgentID:  agID,
					Kind:     domain.WakeTaskAssigned,
					Priority: domain.PrioTask,
					Payload:  []byte(fmt.Sprintf(`{"w":%d}`, w)),
				})
			}
		}
		return nil
	})

	seenAtRound := make(map[string]int)
	for round := 1; round <= 3; round++ {
		clk.now = clk.now.Add(time.Second)
		claimed, err := actors.ClaimBatch(ctx, actor.Capacity{Free: map[domain.EngineName]int{domain.EngineAgy: 3}}, clk.now)
		if err != nil || len(claimed) != 3 {
			t.Fatalf("round %d: got %d claimed (err=%v), want 3", round, len(claimed), err)
		}
		for _, c := range claimed {
			if _, ok := seenAtRound[c.AgentID]; !ok {
				seenAtRound[c.AgentID] = round
			}
			_ = runSvc.Finalize(ctx, c.RunID, runapp.Outcome{Status: domain.RunSucceeded, Turns: 1})
		}
	}
	if len(seenAtRound) != 9 {
		t.Fatalf("I11 violation: expected all 9 ready agents claimed within 3 rounds, got %d (%+v)", len(seenAtRound), seenAtRound)
	}
}
