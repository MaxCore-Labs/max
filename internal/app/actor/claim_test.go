package actor_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/domain"
	"max/internal/testutil/testdb"
)

func insertTeamAndAgent(t *testing.T, tx app.Tx, teamID, teamSlug string, maxRun int, agentID, slug string, engines string, mask int) {
	t.Helper()
	ctx := context.Background()
	if _, ok, _ := tx.GetTeam(ctx, teamID); !ok {
		if err := tx.InsertTeam(ctx, app.TeamRecord{
			ID: teamID, Slug: teamSlug, Name: teamSlug, MaxRunning: maxRun, Status: "active",
		}); err != nil {
			t.Fatalf("InsertTeam(%s): %v", teamSlug, err)
		}
	}
	if err := tx.InsertAgent(ctx, app.AgentRecord{
		ID: agentID, Slug: slug, Name: slug, TeamID: teamID,
		EnginesJSON: engines, EngineMask: mask, ModelsJSON: `{}`,
		Autonomy: domain.AutonomyAuto, Browser: domain.BrowserNone,
		SkillsJSON: `[]`, ParamsJSON: `{}`, ConfigRev: "rev1", Status: domain.AgentActive,
	}); err != nil {
		t.Fatalf("InsertAgent(%s): %v", slug, err)
	}
}

func TestClaimOrder(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	clk := &fakeClock{now: now}
	svc := actor.NewService(st, clk, app.UUIDv7Gen{})

	if err := st.WithTx(ctx, func(tx app.Tx) error {
		insertTeamAndAgent(t, tx, "t1", "core", 5, "a-later-task", "later", `["agy"]`, 4)
		insertTeamAndAgent(t, tx, "t1", "core", 5, "a-older-task", "older", `["agy"]`, 4)
		insertTeamAndAgent(t, tx, "t1", "core", 5, "a-interactive", "inter", `["agy"]`, 4)
		for _, pair := range [][2]string{{"c1", "a-later-task"}, {"c2", "a-older-task"}, {"c3", "a-interactive"}} {
			if err := tx.InsertConversation(ctx, app.ConversationRecord{ID: pair[0], AgentID: pair[1], Sessions: `{}`}); err != nil {
				return err
			}
		}
		c1, c2, c3 := "c1", "c2", "c3"
		clk.now = now.Add(1 * time.Second)
		if _, _, err := svc.Wake(ctx, tx, actor.WakeInput{AgentID: "a-older-task", Kind: domain.WakeTaskAssigned, Priority: domain.PrioTask, ConversationID: &c2}); err != nil {
			return err
		}
		clk.now = now.Add(5 * time.Second)
		if _, _, err := svc.Wake(ctx, tx, actor.WakeInput{AgentID: "a-later-task", Kind: domain.WakeTaskAssigned, Priority: domain.PrioTask, ConversationID: &c1}); err != nil {
			return err
		}
		clk.now = now.Add(10 * time.Second)
		if _, _, err := svc.Wake(ctx, tx, actor.WakeInput{AgentID: "a-interactive", Kind: domain.WakeMessage, Priority: domain.PrioInteractive, ConversationID: &c3}); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	got, err := svc.ClaimBatch(ctx, actor.Capacity{
		Free: map[domain.EngineName]int{domain.EngineAgy: 3},
	}, now.Add(20*time.Second))
	if err != nil {
		t.Fatalf("ClaimBatch: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3", len(got))
	}
	wantOrder := []string{"a-interactive", "a-older-task", "a-later-task"}
	for i, want := range wantOrder {
		if got[i].AgentID != want {
			t.Fatalf("got[%d].AgentID = %q, want %q", i, got[i].AgentID, want)
		}
	}
}

func TestClaimOneRunPerAgent(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	agentID, convID := seedActorFixture(t, st)
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	clk := &fakeClock{now: now}
	svc := actor.NewService(st, clk, app.UUIDv7Gen{})

	busyRun := "run-busy"
	if err := st.WithTx(ctx, func(tx app.Tx) error {
		if err := tx.SetAgentActiveRun(ctx, agentID, &busyRun); err != nil {
			return err
		}
		_, _, err := svc.Wake(ctx, tx, actor.WakeInput{
			AgentID: agentID, Kind: domain.WakeMessage, Priority: domain.PrioInteractive, ConversationID: &convID,
		})
		return err
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	got, err := svc.ClaimBatch(ctx, actor.Capacity{Free: map[domain.EngineName]int{domain.EngineAgy: 3}}, now)
	if err != nil {
		t.Fatalf("ClaimBatch: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 claims while agent has active_run_id, got %d", len(got))
	}
}

func TestClaimRespectsFreeSlots(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	clk := &fakeClock{now: now}
	svc := actor.NewService(st, clk, app.UUIDv7Gen{})

	if err := st.WithTx(ctx, func(tx app.Tx) error {
		for i := range 3 {
			aid := fmt.Sprintf("ag-%d", i)
			cid := fmt.Sprintf("cv-%d", i)
			insertTeamAndAgent(t, tx, "t1", "core", 5, aid, aid, `["agy"]`, 4)
			if err := tx.InsertConversation(ctx, app.ConversationRecord{ID: cid, AgentID: aid, Sessions: `{}`}); err != nil {
				return err
			}
			if _, _, err := svc.Wake(ctx, tx, actor.WakeInput{AgentID: aid, Kind: domain.WakeTaskAssigned, Priority: domain.PrioTask, ConversationID: &cid}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	got, err := svc.ClaimBatch(ctx, actor.Capacity{Free: map[domain.EngineName]int{domain.EngineAgy: 1}}, now)
	if err != nil {
		t.Fatalf("ClaimBatch: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
}

func TestClaimTeamCap(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	clk := &fakeClock{now: now}
	svc := actor.NewService(st, clk, app.UUIDv7Gen{})

	if err := st.WithTx(ctx, func(tx app.Tx) error {
		for i := range 5 {
			aid := fmt.Sprintf("mkt-%d", i)
			cid := fmt.Sprintf("cm-%d", i)
			insertTeamAndAgent(t, tx, "team-mkt", "mkt", 2, aid, aid, `["agy"]`, 4)
			if err := tx.InsertConversation(ctx, app.ConversationRecord{ID: cid, AgentID: aid, Sessions: `{}`}); err != nil {
				return err
			}
			clk.now = now.Add(time.Duration(i) * time.Second)
			if _, _, err := svc.Wake(ctx, tx, actor.WakeInput{AgentID: aid, Kind: domain.WakeTaskAssigned, Priority: domain.PrioTask, ConversationID: &cid}); err != nil {
				return err
			}
		}
		insertTeamAndAgent(t, tx, "team-ops", "ops", 2, "ops-0", "ops-0", `["agy"]`, 4)
		cOps := "co-0"
		if err := tx.InsertConversation(ctx, app.ConversationRecord{ID: cOps, AgentID: "ops-0", Sessions: `{}`}); err != nil {
			return err
		}
		clk.now = now.Add(10 * time.Second)
		_, _, err := svc.Wake(ctx, tx, actor.WakeInput{AgentID: "ops-0", Kind: domain.WakeTaskAssigned, Priority: domain.PrioTask, ConversationID: &cOps})
		return err
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	got, err := svc.ClaimBatch(ctx, actor.Capacity{Free: map[domain.EngineName]int{domain.EngineAgy: 3}}, now.Add(20*time.Second))
	if err != nil {
		t.Fatalf("ClaimBatch: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3 (2 from team-mkt + 1 from team-ops)", len(got))
	}
	if got[2].AgentID != "ops-0" {
		t.Fatalf("got[2].AgentID = %q, want ops-0", got[2].AgentID)
	}
}

func TestDrainSingleConversation(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	agentID, conv1 := seedActorFixture(t, st)
	conv2 := "conv-2"
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	clk := &fakeClock{now: now}
	svc := actor.NewService(st, clk, app.UUIDv7Gen{})

	if err := st.WithTx(ctx, func(tx app.Tx) error {
		if err := tx.InsertConversation(ctx, app.ConversationRecord{ID: conv2, AgentID: agentID, Sessions: `{}`}); err != nil {
			return err
		}
		if _, _, err := svc.Wake(ctx, tx, actor.WakeInput{AgentID: agentID, Kind: domain.WakeTaskAssigned, Priority: domain.PrioTask, ConversationID: &conv2}); err != nil {
			return err
		}
		if _, _, err := svc.Wake(ctx, tx, actor.WakeInput{AgentID: agentID, Kind: domain.WakeMessage, Priority: domain.PrioInteractive, ConversationID: &conv1}); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	got, err := svc.ClaimBatch(ctx, actor.Capacity{Free: map[domain.EngineName]int{domain.EngineAgy: 2}}, now)
	if err != nil || len(got) != 1 {
		t.Fatalf("ClaimBatch: len=%d err=%v, want 1", len(got), err)
	}
	if got[0].ConversationID != conv1 || len(got[0].Wakeups) != 1 {
		t.Fatalf("claimed conv=%q wakeups=%d, want %q and 1", got[0].ConversationID, len(got[0].Wakeups), conv1)
	}

	ag, _, err := st.Read().GetAgent(ctx, agentID)
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if !ag.Ready {
		t.Fatal("expected agent.Ready=true because conv2 wakeup remains queued")
	}
}

func TestClaimNullConversationID(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	agentID, _ := seedActorFixture(t, st)
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	clk := &fakeClock{now: now}
	svc := actor.NewService(st, clk, app.UUIDv7Gen{})

	if err := st.WithTx(ctx, func(tx app.Tx) error {
		_, _, err := svc.Wake(ctx, tx, actor.WakeInput{
			AgentID:  agentID,
			Kind:     domain.WakeHeartbeat,
			Priority: domain.PrioMaintenance,
		})
		return err
	}); err != nil {
		t.Fatalf("Wake heartbeat: %v", err)
	}

	got, err := svc.ClaimBatch(ctx, actor.Capacity{Free: map[domain.EngineName]int{domain.EngineAgy: 1}}, now)
	if err != nil || len(got) != 1 {
		t.Fatalf("ClaimBatch: len=%d err=%v, want 1", len(got), err)
	}
	if got[0].ConversationID == "" {
		t.Fatal("expected non-empty system ConversationID")
	}
}

func TestDrainCapsAt20(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	agentID, convID := seedActorFixture(t, st)
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	clk := &fakeClock{now: now}
	svc := actor.NewService(st, clk, app.UUIDv7Gen{})

	if err := st.WithTx(ctx, func(tx app.Tx) error {
		for i := range 25 {
			if _, _, err := svc.Wake(ctx, tx, actor.WakeInput{
				AgentID:        agentID,
				Kind:           domain.WakeTaskAssigned,
				Priority:       domain.PrioTask,
				ConversationID: &convID,
				Payload:        json.RawMessage(fmt.Sprintf(`{"i":%d}`, i)),
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	got, err := svc.ClaimBatch(ctx, actor.Capacity{Free: map[domain.EngineName]int{domain.EngineAgy: 1}}, now)
	if err != nil || len(got) != 1 {
		t.Fatalf("ClaimBatch: len=%d err=%v", len(got), err)
	}
	if len(got[0].Wakeups) != 20 {
		t.Fatalf("len(Wakeups) = %d, want 20", len(got[0].Wakeups))
	}
	rem, err := st.Read().ListQueuedWakeups(ctx, agentID)
	if err != nil || len(rem) != 5 {
		t.Fatalf("remaining queued = %d (err=%v), want 5", len(rem), err)
	}
}

func TestClaimDefersOnQuota(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	agentID, convID := seedActorFixture(t, st)
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	clk := &fakeClock{now: now}
	svc := actor.NewService(st, clk, app.UUIDv7Gen{})

	if err := st.WithTx(ctx, func(tx app.Tx) error {
		_, _, err := svc.Wake(ctx, tx, actor.WakeInput{
			AgentID: agentID, Kind: domain.WakeTaskAssigned, Priority: domain.PrioTask, ConversationID: &convID,
		})
		return err
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	got, err := svc.ClaimBatch(ctx, actor.Capacity{
		Free: map[domain.EngineName]int{domain.EngineAgy: 2},
		Admit: func(_ domain.EngineName, p domain.Priority) domain.Admission {
			if p < domain.PrioInteractive {
				return domain.DeferShare
			}
			return domain.AdmitNow
		},
	}, now)
	if err != nil {
		t.Fatalf("ClaimBatch: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 claims when quota defers PrioTask, got %d", len(got))
	}
	rem, _ := st.Read().ListQueuedWakeups(ctx, agentID)
	if len(rem) != 1 {
		t.Fatalf("expected wakeup to remain queued, got %d", len(rem))
	}
}

func TestCooldownSkippedForInteractive(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	agentID, convID := seedActorFixture(t, st)
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	clk := &fakeClock{now: now}
	svc := actor.NewService(st, clk, app.UUIDv7Gen{})

	if err := st.WithTx(ctx, func(tx app.Tx) error {
		if err := tx.SetAgentCooldown(ctx, agentID, now.Add(30*time.Second).UnixMilli()); err != nil {
			return err
		}
		_, _, err := svc.Wake(ctx, tx, actor.WakeInput{
			AgentID: agentID, Kind: domain.WakeTaskAssigned, Priority: domain.PrioTask, ConversationID: &convID,
		})
		return err
	}); err != nil {
		t.Fatalf("setup task wake: %v", err)
	}

	gotTask, err := svc.ClaimBatch(ctx, actor.Capacity{Free: map[domain.EngineName]int{domain.EngineAgy: 1}}, now)
	if err != nil || len(gotTask) != 0 {
		t.Fatalf("expected cooldown to defer PrioTask, got len=%d err=%v", len(gotTask), err)
	}

	// Now wake with PrioInteractive while still inside cooldown.
	if err := st.WithTx(ctx, func(tx app.Tx) error {
		_, _, err := svc.Wake(ctx, tx, actor.WakeInput{
			AgentID: agentID, Kind: domain.WakeMessage, Priority: domain.PrioInteractive, ConversationID: &convID,
		})
		return err
	}); err != nil {
		t.Fatalf("setup interactive wake: %v", err)
	}

	gotInter, err := svc.ClaimBatch(ctx, actor.Capacity{Free: map[domain.EngineName]int{domain.EngineAgy: 1}}, now)
	if err != nil || len(gotInter) != 1 {
		t.Fatalf("expected PrioInteractive to bypass cooldown, got len=%d err=%v", len(gotInter), err)
	}
}
