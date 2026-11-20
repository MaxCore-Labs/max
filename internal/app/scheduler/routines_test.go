package scheduler_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"max/internal/adapter/vaultgit"
	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/app/coord"
	runapp "max/internal/app/run"
	"max/internal/app/scheduler"
	"max/internal/domain"
	"max/internal/testutil/fakeengine"
	"max/internal/testutil/testdb"
)

type routineEnv struct {
	ctx      context.Context
	store    app.Store
	vault    *vaultgit.Vault
	actors   *actor.Service
	coordSvc *coord.Service
	sched    *scheduler.Scheduler
}

func setupRoutineEnv(t *testing.T, now time.Time) *routineEnv {
	t.Helper()
	ctx := context.Background()
	st := testdb.New(t)
	v := vaultgit.New(filepath.Join(t.TempDir(), "vault"))
	if err := v.EnsureInstalled(ctx); err != nil {
		t.Fatalf("EnsureInstalled: %v", err)
	}
	clk := fixedClock{t: now}
	ids := app.UUIDv7Gen{}
	actors := actor.NewService(st, clk, ids)
	coordSvc := coord.NewService(coord.Deps{Store: st, Actors: actors, Clock: clk, IDs: ids})
	sched := scheduler.New(st, actors, nil, nil).
		WithCoord(coordSvc).
		WithRoutines(v, time.UTC, ids)

	err := st.WithTx(ctx, func(tx app.Tx) error {
		if err := tx.InsertTeam(ctx, app.TeamRecord{ID: "t-core", Slug: "core", Name: "Core", MaxRunning: 4, Status: "active"}); err != nil {
			return err
		}
		return tx.InsertAgent(ctx, app.AgentRecord{
			ID: "ag-res", Slug: "researcher", Name: "Researcher", TeamID: "t-core",
			EnginesJSON: `["agy"]`, EngineMask: 4, Autonomy: domain.AutonomyAuto,
			Browser: domain.BrowserNone, ConfigRev: "r1", Status: domain.AgentActive,
		})
	})
	if err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	return &routineEnv{ctx: ctx, store: st, vault: v, actors: actors, coordSvc: coordSvc, sched: sched}
}

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

func TestFireCreatesTaskThenWake(t *testing.T) {
	now := time.Date(2026, 11, 20, 8, 0, 15, 0, time.UTC)
	e := setupRoutineEnv(t, now)

	if err := e.vault.Write(e.ctx, "routines/morning-brief.md", []byte(`---
agent: researcher
schedule: "0 8 * * *"
enabled: true
origin: trusted
catch_up: run_once
---
Summarize overnight market news.
`)); err != nil {
		t.Fatal(err)
	}
	if err := e.vault.Write(e.ctx, "routines/triage-inbox.md", []byte(`---
agent: researcher
schedule: "0 8 * * *"
enabled: true
origin: inbound
---
Triage external customer emails.
`)); err != nil {
		t.Fatal(err)
	}

	if err := e.sched.FireRoutines(e.ctx, now); err != nil {
		t.Fatalf("FireRoutines: %v", err)
	}

	tasks, err := e.store.Read().ListScopedTasks(e.ctx, app.TaskFilter{Limit: 10})
	if err != nil || len(tasks) != 2 {
		t.Fatalf("expected 2 routine tasks created, got %d (err=%v)", len(tasks), err)
	}
	origins := map[domain.RootOrigin]bool{}
	for _, tk := range tasks {
		origins[tk.RootOrigin] = true
	}
	if !origins[domain.OriginRoutine] || !origins[domain.OriginExternal] {
		t.Fatalf("expected both routine and external origins, got %+v", origins)
	}

	wakes, _ := e.store.Read().ListQueuedWakeups(e.ctx, "ag-res")
	if len(wakes) != 2 {
		t.Fatalf("expected 2 wakeups with distinct routine idem keys, got %+v", wakes)
	}
	for _, w := range wakes {
		if w.IdempotencyKey == nil || !strings.HasPrefix(*w.IdempotencyKey, "routine:") {
			t.Fatalf("expected routine:<slug>:<min> idempotency key, got %v", w.IdempotencyKey)
		}
	}
}

func TestSkipWhenPreviousActive(t *testing.T) {
	t0 := time.Date(2026, 11, 20, 8, 0, 10, 0, time.UTC)
	e := setupRoutineEnv(t, t0)

	_ = e.vault.Write(e.ctx, "routines/every-min.md", []byte(`---
agent: researcher
schedule: "* * * * *"
enabled: true
---
Check feed.
`))

	if err := e.sched.FireRoutines(e.ctx, t0); err != nil {
		t.Fatal(err)
	}
	// Next minute while first task is still todo/in_progress -> skip without creating a 2nd task
	t1 := t0.Add(time.Minute)
	if err := e.sched.FireRoutines(e.ctx, t1); err != nil {
		t.Fatal(err)
	}

	tasks, _ := e.store.Read().ListScopedTasks(e.ctx, app.TaskFilter{Limit: 10})
	if len(tasks) != 1 {
		t.Fatalf("expected still 1 task when previous active, got %d", len(tasks))
	}
	evs, _ := e.store.Read().ListRunEvents(e.ctx, "routine:every-min")
	if len(evs) != 1 || evs[0].Type != "skip" {
		t.Fatalf("expected 1 skip run_event, got %+v", evs)
	}
}

func TestAutoDisableAfter3(t *testing.T) {
	now := time.Date(2026, 11, 20, 8, 0, 10, 0, time.UTC)
	e := setupRoutineEnv(t, now)

	origRaw := []byte("---\nagent: researcher\nschedule: \"* * * * *\"\nenabled: true\n---\nBody.\n")
	_ = e.vault.Write(e.ctx, "routines/flaky.md", origRaw)

	for range 3 {
		if err := e.sched.RecordRoutineFailure(e.ctx, "flaky", now); err != nil {
			t.Fatalf("RecordRoutineFailure: %v", err)
		}
	}

	inbox, _ := e.store.Read().ListInboxItems(e.ctx, 10)
	if len(inbox) != 1 {
		t.Fatalf("expected 1 Inbox item on auto-disable, got %d", len(inbox))
	}

	// Vault file must remain untouched
	afterRaw, _ := e.vault.Read(e.ctx, "routines/flaky.md")
	if string(afterRaw) != string(origRaw) {
		t.Fatalf("routine vault file was mutated: %s", string(afterRaw))
	}

	// Subsequent FireRoutines must not fire auto-disabled routine
	if err := e.sched.FireRoutines(e.ctx, now); err != nil {
		t.Fatal(err)
	}
	tasks, _ := e.store.Read().ListScopedTasks(e.ctx, app.TaskFilter{Limit: 10})
	if len(tasks) != 0 {
		t.Fatalf("expected auto-disabled routine not to fire, got %d tasks", len(tasks))
	}
}

func TestHeartbeatJitter(t *testing.T) {
	base := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	perSecond := make(map[int64]int)
	minSec := base.Add(54 * time.Minute).Unix() // 60m - 10% = 54m
	maxSec := base.Add(66 * time.Minute).Unix() // 60m + 10% = 66m

	for i := range 1000 {
		next, err := scheduler.ComputeNextHeartbeat("0 * * * *", time.UTC, base, fmt.Sprintf("agent-%04d", i))
		if err != nil {
			t.Fatalf("ComputeNextHeartbeat: %v", err)
		}
		sec := next.Unix()
		if sec < minSec || sec > maxSec {
			t.Fatalf("jittered heartbeat %v outside ±10%% window [%d, %d]", next, minSec, maxSec)
		}
		perSecond[sec]++
	}

	for sec, cnt := range perSecond {
		if cnt > 50 { // > 5% of 1,000 agents
			t.Fatalf("second %d has %d agents (> 5%% cap)", sec, cnt)
		}
	}
}

func TestHeartbeatCreatesTaskAutoClosed(t *testing.T) {
	now := time.Date(2026, 11, 20, 9, 0, 5, 0, time.UTC)
	e := setupRoutineEnv(t, now)

	dueMs := now.Add(-time.Second).UnixMilli()
	_ = e.store.WithTx(e.ctx, func(tx app.Tx) error {
		return tx.SetAgentHeartbeatSchedule(e.ctx, "ag-res", "0 * * * *", &dueMs)
	})

	if err := e.sched.FireHeartbeats(e.ctx, now); err != nil {
		t.Fatalf("FireHeartbeats: %v", err)
	}

	tasks, _ := e.store.Read().ListScopedTasks(e.ctx, app.TaskFilter{Limit: 10})
	if len(tasks) != 1 || tasks[0].RootOrigin != domain.OriginAgent {
		t.Fatalf("expected 1 heartbeat task with origin=agent, got %+v", tasks)
	}

	// Claim and execute heartbeat run -> verify task is auto-closed (done) on Finalize
	fe := fakeengine.New(domain.EngineAgy, app.EngineEvent{Type: "final", Text: "All quiet."})
	zeroDur := time.Duration(0)
	runSvc := runapp.NewService(runapp.Deps{
		Store:    e.store,
		Actors:   e.actors,
		Coord:    e.coordSvc,
		Engines:  map[domain.EngineName]app.Engine{domain.EngineAgy: fe},
		Cooldown: &zeroDur,
		Clock:    fixedClock{t: now},
	})
	claimed, err := e.actors.ClaimBatch(e.ctx, actor.Capacity{Free: map[domain.EngineName]int{domain.EngineAgy: 1}}, now)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimBatch heartbeat: %v %+v", err, claimed)
	}
	if _, err := runSvc.Execute(e.ctx, claimed[0]); err != nil {
		t.Fatalf("Execute heartbeat: %v", err)
	}

	tk, _, _ := e.store.Read().GetTask(e.ctx, tasks[0].ID)
	if tk.Status != string(domain.TaskDone) {
		t.Fatalf("expected heartbeat task auto-closed to done, got %q", tk.Status)
	}
}

func TestBadRoutineFileReportedOnce(t *testing.T) {
	now := time.Date(2026, 11, 20, 8, 0, 10, 0, time.UTC)
	e := setupRoutineEnv(t, now)
	_ = e.vault.Write(e.ctx, "routines/broken.md", []byte("---\nagent: [invalid yaml\n---\nBody"))

	if err := e.sched.FireRoutines(e.ctx, now); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	if err := e.sched.FireRoutines(e.ctx, now.Add(30*time.Second)); err != nil {
		t.Fatalf("tick 2: %v", err)
	}

	inbox, _ := e.store.Read().ListInboxItems(e.ctx, 10)
	if len(inbox) != 1 {
		t.Fatalf("expected exactly 1 Inbox item across multiple ticks for broken routine, got %d", len(inbox))
	}
}

func TestInboundWarning(t *testing.T) {
	now := time.Date(2026, 11, 20, 8, 0, 10, 0, time.UTC)
	e := setupRoutineEnv(t, now)

	// Routine reads inbound mailbox connection without declaring origin: inbound
	_ = e.vault.Write(e.ctx, "routines/check-mail.md", []byte(`---
agent: researcher
schedule: "0 9 * * *"
enabled: true
origin: trusted
connections: [gmail-inbox]
---
Read unread emails.
`))

	if err := e.sched.FireRoutines(e.ctx, now); err != nil {
		t.Fatalf("FireRoutines: %v", err)
	}
	inbox, _ := e.store.Read().ListInboxItems(e.ctx, 10)
	if len(inbox) != 1 {
		t.Fatalf("expected 1 inbound warning in Inbox, got %d", len(inbox))
	}
}
