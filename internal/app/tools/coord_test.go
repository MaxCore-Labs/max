package tools_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/app/coord"
	"max/internal/app/tools"
	"max/internal/domain"
	"max/internal/testutil/testdb"
)

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

type coordEnv struct {
	ctx      context.Context
	store    app.Store
	actors   *actor.Service
	coordSvc *coord.Service
	toolSvc  *tools.Service
	rootTask domain.ID
}

func setupCoordToolsEnv(t *testing.T) *coordEnv {
	t.Helper()
	ctx := context.Background()
	st := testdb.New(t)
	clk := fixedClock{now: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	ids := app.UUIDv7Gen{}
	actors := actor.NewService(st, clk, ids)
	coordSvc := coord.NewService(coord.Deps{Store: st, Actors: actors, Clock: clk, IDs: ids})
	toolSvc := tools.NewService(st, t.TempDir(), clk, ids).WithCoord(coordSvc, actors)

	err := st.WithTx(ctx, func(tx app.Tx) error {
		for _, tm := range []struct{ id, slug string }{
			{"t-core", "core"},
			{"t-mkt", "marketing"},
			{"t-sales", "sales"},
		} {
			if err := tx.InsertTeam(ctx, app.TeamRecord{ID: tm.id, Slug: tm.slug, Name: tm.slug, MaxRunning: 4, Status: "active"}); err != nil {
				return err
			}
		}
		mgr := "ag-chief"
		agents := []app.AgentRecord{
			{ID: "ag-chief", Slug: "chief-of-staff", Name: "Chief", TeamID: "t-core", EnginesJSON: `["agy"]`, EngineMask: 4, Autonomy: domain.AutonomyAuto, Browser: domain.BrowserNone, ConfigRev: "1", Status: domain.AgentActive},
			{ID: "ag-mkt-1", Slug: "marketer-1", Name: "Mkt 1", TeamID: "t-mkt", ManagerAgentID: &mgr, EnginesJSON: `["agy"]`, EngineMask: 4, Autonomy: domain.AutonomyAuto, Browser: domain.BrowserNone, ConfigRev: "1", Status: domain.AgentActive},
			{ID: "ag-mkt-2", Slug: "writer-1", Name: "Writer 1", TeamID: "t-mkt", ManagerAgentID: &mgr, EnginesJSON: `["agy"]`, EngineMask: 4, Autonomy: domain.AutonomyAuto, Browser: domain.BrowserNone, ConfigRev: "1", Status: domain.AgentActive},
			{ID: "ag-sales-1", Slug: "sales-1", Name: "Sales 1", TeamID: "t-sales", ManagerAgentID: &mgr, EnginesJSON: `["agy"]`, EngineMask: 4, Autonomy: domain.AutonomyAuto, Browser: domain.BrowserNone, ConfigRev: "1", Status: domain.AgentActive},
		}
		for _, a := range agents {
			if err := tx.InsertAgent(ctx, a); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed agents: %v", err)
	}

	callerID := "ag-mkt-1"
	rootID, err := coordSvc.Create(ctx, coord.NewTask{
		Title:          "Campaign launch",
		Description:    "Launch Q4 campaign",
		Assignee:       &callerID,
		CreatedByAgent: &callerID,
		Origin:         domain.OriginOwner,
	})
	if err != nil {
		t.Fatalf("create root task: %v", err)
	}

	var convID domain.ID
	_ = st.WithTx(ctx, func(tx app.Tx) error {
		convID, _ = tx.EnsureTaskConversation(ctx, "ag-mkt-1", rootID, "conv-mkt-1")
		return tx.InsertRun(ctx, app.RunRecord{
			ID: "run-mkt-1", AgentID: "ag-mkt-1", ConversationID: convID, TaskID: &rootID,
			Status: domain.RunRunning, Priority: domain.PrioTask, Engine: domain.EngineAgy, Model: "default", StartedAt: 100,
		})
	})

	return &coordEnv{
		ctx:      ctx,
		store:    st,
		actors:   actors,
		coordSvc: coordSvc,
		toolSvc:  toolSvc,
		rootTask: rootID,
	}
}

func mktCaller(rootTask domain.ID, hops int) tools.Caller {
	return tools.Caller{
		RunID:          "run-mkt-1",
		AgentID:        "ag-mkt-1",
		ConversationID: "conv-mkt-1",
		TaskID:         &rootTask,
		Hops:           hops,
		Origin:         domain.OriginOwner,
		Policy: domain.AgentPolicy{
			Scope:    domain.AgentScope{Slug: "marketer-1", TeamSlug: "marketing"},
			Autonomy: domain.AutonomyAuto,
		},
	}
}

func TestDelegateCreatesChildOfCurrentTask(t *testing.T) {
	e := setupCoordToolsEnv(t)
	c := mktCaller(e.rootTask, 3)

	res := e.toolSvc.Delegate(e.ctx, c, "writer-1", "Draft landing copy", "Write hero + CTA", "3 variants", nil, true, nil)
	if res.IsError {
		t.Fatalf("Delegate failed: %s", res.Text)
	}

	children, err := e.store.Read().ListChildTasks(e.ctx, e.rootTask)
	if err != nil || len(children) != 1 {
		t.Fatalf("expected 1 child task, got %d (err=%v)", len(children), err)
	}
	ch := children[0]
	if ch.ParentID == nil || *ch.ParentID != e.rootTask {
		t.Fatalf("parent_id=%v, want %s", ch.ParentID, e.rootTask)
	}
	if ch.CreatedByAgent == nil || *ch.CreatedByAgent != "ag-mkt-1" {
		t.Fatalf("created_by_agent=%v, want ag-mkt-1", ch.CreatedByAgent)
	}
	wakes, _ := e.store.Read().ListQueuedWakeups(e.ctx, "ag-mkt-2")
	if len(wakes) != 1 || wakes[0].Hops != 4 {
		t.Fatalf("expected wakeup with hops=4, got %+v", wakes)
	}
}

func TestDelegateEnforcesRules(t *testing.T) {
	e := setupCoordToolsEnv(t)

	// Cross-team non-manager non-chief -> error mentioning team/target rule
	res := e.toolSvc.Delegate(e.ctx, mktCaller(e.rootTask, 1), "sales-1", "Cold email", "Desc", "", nil, false, nil)
	if !res.IsError || !strings.Contains(res.Text, "team") {
		t.Fatalf("expected cross-team error mentioning cap/rule, got: %+v", res)
	}

	// Hops == 20 -> error mentioning 20 + creates Inbox item
	res = e.toolSvc.Delegate(e.ctx, mktCaller(e.rootTask, 20), "writer-1", "Too deep hops", "Desc", "", nil, false, nil)
	if !res.IsError || !strings.Contains(res.Text, "20") {
		t.Fatalf("expected hops=20 error mentioning 20, got: %+v", res)
	}
}

func TestDelegateEleventhInRunFails(t *testing.T) {
	e := setupCoordToolsEnv(t)
	c := mktCaller(e.rootTask, 0)

	for i := 1; i <= 10; i++ {
		res := e.toolSvc.Delegate(e.ctx, c, "writer-1", fmt.Sprintf("Subtask %d", i), "Desc", "", nil, false, nil)
		if res.IsError {
			t.Fatalf("delegate #%d failed unexpectedly: %s", i, res.Text)
		}
	}
	res11 := e.toolSvc.Delegate(e.ctx, c, "writer-1", "Subtask 11", "Desc", "", nil, false, nil)
	if !res11.IsError || !strings.Contains(res11.Text, "10") {
		t.Fatalf("expected 11th delegate to fail mentioning cap 10, got: %+v", res11)
	}
}

func TestDelegateMailboxFull(t *testing.T) {
	e := setupCoordToolsEnv(t)
	// Fill writer-1 mailbox with 200 distinct non-coalescible wakeups
	err := e.store.WithTx(e.ctx, func(tx app.Tx) error {
		convID, err := tx.EnsureSystemConversation(e.ctx, "ag-mkt-2", "conv-full")
		if err != nil {
			return err
		}
		for i := range 200 {
			if err := tx.InsertWakeupRaw(e.ctx, app.WakeupRecord{
				ID: fmt.Sprintf("w-full-%03d", i), AgentID: "ag-mkt-2", ConversationID: &convID,
				Kind: domain.WakeTaskAssigned, Priority: domain.PrioTask, Payload: `{}`, Status: "queued", CreatedAt: int64(i + 1),
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("fill mailbox: %v", err)
	}

	res := e.toolSvc.Delegate(e.ctx, mktCaller(e.rootTask, 0), "writer-1", "Should rollback", "Desc", "", nil, false, nil)
	if !res.IsError || !strings.Contains(strings.ToLower(res.Text), "hộp thư đầy") {
		t.Fatalf("expected 'hộp thư đầy' error, got: %+v", res)
	}
	children, _ := e.store.Read().ListChildTasks(e.ctx, e.rootTask)
	if len(children) != 0 {
		t.Fatalf("expected 0 child tasks created when mailbox full, got %d", len(children))
	}
}

func TestMessageAgentWritesTaskEvent(t *testing.T) {
	e := setupCoordToolsEnv(t)
	secretBody := "Please align headline tone with brand guide section 4.2"

	res := e.toolSvc.MessageAgent(e.ctx, mktCaller(e.rootTask, 2), "writer-1", secretBody)
	if res.IsError {
		t.Fatalf("MessageAgent failed: %s", res.Text)
	}

	evs, err := e.store.Read().ListTaskEvents(e.ctx, e.rootTask, 50)
	if err != nil {
		t.Fatal(err)
	}
	foundMsg := false
	for _, ev := range evs {
		if ev.Kind == "message" && ev.Body == secretBody {
			foundMsg = true
		}
	}
	if !foundMsg {
		t.Fatalf("expected message body stored in task_events, got %+v", evs)
	}

	wakes, _ := e.store.Read().ListQueuedWakeups(e.ctx, "ag-mkt-2")
	if len(wakes) != 1 {
		t.Fatalf("expected 1 wakeup for writer-1, got %+v", wakes)
	}
	if strings.Contains(wakes[0].Payload, secretBody) {
		t.Fatalf("wakeup payload must carry reference only, not body: %s", wakes[0].Payload)
	}
	if !strings.Contains(wakes[0].Payload, "event_id") {
		t.Fatalf("expected event_id reference in wakeup payload: %s", wakes[0].Payload)
	}
}

func TestSuggestAgentCannotDelegate(t *testing.T) {
	e := setupCoordToolsEnv(t)
	c := mktCaller(e.rootTask, 0)
	c.Policy.Autonomy = domain.AutonomySuggest

	res := e.toolSvc.Delegate(e.ctx, c, "writer-1", "Forbidden delegate", "Desc", "", nil, false, nil)
	if !res.IsError || !strings.Contains(res.Text, "denied") {
		t.Fatalf("expected suggest agent Delegate denied, got: %+v", res)
	}
}

func TestTaskListScoped(t *testing.T) {
	e := setupCoordToolsEnv(t)
	salesAgent := "ag-sales-1"
	_, err := e.coordSvc.Create(e.ctx, coord.NewTask{
		Title: "Secret enterprise deal", Description: "Sales only",
		Assignee: &salesAgent, CreatedByAgent: &salesAgent, Origin: domain.OriginOwner,
	})
	if err != nil {
		t.Fatal(err)
	}

	res := e.toolSvc.TaskList(e.ctx, mktCaller(e.rootTask, 0), "", 50)
	if res.IsError {
		t.Fatalf("TaskList failed: %s", res.Text)
	}
	if !strings.Contains(res.Text, "Campaign launch") {
		t.Fatalf("expected own team task in TaskList: %s", res.Text)
	}
	if strings.Contains(res.Text, "Secret enterprise deal") {
		t.Fatalf("TaskList leaked other team's task: %s", res.Text)
	}
}

func TestTaskGetOutputCapped(t *testing.T) {
	e := setupCoordToolsEnv(t)
	for i := 1; i <= 500; i++ {
		body := fmt.Sprintf("Comment #%03d: %s", i, strings.Repeat("detailed review observation ", 3))
		if err := e.coordSvc.Comment(e.ctx, e.rootTask, coord.Actor{Kind: "agent", ID: "ag-mkt-1"}, body); err != nil {
			t.Fatalf("comment %d: %v", i, err)
		}
	}

	res := e.toolSvc.TaskGet(e.ctx, mktCaller(e.rootTask, 0), e.rootTask)
	if res.IsError {
		t.Fatalf("TaskGet failed: %s", res.Text)
	}
	if utf8.RuneCountInString(res.Text) > 8000 || len(res.Text) > 8000 {
		t.Fatalf("TaskGet exceeded 8000-char cap: bytes=%d runes=%d", len(res.Text), utf8.RuneCountInString(res.Text))
	}
	if !strings.Contains(res.Text, "còn ") {
		t.Fatalf("expected remaining comment count in capped output, got tail: %s", res.Text[max(0, len(res.Text)-200):])
	}
}
