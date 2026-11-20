package coord_test

import (
	"strings"
	"testing"

	"max/internal/app"
	"max/internal/app/coord"
	"max/internal/domain"
)

func seedReviewHierarchy(t *testing.T, e *env) {
	t.Helper()
	revTpl := "reviewer"
	chiefID := "ag-chief"
	leadID := "ag-lead"
	err := e.store.WithTx(e.ctx, func(tx app.Tx) error {
		if err := tx.InsertAgent(e.ctx, app.AgentRecord{
			ID: "ag-lead", Slug: "team-lead", Name: "Lead", TeamID: "team-core",
			ManagerAgentID: &chiefID, EnginesJSON: `["agy"]`, EngineMask: 4,
			Autonomy: domain.AutonomyAuto, Browser: domain.BrowserNone, ConfigRev: "r1", Status: domain.AgentActive,
		}); err != nil {
			return err
		}
		if err := tx.SetAgentManager(e.ctx, "ag-wri", &leadID); err != nil {
			return err
		}
		return tx.InsertAgent(e.ctx, app.AgentRecord{
			ID: "ag-rev", Slug: "reviewer-core", Name: "Reviewer", TemplateSlug: &revTpl, TeamID: "team-core",
			ManagerAgentID: &leadID, EnginesJSON: `["agy"]`, EngineMask: 4,
			Autonomy: domain.AutonomySuggest, Browser: domain.BrowserNone, ConfigRev: "r1", Status: domain.AgentActive,
		})
	})
	if err != nil {
		t.Fatalf("seedReviewHierarchy: %v", err)
	}
}

func TestReviewerDiffersFromAssignee(t *testing.T) {
	e := setupEnv(t)
	seedReviewHierarchy(t, e)

	// Task assigned to writer-1 -> picks ag-rev
	tk1, _ := e.svc.Create(e.ctx, coord.NewTask{
		Title: "Draft 1", Description: "Desc", Assignee: ptr("ag-wri"), NeedsReview: true, Origin: domain.OriginOwner,
	})
	got, err := e.svc.PickReviewer(e.ctx, tk1)
	if err != nil || got == nil || *got != "ag-rev" {
		t.Fatalf("PickReviewer for writer-1 = %v (err=%v), want ag-rev", got, err)
	}

	// Task assigned directly to the sole reviewer ag-rev -> must return nil (owner reviews)
	tk2, _ := e.svc.Create(e.ctx, coord.NewTask{
		Title: "Self task", Description: "Desc", Assignee: ptr("ag-rev"), NeedsReview: true, Origin: domain.OriginOwner,
	})
	got2, err := e.svc.PickReviewer(e.ctx, tk2)
	if err != nil || got2 != nil {
		t.Fatalf("PickReviewer when assignee is sole reviewer = %v (err=%v), want nil", got2, err)
	}
}

func TestApproveCompletes(t *testing.T) {
	e := setupEnv(t)
	seedReviewHierarchy(t, e)

	tkID, _ := e.svc.Create(e.ctx, coord.NewTask{
		Title: "Article", Description: "Desc", Assignee: ptr("ag-wri"), NeedsReview: true, Origin: domain.OriginOwner,
	})
	_ = e.svc.SetStatus(e.ctx, tkID, domain.TaskInProgress, nil, coord.Actor{Kind: "agent", ID: "ag-wri"})
	if err := e.svc.SubmitForReview(e.ctx, tkID, "ag-wri"); err != nil {
		t.Fatalf("SubmitForReview: %v", err)
	}

	tk, _, _ := e.store.Read().GetTask(e.ctx, tkID)
	if tk.Status != string(domain.TaskInReview) || tk.ReviewerAgentID == nil || *tk.ReviewerAgentID != "ag-rev" {
		t.Fatalf("after submit: status=%q reviewer=%v", tk.Status, tk.ReviewerAgentID)
	}

	if err := e.svc.Review(e.ctx, tkID, "ag-rev", true, "Verified all citations"); err != nil {
		t.Fatalf("Review approve: %v", err)
	}
	tk, _, _ = e.store.Read().GetTask(e.ctx, tkID)
	if tk.Status != string(domain.TaskDone) {
		t.Fatalf("after approve: status=%q, want done", tk.Status)
	}
}

func TestChangesReturnsToAssignee(t *testing.T) {
	e := setupEnv(t)
	seedReviewHierarchy(t, e)

	tkID, _ := e.svc.Create(e.ctx, coord.NewTask{
		Title: "Article", Description: "Desc", Assignee: ptr("ag-wri"), NeedsReview: true, Origin: domain.OriginOwner,
	})
	// Drain initial task_assigned wakeup on ag-wri
	_ = e.store.WithTx(e.ctx, func(tx app.Tx) error {
		ws, _ := tx.ListQueuedWakeups(e.ctx, "ag-wri")
		for _, w := range ws {
			_ = tx.InsertRun(e.ctx, app.RunRecord{ID: "r-wri-0", AgentID: "ag-wri", ConversationID: *w.ConversationID, Status: domain.RunParked, Priority: domain.PrioTask, Engine: domain.EngineAgy, Model: "default", StartedAt: 1})
			_, _ = tx.DrainDueWakeupsForConv(e.ctx, "ag-wri", w.ConversationID, *w.ConversationID, "r-wri-0", 1e15, 10)
			_ = tx.FinishWakeupsForRun(e.ctx, "r-wri-0", "done")
		}
		return nil
	})

	_ = e.svc.SetStatus(e.ctx, tkID, domain.TaskInProgress, nil, coord.Actor{Kind: "agent", ID: "ag-wri"})
	if err := e.svc.SubmitForReview(e.ctx, tkID, "ag-wri"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Review(e.ctx, tkID, "ag-rev", false, "Missing benchmark table in section 3"); err != nil {
		t.Fatalf("Review changes: %v", err)
	}

	tk, _, _ := e.store.Read().GetTask(e.ctx, tkID)
	if tk.Status != string(domain.TaskInProgress) || tk.ReviewRounds != 1 {
		t.Fatalf("got status=%q rounds=%d, want in_progress/1", tk.Status, tk.ReviewRounds)
	}

	wakes, _ := e.store.Read().ListQueuedWakeups(e.ctx, "ag-wri")
	if len(wakes) != 1 || wakes[0].Kind != domain.WakeTaskUpdate {
		t.Fatalf("expected 1 task_update wakeup for ag-wri, got %+v", wakes)
	}
	evs, _ := e.store.Read().ListTaskEvents(e.ctx, tkID, 20)
	hasNotes := false
	for _, ev := range evs {
		if ev.Kind == "review" && strings.Contains(ev.Body, "Missing benchmark table") {
			hasNotes = true
		}
	}
	if !hasNotes {
		t.Fatalf("expected review notes in task_events, got %+v", evs)
	}
}

func TestThirdRoundGoesToOwner(t *testing.T) {
	e := setupEnv(t)
	seedReviewHierarchy(t, e)

	tkID, _ := e.svc.Create(e.ctx, coord.NewTask{
		Title: "Tricky spec", Description: "Desc", Assignee: ptr("ag-wri"), NeedsReview: true, Origin: domain.OriginOwner,
	})
	_ = e.svc.SetStatus(e.ctx, tkID, domain.TaskInProgress, nil, coord.Actor{Kind: "agent", ID: "ag-wri"})

	// Round 1 -> changes (rounds=1)
	if err := e.svc.SubmitForReview(e.ctx, tkID, "ag-wri"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Review(e.ctx, tkID, "ag-rev", false, "Fix 1"); err != nil {
		t.Fatal(err)
	}
	// Round 2 -> changes (rounds=2)
	if err := e.svc.SubmitForReview(e.ctx, tkID, "ag-wri"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Review(e.ctx, tkID, "ag-rev", false, "Fix 2"); err != nil {
		t.Fatal(err)
	}

	// 3rd submission must bypass agent reviewer and go straight to owner Inbox
	if err := e.svc.SubmitForReview(e.ctx, tkID, "ag-wri"); err != nil {
		t.Fatalf("3rd SubmitForReview: %v", err)
	}
	tk, _, _ := e.store.Read().GetTask(e.ctx, tkID)
	if tk.ReviewerAgentID != nil {
		t.Fatalf("expected reviewer_agent_id=nil (owner) on 3rd round, got %v", tk.ReviewerAgentID)
	}
	items, _ := e.store.Read().ListInboxItems(e.ctx, 20)
	if len(items) == 0 {
		t.Fatal("expected Inbox item created for owner on 3rd review round")
	}
}

func TestEscalateWalksManagers(t *testing.T) {
	e := setupEnv(t)
	seedReviewHierarchy(t, e)

	tkID, _ := e.svc.Create(e.ctx, coord.NewTask{
		Title: "Out of scope legal question", Description: "Desc", Assignee: ptr("ag-wri"), Origin: domain.OriginOwner,
	})

	// Step 1: writer-1 -> team-lead (ag-lead)
	if err := e.svc.Escalate(e.ctx, tkID, "ag-wri", "Needs contract approval"); err != nil {
		t.Fatalf("Escalate 1: %v", err)
	}
	tk, _, _ := e.store.Read().GetTask(e.ctx, tkID)
	if tk.AssigneeAgentID == nil || *tk.AssigneeAgentID != "ag-lead" {
		t.Fatalf("after 1st escalate: assignee=%v, want ag-lead", tk.AssigneeAgentID)
	}

	// Step 2: team-lead -> chief-of-staff (ag-chief)
	if err := e.svc.Escalate(e.ctx, tkID, "ag-lead", "Exceeds team budget"); err != nil {
		t.Fatalf("Escalate 2: %v", err)
	}
	tk, _, _ = e.store.Read().GetTask(e.ctx, tkID)
	if tk.AssigneeAgentID == nil || *tk.AssigneeAgentID != "ag-chief" {
		t.Fatalf("after 2nd escalate: assignee=%v, want ag-chief", tk.AssigneeAgentID)
	}

	// Step 3: chief-of-staff (no manager) -> owner Inbox + waiting_owner
	if err := e.svc.Escalate(e.ctx, tkID, "ag-chief", "Requires company signature"); err != nil {
		t.Fatalf("Escalate 3 (root): %v", err)
	}
	tk, _, _ = e.store.Read().GetTask(e.ctx, tkID)
	if tk.AssigneeAgentID != nil || tk.Status != string(domain.TaskWaitingOwner) {
		t.Fatalf("after root escalate: assignee=%v status=%q, want nil/waiting_owner", tk.AssigneeAgentID, tk.Status)
	}
	inbox, _ := e.store.Read().ListInboxItems(e.ctx, 20)
	if len(inbox) == 0 {
		t.Fatal("expected owner Inbox item after root escalation")
	}
}

func TestEscalateCarriesHops(t *testing.T) {
	e := setupEnv(t)
	seedReviewHierarchy(t, e)

	tkID, err := e.svc.Create(e.ctx, coord.NewTask{
		Title: "Hop test", Description: "Desc", Assignee: ptr("ag-wri"), Hops: 7, Origin: domain.OriginOwner,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Escalate(e.ctx, tkID, "ag-wri", "Passing up"); err != nil {
		t.Fatalf("Escalate: %v", err)
	}
	wakes, _ := e.store.Read().ListQueuedWakeups(e.ctx, "ag-lead")
	if len(wakes) != 1 || wakes[0].Hops != 8 {
		t.Fatalf("expected manager wakeup hops=8, got %+v", wakes)
	}
}
