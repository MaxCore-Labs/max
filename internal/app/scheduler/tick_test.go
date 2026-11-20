package scheduler_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/app/approvals"
	"max/internal/app/scheduler"
	"max/internal/app/tools"
	"max/internal/domain"
	"max/internal/testutil/testdb"
)

type testClock struct{ now time.Time }

func (c *testClock) Now() time.Time { return c.now }

func setupSchedulerFixture(t *testing.T) (st app.Store, clk *testClock, actorSvc *actor.Service, apprSvc *approvals.Service, sch *scheduler.Scheduler) {
	t.Helper()
	ctx := context.Background()
	st = testdb.New(t)
	clk = &testClock{now: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)}
	actorSvc = actor.NewService(st, clk, app.UUIDv7Gen{})
	apprSvc = approvals.NewService(st, actorSvc, nil, clk, app.UUIDv7Gen{})
	sch = scheduler.New(st, actorSvc, apprSvc, nil)

	if err := st.WithTx(ctx, func(tx app.Tx) error {
		if err := tx.InsertTeam(ctx, app.TeamRecord{ID: "t1", Slug: "core", Name: "Core", MaxRunning: 3, Status: "active"}); err != nil {
			return err
		}
		if err := tx.InsertAgent(ctx, app.AgentRecord{
			ID: "ag-1", Slug: "chief", Name: "Chief", TeamID: "t1",
			EnginesJSON: `["agy"]`, EngineMask: 4, ModelsJSON: `{}`,
			Autonomy: domain.AutonomyAuto, Browser: domain.BrowserNone,
			SkillsJSON: `[]`, ParamsJSON: `{}`, ConfigRev: "r1", Status: domain.AgentActive,
		}); err != nil {
			return err
		}
		return tx.InsertConversation(ctx, app.ConversationRecord{ID: "c1", AgentID: "ag-1", Sessions: `{}`})
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return st, clk, actorSvc, apprSvc, sch
}

func TestTickPromotesDueWakeups(t *testing.T) {
	ctx := context.Background()
	st, clk, actorSvc, _, sch := setupSchedulerFixture(t)

	conv := "c1"
	if err := st.WithTx(ctx, func(tx app.Tx) error {
		_, _, err := actorSvc.Wake(ctx, tx, actor.WakeInput{
			AgentID:        "ag-1",
			Kind:           domain.WakeRoutine,
			Priority:       domain.PrioRoutine,
			ConversationID: &conv,
			NotBefore:      clk.now.Add(10 * time.Minute),
		})
		return err
	}); err != nil {
		t.Fatalf("Wake: %v", err)
	}
	ag0, _, _ := st.Read().GetAgent(ctx, "ag-1")
	if ag0.Ready {
		t.Fatal("expected Ready=false before NotBefore")
	}

	if err := sch.Tick(ctx, clk.now.Add(15*time.Minute)); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	ag1, _, _ := st.Read().GetAgent(ctx, "ag-1")
	if !ag1.Ready {
		t.Fatal("expected Ready=true after Tick promoted due wakeup")
	}
}

func TestTickExpiresApprovals(t *testing.T) {
	ctx := context.Background()
	st, clk, _, apprSvc, sch := setupSchedulerFixture(t)

	id, _, err := apprSvc.Ask(ctx, approvals.Request{
		Caller: tools.Caller{
			RunID:          "run-1",
			AgentID:        "ag-1",
			ConversationID: "c1",
		},
		ToolName: "gmail.send",
		Input:    json.RawMessage(`{"to":"x@y.com"}`),
		Reason:   "external",
		ByMax:    true,
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}

	if err := sch.Tick(ctx, clk.now.Add(25*time.Hour)); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	ap, _, _ := st.Read().GetApproval(ctx, id)
	if ap.Status != "expired" {
		t.Fatalf("Status = %q, want expired", ap.Status)
	}
}

func TestTickReconcilesTeamCounter(t *testing.T) {
	ctx := context.Background()
	st, clk, _, _, sch := setupSchedulerFixture(t)

	if err := st.WithTx(ctx, func(tx app.Tx) error {
		return tx.SetTeamRunningRaw(ctx, "t1", 5)
	}); err != nil {
		t.Fatalf("SetTeamRunningRaw: %v", err)
	}

	if err := sch.Tick(ctx, clk.now); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	tm, _, _ := st.Read().GetTeam(ctx, "t1")
	if tm.Running != 0 {
		t.Fatalf("teams.running = %d, want 0", tm.Running)
	}
}

func TestTickRetention(t *testing.T) {
	ctx := context.Background()
	st, clk, _, _, sch := setupSchedulerFixture(t)

	conv := "c1"
	if err := st.WithTx(ctx, func(tx app.Tx) error {
		if err := tx.InsertWakeupRaw(ctx, app.WakeupRecord{
			ID: "w-31d", AgentID: "ag-1", ConversationID: &conv,
			Kind: domain.WakeMessage, Priority: domain.PrioTask, Payload: `{}`,
			Status: "done", CreatedAt: clk.now.Add(-31 * 24 * time.Hour).UnixMilli(),
		}); err != nil {
			return err
		}
		return tx.InsertWakeupRaw(ctx, app.WakeupRecord{
			ID: "w-29d", AgentID: "ag-1", ConversationID: &conv,
			Kind: domain.WakeMessage, Priority: domain.PrioTask, Payload: `{}`,
			Status: "done", CreatedAt: clk.now.Add(-29 * 24 * time.Hour).UnixMilli(),
		})
	}); err != nil {
		t.Fatalf("seed retention: %v", err)
	}

	if err := sch.Tick(ctx, clk.now); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if _, ok, _ := st.Read().GetWakeup(ctx, "ag-1", "w-31d"); ok {
		t.Fatal("expected 31-day-old done wakeup to be pruned")
	}
	if _, ok, _ := st.Read().GetWakeup(ctx, "ag-1", "w-29d"); !ok {
		t.Fatal("expected 29-day-old done wakeup to be retained")
	}
}

func TestTickClockJumpForward(t *testing.T) {
	ctx := context.Background()
	st, clk, actorSvc, _, sch := setupSchedulerFixture(t)

	conv := "c1"
	if err := st.WithTx(ctx, func(tx app.Tx) error {
		_, _, err := actorSvc.Wake(ctx, tx, actor.WakeInput{
			AgentID:        "ag-1",
			Kind:           domain.WakeTaskAssigned,
			Priority:       domain.PrioTask,
			ConversationID: &conv,
			NotBefore:      clk.now.Add(2 * time.Hour),
		})
		return err
	}); err != nil {
		t.Fatalf("Wake: %v", err)
	}

	jumped := clk.now.Add(72 * time.Hour)
	if err := sch.Tick(ctx, jumped); err != nil {
		t.Fatalf("Tick 1: %v", err)
	}
	if err := sch.Tick(ctx, jumped.Add(30*time.Second)); err != nil {
		t.Fatalf("Tick 2: %v", err)
	}

	queued, err := st.Read().ListQueuedWakeups(ctx, "ag-1")
	if err != nil || len(queued) != 1 {
		t.Fatalf("queued = %d (err=%v), want 1 (no duplicates after +3d jump)", len(queued), err)
	}
}

func TestTickClockJumpBackward(t *testing.T) {
	ctx := context.Background()
	st, clk, _, _, sch := setupSchedulerFixture(t)

	if err := sch.Tick(ctx, clk.now); err != nil {
		t.Fatalf("initial Tick: %v", err)
	}
	backward := clk.now.Add(-2 * time.Hour)
	if err := sch.Tick(ctx, backward); err != nil {
		t.Fatalf("backward Tick failed: %v", err)
	}

	futureRows, err := st.Read().CountFutureQuotaLedgerRows(ctx, clk.now.UnixMilli())
	if err != nil || futureRows != 0 {
		t.Fatalf("future quota_ledger rows = %d (err=%v), want 0", futureRows, err)
	}
}
