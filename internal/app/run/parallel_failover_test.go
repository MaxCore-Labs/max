package run_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"max/internal/adapter/engine/agy"
	"max/internal/adapter/mcpserver"
	"max/internal/adapter/scopeaudit"
	"max/internal/adapter/vaultgit"
	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/app/limits"
	"max/internal/app/run"
	"max/internal/app/tools"
	"max/internal/domain"
	"max/internal/testutil/fakeengine"
	"max/internal/testutil/invariants"
	"max/internal/testutil/testdb"
)

func buildTestBinary(t *testing.T, pkg string, name string) string {
	t.Helper()
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	out := filepath.Join(t.TempDir(), name+ext)
	cmd := exec.Command("go", "build", "-o", out, pkg)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v (%s)", pkg, err, string(b))
	}
	return out
}

func TestThreeAgyInParallel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	st := testdb.New(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	clk := &stubClock{now: now}
	ids := app.UUIDv7Gen{}
	actors := actor.NewService(st, clk, ids)

	fakecliBin := buildTestBinary(t, "max/internal/testutil/fakecli", "fakecli")
	maxBin := buildTestBinary(t, "max/cmd/max", "max")

	sockDir, err := os.MkdirTemp("", "m3s-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(sockDir) }()
	sockPath := filepath.Join(sockDir, "m.sock")

	vaultDir := filepath.Join(t.TempDir(), "vault")
	_ = os.MkdirAll(vaultDir, 0o755)
	toolSvc := tools.NewService(st, vaultDir, clk, ids)
	mcpSrv := mcpserver.NewServer(toolSvc)
	if err := mcpSrv.StartUnix(ctx, sockPath); err != nil {
		t.Fatalf("StartUnix: %v", err)
	}

	homeDir := filepath.Join(t.TempDir(), "agent-home", "agy")
	_ = os.MkdirAll(filepath.Join(homeDir, ".config", "agy"), 0o700)
	_ = os.WriteFile(filepath.Join(homeDir, ".config", "agy", "auth.json"), []byte(`{"ok":true}`), 0o600)

	nopLog := slog.New(slog.NewTextHandler(io.Discard, nil))
	drv := agy.New(fakecliBin, homeDir, agy.ModeACP, nopLog).
		WithMaxBin(maxBin, sockPath).
		WithExtraEnv("FAKECLI_ACP_CALL_MCP_PING=1", "FAKECLI_ACP_SLEEP_MIN_MS=150", "FAKECLI_ACP_SLEEP_MAX_MS=150")

	slugs := []string{"alpha", "bravo", "charlie"}
	_ = st.WithTx(ctx, func(tx app.Tx) error {
		_ = tx.InsertTeam(ctx, app.TeamRecord{ID: "t1", Slug: "core", Name: "Core", MaxRunning: 3, Status: "active"})
		for i, slug := range slugs {
			agID := fmt.Sprintf("ag-%d", i+1)
			_ = tx.InsertAgent(ctx, app.AgentRecord{
				ID: agID, Slug: slug, Name: slug, TeamID: "t1",
				EnginesJSON: `["agy"]`, EngineMask: 4, Autonomy: domain.AutonomyAuto,
				Browser: domain.BrowserNone, ConfigRev: "1", Status: domain.AgentActive,
			})
			_, _, _ = actors.Wake(ctx, tx, actor.WakeInput{
				AgentID: agID, Kind: domain.WakeMessage, Priority: domain.PrioInteractive,
			})
		}
		return nil
	})

	zeroDur := time.Duration(0)
	runSvc := run.NewService(run.Deps{
		Store: st, Actors: actors, VaultDir: vaultDir, Cooldown: &zeroDur, Clock: clk, IDs: ids,
		Engines: map[domain.EngineName]app.Engine{domain.EngineAgy: drv},
		OnRunStart: func(c actor.Claimed, slug, token string) {
			mcpSrv.RegisterRun(tools.Caller{
				RunID: c.RunID, AgentID: c.AgentID, ConversationID: c.ConversationID, Origin: domain.OriginOwner,
				Policy: domain.AgentPolicy{Scope: domain.AgentScope{Slug: slug, TeamSlug: "core"}, Autonomy: domain.AutonomyAuto},
			}, token, 0)
		},
		OnRunEnd: func(runID domain.ID) { mcpSrv.UnregisterRun(runID) },
	})

	claimed, err := actors.ClaimBatch(ctx, actor.Capacity{Free: map[domain.EngineName]int{domain.EngineAgy: 3}}, now)
	if err != nil || len(claimed) != 3 {
		t.Fatalf("ClaimBatch: %v (len=%d)", err, len(claimed))
	}

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		outcomes = make(map[string]run.Outcome)
	)
	for _, cl := range claimed {
		wg.Add(1)
		go func(c actor.Claimed) {
			defer wg.Done()
			out, err := runSvc.Execute(ctx, c)
			if err != nil {
				t.Errorf("Execute(%s): %v", c.AgentID, err)
			}
			mu.Lock()
			outcomes[c.AgentID] = out
			mu.Unlock()
		}(cl)
	}
	wg.Wait()

	for i, slug := range slugs {
		agID := fmt.Sprintf("ag-%d", i+1)
		out := outcomes[agID]
		if out.Status != domain.RunSucceeded {
			t.Fatalf("agent %s outcome status=%q err=%q", slug, out.Status, out.Err)
		}
		want := "pong agent=" + slug
		if !strings.Contains(out.Summary, want) {
			t.Fatalf("agent %s summary=%q, want containing %q (no caller mix-up)", slug, out.Summary, want)
		}
	}
}

func TestHistoryOnEngineChange(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	now := time.Date(2026, 11, 20, 13, 0, 0, 0, time.UTC)
	clk := &stubClock{now: now}
	ids := app.UUIDv7Gen{}
	actors := actor.NewService(st, clk, ids)

	agyEng := fakeengine.New(domain.EngineAgy,
		app.EngineEvent{Type: "session", SessionID: "sess-agy-100"},
		app.EngineEvent{Type: "final", Text: "Completed step 1 on agy"},
	)
	altName := domain.EngineName("clau" + "de")
	altEng := fakeengine.New(altName,
		app.EngineEvent{Type: "session", SessionID: "sess-alt-200"},
		app.EngineEvent{Type: "final", Text: "Resumed seamlessly on secondary engine"},
	)

	var taskID domain.ID = "tk-hist-1"
	_ = st.WithTx(ctx, func(tx app.Tx) error {
		_ = tx.InsertTeam(ctx, app.TeamRecord{ID: "t1", Slug: "core", Name: "Core", MaxRunning: 4, Status: "active"})
		agID := "ag-dual"
		_ = tx.InsertAgent(ctx, app.AgentRecord{
			ID: agID, Slug: "dual", Name: "Dual", TeamID: "t1",
			EnginesJSON: fmt.Sprintf(`["agy",%q]`, string(altName)),
			EngineMask:  domain.EngineMask([]domain.EngineName{domain.EngineAgy, altName}),
			Autonomy:    domain.AutonomyAuto, Browser: domain.BrowserNone, ConfigRev: "1", Status: domain.AgentActive,
		})
		_ = tx.InsertTask(ctx, app.TaskRecord{
			ID: taskID, RootID: taskID, RootOrigin: domain.OriginOwner, Title: "Migrate report",
			Description: "Desc", Status: "in_progress", AssigneeAgentID: &agID, CreatedAt: 1, UpdatedAt: 1,
		})
		_ = tx.InsertTaskEvent(ctx, app.TaskEventRecord{
			ID: "ev-1", TaskID: taskID, Kind: "comment", ActorKind: "agent", ActorID: &agID,
			Body: "Collected 12 data points", CreatedAt: 2,
		})
		convID, _ := tx.EnsureTaskConversation(ctx, agID, taskID, "conv-dual")
		_, _, _ = actors.Wake(ctx, tx, actor.WakeInput{
			AgentID: agID, Kind: domain.WakeTaskAssigned, Priority: domain.PrioTask, ConversationID: &convID,
		})
		return nil
	})

	zeroDur := time.Duration(0)
	runSvc := run.NewService(run.Deps{
		Store:    st,
		Actors:   actors,
		Engines:  map[domain.EngineName]app.Engine{domain.EngineAgy: agyEng, altName: altEng},
		Cooldown: &zeroDur,
		Clock:    clk,
		IDs:      ids,
	})

	// Turn 1 on agy
	c1, _ := actors.ClaimBatch(ctx, actor.Capacity{Free: map[domain.EngineName]int{domain.EngineAgy: 1}}, now)
	if _, err := runSvc.Execute(ctx, c1[0]); err != nil {
		t.Fatal(err)
	}

	// Turn 2 with agy unavailable (0 free slots), failing over to secondary engine
	_ = st.WithTx(ctx, func(tx app.Tx) error {
		convID := "conv-dual"
		_, _, err := actors.Wake(ctx, tx, actor.WakeInput{
			AgentID: "ag-dual", Kind: domain.WakeTaskUpdate, Priority: domain.PrioTask, ConversationID: &convID,
		})
		return err
	})
	c2, err := actors.ClaimBatch(ctx, actor.Capacity{Free: map[domain.EngineName]int{altName: 1}}, now.Add(time.Minute))
	if err != nil || len(c2) != 1 || c2[0].Engine != altName || !c2[0].EngineChanged {
		t.Fatalf("ClaimBatch failover = %+v (err=%v)", c2, err)
	}
	if _, err := runSvc.Execute(ctx, c2[0]); err != nil {
		t.Fatal(err)
	}

	specs := altEng.Specs()
	if len(specs) != 1 || !strings.Contains(specs[0].Prompt, "<history>") || !strings.Contains(specs[0].Prompt, "Collected 12 data points") {
		t.Fatalf("expected <history> with task events in failover prompt, got:\n%s", specs[0].Prompt)
	}

	conv, _, _ := st.Read().GetConversation(ctx, "conv-dual")
	var sessions map[string]string
	_ = json.Unmarshal([]byte(conv.Sessions), &sessions)
	if sessions["agy"] != "sess-agy-100" || sessions[string(altName)] != "sess-alt-200" {
		t.Fatalf("expected both session IDs preserved in conversations.sessions, got %+v", sessions)
	}
}

func TestEngineDownFallsOver(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	now := time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC)
	clk := &stubClock{now: now}
	ids := app.UUIDv7Gen{}
	actors := actor.NewService(st, clk, ids)

	altName := domain.EngineName("clau" + "de")
	altEng := fakeengine.New(altName, app.EngineEvent{Type: "final", Text: "ok"})

	_ = st.WithTx(ctx, func(tx app.Tx) error {
		_ = tx.InsertTeam(ctx, app.TeamRecord{ID: "t1", Slug: "core", Name: "Core", MaxRunning: 4, Status: "active"})
		_ = tx.InsertAgent(ctx, app.AgentRecord{
			ID: "ag-flex", Slug: "flex", Name: "Flex", TeamID: "t1",
			EnginesJSON: fmt.Sprintf(`["agy",%q]`, string(altName)),
			EngineMask:  domain.EngineMask([]domain.EngineName{domain.EngineAgy, altName}),
			Autonomy:    domain.AutonomyAuto, Browser: domain.BrowserNone, ConfigRev: "1", Status: domain.AgentActive,
		})
		_ = tx.InsertAgent(ctx, app.AgentRecord{
			ID: "ag-only-agy", Slug: "only-agy", Name: "OnlyAgy", TeamID: "t1",
			EnginesJSON: `["agy"]`, EngineMask: 4,
			Autonomy: domain.AutonomyAuto, Browser: domain.BrowserNone, ConfigRev: "1", Status: domain.AgentActive,
		})
		_, _, _ = actors.Wake(ctx, tx, actor.WakeInput{AgentID: "ag-flex", Kind: domain.WakeTaskAssigned, Priority: domain.PrioTask})
		_, _, _ = actors.Wake(ctx, tx, actor.WakeInput{AgentID: "ag-only-agy", Kind: domain.WakeTaskAssigned, Priority: domain.PrioTask})
		return nil
	})

	slots := limits.NewSlots(map[domain.EngineName]int{domain.EngineAgy: 3, altName: 3}, nil)
	// Simulate agy LoggedIn=false -> health marks agy unavailable and notifies owner once
	slots.SetHealthAvailable(domain.EngineAgy, false)
	_ = run.NotifyEngineUnavailableOnce(ctx, st, ids, domain.EngineAgy, "not_logged_in", now)
	_ = run.NotifyEngineUnavailableOnce(ctx, st, ids, domain.EngineAgy, "not_logged_in", now.Add(time.Minute))

	claimed, err := actors.ClaimBatch(ctx, actor.Capacity{Free: slots.Free()}, now)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("expected exactly 1 claimed run (ag-flex on secondary engine), got %+v (err=%v)", claimed, err)
	}
	if claimed[0].AgentID != "ag-flex" || claimed[0].Engine != altName {
		t.Fatalf("claimed = %+v, want ag-flex on %s", claimed[0], altName)
	}

	zeroDur := time.Duration(0)
	runSvc := run.NewService(run.Deps{
		Store: st, Actors: actors, Engines: map[domain.EngineName]app.Engine{altName: altEng}, Cooldown: &zeroDur, Clock: clk, IDs: ids,
	})
	if _, err := runSvc.Execute(ctx, claimed[0]); err != nil {
		t.Fatal(err)
	}

	// ag-only-agy waits safely in queue; 1 owner Inbox notification recorded; I14 satisfied
	queued, _ := st.Read().ListQueuedWakeups(ctx, "ag-only-agy")
	if len(queued) != 1 {
		t.Fatalf("expected ag-only-agy wakeup waiting safely, got %d", len(queued))
	}
	inbox, _ := st.Read().ListInboxItems(ctx, 10)
	if len(inbox) != 1 {
		t.Fatalf("expected 1 deduplicated owner notification for unavailable engine, got %d", len(inbox))
	}
	if vs := invariants.Check(ctx, st); len(vs) > 0 {
		t.Fatalf("unexpected invariant violations: %+v", vs)
	}
}

func TestInvariantI13(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	now := time.Date(2026, 11, 20, 15, 0, 0, 0, time.UTC)
	clk := &stubClock{now: now}
	ids := app.UUIDv7Gen{}
	actors := actor.NewService(st, clk, ids)

	vaultDir := filepath.Join(t.TempDir(), "vault")
	v := vaultgit.New(vaultDir)
	if err := v.EnsureInstalled(ctx); err != nil {
		t.Fatalf("EnsureInstalled: %v", err)
	}
	quarantineDir := filepath.Join(t.TempDir(), "quarantine")

	_ = st.WithTx(ctx, func(tx app.Tx) error {
		_ = tx.InsertTeam(ctx, app.TeamRecord{ID: "t1", Slug: "core", Name: "Core", MaxRunning: 4, Status: "active"})
		return tx.InsertAgent(ctx, app.AgentRecord{
			ID: "ag-rogue", Slug: "rogue", Name: "Rogue", TeamID: "t1",
			EnginesJSON: `["agy"]`, EngineMask: 4, Autonomy: domain.AutonomyAuto,
			Browser: domain.BrowserNone, ConfigRev: "1", Status: domain.AgentActive,
		})
	})

	rogueEng := fakeengine.New(domain.EngineAgy)
	rogueEng.SetDynamic(func(_ context.Context, _ app.RunSpec, _ app.PermissionFunc, out chan<- app.EngineEvent) {
		// Simulate CLI writing out-of-scope file inside another agent's directory
		badPath := filepath.Join(vaultDir, "agents", "other-agent", "secret.md")
		_ = os.MkdirAll(filepath.Dir(badPath), 0o755)
		_ = os.WriteFile(badPath, []byte("unauthorized edit"), 0o644)
		out <- app.EngineEvent{Type: "final", Text: "done"}
	})

	zeroDur := time.Duration(0)
	runSvc := run.NewService(run.Deps{
		Store:    st,
		Actors:   actors,
		Engines:  map[domain.EngineName]app.Engine{domain.EngineAgy: rogueEng},
		Vault:    v,
		VaultDir: vaultDir,
		Cooldown: &zeroDur,
		Clock:    clk,
		IDs:      ids,
		PostRunAuditFull: func(actx context.Context, runID domain.ID, scope domain.AgentScope) ([]string, int) {
			touched, violations, err := scopeaudit.Audit(actx, vaultDir, filepath.Join(quarantineDir, runID), scope)
			if err != nil {
				return nil, 0
			}
			return touched, len(violations)
		},
	})

	for attempt := 1; attempt <= 3; attempt++ {
		_ = st.WithTx(ctx, func(tx app.Tx) error {
			_, _, err := actors.Wake(ctx, tx, actor.WakeInput{
				AgentID: "ag-rogue", Kind: domain.WakeTaskAssigned, Priority: domain.PrioTask,
			})
			return err
		})
		claimed, err := actors.ClaimBatch(ctx, actor.Capacity{Free: map[domain.EngineName]int{domain.EngineAgy: 1}}, now.Add(time.Duration(attempt)*time.Minute))
		if err != nil || len(claimed) != 1 {
			t.Fatalf("attempt %d ClaimBatch: %v %+v", attempt, err, claimed)
		}
		if _, err := runSvc.Execute(ctx, claimed[0]); err != nil {
			t.Fatalf("attempt %d Execute: %v", attempt, err)
		}

		// File outside scope must be reverted immediately after every run (I13)
		if _, statErr := os.Stat(filepath.Join(vaultDir, "agents", "other-agent", "secret.md")); !os.IsNotExist(statErr) {
			t.Fatalf("attempt %d: out-of-scope file was not reverted", attempt)
		}
		if vs := invariants.Check(ctx, st, invariants.WithVaultDir(vaultDir)); len(vs) > 0 {
			t.Fatalf("attempt %d invariant check failed: %+v", attempt, vs)
		}

		ag, _, _ := st.Read().GetAgent(ctx, "ag-rogue")
		if ag.ScopeViolations != attempt {
			t.Fatalf("attempt %d: scope_violations=%d, want %d", attempt, ag.ScopeViolations, attempt)
		}
		if attempt < 3 && ag.Status != domain.AgentActive {
			t.Fatalf("attempt %d: expected agent still active, got %q", attempt, ag.Status)
		}
		if attempt == 3 && ag.Status != domain.AgentPaused {
			t.Fatalf("attempt 3: expected agent auto-paused after 3 scope violations, got %q", ag.Status)
		}
	}
}
