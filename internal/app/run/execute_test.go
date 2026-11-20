package run_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/app/run"
	"max/internal/domain"
	"max/internal/testutil/fakeengine"
	"max/internal/testutil/testdb"
)

type stubClock struct{ now time.Time }

func (c *stubClock) Now() time.Time { return c.now }

type stubPrompts struct{}

func (stubPrompts) System(_ context.Context, agentID domain.ID) (string, error) {
	return "SYSTEM:" + agentID, nil
}

func (stubPrompts) RunMessage(_ context.Context, c app.Claimed) (string, error) {
	if c.EngineChanged {
		return "<context></context>\n<history>prior task summary</history>\n<inbox></inbox>", nil
	}
	return "<context></context>\n<inbox></inbox>", nil
}

type stubVault struct {
	commits int
}

func (v *stubVault) EnsureInstalled(context.Context) error { return nil }
func (v *stubVault) CommitPaths(_ context.Context, paths []string, _ string) (string, error) {
	if len(paths) == 0 {
		return "", nil
	}
	v.commits++
	return "deadbeef", nil
}
func (v *stubVault) PullIfIdle(context.Context, int) error                      { return nil }
func (v *stubVault) Push(context.Context) error                                 { return nil }
func (v *stubVault) Read(context.Context, string) ([]byte, error)               { return nil, nil }
func (v *stubVault) Write(context.Context, string, []byte) error                { return nil }
func (v *stubVault) Remove(context.Context, string) error                       { return nil }
func (v *stubVault) ListDir(context.Context, string) ([]string, error)          { return nil, nil }
func (v *stubVault) HeadSHA(context.Context) (string, error)                    { return "head0", nil }
func (v *stubVault) RevertLastByPrefix(context.Context, string) (string, error) { return "rev0", nil }
func (v *stubVault) MirrorSkills(context.Context) error                         { return nil }
func (v *stubVault) RefuseIfProjectSettings(context.Context) error              { return nil }

func setupClaimedRun(
	t *testing.T,
	st app.Store,
	now time.Time,
	withTask bool,
	priorConvTurns int,
) (actorSvc *actor.Service, claimed actor.Claimed, taskID domain.ID) {
	t.Helper()
	ctx := context.Background()
	clk := &stubClock{now: now}
	actorSvc = actor.NewService(st, clk, app.UUIDv7Gen{})

	convID := "conv-1"
	agentID := "agent-1"
	var taskPtr *domain.ID
	if withTask {
		tid := "task-1"
		taskID = tid
		taskPtr = &tid
	}

	if err := st.WithTx(ctx, func(tx app.Tx) error {
		if err := tx.InsertTeam(ctx, app.TeamRecord{
			ID: "team-1", Slug: "core", Name: "Core", MaxRunning: 3, Status: "active",
		}); err != nil {
			return err
		}
		if err := tx.InsertAgent(ctx, app.AgentRecord{
			ID: agentID, Slug: "chief", Name: "Chief", TeamID: "team-1",
			EnginesJSON: `["agy"]`, EngineMask: 4, ModelsJSON: `{}`,
			Autonomy: domain.AutonomyAuto, Browser: domain.BrowserNone,
			SkillsJSON: `[]`, ParamsJSON: `{}`, ConfigRev: "r1", Status: domain.AgentActive,
		}); err != nil {
			return err
		}
		if withTask {
			if err := tx.InsertTask(ctx, app.TaskRecord{
				ID: taskID, RootID: taskID, RootOrigin: domain.OriginOwner,
				Title: "Write brief", Description: "Desc", Status: "in_progress",
				AssigneeAgentID: &agentID, CreatedAt: now.UnixMilli(), UpdatedAt: now.UnixMilli(),
			}); err != nil {
				return err
			}
		}
		if err := tx.InsertConversation(ctx, app.ConversationRecord{
			ID: convID, AgentID: agentID, TaskID: taskPtr, Sessions: `{}`, TurnsUsed: priorConvTurns,
		}); err != nil {
			return err
		}
		_, _, err := actorSvc.Wake(ctx, tx, actor.WakeInput{
			AgentID: agentID, Kind: domain.WakeTaskAssigned, Priority: domain.PrioTask, ConversationID: &convID,
		})
		return err
	}); err != nil {
		t.Fatalf("setupClaimedRun: %v", err)
	}

	batch, err := actorSvc.ClaimBatch(ctx, actor.Capacity{
		Free: map[domain.EngineName]int{domain.EngineAgy: 2},
	}, now)
	if err != nil || len(batch) != 1 {
		t.Fatalf("ClaimBatch: len=%d err=%v", len(batch), err)
	}
	return actorSvc, batch[0], taskID
}

func TestSucceeded(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	actorSvc, claimed, _ := setupClaimedRun(t, st, now, false, 0)

	eng := fakeengine.New(domain.EngineAgy,
		app.EngineEvent{Type: "session", SessionID: "sess-agy-42"},
		app.EngineEvent{Type: "text_delta", Text: "Hello "},
		app.EngineEvent{Type: "text_delta", Text: "owner!"},
		app.EngineEvent{Type: "final", InputTokens: 120, OutputTokens: 30},
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
	if out.Status != domain.RunSucceeded {
		t.Fatalf("Status = %q, want succeeded", out.Status)
	}

	rRec, _, _ := st.Read().GetRun(ctx, claimed.RunID)
	if rRec.Status != domain.RunSucceeded || rRec.EndedAt == nil {
		t.Fatalf("run record = %+v", rRec)
	}
	wRec, _, _ := st.Read().GetWakeup(ctx, claimed.AgentID, claimed.Wakeups[0].ID)
	if wRec.Status != "done" {
		t.Fatalf("wakeup status = %q, want done", wRec.Status)
	}
	ag, _, _ := st.Read().GetAgent(ctx, claimed.AgentID)
	if ag.ActiveRunID != nil {
		t.Fatalf("active_run_id = %v, want nil", ag.ActiveRunID)
	}
	tm, _, _ := st.Read().GetTeam(ctx, "team-1")
	if tm.Running != 0 {
		t.Fatalf("teams.running = %d, want 0", tm.Running)
	}
	conv, _, _ := st.Read().GetConversation(ctx, claimed.ConversationID)
	if !strings.Contains(conv.Sessions, `"sess-agy-42"`) {
		t.Fatalf("conversations.sessions = %s, want sess-agy-42", conv.Sessions)
	}
}

func makeMultiTurnScript(turns int, endFinal bool) []app.EngineEvent {
	evs := []app.EngineEvent{{Type: "session", SessionID: "s-turns"}}
	for i := range turns {
		evs = append(evs, app.EngineEvent{Type: "tool_call", ToolUseID: "tc", ToolName: "Read"})
		if i < turns-1 {
			evs = append(evs, app.EngineEvent{Type: "tool_result", ToolUseID: "tc", ToolOK: true})
		}
	}
	if endFinal {
		evs = append(evs, app.EngineEvent{Type: "final", InputTokens: 50, OutputTokens: 10})
	}
	return evs
}

func TestTurnLimit(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	actorSvc, claimed, _ := setupClaimedRun(t, st, now, true, 0)

	eng := fakeengine.New(domain.EngineAgy, makeMultiTurnScript(41, true)...)
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
	if out.Status != domain.RunTurnLimit {
		t.Fatalf("Status = %q, want turn_limit", out.Status)
	}

	queued, err := st.Read().ListQueuedWakeups(ctx, claimed.AgentID)
	if err != nil || len(queued) != 1 {
		t.Fatalf("queued = %d (err=%v), want 1 continue wakeup", len(queued), err)
	}
	if queued[0].Kind != domain.WakeContinue || queued[0].ConversationID == nil || *queued[0].ConversationID != claimed.ConversationID {
		t.Fatalf("unexpected continue wakeup: %+v", queued[0])
	}
}

func TestTaskTurnCapBlocks(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	actorSvc, claimed, taskID := setupClaimedRun(t, st, now, true, 395)

	eng := fakeengine.New(domain.EngineAgy, makeMultiTurnScript(6, true)...)
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

	queued, _ := st.Read().ListQueuedWakeups(ctx, claimed.AgentID)
	if len(queued) != 0 {
		t.Fatalf("expected 0 continue wakeups when task hit 400 turns, got %d", len(queued))
	}
	tk, _, _ := st.Read().GetTask(ctx, taskID)
	if tk.Status != "blocked" || tk.BlockedReason == nil || *tk.BlockedReason != "stalled" {
		t.Fatalf("task = (status=%q, reason=%v), want blocked(stalled)", tk.Status, tk.BlockedReason)
	}
	inbox, _ := st.Read().ListInboxItems(ctx, 10)
	if len(inbox) == 0 {
		t.Fatal("expected Inbox item when task stalls at 400 turns")
	}
}

func TestInterruptedRequeues(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	actorSvc, claimed, _ := setupClaimedRun(t, st, now, false, 0)

	eng := fakeengine.New(domain.EngineAgy,
		app.EngineEvent{Type: "session", SessionID: "s1"},
		app.EngineEvent{Type: "error", ErrKind: "crash", ErrMessage: "unexpected eof"},
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
	if out.Status != domain.RunInterrupted {
		t.Fatalf("Status = %q, want interrupted", out.Status)
	}
	wRec, _, _ := st.Read().GetWakeup(ctx, claimed.AgentID, claimed.Wakeups[0].ID)
	if wRec.Status != "queued" || wRec.Attempts != 1 {
		t.Fatalf("wakeup = (status=%q, attempts=%d), want (queued, 1)", wRec.Status, wRec.Attempts)
	}
}

func TestThirdFailureDrops(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	clk := &stubClock{now: now}
	actorSvc := actor.NewService(st, clk, app.UUIDv7Gen{})

	convID := "conv-3fail"
	taskID := "task-3fail"
	agentID := "agent-1"
	if err := st.WithTx(ctx, func(tx app.Tx) error {
		if err := tx.InsertTeam(ctx, app.TeamRecord{ID: "team-1", Slug: "core", Name: "Core", MaxRunning: 2, Status: "active"}); err != nil {
			return err
		}
		if err := tx.InsertAgent(ctx, app.AgentRecord{
			ID: agentID, Slug: "chief", Name: "Chief", TeamID: "team-1",
			EnginesJSON: `["agy"]`, EngineMask: 4, ModelsJSON: `{}`,
			Autonomy: domain.AutonomyAuto, Browser: domain.BrowserNone,
			SkillsJSON: `[]`, ParamsJSON: `{}`, ConfigRev: "r1", Status: domain.AgentActive,
		}); err != nil {
			return err
		}
		if err := tx.InsertTask(ctx, app.TaskRecord{
			ID: taskID, RootID: taskID, RootOrigin: domain.OriginOwner,
			Title: "Flaky", Description: "Desc", Status: "in_progress",
			AssigneeAgentID: &agentID, CreatedAt: now.UnixMilli(), UpdatedAt: now.UnixMilli(),
		}); err != nil {
			return err
		}
		if err := tx.InsertConversation(ctx, app.ConversationRecord{
			ID: convID, AgentID: agentID, TaskID: &taskID, Sessions: `{}`,
		}); err != nil {
			return err
		}
		if err := tx.InsertWakeupRaw(ctx, app.WakeupRecord{
			ID: "w-third", AgentID: agentID, ConversationID: &convID, Kind: domain.WakeTaskAssigned,
			Priority: domain.PrioTask, Payload: string(json.RawMessage(`{}`)), Status: "queued",
			Attempts: 2, CreatedAt: now.UnixMilli(),
		}); err != nil {
			return err
		}
		return tx.RecomputeAgentReady(ctx, agentID, now.UnixMilli())
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	batch, err := actorSvc.ClaimBatch(ctx, actor.Capacity{Free: map[domain.EngineName]int{domain.EngineAgy: 1}}, now)
	if err != nil || len(batch) != 1 {
		t.Fatalf("ClaimBatch: %v", err)
	}
	if batch[0].Wakeups[0].Attempts != 3 {
		t.Fatalf("claimed Attempts = %d, want 3", batch[0].Wakeups[0].Attempts)
	}

	eng := fakeengine.New(domain.EngineAgy, app.EngineEvent{Type: "error", ErrKind: "crash", ErrMessage: "boom"})
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

	wRec, _, _ := st.Read().GetWakeup(ctx, agentID, "w-third")
	if wRec.Status != "dropped" {
		t.Fatalf("wakeup status = %q, want dropped", wRec.Status)
	}
	tk, _, _ := st.Read().GetTask(ctx, taskID)
	if tk.Status != "blocked" || tk.BlockedReason == nil || *tk.BlockedReason != "error" {
		t.Fatalf("task = (status=%q, reason=%v), want blocked(error)", tk.Status, tk.BlockedReason)
	}
	inbox, _ := st.Read().ListInboxItems(ctx, 10)
	if len(inbox) == 0 {
		t.Fatal("expected Inbox item after 3rd failure")
	}
}
