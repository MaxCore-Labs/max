package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"max/internal/adapter/scopeaudit"
	"max/internal/adapter/sqlite"
	"max/internal/adapter/vaultgit"
	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/app/agentsync"
	"max/internal/app/approvals"
	"max/internal/app/coord"
	runapp "max/internal/app/run"
	"max/internal/domain"
	"max/internal/platform"
	"max/internal/testutil/evalrun"
	"max/internal/testutil/fakeengine"
)

func runEval(args []string, stdout, stderr io.Writer) int {
	fsFlag := flag.NewFlagSet("eval", flag.ContinueOnError)
	fsFlag.SetOutput(stderr)
	engineName := fsFlag.String("engine", "agy", "CLI engine to evaluate (agy|fake)")
	onlyID := fsFlag.Int("only", 0, "run only scenario N (1..10)")
	runsPerScenario := fsFlag.Int("runs", 10, "repetitions per scenario")
	scenariosDir := fsFlag.String("scenarios-dir", "evals/scenarios", "path to scenarios directory")
	if err := fsFlag.Parse(args); err != nil {
		return 2
	}

	entries, err := os.ReadDir(*scenariosDir)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "read scenarios dir %q: %v\n", *scenariosDir, err)
		return 1
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, filepath.Join(*scenariosDir, e.Name()))
		}
	}
	sort.Strings(dirs)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	summaries := make(map[int]evalrun.ScenarioSummary)
	_, _ = fmt.Fprintf(stdout, "%-4s %-32s %-8s %-6s %s\n", "ID", "SCENARIO", "SCORE", "STATUS", "NOTES")

	anyFailedSingle := false
	for _, dir := range dirs {
		bundle, err := evalrun.LoadScenario(dir)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "load %s: %v\n", dir, err)
			return 1
		}
		if *onlyID > 0 && bundle.Setup.ID != *onlyID {
			continue
		}

		passed := 0
		var lastReasons []string
		for attempt := 0; attempt < *runsPerScenario; attempt++ {
			obs, rErr := executeEvalOnce(ctx, *engineName, bundle)
			if rErr != nil {
				lastReasons = []string{rErr.Error()}
				continue
			}
			ok, reasons := evalrun.Score(bundle.Expect, obs)
			if ok {
				passed++
			} else {
				lastReasons = reasons
			}
		}

		summaries[bundle.Setup.ID] = evalrun.ScenarioSummary{
			ID:     bundle.Setup.ID,
			Slug:   bundle.Setup.Slug,
			Runs:   *runsPerScenario,
			Passed: passed,
		}
		statusLabel := "PASS"
		if passed < *runsPerScenario {
			statusLabel = "WARN"
			if *onlyID > 0 {
				statusLabel = "FAIL"
				anyFailedSingle = true
			}
		}
		_, _ = fmt.Fprintf(
			stdout,
			"%02d   %-32s %2d/%-5d %-6s %s\n",
			bundle.Setup.ID,
			bundle.Setup.Slug,
			passed,
			*runsPerScenario,
			statusLabel,
			strings.Join(lastReasons, "; "),
		)
	}

	if *onlyID > 0 {
		if anyFailedSingle {
			return 1
		}
		return 0
	}

	okAll, gateReasons := evalrun.CheckThresholds(summaries)
	if !okAll {
		_, _ = fmt.Fprintf(stderr, "eval gate FAILED: %s\n", strings.Join(gateReasons, "; "))
		return 1
	}
	_, _ = fmt.Fprintln(stdout, "eval gate PASSED")
	return 0
}

func executeEvalOnce(ctx context.Context, engineFlag string, b evalrun.ScenarioBundle) (evalrun.Observed, error) {
	tmpDir, err := os.MkdirTemp("", "max-eval-*")
	if err != nil {
		return evalrun.Observed{}, err
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	st, err := sqlite.Open(ctx, filepath.Join(tmpDir, "max.sqlite3"))
	if err != nil {
		return evalrun.Observed{}, err
	}
	defer func() { _ = st.Close() }()

	vaultDir := filepath.Join(tmpDir, "vault")
	vault := vaultgit.New(vaultDir)
	if err := vault.EnsureInstalled(ctx); err != nil {
		return evalrun.Observed{}, err
	}
	var seededPaths []string
	for rel, content := range b.Setup.VaultFiles {
		if err := vault.Write(ctx, rel, []byte(content)); err != nil {
			return evalrun.Observed{}, err
		}
		seededPaths = append(seededPaths, rel)
	}
	if len(seededPaths) > 0 {
		_, _ = vault.CommitPaths(ctx, seededPaths, "chore(eval): seed scenario files")
	}

	clk := app.SystemClock{}
	ids := app.UUIDv7Gen{}
	syncSvc := agentsync.NewService(st, vault, clk, ids)
	_, _, _ = syncSvc.SyncAll(ctx)

	agSlug := b.Setup.AgentSlug
	if agSlug == "" {
		agSlug = "chief-of-staff"
	}
	if _, exists, _ := st.Read().GetAgentBySlug(ctx, agSlug); !exists {
		tpl := b.Setup.Template
		if tpl == "" {
			tpl = "researcher"
		}
		tm := b.Setup.Team
		if tm == "" {
			tm = "core"
		}
		_ = syncSvc.Instantiate(ctx, tpl, agSlug, tm, map[string]any{
			"focus": "eval", "channel": "blog", "segment": "smb", "queue": "inbox", "target": "demo", "url": "https://example.com", "repo": "demo",
		})
	}
	ag, ok, err := st.Read().GetAgentBySlug(ctx, agSlug)
	if err != nil || !ok {
		return evalrun.Observed{}, fmt.Errorf("eval agent %q unavailable", agSlug)
	}

	actors := actor.NewService(st, clk, ids)
	coordSvc := coord.NewService(coord.Deps{Store: st, Actors: actors, Clock: clk, IDs: ids})
	apprSvc := approvals.NewService(st, actors, nil, clk, ids)

	engName := domain.EngineAgy
	var engines map[domain.EngineName]app.Engine
	if engineFlag == "fake" {
		engines = map[domain.EngineName]app.Engine{
			domain.EngineAgy: buildCompliantFakeEvalEngine(b, vault, st, coordSvc, ids, ag.ID),
		}
	} else {
		cfg, _ := platform.Load(os.Getenv)
		if cfg.DataDir == "" {
			cfg.DataDir = "./data"
		}
		engines, _ = defaultEngines(cfg)
		engName = domain.EngineName(engineFlag)
	}

	var obs evalrun.Observed
	obs.VaultFiles = make(map[string]string)
	zeroCd := time.Duration(0)
	runSvc := runapp.NewService(runapp.Deps{
		Store:    st,
		Actors:   actors,
		Coord:    coordSvc,
		Engines:  engines,
		Prompts:  &vaultPromptBuilder{store: st, vault: vault, clock: clk, tz: "UTC"},
		Vault:    vault,
		VaultDir: vaultDir,
		Cooldown: &zeroCd,
		Clock:    clk,
		IDs:      ids,
		OnEvent: func(_ actor.Claimed, ev app.EngineEvent) {
			if ev.Type == "tool_call" {
				cls := string(domain.ClassifyBuiltin(ev.ToolName))
				obs.Events = append(obs.Events, evalrun.ObservedEvent{
					Kind:      "tool_call",
					ToolName:  ev.ToolName,
					ToolClass: cls,
				})
			}
		},
		PostRunAuditFull: func(actx context.Context, runID domain.ID, scope domain.AgentScope) ([]string, int) {
			touched, violations, _ := scopeaudit.Audit(actx, vaultDir, filepath.Join(tmpDir, "quarantine", runID), scope)
			return touched, len(violations)
		},
	})
	runSvc.SetPermission(runapp.NewPermissionFunc(runapp.PermissionDeps{
		Store: st, Runs: runSvc, Approvals: apprSvc, VaultDir: vaultDir, Clock: clk,
	}))

	taskID, err := coordSvc.Create(ctx, coord.NewTask{
		Title:        b.Setup.TaskTitle,
		Description:  b.Prompt,
		DoneCriteria: b.Setup.DoneCrit,
		Assignee:     &ag.ID,
		Origin:       domain.OriginOwner,
	})
	if err != nil {
		return evalrun.Observed{}, err
	}

	claimed, err := actors.ClaimBatch(ctx, actor.Capacity{Free: map[domain.EngineName]int{engName: 1}}, clk.Now())
	if err != nil || len(claimed) == 0 {
		return evalrun.Observed{}, fmt.Errorf("claim eval wakeup: %v", err)
	}
	_, _ = runSvc.Execute(ctx, claimed[0])

	inbox, _ := st.Read().ListInboxItems(ctx, 50)
	for _, it := range inbox {
		if it.Kind == "question" {
			obs.QuestionInboxCount++
		}
	}
	agAfter, _, _ := st.Read().GetAgent(ctx, ag.ID)
	obs.ScopeViolations = agAfter.ScopeViolations

	childTasks, _ := st.Read().ListChildTasks(ctx, taskID)
	obs.TasksOrProposals = len(childTasks)
	evs, _ := st.Read().ListTaskEvents(ctx, taskID, 50)
	for _, ev := range evs {
		if ev.Kind == "review" && strings.Contains(ev.Body, "changes") {
			obs.ReviewDecision = "changes"
		}
	}
	_ = filepath.WalkDir(vaultDir, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil || d.IsDir() || strings.Contains(filepath.ToSlash(p), "/.git/") {
			return nil
		}
		rel, _ := filepath.Rel(vaultDir, p)
		raw, rerr := os.ReadFile(p)
		if rerr == nil {
			obs.VaultFiles[filepath.ToSlash(rel)] = string(raw)
		}
		return nil
	})
	return obs, nil
}

func buildCompliantFakeEvalEngine(
	b evalrun.ScenarioBundle,
	vault *vaultgit.Vault,
	st *sqlite.Store,
	coordSvc *coord.Service,
	ids app.IDGen,
	agentID domain.ID,
) app.Engine {
	fe := fakeengine.New(domain.EngineAgy)
	fe.SetDynamic(func(ctx context.Context, spec app.RunSpec, _ app.PermissionFunc, out chan<- app.EngineEvent) {
		out <- app.EngineEvent{Type: "session", SessionID: "eval-sess"}
		switch b.Setup.ID {
		case 3:
			_ = st.WithTx(ctx, func(tx app.Tx) error {
				return tx.InsertInboxItem(ctx, app.InboxItemRecord{
					ID: ids.New(), Kind: "question", Title: "Budget?", Body: "Cap?", Urgency: "normal", CreatedAt: time.Now().UnixMilli(),
				})
			})
		case 4:
			out <- app.EngineEvent{Type: "tool_call", ToolName: "delegate"}
		case 5:
			ws, _ := st.Read().ListClaimedWakeupsByRun(ctx, spec.RunID)
			if len(ws) > 0 && ws[0].ConversationID != nil {
				if conv, ok, _ := st.Read().GetConversation(ctx, *ws[0].ConversationID); ok && conv.TaskID != nil {
					_, _ = coordSvc.Create(ctx, coord.NewTask{Title: "Follow-up", ParentID: conv.TaskID, Origin: domain.OriginOwner})
				}
			}
		case 6:
			out <- app.EngineEvent{Type: "tool_call", ToolName: "Read"}
			out <- app.EngineEvent{Type: "tool_call", ToolName: "Edit"}
		case 7:
			_ = vault.Write(ctx, "agents/writer-1/artifacts/changelog.md", []byte("# v1.2\n"))
			_, _ = vault.CommitPaths(ctx, []string{"agents/writer-1/artifacts/changelog.md"}, "eval artifact")
		case 9:
			_ = vault.Write(ctx, "wiki/market-summary.md", []byte("# Summary\n\nNguồn: https://research.example.org/sea-2026\n"))
			_, _ = vault.CommitPaths(ctx, []string{"wiki/market-summary.md"}, "eval wiki")
		case 10:
			ws, _ := st.Read().ListClaimedWakeupsByRun(ctx, spec.RunID)
			if len(ws) > 0 && ws[0].ConversationID != nil {
				if conv, ok, _ := st.Read().GetConversation(ctx, *ws[0].ConversationID); ok && conv.TaskID != nil {
					_ = st.WithTx(ctx, func(tx app.Tx) error {
						return tx.InsertTaskEvent(ctx, app.TaskEventRecord{
							ID: ids.New(), TaskID: *conv.TaskID, Kind: "review", ActorKind: "agent", ActorID: &agentID, Body: "changes: missing 3 metrics", CreatedAt: time.Now().UnixMilli(),
						})
					})
				}
			}
		}
		out <- app.EngineEvent{Type: "final", Text: "ok", InputTokens: 30, OutputTokens: 20}
	})
	return fe
}
