package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"max/internal/adapter/sqlite"
	"max/internal/app"
	"max/internal/domain"
	"max/internal/testutil/testdb"
)

func TestMigrateCreatesSchema(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "max.sqlite3")
	st, err := sqlite.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	jm, fk, err := st.Pragmas(ctx)
	if err != nil {
		t.Fatalf("Pragmas: %v", err)
	}
	if jm != "wal" {
		t.Fatalf("journal_mode = %q, want wal", jm)
	}
	if fk != 1 {
		t.Fatalf("foreign_keys = %d, want 1", fk)
	}

	wantTables := []string{
		"teams", "agents", "conversations", "wakeups", "goals", "tasks", "task_blockers",
		"runs", "tool_calls", "approvals", "grants", "quota_ledger", "quota_state",
		"usage_daily", "leases", "outbox", "threads", "messages", "task_events",
		"run_events", "inbox_items", "proposals", "routine_state", "connections",
		"users", "auth_sessions", "kv", "audit_log", "engine_binaries",
	}
	got, err := st.ListTables(ctx)
	if err != nil {
		t.Fatalf("ListTables: %v", err)
	}
	set := make(map[string]bool, len(got))
	for _, name := range got {
		set[name] = true
	}
	for _, want := range wantTables {
		if !set[want] {
			t.Fatalf("missing table %q from migrated schema (have %v)", want, got)
		}
	}
}

func TestWithTxRollsBackOnError(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)

	boom := errors.New("boom")
	err := st.WithTx(ctx, func(tx app.Tx) error {
		if err := tx.UpsertKV(ctx, "k1", "v1", 1000); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("expected boom error, got %v", err)
	}
	_, ok, err := st.Read().GetKV(ctx, "k1")
	if err != nil {
		t.Fatalf("GetKV: %v", err)
	}
	if ok {
		t.Fatal("expected rolled-back kv entry to be absent")
	}
}

func TestSingleWriter(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)

	var wg sync.WaitGroup
	errCh := make(chan error, 8)
	for g := range 4 {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for iter := range 25 {
				err := st.WithTx(ctx, func(tx app.Tx) error {
					return tx.UpsertKV(ctx, "key-"+string(rune('a'+id)), "val", int64(iter))
				})
				if err != nil {
					errCh <- err
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent WithTx failed: %v", err)
	}
}

func TestUniqueIdempotency(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)

	err := st.WithTx(ctx, func(tx app.Tx) error {
		if err := tx.InsertTeam(ctx, app.TeamRecord{
			ID: "team-1", Slug: "core", Name: "Core", MaxRunning: 2, Status: "active",
		}); err != nil {
			return err
		}
		return tx.InsertAgent(ctx, app.AgentRecord{
			ID: "agent-1", Slug: "chief", Name: "Chief", TeamID: "team-1",
			EnginesJSON: `["agy"]`, EngineMask: 4, ModelsJSON: `{}`,
			Autonomy: domain.AutonomyAuto, Browser: domain.BrowserNone,
			SkillsJSON: `[]`, ParamsJSON: `{}`, ConfigRev: "rev1", Status: domain.AgentActive,
		})
	})
	if err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	idem := "routine:daily:100"
	err = st.WithTx(ctx, func(tx app.Tx) error {
		return tx.InsertWakeupRaw(ctx, app.WakeupRecord{
			ID: "w1", AgentID: "agent-1", Kind: domain.WakeRoutine, Priority: domain.PrioRoutine,
			Payload: `{}`, IdempotencyKey: &idem, Status: "queued", CreatedAt: 1000,
		})
	})
	if err != nil {
		t.Fatalf("first InsertWakeupRaw: %v", err)
	}

	err = st.WithTx(ctx, func(tx app.Tx) error {
		return tx.InsertWakeupRaw(ctx, app.WakeupRecord{
			ID: "w2", AgentID: "agent-1", Kind: domain.WakeRoutine, Priority: domain.PrioRoutine,
			Payload: `{}`, IdempotencyKey: &idem, Status: "queued", CreatedAt: 1001,
		})
	})
	if err == nil {
		t.Fatal("expected duplicate queued idempotency_key to fail unique constraint")
	}
}

func TestOpenSpecialCharactersInPath(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "sub#dir%1", "max.sqlite3")
	st, err := sqlite.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("Open with special chars: %v", err)
	}
	defer func() { _ = st.Close() }()

	if err := st.WithTx(ctx, func(tx app.Tx) error {
		return tx.UpsertKV(ctx, "hello", "world", 42)
	}); err != nil {
		t.Fatalf("UpsertKV: %v", err)
	}
	got, ok, err := st.Read().GetKV(ctx, "hello")
	if err != nil || !ok || got != "world" {
		t.Fatalf("GetKV = (%q, %v, %v), want (\"world\", true, nil)", got, ok, err)
	}
}
