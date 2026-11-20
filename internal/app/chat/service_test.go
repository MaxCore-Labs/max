package chat_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/app/chat"
	"max/internal/domain"
	"max/internal/testutil/testdb"
)

type chatClock struct{ now time.Time }

func (c *chatClock) Now() time.Time { return c.now }

func setupChatFixture(t *testing.T) (st app.Store, clk *chatClock, svc *chat.Service, agentID domain.ID) {
	t.Helper()
	ctx := context.Background()
	st = testdb.New(t)
	clk = &chatClock{now: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	actorSvc := actor.NewService(st, clk, app.UUIDv7Gen{})
	svc = chat.NewService(st, actorSvc, clk, app.UUIDv7Gen{}, nil)

	agentID = "ag-chief"
	if err := st.WithTx(ctx, func(tx app.Tx) error {
		if err := tx.InsertTeam(ctx, app.TeamRecord{ID: "t1", Slug: "core", Name: "Core", MaxRunning: 2, Status: "active"}); err != nil {
			return err
		}
		return tx.InsertAgent(ctx, app.AgentRecord{
			ID: agentID, Slug: "chief-of-staff", Name: "Chief", TeamID: "t1",
			EnginesJSON: `["agy"]`, EngineMask: 4, ModelsJSON: `{}`,
			Autonomy: domain.AutonomyAuto, Browser: domain.BrowserNone,
			SkillsJSON: `[]`, ParamsJSON: `{}`, ConfigRev: "r1", Status: domain.AgentActive,
		})
	}); err != nil {
		t.Fatalf("seed chat fixture: %v", err)
	}
	return st, clk, svc, agentID
}

func TestSendCreatesMessageAndWake(t *testing.T) {
	ctx := context.Background()
	st, _, svc, agentID := setupChatFixture(t)

	thID, err := svc.CreateThread(ctx, agentID, "Báo cáo sáng")
	if err != nil || thID == "" {
		t.Fatalf("CreateThread: %v", err)
	}

	msgID, wakeID, err := svc.Send(ctx, thID, "Tóm tắt các việc tồn đọng", nil, "")
	if err != nil || msgID == "" || wakeID == "" {
		t.Fatalf("Send = (%q, %q, %v)", msgID, wakeID, err)
	}

	conv, ok, err := st.Read().GetConversationByThread(ctx, agentID, thID)
	if err != nil || !ok {
		t.Fatalf("expected conversation created for thread: ok=%v err=%v", ok, err)
	}

	msgs, _, err := svc.ListMessages(ctx, thID, 10, "")
	if err != nil || len(msgs) != 1 || msgs[0].Role != "user" {
		t.Fatalf("messages = %+v (err=%v), want 1 user message", msgs, err)
	}

	queued, err := st.Read().ListQueuedWakeups(ctx, agentID)
	if err != nil || len(queued) != 1 {
		t.Fatalf("queued wakeups = %d (err=%v), want 1", len(queued), err)
	}
	if queued[0].Kind != domain.WakeMessage || queued[0].Priority != domain.PrioInteractive || queued[0].ConversationID == nil || *queued[0].ConversationID != conv.ID {
		t.Fatalf("unexpected wakeup: %+v", queued[0])
	}
}

func TestSendIdempotent(t *testing.T) {
	ctx := context.Background()
	st, _, svc, agentID := setupChatFixture(t)

	thID, _ := svc.CreateThread(ctx, agentID, "Idempotent")
	m1, w1, err := svc.Send(ctx, thID, "Xin chào", nil, "msg-idem-1")
	if err != nil {
		t.Fatalf("1st Send: %v", err)
	}
	m2, w2, err := svc.Send(ctx, thID, "Xin chào", nil, "msg-idem-1")
	if err != nil {
		t.Fatalf("2nd Send: %v", err)
	}
	if m1 != m2 || w1 == "" || w2 == "" {
		t.Fatalf("got (%q,%q) then (%q,%q)", m1, w1, m2, w2)
	}

	msgs, _, _ := svc.ListMessages(ctx, thID, 10, "")
	if len(msgs) != 1 {
		t.Fatalf("len(msgs) = %d, want 1", len(msgs))
	}
	queued, _ := st.Read().ListQueuedWakeups(ctx, agentID)
	if len(queued) != 1 {
		t.Fatalf("len(queued) = %d, want 1", len(queued))
	}
}

func TestSendWhileBusy(t *testing.T) {
	ctx := context.Background()
	st, clk, svc, agentID := setupChatFixture(t)

	// Agent is currently running a task ("Đối soát hóa đơn") started 7 minutes ago.
	taskID := "task-busy-1"
	otherConv := "conv-task-busy"
	busyRunID := "run-busy-7m"
	startedAt := clk.now.Add(-7 * time.Minute).UnixMilli()

	if err := st.WithTx(ctx, func(tx app.Tx) error {
		if err := tx.InsertTask(ctx, app.TaskRecord{
			ID: taskID, RootID: taskID, RootOrigin: domain.OriginOwner,
			Title: "Đối soát hóa đơn", Description: "Desc", Status: "in_progress",
			AssigneeAgentID: &agentID, CreatedAt: startedAt, UpdatedAt: startedAt,
		}); err != nil {
			return err
		}
		if err := tx.InsertConversation(ctx, app.ConversationRecord{
			ID: otherConv, AgentID: agentID, TaskID: &taskID, Sessions: `{}`,
		}); err != nil {
			return err
		}
		if err := tx.InsertRun(ctx, app.RunRecord{
			ID: busyRunID, AgentID: agentID, ConversationID: otherConv, TaskID: &taskID,
			Status: domain.RunRunning, Priority: domain.PrioTask, Engine: domain.EngineAgy,
			Model: "default", StartedAt: startedAt,
		}); err != nil {
			return err
		}
		return tx.SetAgentActiveRun(ctx, agentID, &busyRunID)
	}); err != nil {
		t.Fatalf("seed busy run: %v", err)
	}

	thID, _ := svc.CreateThread(ctx, agentID, "Hỏi nhanh")
	if _, _, err := svc.Send(ctx, thID, "Đang làm gì đó?", nil, ""); err != nil {
		t.Fatalf("Send while busy: %v", err)
	}

	snap, err := svc.Snapshot(ctx, thID)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap.Messages) != 1 {
		t.Fatalf("len(snap.Messages) = %d, want 1", len(snap.Messages))
	}
	if snap.BusyWith == nil {
		t.Fatal("expected snap.BusyWith != nil when agent is running another conversation")
	}
	if !strings.Contains(snap.BusyWith.Title, "Đối soát hóa đơn") || snap.BusyWith.MinutesRunning != 7 {
		t.Fatalf("BusyWith = %+v, want title 'Đối soát hóa đơn' and 7 minutes", snap.BusyWith)
	}
}

func TestResetClearsSessions(t *testing.T) {
	ctx := context.Background()
	st, _, svc, agentID := setupChatFixture(t)

	thID, _ := svc.CreateThread(ctx, agentID, "Reset me")
	if _, _, err := svc.Send(ctx, thID, "Hello", nil, ""); err != nil {
		t.Fatalf("Send: %v", err)
	}
	conv, _, _ := st.Read().GetConversationByThread(ctx, agentID, thID)
	engStr := "agy"
	if err := st.WithTx(ctx, func(tx app.Tx) error {
		return tx.UpdateConversationAfterRun(ctx, conv.ID, `{"agy":"sess-old"}`, &engStr, 2)
	}); err != nil {
		t.Fatalf("UpdateConversationAfterRun: %v", err)
	}

	if err := svc.Reset(ctx, thID); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	convAfter, _, _ := st.Read().GetConversationByThread(ctx, agentID, thID)
	if convAfter.Sessions != "{}" || convAfter.LastEngine != nil {
		t.Fatalf("expected sessions='{}' and nil last_engine after Reset, got %+v", convAfter)
	}
}
