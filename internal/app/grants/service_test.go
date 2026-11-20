package grants_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"max/internal/adapter/mcpserver"
	"max/internal/app"
	"max/internal/app/grants"
	"max/internal/app/tools"
	"max/internal/domain"
	"max/internal/testutil/testdb"
)

func seedGrantAgent(t *testing.T, st app.Store) {
	t.Helper()
	ctx := context.Background()
	err := st.WithTx(ctx, func(tx app.Tx) error {
		if err := tx.InsertTeam(ctx, app.TeamRecord{ID: "t1", Slug: "core", Name: "Core", MaxRunning: 2, Status: "active"}); err != nil {
			return err
		}
		return tx.InsertAgent(ctx, app.AgentRecord{
			ID: "a1", Slug: "sales", Name: "Sales", TeamID: "t1",
			EnginesJSON: `["agy"]`, EngineMask: 4, Autonomy: domain.AutonomyAuto,
			Browser: domain.BrowserNone, ConfigRev: "r1", Status: domain.AgentActive,
		})
	})
	if err != nil {
		t.Fatalf("seedGrantAgent: %v", err)
	}
}

func TestMaxPerDay(t *testing.T) {
	st := testdb.New(t)
	seedGrantAgent(t, st)
	ctx := context.Background()
	day1 := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	svc := grants.NewService(st, app.SystemClock{}, app.UUIDv7Gen{})

	max2 := 2
	gid, err := svc.Create(ctx, grants.NewGrant{
		AgentID:     "a1",
		Connection:  "gmail",
		Tool:        "send_email",
		Constraints: domain.Constraints{OneOf: map[string][]string{"to": {"a@x.com"}}},
		MaxPerDay:   &max2,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	for attempt := 1; attempt <= 3; attempt++ {
		var allowed bool
		err := st.WithTx(ctx, func(tx app.Tx) error {
			var cerr error
			allowed, cerr = svc.CountUse(ctx, tx, gid, day1)
			return cerr
		})
		if err != nil {
			t.Fatalf("CountUse %d: %v", attempt, err)
		}
		want := attempt <= 2
		if allowed != want {
			t.Fatalf("attempt %d allowed = %v, want %v", attempt, allowed, want)
		}
	}

	// On the 3rd attempt in the same day, Active excludes the exhausted grant so policy returns Ask.
	activeSameDay, _ := svc.Active(ctx, "a1", day1)
	dec := domain.Decide(
		domain.AgentPolicy{Scope: domain.AgentScope{Slug: "sales"}, Autonomy: domain.AutonomyAuto},
		domain.ToolCall{Name: "send_email", Class: domain.ToolClassE, Connection: "gmail", Input: json.RawMessage(`{"to":"a@x.com"}`)},
		domain.OriginOwner,
		activeSameDay,
		day1,
	)
	if dec.Verdict != domain.Ask {
		t.Fatalf("3rd use on same day verdict = %v, want Ask", dec.Verdict)
	}

	// Next day resets daily counter -> allowed again!
	day2 := day1.Add(24 * time.Hour)
	var allowedNextDay bool
	_ = st.WithTx(ctx, func(tx app.Tx) error {
		var cerr error
		allowedNextDay, cerr = svc.CountUse(ctx, tx, gid, day2)
		return cerr
	})
	if !allowedNextDay {
		t.Fatal("expected CountUse allowed=true on next calendar day")
	}
}

func TestRevokedGrant(t *testing.T) {
	st := testdb.New(t)
	seedGrantAgent(t, st)
	ctx := context.Background()
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	svc := grants.NewService(st, app.SystemClock{}, app.UUIDv7Gen{})

	gid, err := svc.Create(ctx, grants.NewGrant{
		AgentID:     "a1",
		Connection:  "gmail",
		Tool:        "send_email",
		Constraints: domain.Constraints{Equals: map[string]string{"to": "a@x.com"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.Revoke(ctx, gid); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	active, err := svc.Active(ctx, "a1", now)
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("Active after Revoke = %+v, want empty", active)
	}
}

func TestOnlyOwnerCreates(t *testing.T) {
	st := testdb.New(t)
	toolSvc := tools.NewService(st, t.TempDir(), app.SystemClock{}, app.UUIDv7Gen{})
	names := mcpserver.RegisteredToolNames(toolSvc)
	for _, n := range names {
		if strings.Contains(strings.ToLower(n), "grant") {
			t.Fatalf("forbidden MCP tool exposing grant creation/modification to agents: %q", n)
		}
	}
}
