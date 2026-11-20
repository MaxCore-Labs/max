package coord_test

import (
	"testing"

	"max/internal/app"
	"max/internal/app/coord"
	"max/internal/domain"
)

func TestDoneUnblocksDependents(t *testing.T) {
	e := setupEnv(t)
	b1, _ := e.svc.Create(e.ctx, coord.NewTask{Title: "B1", Description: "B1", Assignee: ptr("ag-res"), Origin: domain.OriginOwner})
	b2, _ := e.svc.Create(e.ctx, coord.NewTask{Title: "B2", Description: "B2", Assignee: ptr("ag-chief"), Origin: domain.OriginOwner})
	dep, _ := e.svc.Create(e.ctx, coord.NewTask{
		Title: "Dep", Description: "Dep", Assignee: ptr("ag-wri"), BlockedBy: []domain.ID{b1, b2}, Origin: domain.OriginOwner,
	})

	if err := e.svc.SetStatus(e.ctx, b1, domain.TaskDone, nil, coord.Actor{Kind: "agent", ID: "ag-res"}); err != nil {
		t.Fatalf("done b1: %v", err)
	}
	tk, _, _ := e.store.Read().GetTask(e.ctx, dep)
	if tk.Status != string(domain.TaskBlocked) {
		t.Fatalf("expected still blocked after b1, got %q", tk.Status)
	}

	if err := e.svc.SetStatus(e.ctx, b2, domain.TaskDone, nil, coord.Actor{Kind: "agent", ID: "ag-chief"}); err != nil {
		t.Fatalf("done b2: %v", err)
	}
	tk, _, _ = e.store.Read().GetTask(e.ctx, dep)
	if tk.Status != string(domain.TaskTodo) || tk.BlockedReason != nil {
		t.Fatalf("expected todo/nil after last blocker done, got %q/%v", tk.Status, tk.BlockedReason)
	}
	wakes, _ := e.store.Read().ListQueuedWakeups(e.ctx, "ag-wri")
	if len(wakes) != 1 || wakes[0].Kind != domain.WakeTaskAssigned {
		t.Fatalf("expected 1 task_assigned wakeup on unblock, got %+v", wakes)
	}
}

func TestDoneWakesParentWhenAllChildrenFinal(t *testing.T) {
	e := setupEnv(t)
	pID, _ := e.svc.Create(e.ctx, coord.NewTask{
		Title: "Parent", Description: "Parent", Assignee: ptr("ag-chief"), CreatedByAgent: ptr("ag-chief"), Origin: domain.OriginOwner,
	})
	_ = e.store.WithTx(e.ctx, func(tx app.Tx) error {
		ws, _ := tx.ListQueuedWakeups(e.ctx, "ag-chief")
		for _, w := range ws {
			_ = tx.InsertRun(e.ctx, app.RunRecord{ID: "r-init", AgentID: "ag-chief", ConversationID: *w.ConversationID, Status: domain.RunParked, Priority: domain.PrioTask, Engine: domain.EngineAgy, Model: "default", StartedAt: 1})
			_, _ = tx.DrainDueWakeupsForConv(e.ctx, "ag-chief", w.ConversationID, *w.ConversationID, "r-init", 1e15, 10)
			_ = tx.FinishWakeupsForRun(e.ctx, "r-init", "done")
		}
		return nil
	})

	c1, _ := e.svc.Create(e.ctx, coord.NewTask{
		Title: "C1", Description: "C1", ParentID: &pID, Assignee: ptr("ag-res"), CreatedByAgent: ptr("ag-chief"), Origin: domain.OriginOwner,
	})
	c2, _ := e.svc.Create(e.ctx, coord.NewTask{
		Title: "C2", Description: "C2", ParentID: &pID, Assignee: ptr("ag-wri"), CreatedByAgent: ptr("ag-chief"), Origin: domain.OriginOwner,
	})

	if err := e.svc.SetStatus(e.ctx, c1, domain.TaskDone, nil, coord.Actor{Kind: "agent", ID: "ag-res"}); err != nil {
		t.Fatal(err)
	}
	wakes, _ := e.store.Read().ListQueuedWakeups(e.ctx, "ag-chief")
	if len(wakes) != 0 {
		t.Fatalf("expected parent not woken after first child, got %+v", wakes)
	}

	if err := e.svc.SetStatus(e.ctx, c2, domain.TaskDone, nil, coord.Actor{Kind: "agent", ID: "ag-wri"}); err != nil {
		t.Fatal(err)
	}
	wakes, _ = e.store.Read().ListQueuedWakeups(e.ctx, "ag-chief")
	if len(wakes) != 1 || wakes[0].Kind != domain.WakeChildDone {
		t.Fatalf("expected 1 child_done wakeup after all children done, got %+v", wakes)
	}
}

func TestCancelBlockerEscalates(t *testing.T) {
	e := setupEnv(t)
	bID, _ := e.svc.Create(e.ctx, coord.NewTask{
		Title: "Blocker", Description: "B", Assignee: ptr("ag-res"), Origin: domain.OriginOwner,
	})
	dID, _ := e.svc.Create(e.ctx, coord.NewTask{
		Title:          "Dependent",
		Description:    "D",
		Assignee:       ptr("ag-wri"),
		CreatedByAgent: ptr("ag-chief"),
		BlockedBy:      []domain.ID{bID},
		Origin:         domain.OriginOwner,
	})

	if err := e.svc.Cancel(e.ctx, bID, coord.Actor{Kind: "owner"}); err != nil {
		t.Fatalf("Cancel blocker: %v", err)
	}
	tk, _, _ := e.store.Read().GetTask(e.ctx, dID)
	if tk.Status != string(domain.TaskBlocked) || tk.BlockedReason == nil || *tk.BlockedReason != string(domain.BlockedNeedsOwner) {
		t.Fatalf("got status=%q reason=%v, want blocked/needs_owner", tk.Status, tk.BlockedReason)
	}
	wakes, _ := e.store.Read().ListQueuedWakeups(e.ctx, "ag-chief")
	if len(wakes) != 1 {
		t.Fatalf("expected 1 escalation wakeup for creator ag-chief, got %+v", wakes)
	}
}

func TestCancelCascades(t *testing.T) {
	e := setupEnv(t)
	pID, _ := e.svc.Create(e.ctx, coord.NewTask{
		Title: "Parent", Description: "P", Assignee: ptr("ag-chief"), Origin: domain.OriginOwner,
	})
	cID, _ := e.svc.Create(e.ctx, coord.NewTask{
		Title: "Child", Description: "C", ParentID: &pID, Assignee: ptr("ag-res"), Origin: domain.OriginOwner,
	})

	var convID domain.ID
	err := e.store.WithTx(e.ctx, func(tx app.Tx) error {
		var cerr error
		convID, cerr = tx.EnsureTaskConversation(e.ctx, "ag-res", cID, "conv-child")
		if cerr != nil {
			return cerr
		}
		if err := tx.InsertRun(e.ctx, app.RunRecord{
			ID: "run-child-live", AgentID: "ag-res", ConversationID: convID, TaskID: &cID,
			Status: domain.RunRunning, Priority: domain.PrioTask, Engine: domain.EngineAgy, Model: "default", StartedAt: 100,
		}); err != nil {
			return err
		}
		return tx.InsertApproval(e.ctx, app.ApprovalRecord{
			ID: "ap-child-1", RunID: "run-child-live", AgentID: "ag-res", ConversationID: convID,
			ToolName: "send_email", Input: `{}`, InputHash: "h1", Preview: "send", Reason: "external",
			Status: "pending", RequestedAt: 100, ExpiresAt: 999999999,
		})
	})
	if err != nil {
		t.Fatalf("seed active run + approval: %v", err)
	}

	if err := e.svc.Cancel(e.ctx, pID, coord.Actor{Kind: "owner"}); err != nil {
		t.Fatalf("Cancel parent: %v", err)
	}

	pTk, _, _ := e.store.Read().GetTask(e.ctx, pID)
	cTk, _, _ := e.store.Read().GetTask(e.ctx, cID)
	if pTk.Status != string(domain.TaskCancelled) || cTk.Status != string(domain.TaskCancelled) {
		t.Fatalf("expected parent and child cancelled, got parent=%q child=%q", pTk.Status, cTk.Status)
	}

	ap, _, _ := e.store.Read().GetApproval(e.ctx, "ap-child-1")
	if ap.Status != "expired" {
		t.Fatalf("expected pending approval expired, got %q", ap.Status)
	}

	e.mu.Lock()
	cancelledRuns := append([]domain.ID(nil), e.cancelled...)
	e.mu.Unlock()
	if len(cancelledRuns) != 1 || cancelledRuns[0] != "run-child-live" {
		t.Fatalf("expected CancelRun called with run-child-live, got %+v", cancelledRuns)
	}
}

func TestCommentDoesNotWakeOnMention(t *testing.T) {
	e := setupEnv(t)
	tkID, _ := e.svc.Create(e.ctx, coord.NewTask{
		Title: "Draft copy", Description: "Desc", Assignee: ptr("ag-res"), Origin: domain.OriginOwner,
	})
	_ = e.store.WithTx(e.ctx, func(tx app.Tx) error {
		ws, _ := tx.ListQueuedWakeups(e.ctx, "ag-res")
		for _, w := range ws {
			_ = tx.InsertRun(e.ctx, app.RunRecord{ID: "r-drain", AgentID: "ag-res", ConversationID: *w.ConversationID, Status: domain.RunParked, Priority: domain.PrioTask, Engine: domain.EngineAgy, Model: "default", StartedAt: 1})
			_, _ = tx.DrainDueWakeupsForConv(e.ctx, "ag-res", w.ConversationID, *w.ConversationID, "r-drain", 1e15, 10)
			_ = tx.FinishWakeupsForRun(e.ctx, "r-drain", "done")
		}
		return nil
	})

	if err := e.svc.Comment(e.ctx, tkID, coord.Actor{Kind: "owner"}, "FYI @writer-1 please review section 2"); err != nil {
		t.Fatalf("Comment: %v", err)
	}
	if err := e.svc.Comment(e.ctx, tkID, coord.Actor{Kind: "owner"}, "Also @writer-1 check title"); err != nil {
		t.Fatalf("Comment 2: %v", err)
	}

	wriWakes, _ := e.store.Read().ListQueuedWakeups(e.ctx, "ag-wri")
	if len(wriWakes) != 0 {
		t.Fatalf("expected @mention NOT to wake writer-1, got %+v", wriWakes)
	}

	resWakes, _ := e.store.Read().ListQueuedWakeups(e.ctx, "ag-res")
	if len(resWakes) != 1 || resWakes[0].Kind != domain.WakeTaskUpdate || resWakes[0].CoalescedCount != 1 {
		t.Fatalf("expected 1 coalesced task_update wakeup for assignee ag-res, got %+v", resWakes)
	}
}
