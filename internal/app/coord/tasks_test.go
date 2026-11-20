package coord_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/app/coord"
	"max/internal/domain"
	"max/internal/testutil/testdb"
)

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

type env struct {
	ctx       context.Context
	store     app.Store
	actors    *actor.Service
	svc       *coord.Service
	cancelled []domain.ID
	mu        sync.Mutex
}

func setupEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	st := testdb.New(t)
	clk := fixedClock{t: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)}
	ids := app.UUIDv7Gen{}
	actors := actor.NewService(st, clk, ids)
	e := &env{ctx: ctx, store: st, actors: actors}
	e.svc = coord.NewService(coord.Deps{
		Store:  st,
		Actors: actors,
		Clock:  clk,
		IDs:    ids,
		CancelRun: func(runID domain.ID) {
			e.mu.Lock()
			e.cancelled = append(e.cancelled, runID)
			e.mu.Unlock()
		},
	})

	err := st.WithTx(ctx, func(tx app.Tx) error {
		if err := tx.InsertTeam(ctx, app.TeamRecord{
			ID: "team-core", Slug: "core", Name: "Core", MaxRunning: 4, Status: "active",
		}); err != nil {
			return err
		}
		for _, ag := range []struct{ id, slug string }{
			{"ag-chief", "chief-of-staff"},
			{"ag-res", "researcher"},
			{"ag-wri", "writer-1"},
		} {
			if err := tx.InsertAgent(ctx, app.AgentRecord{
				ID: ag.id, Slug: ag.slug, Name: ag.slug, TeamID: "team-core",
				EnginesJSON: `["agy"]`, EngineMask: 4, Autonomy: domain.AutonomyAuto,
				Browser: domain.BrowserNone, ConfigRev: "r1", Status: domain.AgentActive,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed env: %v", err)
	}
	return e
}

func ptr[T any](v T) *T { return &v }

func TestCreateWakesAssignee(t *testing.T) {
	e := setupEnv(t)
	id, err := e.svc.Create(e.ctx, coord.NewTask{
		Title:       "Research market",
		Description: "Collect 5 competitors",
		Assignee:    ptr("ag-res"),
		Origin:      domain.OriginOwner,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	tk, ok, err := e.store.Read().GetTask(e.ctx, id)
	if err != nil || !ok {
		t.Fatalf("GetTask: ok=%v err=%v", ok, err)
	}
	if tk.Status != string(domain.TaskTodo) {
		t.Fatalf("status=%q, want todo", tk.Status)
	}
	wakes, err := e.store.Read().ListQueuedWakeups(e.ctx, "ag-res")
	if err != nil {
		t.Fatalf("ListQueuedWakeups: %v", err)
	}
	if len(wakes) != 1 || wakes[0].Kind != domain.WakeTaskAssigned {
		t.Fatalf("got wakeups %+v, want 1 task_assigned", wakes)
	}
}

func TestCreateBlockedDoesNotWake(t *testing.T) {
	e := setupEnv(t)
	bID, err := e.svc.Create(e.ctx, coord.NewTask{
		Title: "Blocker", Description: "First step", Assignee: ptr("ag-res"), Origin: domain.OriginOwner,
	})
	if err != nil {
		t.Fatalf("Create blocker: %v", err)
	}
	dID, err := e.svc.Create(e.ctx, coord.NewTask{
		Title:       "Write post",
		Description: "Depends on research",
		Assignee:    ptr("ag-wri"),
		BlockedBy:   []domain.ID{bID},
		Origin:      domain.OriginOwner,
	})
	if err != nil {
		t.Fatalf("Create dependent: %v", err)
	}
	tk, _, _ := e.store.Read().GetTask(e.ctx, dID)
	if tk.Status != string(domain.TaskBlocked) || tk.BlockedReason == nil || *tk.BlockedReason != string(domain.BlockedDependency) {
		t.Fatalf("status=%q reason=%v, want blocked/dependency", tk.Status, tk.BlockedReason)
	}
	wakes, _ := e.store.Read().ListQueuedWakeups(e.ctx, "ag-wri")
	if len(wakes) != 0 {
		t.Fatalf("expected 0 wakeups for blocked assignee, got %d", len(wakes))
	}
}

func TestCreateRejectsCycle(t *testing.T) {
	e := setupEnv(t)
	tA, err := e.svc.Create(e.ctx, coord.NewTask{
		Title: "A", Description: "A", Origin: domain.OriginOwner,
	})
	if err != nil {
		t.Fatal(err)
	}
	tB, err := e.svc.Create(e.ctx, coord.NewTask{
		Title: "B", Description: "B", BlockedBy: []domain.ID{tA}, Origin: domain.OriginOwner,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.AddBlocker(e.ctx, tA, tB); err == nil {
		t.Fatal("expected cycle error when adding tA blocked by tB")
	}
	if _, err := e.svc.Create(e.ctx, coord.NewTask{
		ID: "self-1", Title: "Self", Description: "Self", BlockedBy: []domain.ID{"self-1"}, Origin: domain.OriginOwner,
	}); err == nil {
		t.Fatal("expected cycle error on self-dependency")
	}
}

func TestRootFields(t *testing.T) {
	e := setupEnv(t)
	rootID, err := e.svc.Create(e.ctx, coord.NewTask{
		Title: "Root", Description: "Root", Origin: domain.OriginRoutine, CreatedByAgent: ptr("ag-chief"),
	})
	if err != nil {
		t.Fatal(err)
	}
	childID, err := e.svc.Create(e.ctx, coord.NewTask{
		Title: "Child", Description: "Child", ParentID: &rootID, Origin: domain.OriginAgent, CreatedByAgent: ptr("ag-chief"),
	})
	if err != nil {
		t.Fatal(err)
	}
	grandID, err := e.svc.Create(e.ctx, coord.NewTask{
		Title: "Grandchild", Description: "Grand", ParentID: &childID, Origin: domain.OriginAgent,
	})
	if err != nil {
		t.Fatal(err)
	}
	grand, _, _ := e.store.Read().GetTask(e.ctx, grandID)
	if grand.RootID != rootID || grand.RootOrigin != domain.OriginRoutine || grand.Depth != 2 {
		t.Fatalf("got root=%q origin=%q depth=%d, want %q/routine/2", grand.RootID, grand.RootOrigin, grand.Depth, rootID)
	}
}

func TestCheckoutAtomic(t *testing.T) {
	e := setupEnv(t)
	tkID, err := e.svc.Create(e.ctx, coord.NewTask{
		Title: "Atomic work", Description: "Desc", Assignee: ptr("ag-res"), Origin: domain.OriginOwner,
	})
	if err != nil {
		t.Fatal(err)
	}

	var wins atomic.Int32
	var wg sync.WaitGroup
	for _, runID := range []string{"run-1", "run-2"} {
		wg.Add(1)
		go func(rid string) {
			defer wg.Done()
			var ok bool
			err := e.store.WithTx(e.ctx, func(tx app.Tx) error {
				var cerr error
				ok, cerr = e.svc.Checkout(e.ctx, tx, tkID, "ag-res", rid)
				return cerr
			})
			if err != nil {
				t.Errorf("Checkout(%s): %v", rid, err)
			}
			if ok {
				wins.Add(1)
			}
		}(runID)
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("expected exactly 1 checkout winner, got %d", wins.Load())
	}
}

func TestCheckoutRefusesBlocked(t *testing.T) {
	e := setupEnv(t)
	bID, _ := e.svc.Create(e.ctx, coord.NewTask{Title: "B", Description: "B", Origin: domain.OriginOwner})
	dID, _ := e.svc.Create(e.ctx, coord.NewTask{
		Title: "D", Description: "D", Assignee: ptr("ag-wri"), BlockedBy: []domain.ID{bID}, Origin: domain.OriginOwner,
	})
	var ok bool
	err := e.store.WithTx(e.ctx, func(tx app.Tx) error {
		var cerr error
		ok, cerr = e.svc.Checkout(e.ctx, tx, dID, "ag-wri", "run-blocked")
		return cerr
	})
	if err != nil || ok {
		t.Fatalf("Checkout on blocked task: ok=%v err=%v, want false/nil", ok, err)
	}
}
