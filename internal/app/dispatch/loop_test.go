package dispatch_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/app/dispatch"
	"max/internal/app/limits"
	"max/internal/app/run"
	"max/internal/domain"
	"max/internal/testutil/fakeengine"
	"max/internal/testutil/testdb"
)

func seedNReadyAgents(t *testing.T, st app.Store, actorSvc *actor.Service, n int) []string {
	t.Helper()
	ctx := context.Background()
	var ids []string
	if err := st.WithTx(ctx, func(tx app.Tx) error {
		if err := tx.InsertTeam(ctx, app.TeamRecord{ID: "t1", Slug: "core", Name: "Core", MaxRunning: 8, Status: "active"}); err != nil {
			return err
		}
		for i := range n {
			aid := fmt.Sprintf("ag-%d", i)
			cid := fmt.Sprintf("cv-%d", i)
			ids = append(ids, aid)
			if err := tx.InsertAgent(ctx, app.AgentRecord{
				ID: aid, Slug: aid, Name: aid, TeamID: "t1",
				EnginesJSON: `["agy"]`, EngineMask: 4, ModelsJSON: `{}`,
				Autonomy: domain.AutonomyAuto, Browser: domain.BrowserNone,
				SkillsJSON: `[]`, ParamsJSON: `{}`, ConfigRev: "r1", Status: domain.AgentActive,
			}); err != nil {
				return err
			}
			if err := tx.InsertConversation(ctx, app.ConversationRecord{ID: cid, AgentID: aid, Sessions: `{}`}); err != nil {
				return err
			}
			// Use PrioInteractive so all 3 slots can be used concurrently in the 3-slot pool test.
			if _, _, err := actorSvc.Wake(ctx, tx, actor.WakeInput{
				AgentID:        aid,
				Kind:           domain.WakeMessage,
				Priority:       domain.PrioInteractive,
				ConversationID: &cid,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seedNReadyAgents: %v", err)
	}
	return ids
}

func TestDispatcherRunsUpToSlots(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	st := testdb.New(t)
	actorSvc := actor.NewService(st, app.SystemClock{}, app.UUIDv7Gen{})
	agentIDs := seedNReadyAgents(t, st, actorSvc, 5)

	var active atomic.Int32
	var peak atomic.Int32
	var finished atomic.Int32

	eng := fakeengine.New(domain.EngineAgy)
	eng.SetDynamic(func(rctx context.Context, _ app.RunSpec, _ app.PermissionFunc, out chan<- app.EngineEvent) {
		cur := active.Add(1)
		for {
			old := peak.Load()
			if cur <= old || peak.CompareAndSwap(old, cur) {
				break
			}
		}
		defer active.Add(-1)

		out <- app.EngineEvent{Type: "session", SessionID: "s-disp"}
		select {
		case <-rctx.Done():
			return
		case <-time.After(50 * time.Millisecond):
		}
		out <- app.EngineEvent{Type: "final", InputTokens: 10, OutputTokens: 5}
		finished.Add(1)
	})

	slots := limits.NewSlots(map[domain.EngineName]int{domain.EngineAgy: 3}, nil)
	quota := limits.NewQuota(st, app.SystemClock{}, limits.QuotaConfig{ShortHours: 5, BackgroundShare: 0.7})
	runSvc := run.NewService(run.Deps{
		Store:   st,
		Actors:  actorSvc,
		Engines: map[domain.EngineName]app.Engine{domain.EngineAgy: eng},
		Clock:   app.SystemClock{},
		IDs:     app.UUIDv7Gen{},
	})

	disp := dispatch.New(dispatch.Deps{
		Store:        st,
		Actors:       actorSvc,
		Runs:         runSvc,
		Slots:        slots,
		Quota:        quota,
		Clock:        app.SystemClock{},
		PollInterval: 20 * time.Millisecond,
	})

	doneCh := make(chan error, 1)
	go func() { doneCh <- disp.Run(ctx) }()
	disp.Notify()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		allDone := true
		for _, id := range agentIDs {
			rem, _ := st.Read().ListQueuedWakeups(ctx, id)
			ag, _, _ := st.Read().GetAgent(ctx, id)
			if len(rem) > 0 || ag.ActiveRunID != nil {
				allDone = false
				break
			}
		}
		if allDone && finished.Load() == 5 {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	cancel()
	if err := <-doneCh; err != nil {
		t.Fatalf("disp.Run: %v", err)
	}

	if finished.Load() != 5 {
		t.Fatalf("finished = %d, want 5", finished.Load())
	}
	if peak.Load() != 3 {
		t.Fatalf("peak concurrent runs = %d, want 3", peak.Load())
	}
}

func TestDispatcherShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	st := testdb.New(t)
	actorSvc := actor.NewService(st, app.SystemClock{}, app.UUIDv7Gen{})
	agentIDs := seedNReadyAgents(t, st, actorSvc, 2)

	started := make(chan struct{}, 2)
	eng := fakeengine.New(domain.EngineAgy)
	eng.SetDynamic(func(rctx context.Context, _ app.RunSpec, _ app.PermissionFunc, out chan<- app.EngineEvent) {
		out <- app.EngineEvent{Type: "session", SessionID: "s-hang"}
		started <- struct{}{}
		<-rctx.Done()
	})

	slots := limits.NewSlots(map[domain.EngineName]int{domain.EngineAgy: 2}, nil)
	quota := limits.NewQuota(st, app.SystemClock{}, limits.QuotaConfig{ShortHours: 5, BackgroundShare: 0.7})
	runSvc := run.NewService(run.Deps{
		Store:   st,
		Actors:  actorSvc,
		Engines: map[domain.EngineName]app.Engine{domain.EngineAgy: eng},
		Clock:   app.SystemClock{},
		IDs:     app.UUIDv7Gen{},
	})

	disp := dispatch.New(dispatch.Deps{
		Store:        st,
		Actors:       actorSvc,
		Runs:         runSvc,
		Slots:        slots,
		Quota:        quota,
		Clock:        app.SystemClock{},
		PollInterval: 20 * time.Millisecond,
	})

	doneCh := make(chan error, 1)
	go func() { doneCh <- disp.Run(ctx) }()
	disp.Notify()

	<-started
	<-started

	shutdownStart := time.Now()
	cancel()
	err := <-doneCh
	elapsed := time.Since(shutdownStart)

	if err != nil {
		t.Fatalf("disp.Run returned unexpected error on shutdown: %v", err)
	}
	if elapsed >= 15*time.Second {
		t.Fatalf("shutdown took %v, want < 15s", elapsed)
	}

	for _, id := range agentIDs {
		ag, _, _ := st.Read().GetAgent(context.Background(), id)
		if ag.ActiveRunID != nil {
			t.Fatalf("agent %s still has active_run_id after shutdown", id)
		}
		rem, _ := st.Read().ListQueuedWakeups(context.Background(), id)
		if len(rem) != 1 {
			t.Fatalf("expected interrupted wakeup requeued for %s, got %d", id, len(rem))
		}
	}
}
