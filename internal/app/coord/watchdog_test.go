package coord_test

import (
	"testing"
	"time"

	"max/internal/app"
	"max/internal/app/coord"
	"max/internal/domain"
)

func TestStaleChildWakesParent(t *testing.T) {
	e := setupEnv(t)
	pID, _ := e.svc.Create(e.ctx, coord.NewTask{
		Title: "Parent", Description: "P", Assignee: ptr("ag-chief"), CreatedByAgent: ptr("ag-chief"), Origin: domain.OriginOwner,
	})
	// Drain initial wakeup on ag-chief
	_ = e.store.WithTx(e.ctx, func(tx app.Tx) error {
		ws, _ := tx.ListQueuedWakeups(e.ctx, "ag-chief")
		for _, w := range ws {
			_ = tx.InsertRun(e.ctx, app.RunRecord{ID: "r-wd-0", AgentID: "ag-chief", ConversationID: *w.ConversationID, Status: domain.RunParked, Priority: domain.PrioTask, Engine: domain.EngineAgy, Model: "default", StartedAt: 1})
			_, _ = tx.DrainDueWakeupsForConv(e.ctx, "ag-chief", w.ConversationID, *w.ConversationID, "r-wd-0", 1e15, 10)
			_ = tx.FinishWakeupsForRun(e.ctx, "r-wd-0", "done")
		}
		return nil
	})

	cID, _ := e.svc.Create(e.ctx, coord.NewTask{
		Title: "Stale child", Description: "C", ParentID: &pID, Assignee: ptr("ag-res"), CreatedByAgent: ptr("ag-chief"), Origin: domain.OriginOwner,
	})
	_ = cID

	later := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC) // 8 days later (> 7 days stale)
	woken1, err := e.svc.Watchdog(e.ctx, later)
	if err != nil || woken1 != 1 {
		t.Fatalf("first Watchdog: woken=%d err=%v, want 1/nil", woken1, err)
	}
	wakes, _ := e.store.Read().ListQueuedWakeups(e.ctx, "ag-chief")
	if len(wakes) != 1 {
		t.Fatalf("expected 1 wakeup on delegator ag-chief, got %d", len(wakes))
	}

	// Immediate re-run on same day must not create a second wakeup
	woken2, err := e.svc.Watchdog(e.ctx, later.Add(5*time.Minute))
	if err != nil || woken2 != 0 {
		t.Fatalf("second Watchdog same day: woken=%d err=%v, want 0/nil", woken2, err)
	}
	wakes, _ = e.store.Read().ListQueuedWakeups(e.ctx, "ag-chief")
	if len(wakes) != 1 || wakes[0].CoalescedCount != 0 {
		t.Fatalf("expected still 1 uncoalesced wakeup, got %+v", wakes)
	}
}

func TestOverdueChildWakesParent(t *testing.T) {
	e := setupEnv(t)
	pID, _ := e.svc.Create(e.ctx, coord.NewTask{
		Title: "Parent", Description: "P", Assignee: ptr("ag-chief"), CreatedByAgent: ptr("ag-chief"), Origin: domain.OriginOwner,
	})
	_ = e.store.WithTx(e.ctx, func(tx app.Tx) error {
		ws, _ := tx.ListQueuedWakeups(e.ctx, "ag-chief")
		for _, w := range ws {
			_ = tx.InsertRun(e.ctx, app.RunRecord{ID: "r-od-0", AgentID: "ag-chief", ConversationID: *w.ConversationID, Status: domain.RunParked, Priority: domain.PrioTask, Engine: domain.EngineAgy, Model: "default", StartedAt: 1})
			_, _ = tx.DrainDueWakeupsForConv(e.ctx, "ag-chief", w.ConversationID, *w.ConversationID, "r-od-0", 1e15, 10)
			_ = tx.FinishWakeupsForRun(e.ctx, "r-od-0", "done")
		}
		return nil
	})

	due := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	_, err := e.svc.Create(e.ctx, coord.NewTask{
		Title: "Urgent child", Description: "C", ParentID: &pID, Assignee: ptr("ag-res"),
		CreatedByAgent: ptr("ag-chief"), DueAt: &due, Origin: domain.OriginOwner,
	})
	if err != nil {
		t.Fatal(err)
	}

	woken, err := e.svc.Watchdog(e.ctx, due.Add(time.Minute))
	if err != nil || woken != 1 {
		t.Fatalf("Watchdog overdue: woken=%d err=%v, want 1/nil", woken, err)
	}
	wakes, _ := e.store.Read().ListQueuedWakeups(e.ctx, "ag-chief")
	if len(wakes) != 1 {
		t.Fatalf("expected 1 overdue report wakeup for ag-chief, got %d", len(wakes))
	}
}

func TestIdleRunsBlocksTask(t *testing.T) {
	e := setupEnv(t)
	seedReviewHierarchy(t, e)

	tkID, _ := e.svc.Create(e.ctx, coord.NewTask{
		Title: "Spinning task", Description: "Desc", Assignee: ptr("ag-wri"), Origin: domain.OriginOwner,
	})

	for i := 1; i <= 3; i++ {
		err := e.store.WithTx(e.ctx, func(tx app.Tx) error {
			return e.svc.NoteRunOutcome(e.ctx, tx, tkID, false)
		})
		if err != nil {
			t.Fatalf("NoteRunOutcome #%d: %v", i, err)
		}
	}

	tk, _, _ := e.store.Read().GetTask(e.ctx, tkID)
	if tk.Status != string(domain.TaskBlocked) || tk.BlockedReason == nil || *tk.BlockedReason != string(domain.BlockedStalled) {
		t.Fatalf("got status=%q reason=%v, want blocked/stalled", tk.Status, tk.BlockedReason)
	}
	if tk.IdleRuns != 3 {
		t.Fatalf("idle_runs=%d, want 3", tk.IdleRuns)
	}
	// Escalated to writer-1's manager (ag-lead)
	wakes, _ := e.store.Read().ListQueuedWakeups(e.ctx, "ag-lead")
	if len(wakes) != 1 {
		t.Fatalf("expected 1 escalation wakeup for manager ag-lead, got %+v", wakes)
	}
}

func TestProgressResetsIdle(t *testing.T) {
	e := setupEnv(t)
	tkID, _ := e.svc.Create(e.ctx, coord.NewTask{
		Title: "Recovering task", Description: "Desc", Assignee: ptr("ag-res"), Origin: domain.OriginOwner,
	})
	_ = e.store.WithTx(e.ctx, func(tx app.Tx) error {
		if err := e.svc.NoteRunOutcome(e.ctx, tx, tkID, false); err != nil {
			return err
		}
		return e.svc.NoteRunOutcome(e.ctx, tx, tkID, false)
	})
	tk, _, _ := e.store.Read().GetTask(e.ctx, tkID)
	if tk.IdleRuns != 2 {
		t.Fatalf("idle_runs=%d, want 2", tk.IdleRuns)
	}

	_ = e.store.WithTx(e.ctx, func(tx app.Tx) error {
		return e.svc.NoteRunOutcome(e.ctx, tx, tkID, true)
	})
	tk, _, _ = e.store.Read().GetTask(e.ctx, tkID)
	if tk.IdleRuns != 0 {
		t.Fatalf("expected idle_runs reset to 0 on progress, got %d", tk.IdleRuns)
	}
}

func TestProgressDefinition(t *testing.T) {
	if coord.DidRunProgress(false, []string{"agents/researcher/notes/NOTES.md"}, 0) {
		t.Fatal("editing NOTES.md alone must not count as task progress")
	}
	if !coord.DidRunProgress(true, nil, 0) {
		t.Fatal("task status change must count as progress")
	}
	if !coord.DidRunProgress(false, []string{"agents/researcher/artifacts/report.md"}, 0) {
		t.Fatal("writing file under artifacts/ must count as progress")
	}
	if !coord.DidRunProgress(false, nil, 1) {
		t.Fatal("creating a child task must count as progress")
	}
}
