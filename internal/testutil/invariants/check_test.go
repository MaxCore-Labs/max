package invariants_test

import (
	"context"
	"slices"
	"testing"

	"max/internal/adapter/sqlite"
	"max/internal/app"
	"max/internal/domain"
	"max/internal/testutil/invariants"
	"max/internal/testutil/testdb"
)

func hasCode(vs []invariants.Violation, code string) bool {
	return slices.ContainsFunc(vs, func(v invariants.Violation) bool {
		return v.Code == code
	})
}

func TestInvariantsDetect(t *testing.T) {
	ctx := context.Background()

	t.Run("I1 duplicate running runs for one agent", func(t *testing.T) {
		st := testdb.New(t)
		rawSt := st.(*sqlite.Store)
		if err := st.WithTx(ctx, func(tx app.Tx) error {
			_ = tx.InsertTeam(ctx, app.TeamRecord{ID: "t1", Slug: "core", Name: "Core", MaxRunning: 4, Status: "active"})
			_ = tx.InsertAgent(ctx, app.AgentRecord{
				ID: "a1", Slug: "ag1", Name: "Ag1", TeamID: "t1",
				EnginesJSON: `["agy"]`, EngineMask: 4, Autonomy: domain.AutonomyAuto,
				Browser: domain.BrowserNone, ConfigRev: "r1", Status: domain.AgentActive,
			})
			_ = tx.InsertConversation(ctx, app.ConversationRecord{ID: "c1", AgentID: "a1", Sessions: `{}`})
			return nil
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if err := rawSt.ExecRaw(ctx, `
			INSERT INTO runs (id, agent_id, conversation_id, status, priority, engine, model, started_at)
			VALUES ('r1', 'a1', 'c1', 'running', 50, 'agy', 'default', 1000),
			       ('r2', 'a1', 'c1', 'running', 50, 'agy', 'default', 1001)
		`); err != nil {
			t.Fatalf("plant I1: %v", err)
		}

		vs := invariants.Check(ctx, st)
		if !hasCode(vs, "I1") {
			t.Fatalf("violations = %+v, expected code I1", vs)
		}
	})

	t.Run("I3 wakeup in invalid terminal state", func(t *testing.T) {
		st := testdb.New(t)
		rawSt := st.(*sqlite.Store)
		if err := st.WithTx(ctx, func(tx app.Tx) error {
			_ = tx.InsertTeam(ctx, app.TeamRecord{ID: "t1", Slug: "core", Name: "Core", MaxRunning: 4, Status: "active"})
			_ = tx.InsertAgent(ctx, app.AgentRecord{
				ID: "a1", Slug: "ag1", Name: "Ag1", TeamID: "t1",
				EnginesJSON: `["agy"]`, EngineMask: 4, Autonomy: domain.AutonomyAuto,
				Browser: domain.BrowserNone, ConfigRev: "r1", Status: domain.AgentActive,
			})
			return nil
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if err := rawSt.ExecRaw(ctx, `
			INSERT INTO wakeups (id, agent_id, kind, priority, payload, status, run_id, created_at)
			VALUES ('w1', 'a1', 'message', 100, '{}', 'done', NULL, 1000)
		`); err != nil {
			t.Fatalf("plant I3: %v", err)
		}

		vs := invariants.Check(ctx, st)
		if !hasCode(vs, "I3") {
			t.Fatalf("violations = %+v, expected code I3", vs)
		}
	})

	t.Run("I5 stale or duplicate task checkout_run_id", func(t *testing.T) {
		st := testdb.New(t)
		rawSt := st.(*sqlite.Store)
		if err := st.WithTx(ctx, func(tx app.Tx) error {
			_ = tx.InsertTeam(ctx, app.TeamRecord{ID: "t1", Slug: "core", Name: "Core", MaxRunning: 4, Status: "active"})
			_ = tx.InsertAgent(ctx, app.AgentRecord{
				ID: "a1", Slug: "ag1", Name: "Ag1", TeamID: "t1",
				EnginesJSON: `["agy"]`, EngineMask: 4, Autonomy: domain.AutonomyAuto,
				Browser: domain.BrowserNone, ConfigRev: "r1", Status: domain.AgentActive,
			})
			_ = tx.InsertConversation(ctx, app.ConversationRecord{ID: "c1", AgentID: "a1", Sessions: `{}`})
			return nil
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if err := rawSt.ExecRaw(ctx, `
			INSERT INTO runs (id, agent_id, conversation_id, status, priority, engine, model, started_at, ended_at)
			VALUES ('r-done', 'a1', 'c1', 'succeeded', 50, 'agy', 'default', 1000, 2000);
			INSERT INTO tasks (id, root_id, root_origin, depth, title, description, status, assignee_agent_id, checkout_run_id, created_at, updated_at)
			VALUES ('tk1', 'tk1', 'owner', 0, 'Task 1', 'Desc', 'in_progress', 'a1', 'r-done', 1000, 1000);
		`); err != nil {
			t.Fatalf("plant I5: %v", err)
		}

		vs := invariants.Check(ctx, st)
		if !hasCode(vs, "I5") {
			t.Fatalf("violations = %+v, expected code I5", vs)
		}
	})
}
