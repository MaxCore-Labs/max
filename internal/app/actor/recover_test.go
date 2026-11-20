package actor_test

import (
	"context"
	"testing"
	"time"

	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/domain"
	"max/internal/testutil/testdb"
)

func TestEnginePick(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	clk := &fakeClock{now: now}
	svc := actor.NewService(st, clk, app.UUIDv7Gen{})

	convID := "conv-switch"
	lastEng := string(domain.EngineAgy)
	if err := st.WithTx(ctx, func(tx app.Tx) error {
		insertTeamAndAgent(t, tx, "t1", "core", 3, "ag-multi", "multi", `["agy","claude"]`, 5)
		if err := tx.InsertConversation(ctx, app.ConversationRecord{
			ID: convID, AgentID: "ag-multi", Sessions: `{"agy":"sess-agy-1"}`, LastEngine: &lastEng,
		}); err != nil {
			return err
		}
		_, _, err := svc.Wake(ctx, tx, actor.WakeInput{
			AgentID: "ag-multi", Kind: domain.WakeTaskAssigned, Priority: domain.PrioTask, ConversationID: &convID,
		})
		return err
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	got, err := svc.ClaimBatch(ctx, actor.Capacity{
		Free: map[domain.EngineName]int{domain.EngineAgy: 0, domain.EngineClaude: 1},
	}, now)
	if err != nil || len(got) != 1 {
		t.Fatalf("ClaimBatch: len=%d err=%v", len(got), err)
	}
	if got[0].Engine != domain.EngineClaude {
		t.Fatalf("Engine = %q, want claude", got[0].Engine)
	}
	if !got[0].EngineChanged {
		t.Fatal("expected EngineChanged=true when switching away from conversation's prior agy session")
	}
	if got[0].SessionID != "" {
		t.Fatalf("SessionID = %q, want empty on new engine", got[0].SessionID)
	}
}

func TestRecover(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	clk := &fakeClock{now: now}
	svc := actor.NewService(st, clk, app.UUIDv7Gen{})

	run1, run2 := "run-crash-1", "run-crash-2"
	if err := st.WithTx(ctx, func(tx app.Tx) error {
		insertTeamAndAgent(t, tx, "t1", "core", 4, "ag-1", "ag-1", `["agy"]`, 4)
		insertTeamAndAgent(t, tx, "t1", "core", 4, "ag-2", "ag-2", `["agy"]`, 4)
		c1, c2 := "c-1", "c-2"
		if err := tx.InsertConversation(ctx, app.ConversationRecord{ID: c1, AgentID: "ag-1", Sessions: `{}`}); err != nil {
			return err
		}
		if err := tx.InsertConversation(ctx, app.ConversationRecord{ID: c2, AgentID: "ag-2", Sessions: `{}`}); err != nil {
			return err
		}
		if err := tx.InsertRun(ctx, app.RunRecord{
			ID: run1, AgentID: "ag-1", ConversationID: c1, Status: domain.RunRunning,
			Priority: domain.PrioTask, Engine: domain.EngineAgy, Model: "default", Attempt: 1, StartedAt: now.UnixMilli(),
		}); err != nil {
			return err
		}
		if err := tx.InsertRun(ctx, app.RunRecord{
			ID: run2, AgentID: "ag-2", ConversationID: c2, Status: domain.RunRunning,
			Priority: domain.PrioTask, Engine: domain.EngineAgy, Model: "default", Attempt: 3, StartedAt: now.UnixMilli(),
		}); err != nil {
			return err
		}
		if err := tx.SetAgentActiveRun(ctx, "ag-1", &run1); err != nil {
			return err
		}
		if err := tx.SetAgentActiveRun(ctx, "ag-2", &run2); err != nil {
			return err
		}
		if err := tx.AdjustTeamRunning(ctx, "t1", 2); err != nil {
			return err
		}
		if err := tx.InsertWakeupRaw(ctx, app.WakeupRecord{
			ID: "w-ok", AgentID: "ag-1", ConversationID: &c1, Kind: domain.WakeTaskAssigned,
			Priority: domain.PrioTask, Payload: `{}`, Status: "claimed", RunID: &run1, Attempts: 1, CreatedAt: now.UnixMilli(),
		}); err != nil {
			return err
		}
		return tx.InsertWakeupRaw(ctx, app.WakeupRecord{
			ID: "w-poison", AgentID: "ag-2", ConversationID: &c2, Kind: domain.WakeTaskAssigned,
			Priority: domain.PrioTask, Payload: `{}`, Status: "claimed", RunID: &run2, Attempts: 3, CreatedAt: now.UnixMilli(),
		})
	}); err != nil {
		t.Fatalf("seed crash state: %v", err)
	}

	interrupted, err := svc.Recover(ctx, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if interrupted != 2 {
		t.Fatalf("interrupted = %d, want 2", interrupted)
	}

	r1, _, _ := st.Read().GetRun(ctx, run1)
	r2, _, _ := st.Read().GetRun(ctx, run2)
	if r1.Status != domain.RunInterrupted || r2.Status != domain.RunInterrupted {
		t.Fatalf("runs status = (%q, %q), want interrupted", r1.Status, r2.Status)
	}
	w1, _, _ := st.Read().GetWakeup(ctx, "ag-1", "w-ok")
	w2, _, _ := st.Read().GetWakeup(ctx, "ag-2", "w-poison")
	if w1.Status != "queued" || w1.RunID != nil {
		t.Fatalf("w1 = %+v, want queued with nil run_id", w1)
	}
	if w2.Status != "dropped" {
		t.Fatalf("w2.Status = %q, want dropped", w2.Status)
	}
	inbox, err := st.Read().ListInboxItems(ctx, 10)
	if err != nil || len(inbox) != 1 {
		t.Fatalf("inbox = %d (err=%v), want 1 item for dropped wakeup", len(inbox), err)
	}
	ag1, _, _ := st.Read().GetAgent(ctx, "ag-1")
	ag2, _, _ := st.Read().GetAgent(ctx, "ag-2")
	if ag1.ActiveRunID != nil || ag2.ActiveRunID != nil {
		t.Fatal("expected active_run_id NULL after Recover")
	}
	if !ag1.Ready || ag2.Ready {
		t.Fatalf("ready = (%v, %v), want (true, false)", ag1.Ready, ag2.Ready)
	}
	tm, _, _ := st.Read().GetTeam(ctx, "t1")
	if tm.Running != 0 {
		t.Fatalf("teams.running = %d, want 0", tm.Running)
	}

	run3 := "run-sigterm"
	c1 := "c-1"
	if err := st.WithTx(ctx, func(tx app.Tx) error {
		if err := tx.InsertRun(ctx, app.RunRecord{
			ID: run3, AgentID: "ag-1", ConversationID: c1, Status: domain.RunRunning,
			Priority: domain.PrioTask, Engine: domain.EngineAgy, Model: "default", Attempt: 3, StartedAt: now.UnixMilli(),
		}); err != nil {
			return err
		}
		if err := tx.SetAgentActiveRun(ctx, "ag-1", &run3); err != nil {
			return err
		}
		return tx.InsertWakeupRaw(ctx, app.WakeupRecord{
			ID: "w-sigterm", AgentID: "ag-1", ConversationID: &c1, Kind: domain.WakeTaskAssigned,
			Priority: domain.PrioTask, Payload: `{}`, Status: "claimed", RunID: &run3, Attempts: 3, CreatedAt: now.UnixMilli(),
		})
	}); err != nil {
		t.Fatalf("seed sigterm state: %v", err)
	}

	if n, err := svc.RecoverGraceful(ctx, now.Add(2*time.Minute)); err != nil || n != 1 {
		t.Fatalf("RecoverGraceful = (%d, %v), want (1, nil)", n, err)
	}
	wSig, _, _ := st.Read().GetWakeup(ctx, "ag-1", "w-sigterm")
	if wSig.Status != "queued" || wSig.Attempts != 2 {
		t.Fatalf("graceful shutdown wakeup = (status=%q, attempts=%d), want (queued, 2)", wSig.Status, wSig.Attempts)
	}
}
