package run_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/app/run"
	"max/internal/domain"
	"max/internal/testutil/fakeengine"
	"max/internal/testutil/testdb"
)

func TestQuotaStopped(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	actorSvc, claimed, _ := setupClaimedRun(t, st, now, false, 0)

	resetAt := now.Add(45 * time.Minute)
	eng := fakeengine.New(domain.EngineAgy,
		app.EngineEvent{Type: "session", SessionID: "s-quota"},
		app.EngineEvent{Type: "limit", LimitResetAt: &resetAt},
	)
	svc := run.NewService(run.Deps{
		Store:   st,
		Actors:  actorSvc,
		Engines: map[domain.EngineName]app.Engine{domain.EngineAgy: eng},
		Prompts: stubPrompts{},
		Vault:   &stubVault{},
		Clock:   &stubClock{now: now.Add(time.Second)},
		IDs:     app.UUIDv7Gen{},
	})

	out, err := svc.Execute(ctx, claimed)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.Status != domain.RunQuotaStopped {
		t.Fatalf("Status = %q, want quota_stopped", out.Status)
	}

	wRec, _, _ := st.Read().GetWakeup(ctx, claimed.AgentID, claimed.Wakeups[0].ID)
	if wRec.Status != "queued" || wRec.Attempts != 0 {
		t.Fatalf("wakeup = (status=%q, attempts=%d), want (queued, 0 — attempts not incremented)", wRec.Status, wRec.Attempts)
	}
	qs, ok, err := st.Read().GetQuotaState(ctx, domain.EngineAgy)
	if err != nil || !ok || qs.LimitedUntil == nil || *qs.LimitedUntil != resetAt.UnixMilli() {
		t.Fatalf("quota_state = (%+v, %v, %v), want LimitedUntil=%d", qs, ok, err, resetAt.UnixMilli())
	}
}

func TestRunTokenInvalidAfterEnd(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	actorSvc, claimed, _ := setupClaimedRun(t, st, now, false, 0)

	var seenToken string
	var activeDuringRun bool
	eng := fakeengine.New(domain.EngineAgy)
	var svc *run.Service
	eng.SetDynamic(func(_ context.Context, spec app.RunSpec, _ app.PermissionFunc, out chan<- app.EngineEvent) {
		seenToken = spec.RunToken
		_, activeDuringRun = svc.ResolveRunToken(spec.RunToken)
		out <- app.EngineEvent{Type: "session", SessionID: "s-tok"}
		out <- app.EngineEvent{Type: "final", InputTokens: 10, OutputTokens: 5}
	})

	svc = run.NewService(run.Deps{
		Store:   st,
		Actors:  actorSvc,
		Engines: map[domain.EngineName]app.Engine{domain.EngineAgy: eng},
		Prompts: stubPrompts{},
		Vault:   &stubVault{},
		Clock:   &stubClock{now: now},
		IDs:     app.UUIDv7Gen{},
	})

	if _, err := svc.Execute(ctx, claimed); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if seenToken == "" || !activeDuringRun {
		t.Fatalf("expected non-empty token active during run (token=%q active=%v)", seenToken, activeDuringRun)
	}
	if _, ok := svc.ResolveRunToken(seenToken); ok {
		t.Fatal("expected run token to be invalidated immediately after Finalize")
	}
}

func TestCheckoutCleared(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	actorSvc, claimed, taskID := setupClaimedRun(t, st, now, true, 0)

	if err := st.WithTx(ctx, func(tx app.Tx) error {
		tk, _, _ := tx.GetTask(ctx, taskID)
		runID := claimed.RunID
		tk.CheckoutRunID = &runID
		// Update checkout_run_id via raw insert replacement or helper: set directly via SQLite store test fixture.
		return nil
	}); err != nil {
		t.Fatalf("prep: %v", err)
	}
	// Seed a second task checked out by claimed.RunID to verify ClearTaskCheckout clears it.
	checkedTaskID := "task-checked"
	if err := st.WithTx(ctx, func(tx app.Tx) error {
		runID := claimed.RunID
		return tx.InsertTask(ctx, app.TaskRecord{
			ID: checkedTaskID, RootID: checkedTaskID, RootOrigin: domain.OriginOwner,
			Title: "Checked", Description: "D", Status: "in_progress",
			CheckoutRunID: &runID, CreatedAt: now.UnixMilli(), UpdatedAt: now.UnixMilli(),
		})
	}); err != nil {
		t.Fatalf("InsertTask checked: %v", err)
	}

	eng := fakeengine.New(domain.EngineAgy,
		app.EngineEvent{Type: "session", SessionID: "s-chk"},
		app.EngineEvent{Type: "final"},
	)
	svc := run.NewService(run.Deps{
		Store:   st,
		Actors:  actorSvc,
		Engines: map[domain.EngineName]app.Engine{domain.EngineAgy: eng},
		Prompts: stubPrompts{},
		Vault:   &stubVault{},
		Clock:   &stubClock{now: now.Add(time.Second)},
		IDs:     app.UUIDv7Gen{},
	})

	if _, err := svc.Execute(ctx, claimed); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	tk, _, _ := st.Read().GetTask(ctx, checkedTaskID)
	if tk.CheckoutRunID != nil {
		t.Fatalf("CheckoutRunID = %v, want nil", tk.CheckoutRunID)
	}
}

func TestColdResumeStartsFreshSession(t *testing.T) {
	cases := []struct {
		name        string
		age         time.Duration
		inputTokens int64
		wantRebuild bool
	}{
		{"cold >60m and >60k tokens rebuilds", 61 * time.Minute, 60001, true},
		{"warm 59m resumes existing session", 59 * time.Minute, 60001, false},
		{"small 59999 tokens resumes existing session", 61 * time.Minute, 59999, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st := testdb.New(t)
			now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
			clk := &stubClock{now: now}
			actorSvc := actor.NewService(st, clk, app.UUIDv7Gen{})

			convID := "conv-cold"
			agentID := "agent-1"
			lastEng := string(domain.EngineAgy)
			endedAt := now.Add(-tc.age).UnixMilli()
			startedAt := endedAt - 5000

			if err := st.WithTx(ctx, func(tx app.Tx) error {
				if err := tx.InsertTeam(ctx, app.TeamRecord{ID: "t1", Slug: "core", Name: "Core", MaxRunning: 2, Status: "active"}); err != nil {
					return err
				}
				if err := tx.InsertAgent(ctx, app.AgentRecord{
					ID: agentID, Slug: "chief", Name: "Chief", TeamID: "t1",
					EnginesJSON: `["agy"]`, EngineMask: 4, ModelsJSON: `{}`,
					Autonomy: domain.AutonomyAuto, Browser: domain.BrowserNone,
					SkillsJSON: `[]`, ParamsJSON: `{}`, ConfigRev: "r1", Status: domain.AgentActive,
				}); err != nil {
					return err
				}
				if err := tx.InsertConversation(ctx, app.ConversationRecord{
					ID: convID, AgentID: agentID, Sessions: `{"agy":"old-sess-1"}`, LastEngine: &lastEng,
				}); err != nil {
					return err
				}
				if err := tx.InsertRun(ctx, app.RunRecord{
					ID: "prev-run", AgentID: agentID, ConversationID: convID,
					Status: domain.RunSucceeded, Priority: domain.PrioTask, Engine: domain.EngineAgy,
					Model: "default", InputTokens: tc.inputTokens, StartedAt: startedAt, EndedAt: &endedAt,
				}); err != nil {
					return err
				}
				_, _, err := actorSvc.Wake(ctx, tx, actor.WakeInput{
					AgentID: agentID, Kind: domain.WakeMessage, Priority: domain.PrioInteractive, ConversationID: &convID,
				})
				return err
			}); err != nil {
				t.Fatalf("seed: %v", err)
			}

			batch, err := actorSvc.ClaimBatch(ctx, actor.Capacity{Free: map[domain.EngineName]int{domain.EngineAgy: 1}}, now)
			if err != nil || len(batch) != 1 {
				t.Fatalf("ClaimBatch: %v", err)
			}

			eng := fakeengine.New(domain.EngineAgy,
				app.EngineEvent{Type: "session", SessionID: "new-sess-2"},
				app.EngineEvent{Type: "final", InputTokens: 100, OutputTokens: 20},
			)
			svc := run.NewService(run.Deps{
				Store:   st,
				Actors:  actorSvc,
				Engines: map[domain.EngineName]app.Engine{domain.EngineAgy: eng},
				Prompts: stubPrompts{},
				Vault:   &stubVault{},
				Clock:   clk,
				IDs:     app.UUIDv7Gen{},
			})

			if _, err := svc.Execute(ctx, batch[0]); err != nil {
				t.Fatalf("Execute: %v", err)
			}

			specs := eng.Specs()
			if len(specs) != 1 {
				t.Fatalf("len(specs) = %d, want 1", len(specs))
			}
			events, _ := st.Read().ListRunEvents(ctx, batch[0].RunID)
			hasRebuildEvent := false
			for _, ev := range events {
				if ev.Type == "session_rebuilt" {
					hasRebuildEvent = true
				}
			}

			if tc.wantRebuild {
				if specs[0].SessionID != "" {
					t.Fatalf("SessionID = %q, want empty on cold rebuild", specs[0].SessionID)
				}
				if !strings.Contains(specs[0].Prompt, "<history>") {
					t.Fatalf("Prompt missing <history>: %q", specs[0].Prompt)
				}
				if !hasRebuildEvent {
					t.Fatal("expected run_events entry with type='session_rebuilt'")
				}
			} else {
				if specs[0].SessionID != "old-sess-1" {
					t.Fatalf("SessionID = %q, want old-sess-1", specs[0].SessionID)
				}
				if hasRebuildEvent {
					t.Fatal("unexpected session_rebuilt event on warm resume")
				}
			}
		})
	}
}

type flakyTxStore struct {
	app.Store
	failRemaining atomic.Int32
}

func (f *flakyTxStore) WithTx(ctx context.Context, fn func(tx app.Tx) error) error {
	for {
		rem := f.failRemaining.Load()
		if rem <= 0 {
			break
		}
		if f.failRemaining.CompareAndSwap(rem, rem-1) {
			return errors.New("simulated transient sqlite error")
		}
	}
	return f.Store.WithTx(ctx, fn)
}

func TestFinalizeSurvivesStoreError(t *testing.T) {
	ctx := context.Background()
	baseStore := testdb.New(t)
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

	// Subcase 1: First Finalize attempt fails, retry succeeds.
	actorSvc1, claimed1, _ := setupClaimedRun(t, baseStore, now, false, 0)
	flaky1 := &flakyTxStore{Store: baseStore}
	flaky1.failRemaining.Store(1)

	eng1 := fakeengine.New(domain.EngineAgy,
		app.EngineEvent{Type: "session", SessionID: "s-retry"},
		app.EngineEvent{Type: "final"},
	)
	svc1 := run.NewService(run.Deps{
		Store:           flaky1,
		Actors:          actorSvc1,
		Engines:         map[domain.EngineName]app.Engine{domain.EngineAgy: eng1},
		Prompts:         stubPrompts{},
		Vault:           &stubVault{},
		Clock:           &stubClock{now: now.Add(time.Second)},
		IDs:             app.UUIDv7Gen{},
		FinalizeBackoff: time.Millisecond,
	})
	if _, err := svc1.Execute(ctx, claimed1); err != nil {
		t.Fatalf("expected Execute to survive 1 transient store error, got %v", err)
	}
	r1, _, _ := baseStore.Read().GetRun(ctx, claimed1.RunID)
	if r1.Status != domain.RunSucceeded {
		t.Fatalf("r1.Status = %q, want succeeded", r1.Status)
	}

	// Subcase 2: All 3 Finalize attempts fail -> run stays 'running' & wakeup stays 'claimed' for Recover.
	baseStore2 := testdb.New(t)
	actorSvc2, claimed2, _ := setupClaimedRun(t, baseStore2, now, false, 0)
	flaky2 := &flakyTxStore{Store: baseStore2}
	flaky2.failRemaining.Store(10)

	svc2 := run.NewService(run.Deps{
		Store:           flaky2,
		Actors:          actorSvc2,
		Engines:         map[domain.EngineName]app.Engine{domain.EngineAgy: eng1},
		Prompts:         stubPrompts{},
		Vault:           &stubVault{},
		Clock:           &stubClock{now: now.Add(time.Second)},
		IDs:             app.UUIDv7Gen{},
		FinalizeBackoff: time.Millisecond,
	})
	if _, err := svc2.Execute(ctx, claimed2); err == nil {
		t.Fatal("expected error when all 3 Finalize retries fail")
	}
	r2, _, _ := baseStore2.Read().GetRun(ctx, claimed2.RunID)
	if r2.Status != domain.RunRunning {
		t.Fatalf("r2.Status = %q, want running so Recover can reclaim it", r2.Status)
	}
	nRec, err := actorSvc2.Recover(ctx, now.Add(2*time.Minute))
	if err != nil || nRec != 1 {
		t.Fatalf("Recover = (%d, %v), want (1, nil)", nRec, err)
	}
	queued, _ := baseStore2.Read().ListQueuedWakeups(ctx, claimed2.AgentID)
	if len(queued) != 1 {
		t.Fatalf("expected wakeup preserved and requeued by Recover, got %d", len(queued))
	}
}
