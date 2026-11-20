package actor_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/domain"
	"max/internal/testutil/testdb"
)

type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time { return c.now }

func seedActorFixture(t *testing.T, st app.Store) (agentID, convID domain.ID) {
	t.Helper()
	ctx := context.Background()
	agentID = "agent-chief"
	convID = "conv-1"
	err := st.WithTx(ctx, func(tx app.Tx) error {
		if err := tx.InsertTeam(ctx, app.TeamRecord{
			ID: "team-core", Slug: "core", Name: "Core", MaxRunning: 2, Status: "active",
		}); err != nil {
			return err
		}
		if err := tx.InsertAgent(ctx, app.AgentRecord{
			ID: agentID, Slug: "chief", Name: "Chief", TeamID: "team-core",
			EnginesJSON: `["agy"]`, EngineMask: 4, ModelsJSON: `{}`,
			Autonomy: domain.AutonomyAuto, Browser: domain.BrowserNone,
			SkillsJSON: `[]`, ParamsJSON: `{}`, ConfigRev: "rev1", Status: domain.AgentActive,
		}); err != nil {
			return err
		}
		return tx.InsertConversation(ctx, app.ConversationRecord{
			ID: convID, AgentID: agentID, Sessions: `{}`,
		})
	})
	if err != nil {
		t.Fatalf("seedActorFixture: %v", err)
	}
	return agentID, convID
}

func TestWakeSetsReady(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	agentID, convID := seedActorFixture(t, st)
	clk := &fakeClock{now: time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)}
	svc := actor.NewService(st, clk, app.UUIDv7Gen{})

	var id domain.ID
	err := st.WithTx(ctx, func(tx app.Tx) error {
		var werr error
		id, _, werr = svc.Wake(ctx, tx, actor.WakeInput{
			AgentID:        agentID,
			Kind:           domain.WakeMessage,
			Priority:       domain.PrioInteractive,
			ConversationID: &convID,
			Payload:        json.RawMessage(`{"msg_id":"m1"}`),
		})
		return werr
	})
	if err != nil || id == "" {
		t.Fatalf("Wake: id=%q err=%v", id, err)
	}

	ag, ok, err := st.Read().GetAgent(ctx, agentID)
	if err != nil || !ok {
		t.Fatalf("GetAgent: ok=%v err=%v", ok, err)
	}
	if !ag.Ready {
		t.Fatal("expected agent.Ready=true")
	}
	if ag.ReadyPriority != int(domain.PrioInteractive) {
		t.Fatalf("ReadyPriority = %d, want %d", ag.ReadyPriority, domain.PrioInteractive)
	}
	if ag.ReadySince == nil || *ag.ReadySince != clk.now.UnixMilli() {
		t.Fatalf("ReadySince = %v, want %d", ag.ReadySince, clk.now.UnixMilli())
	}
}

func TestWakeCoalescesMessages(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	agentID, convID := seedActorFixture(t, st)
	clk := &fakeClock{now: time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)}
	svc := actor.NewService(st, clk, app.UUIDv7Gen{})

	var ids [3]domain.ID
	var coal [3]bool
	for i := range 3 {
		clk.now = clk.now.Add(time.Second)
		err := st.WithTx(ctx, func(tx app.Tx) error {
			var werr error
			ids[i], coal[i], werr = svc.Wake(ctx, tx, actor.WakeInput{
				AgentID:        agentID,
				Kind:           domain.WakeMessage,
				Priority:       domain.PrioInteractive,
				ConversationID: &convID,
				Payload:        json.RawMessage(fmt.Sprintf(`{"seq":%d}`, i+1)),
			})
			return werr
		})
		if err != nil {
			t.Fatalf("Wake %d: %v", i, err)
		}
	}
	if coal[0] || !coal[1] || !coal[2] {
		t.Fatalf("coalesced flags = %v, want [false true true]", coal)
	}
	if ids[1] != ids[0] || ids[2] != ids[0] {
		t.Fatalf("expected coalesced wakeups to return first id %q, got %v", ids[0], ids)
	}

	queued, err := st.Read().ListQueuedWakeups(ctx, agentID)
	if err != nil {
		t.Fatalf("ListQueuedWakeups: %v", err)
	}
	if len(queued) != 1 {
		t.Fatalf("len(queued) = %d, want 1", len(queued))
	}
	if queued[0].CoalescedCount != 2 {
		t.Fatalf("CoalescedCount = %d, want 2", queued[0].CoalescedCount)
	}
}

func TestWakeNeverCoalescesAssignments(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	agentID, convID := seedActorFixture(t, st)
	clk := &fakeClock{now: time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)}
	svc := actor.NewService(st, clk, app.UUIDv7Gen{})

	for i := range 3 {
		err := st.WithTx(ctx, func(tx app.Tx) error {
			_, coal, werr := svc.Wake(ctx, tx, actor.WakeInput{
				AgentID:        agentID,
				Kind:           domain.WakeTaskAssigned,
				Priority:       domain.PrioTask,
				ConversationID: &convID,
				Payload:        json.RawMessage(fmt.Sprintf(`{"task":%d}`, i)),
			})
			if coal {
				t.Fatalf("task_assigned %d unexpectedly coalesced", i)
			}
			return werr
		})
		if err != nil {
			t.Fatalf("Wake %d: %v", i, err)
		}
	}

	queued, err := st.Read().ListQueuedWakeups(ctx, agentID)
	if err != nil {
		t.Fatalf("ListQueuedWakeups: %v", err)
	}
	if len(queued) != 3 {
		t.Fatalf("len(queued) = %d, want 3", len(queued))
	}
}

func TestWakeIdempotent(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	agentID, convID := seedActorFixture(t, st)
	clk := &fakeClock{now: time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)}
	svc := actor.NewService(st, clk, app.UUIDv7Gen{})

	key := "routine:morning:29918880"
	var id1, id2 domain.ID
	var coal1, coal2 bool

	if err := st.WithTx(ctx, func(tx app.Tx) error {
		var werr error
		id1, coal1, werr = svc.Wake(ctx, tx, actor.WakeInput{
			AgentID:        agentID,
			Kind:           domain.WakeTaskAssigned,
			Priority:       domain.PrioRoutine,
			ConversationID: &convID,
			IdempotencyKey: &key,
		})
		return werr
	}); err != nil {
		t.Fatalf("first Wake: %v", err)
	}

	if err := st.WithTx(ctx, func(tx app.Tx) error {
		var werr error
		id2, coal2, werr = svc.Wake(ctx, tx, actor.WakeInput{
			AgentID:        agentID,
			Kind:           domain.WakeTaskAssigned,
			Priority:       domain.PrioRoutine,
			ConversationID: &convID,
			IdempotencyKey: &key,
		})
		return werr
	}); err != nil {
		t.Fatalf("second idempotent Wake: %v", err)
	}
	if coal1 || !coal2 || id1 != id2 {
		t.Fatalf("got (%q,%v) then (%q,%v), want same id and coal2=true", id1, coal1, id2, coal2)
	}

	queued, err := st.Read().ListQueuedWakeups(ctx, agentID)
	if err != nil || len(queued) != 1 {
		t.Fatalf("queued = %d (err=%v), want 1", len(queued), err)
	}
}

func TestWakeFutureNotReady(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	agentID, convID := seedActorFixture(t, st)
	now := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	clk := &fakeClock{now: now}
	svc := actor.NewService(st, clk, app.UUIDv7Gen{})

	if err := st.WithTx(ctx, func(tx app.Tx) error {
		_, _, err := svc.Wake(ctx, tx, actor.WakeInput{
			AgentID:        agentID,
			Kind:           domain.WakeHeartbeat,
			Priority:       domain.PrioMaintenance,
			ConversationID: &convID,
			NotBefore:      now.Add(time.Hour),
		})
		return err
	}); err != nil {
		t.Fatalf("Wake future: %v", err)
	}

	ag, _, err := st.Read().GetAgent(ctx, agentID)
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if ag.Ready {
		t.Fatal("expected Ready=false before NotBefore")
	}

	if err := st.WithTx(ctx, func(tx app.Tx) error {
		return svc.RecomputeReady(ctx, tx, agentID, now.Add(2*time.Hour))
	}); err != nil {
		t.Fatalf("RecomputeReady: %v", err)
	}

	ag2, _, err := st.Read().GetAgent(ctx, agentID)
	if err != nil {
		t.Fatalf("GetAgent after RecomputeReady: %v", err)
	}
	if !ag2.Ready {
		t.Fatal("expected Ready=true after NotBefore elapsed")
	}
}

func TestMailboxFull(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	agentID, convID := seedActorFixture(t, st)
	clk := &fakeClock{now: time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)}
	svc := actor.NewService(st, clk, app.UUIDv7Gen{})

	if err := st.WithTx(ctx, func(tx app.Tx) error {
		for i := range 200 {
			if _, _, err := svc.Wake(ctx, tx, actor.WakeInput{
				AgentID:        agentID,
				Kind:           domain.WakeTaskAssigned,
				Priority:       domain.PrioTask,
				ConversationID: &convID,
				Payload:        json.RawMessage(fmt.Sprintf(`{"n":%d}`, i)),
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("fill 200 wakeups: %v", err)
	}

	// 201st coalescible message (no existing message to coalesce into) must return ErrMailboxFull.
	err := st.WithTx(ctx, func(tx app.Tx) error {
		_, _, werr := svc.Wake(ctx, tx, actor.WakeInput{
			AgentID:        agentID,
			Kind:           domain.WakeMessage,
			Priority:       domain.PrioTask,
			ConversationID: &convID,
		})
		return werr
	})
	if !errors.Is(err, actor.ErrMailboxFull) {
		t.Fatalf("201st message err = %v, want ErrMailboxFull", err)
	}

	// 201st non-coalescible task_assigned MUST still be inserted.
	if err := st.WithTx(ctx, func(tx app.Tx) error {
		_, _, werr := svc.Wake(ctx, tx, actor.WakeInput{
			AgentID:        agentID,
			Kind:           domain.WakeTaskAssigned,
			Priority:       domain.PrioTask,
			ConversationID: &convID,
		})
		return werr
	}); err != nil {
		t.Fatalf("201st task_assigned failed: %v", err)
	}

	queued, err := st.Read().ListQueuedWakeups(ctx, agentID)
	if err != nil || len(queued) != 201 {
		t.Fatalf("len(queued) = %d (err=%v), want 201", len(queued), err)
	}
}

func TestBurstOfOwnerMessages(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	agentID, convID := seedActorFixture(t, st)
	clk := &fakeClock{now: time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)}
	svc := actor.NewService(st, clk, app.UUIDv7Gen{})

	activeRun := "run-busy-1"
	if err := st.WithTx(ctx, func(tx app.Tx) error {
		return tx.SetAgentActiveRun(ctx, agentID, &activeRun)
	}); err != nil {
		t.Fatalf("SetAgentActiveRun: %v", err)
	}

	for i := range 50 {
		clk.now = clk.now.Add(10 * time.Millisecond)
		if err := st.WithTx(ctx, func(tx app.Tx) error {
			_, _, werr := svc.Wake(ctx, tx, actor.WakeInput{
				AgentID:        agentID,
				Kind:           domain.WakeMessage,
				Priority:       domain.PrioInteractive,
				ConversationID: &convID,
				Payload:        json.RawMessage(fmt.Sprintf(`{"msg":%d}`, i)),
			})
			return werr
		}); err != nil {
			t.Fatalf("owner message %d: %v", i, err)
		}
	}

	// Simulate active run ending.
	if err := st.WithTx(ctx, func(tx app.Tx) error {
		if err := tx.SetAgentActiveRun(ctx, agentID, nil); err != nil {
			return err
		}
		return svc.RecomputeReady(ctx, tx, agentID, clk.now)
	}); err != nil {
		t.Fatalf("clear active run: %v", err)
	}

	queued, err := st.Read().ListQueuedWakeups(ctx, agentID)
	if err != nil {
		t.Fatalf("ListQueuedWakeups: %v", err)
	}
	if len(queued) != 1 {
		t.Fatalf("expected 1 coalesced wakeup after 50 burst messages, got %d", len(queued))
	}
	if queued[0].CoalescedCount != 49 {
		t.Fatalf("CoalescedCount = %d, want 49", queued[0].CoalescedCount)
	}
}
