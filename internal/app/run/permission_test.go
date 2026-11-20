package run_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/app/approvals"
	"max/internal/app/run"
	"max/internal/domain"
	"max/internal/testutil/fakeengine"
	"max/internal/testutil/testdb"
)

func setupPermTest(t *testing.T, shell bool) (app.Store, *actor.Service, *approvals.Service, *run.Service, actor.Claimed) {
	t.Helper()
	ctx := context.Background()
	st := testdb.New(t)
	actors := actor.NewService(st, app.SystemClock{}, app.UUIDv7Gen{})
	appr := approvals.NewService(st, actors, nil, app.SystemClock{}, app.UUIDv7Gen{})

	err := st.WithTx(ctx, func(tx app.Tx) error {
		if err := tx.InsertTeam(ctx, app.TeamRecord{
			ID: "team-1", Slug: "core", Name: "Core", MaxRunning: 4, Status: "active",
		}); err != nil {
			return err
		}
		if err := tx.InsertAgent(ctx, app.AgentRecord{
			ID: "ag-1", Slug: "writer", Name: "Writer", TeamID: "team-1",
			EnginesJSON: `["agy"]`, EngineMask: 4, Autonomy: domain.AutonomyAuto,
			Browser: domain.BrowserNone, Shell: shell, ConfigRev: "r1", Status: domain.AgentActive,
		}); err != nil {
			return err
		}
		_, _, err := actors.Wake(ctx, tx, actor.WakeInput{
			AgentID:  "ag-1",
			Kind:     domain.WakeMessage,
			Priority: domain.PrioInteractive,
			Payload:  json.RawMessage(`{"text":"hi"}`),
		})
		return err
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	claimed, err := actors.ClaimBatch(ctx, actor.Capacity{
		Free:  map[domain.EngineName]int{domain.EngineAgy: 1},
		Admit: func(domain.EngineName, domain.Priority) domain.Admission { return domain.AdmitNow },
	}, app.SystemClock{}.Now())
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %v, err = %v", claimed, err)
	}

	var runSvc *run.Service
	fe := fakeengine.New(domain.EngineAgy)
	runSvc = run.NewService(run.Deps{
		Store:    st,
		Actors:   actors,
		Engines:  map[domain.EngineName]app.Engine{domain.EngineAgy: fe},
		VaultDir: "/vault",
	})
	return st, actors, appr, runSvc, claimed[0]
}

func TestPermissionFuncAllowRecordsTouch(t *testing.T) {
	ctx := context.Background()
	st, _, appr, runSvc, claimed := setupPermTest(t, false)
	perm := run.NewPermissionFunc(run.PermissionDeps{
		Store:     st,
		Runs:      runSvc,
		Approvals: appr,
		VaultDir:  "/vault",
	})

	tok := runSvc.RegisterTestToken(run.TokenIdentity{
		RunID:          claimed.RunID,
		AgentID:        claimed.AgentID,
		ConversationID: claimed.ConversationID,
		AgentSlug:      "writer",
	})

	dec := perm(ctx, app.ToolAsk{
		RunToken: tok,
		ToolName: "write_file",
		Path:     "/vault/agents/writer/notes/x.md",
		Input:    json.RawMessage(`{"path":"agents/writer/notes/x.md"}`),
	})
	if dec.Verdict != domain.Allow {
		t.Fatalf("verdict = %v (%s), want Allow", dec.Verdict, dec.Reason)
	}
	touched := runSvc.TouchedPaths(claimed.RunID)
	if !slices.Contains(touched, "agents/writer/notes/x.md") {
		t.Fatalf("touched = %v, want agents/writer/notes/x.md", touched)
	}
}

func TestPermissionFuncAskParks(t *testing.T) {
	ctx := context.Background()
	st, _, appr, runSvc, claimed := setupPermTest(t, true)
	perm := run.NewPermissionFunc(run.PermissionDeps{
		Store:     st,
		Runs:      runSvc,
		Approvals: appr,
		VaultDir:  "/vault",
	})

	tok := runSvc.RegisterTestToken(run.TokenIdentity{
		RunID:          claimed.RunID,
		AgentID:        claimed.AgentID,
		ConversationID: claimed.ConversationID,
		AgentSlug:      "writer",
	})

	dec := perm(ctx, app.ToolAsk{
		RunToken: tok,
		ToolName: "run_command",
		Input:    json.RawMessage(`{"cmd":"ls -la"}`),
	})
	if dec.Verdict != domain.Ask {
		t.Fatalf("verdict = %v (%s), want Ask", dec.Verdict, dec.Reason)
	}

	rows, err := st.Read().ListApprovalsPage(ctx, "pending", 10, nil, "")
	if err != nil || len(rows) != 1 {
		t.Fatalf("pending approvals = %d (err=%v), want 1", len(rows), err)
	}
}

func TestPermissionFuncConsumesApproved(t *testing.T) {
	ctx := context.Background()
	st, _, appr, runSvc, claimed := setupPermTest(t, true)
	perm := run.NewPermissionFunc(run.PermissionDeps{
		Store:     st,
		Runs:      runSvc,
		Approvals: appr,
		VaultDir:  "/vault",
	})

	tok := runSvc.RegisterTestToken(run.TokenIdentity{
		RunID:          claimed.RunID,
		AgentID:        claimed.AgentID,
		ConversationID: claimed.ConversationID,
		AgentSlug:      "writer",
	})

	input := json.RawMessage(`{"cmd":"echo ok"}`)
	// First call creates pending approval (Ask)
	dec1 := perm(ctx, app.ToolAsk{RunToken: tok, ToolName: "run_command", Input: input})
	if dec1.Verdict != domain.Ask {
		t.Fatalf("initial verdict = %v, want Ask", dec1.Verdict)
	}

	rows, _ := st.Read().ListApprovalsPage(ctx, "pending", 10, nil, "")
	if err := appr.Decide(ctx, rows[0].ID, true, "idem-allow-1"); err != nil {
		t.Fatalf("Decide allow: %v", err)
	}

	// Next call with matching input consumes the approved record once -> Allow
	dec2 := perm(ctx, app.ToolAsk{RunToken: tok, ToolName: "run_command", Input: input})
	if dec2.Verdict != domain.Allow {
		t.Fatalf("post-approve verdict = %v (%s), want Allow", dec2.Verdict, dec2.Reason)
	}

	// Repeating the exact same call again -> no longer allowed, returns Ask again
	dec3 := perm(ctx, app.ToolAsk{RunToken: tok, ToolName: "run_command", Input: input})
	if dec3.Verdict != domain.Ask {
		t.Fatalf("second post-approve verdict = %v, want Ask (single-use consumption)", dec3.Verdict)
	}
}

func TestPermissionFuncDeadRun(t *testing.T) {
	ctx := context.Background()
	st, _, appr, runSvc, _ := setupPermTest(t, false)
	perm := run.NewPermissionFunc(run.PermissionDeps{
		Store:     st,
		Runs:      runSvc,
		Approvals: appr,
		VaultDir:  "/vault",
	})

	dec := perm(ctx, app.ToolAsk{
		RunToken: "mrt_expired_or_unknown_token",
		ToolName: "read_file",
		Path:     "company/COMPANY.md",
	})
	if dec.Verdict != domain.Deny {
		t.Fatalf("dead run token verdict = %v, want Deny", dec.Verdict)
	}
}
