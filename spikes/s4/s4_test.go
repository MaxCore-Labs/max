package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func setupSingleAgentTestStore(t *testing.T, subDir string) *store {
	t.Helper()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), subDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	st, err := openStore(ctx, filepath.Join(dir, "actor.db"), "optimized")
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = st.close() })

	if _, err := st.writeDB.ExecContext(ctx, `INSERT INTO teams (id, slug, name, max_running) VALUES ('t1', 'core', 'Core', 2)`); err != nil {
		t.Fatalf("insert team: %v", err)
	}
	if _, err := st.writeDB.ExecContext(ctx, `INSERT INTO agents (id, slug, name, team_id, config_rev) VALUES ('a1', 'cos', 'Chief', 't1', 'r1')`); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	if _, err := st.writeDB.ExecContext(ctx, `INSERT INTO conversations (id, agent_id, task_id) VALUES ('c1', 'a1', 'task-1')`); err != nil {
		t.Fatalf("insert conv: %v", err)
	}
	return st
}

func TestURIPathEscapingSpecialChars(t *testing.T) {
	// Verifies minor review item (store.go 44-45): paths containing %, #, space work cleanly.
	st := setupSingleAgentTestStore(t, "dir%25 with #hash")
	if err := checkInvariants(context.Background(), st, 1000); err != nil {
		t.Fatalf("checkInvariants: %v", err)
	}
}

func TestWakeCoalescingDelayedAndCooldownExemption(t *testing.T) {
	ctx := context.Background()
	st := setupSingleAgentTestStore(t, "wake-cd")
	now := int64(1_700_000_000_000)

	// 1. Delayed wakeup (`not_before = now + 5000`) leaves `ready = 0` until scheduler promotes it.
	_, coalesced, err := st.wake(ctx, wakeInput{
		AgentID: "a1", ConversationID: "c1", Kind: "routine", Priority: 30,
		Payload: `{}`, NotBeforeMS: now + 5000, CreatedAtMS: now,
	})
	if err != nil || coalesced {
		t.Fatalf("delayed wake: coalesced=%v err=%v", coalesced, err)
	}
	claimed, _, _, err := st.claimWork(ctx, 2, 4, now, false)
	if err != nil || len(claimed) != 0 {
		t.Fatalf("expected 0 claims before not_before, got %d (err=%v)", len(claimed), err)
	}

	// 2. Promote at `now + 5000`, claim & finish with 30s cooldown (`cooldown_until = now + 35000`).
	promoted, err := st.promoteDueWakeups(ctx, now+5000)
	if err != nil || promoted != 1 {
		t.Fatalf("promoteDueWakeups: promoted=%d err=%v", promoted, err)
	}
	claimed, _, _, err = st.claimWork(ctx, 2, 4, now+5000, false)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("expected 1 claim after promote, got %d (err=%v)", len(claimed), err)
	}
	if err := checkInvariants(ctx, st, now+5000); err != nil {
		t.Fatalf("invariants mid-run: %v", err)
	}
	if _, _, err := st.finishRun(ctx, claimed[0], 30_000, now+5010); err != nil {
		t.Fatalf("finishRun: %v", err)
	}

	// 3. During cooldown (`now + 6000`), `task` priority (50) is blocked by cooldown,
	//    and coalescible `message` coalesces properly.
	_, _, err = st.wake(ctx, wakeInput{
		AgentID: "a1", ConversationID: "c1", Kind: "message", Priority: 50,
		Payload: `{"m":1}`, NotBeforeMS: 0, CreatedAtMS: now + 6000,
	})
	if err != nil {
		t.Fatalf("wake message 1: %v", err)
	}
	_, coalesced2, err := st.wake(ctx, wakeInput{
		AgentID: "a1", ConversationID: "c1", Kind: "message", Priority: 50,
		Payload: `{"m":2}`, NotBeforeMS: 0, CreatedAtMS: now + 6001,
	})
	if err != nil || !coalesced2 {
		t.Fatalf("expected second message to coalesce, got coalesced=%v err=%v", coalesced2, err)
	}
	claimedDuringCD, _, _, err := st.claimWork(ctx, 2, 4, now+6002, false)
	if err != nil || len(claimedDuringCD) != 0 {
		t.Fatalf("expected 0 claims for PrioTask during cooldown, got %d (err=%v)", len(claimedDuringCD), err)
	}

	// 4. Spec 8.2.4: `interactive` (`Priority = 100`) bypasses `cooldown_until` immediately!
	_, _, err = st.wake(ctx, wakeInput{
		AgentID: "a1", ConversationID: "c1", Kind: "owner_reply", Priority: 100,
		Payload: `{"owner":true}`, NotBeforeMS: 0, CreatedAtMS: now + 6005,
	})
	if err != nil {
		t.Fatalf("wake interactive: %v", err)
	}
	claimedInteractive, _, _, err := st.claimWork(ctx, 2, 4, now+6006, false)
	if err != nil || len(claimedInteractive) != 1 || len(claimedInteractive[0].WakeupIDs) != 2 {
		t.Fatalf("expected interactive claim draining both wakeups of c1, got %+v (err=%v)", claimedInteractive, err)
	}
}

func TestNullableConversationIDAndPoisonWakeupCap(t *testing.T) {
	ctx := context.Background()
	st := setupSingleAgentTestStore(t, "null-conv-poison")
	now := int64(1_700_000_000_000)

	// Enqueue wakeup with NULL conversation_id (Thread #12).
	wid, _, err := st.wake(ctx, wakeInput{
		AgentID: "a1", ConversationID: "", Kind: "heartbeat", Priority: 10,
		Payload: `{}`, NotBeforeMS: 0, CreatedAtMS: now,
	})
	if err != nil {
		t.Fatalf("wake null conv: %v", err)
	}

	// 1st claim -> graceful SIGTERM recovery (`graceful=true`) must NOT burn an attempt!
	c1, _, _, err := st.claimWork(ctx, 1, 4, now+1, false)
	if err != nil || len(c1) != 1 {
		t.Fatalf("claim 1: %+v err=%v", c1, err)
	}
	if _, _, dr, err := st.recoverOnBoot(ctx, now+2, true); err != nil || dr != 0 {
		t.Fatalf("graceful recover: dr=%d err=%v", dr, err)
	}
	var attemptsAfterGraceful int
	_ = st.readDB.QueryRowContext(ctx, `SELECT attempts FROM wakeups WHERE agent_id = 'a1' AND id = ?`, wid).Scan(&attemptsAfterGraceful)
	if attemptsAfterGraceful != 0 {
		t.Fatalf("expected attempts=0 after graceful shutdown, got %d", attemptsAfterGraceful)
	}

	// 3 consecutive crashes (`graceful=false`): attempts reach 1 -> 2 -> 3 (`dropped` on 3rd crash!).
	for crash := 1; crash <= 3; crash++ {
		cs, _, _, err := st.claimWork(ctx, 1, 4, now+int64(crash*10), false)
		if err != nil || len(cs) != 1 {
			t.Fatalf("crash iter %d claim: %+v err=%v", crash, cs, err)
		}
		_, _, dr, err := st.recoverOnBoot(ctx, now+int64(crash*10+5), false)
		if err != nil {
			t.Fatalf("crash iter %d recover: %v", crash, err)
		}
		if crash < 3 && dr != 0 {
			t.Fatalf("crash iter %d dropped prematurely", crash)
		}
		if crash == 3 && dr != 1 {
			t.Fatalf("crash iter 3 expected 1 poison wakeup dropped, got %d", dr)
		}
		if err := checkInvariants(ctx, st, now+int64(crash*10+6)); err != nil {
			t.Fatalf("invariants after crash %d: %v", crash, err)
		}
	}

	var status string
	var finalAttempts int
	_ = st.readDB.QueryRowContext(ctx, `SELECT status, attempts FROM wakeups WHERE agent_id = 'a1' AND id = ?`, wid).Scan(&status, &finalAttempts)
	if status != "dropped" || finalAttempts != 3 {
		t.Fatalf("expected dropped with attempts=3, got status=%q attempts=%d", status, finalAttempts)
	}
}
