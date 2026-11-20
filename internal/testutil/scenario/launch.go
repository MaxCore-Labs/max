package scenario

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"max/internal/adapter/sqlite"
	"max/internal/adapter/vaultgit"
	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/app/agentsync"
	"max/internal/app/coord"
	"max/internal/app/limits"
	runapp "max/internal/app/run"
	"max/internal/app/tools"
	"max/internal/domain"
	"max/internal/testutil/fakeengine"
	"max/internal/testutil/invariants"
)

// MultiAgentConfig configures RunMultiAgentSim.
type MultiAgentConfig struct {
	Agents int
	Teams  int
	Goals  int
	Slots  int
}

// MultiAgentResult reports outcomes and invariant checks from RunMultiAgentSim.
type MultiAgentResult struct {
	Runs       int
	Delegates  int
	Messages   int
	Reviews    int
	Violations []invariants.Violation
}

// RunMultiAgentSim exercises 200 agents across 5 teams with delegate, message_agent, blocker unblocking, and review rounds.
func RunMultiAgentSim(ctx context.Context, cfg MultiAgentConfig) (MultiAgentResult, error) {
	if cfg.Agents <= 0 {
		cfg.Agents = 200
	}
	if cfg.Teams <= 0 {
		cfg.Teams = 5
	}
	if cfg.Goals <= 0 {
		cfg.Goals = 10
	}
	if cfg.Slots <= 0 {
		cfg.Slots = 6
	}

	tmpDir, err := os.MkdirTemp("", "max-sim-multi-*")
	if err != nil {
		return MultiAgentResult{}, err
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	st, err := sqlite.Open(ctx, tmpDir+"/max.sqlite3")
	if err != nil {
		return MultiAgentResult{}, err
	}
	defer func() { _ = st.Close() }()

	vaultDir := tmpDir + "/vault"
	vault := vaultgit.New(vaultDir)
	if err := vault.EnsureInstalled(ctx); err != nil {
		return MultiAgentResult{}, err
	}

	clk := app.SystemClock{}
	ids := app.UUIDv7Gen{}
	syncSvc := agentsync.NewService(st, vault, clk, ids)
	if _, _, err := syncSvc.SyncAll(ctx); err != nil {
		return MultiAgentResult{}, err
	}
	actors := actor.NewService(st, clk, ids)
	coordSvc := coord.NewService(coord.Deps{Store: st, Actors: actors, Clock: clk, IDs: ids})
	toolSvc := tools.NewService(st, vaultDir, clk, ids).WithCoord(coordSvc, actors)

	teamSlugs := []string{"core", "marketing", "sales", "support", "ops"}
	if err := st.WithTx(ctx, func(tx app.Tx) error {
		for a := range cfg.Agents {
			tmSlug := teamSlugs[(a/5)%len(teamSlugs)]
			tm, ok, _ := tx.GetTeamBySlug(ctx, tmSlug)
			if !ok {
				continue
			}
			role := "researcher"
			switch a % 5 {
			case 1:
				role = "writer"
			case 2:
				role = "reviewer"
			case 3:
				role = "marketer"
			case 4:
				role = "monitor"
			}
			slug := fmt.Sprintf("%s-%03d", role, a)
			if err := tx.InsertAgent(ctx, app.AgentRecord{
				ID:           fmt.Sprintf("sim-ag-%03d", a),
				Slug:         slug,
				Name:         slug,
				TemplateSlug: &role,
				TeamID:       tm.ID,
				EnginesJSON:  `["agy"]`,
				EngineMask:   4,
				Autonomy:     domain.AutonomyAuto,
				Browser:      domain.BrowserNone,
				ConfigRev:    "sim-1",
				Status:       domain.AgentActive,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return MultiAgentResult{}, err
	}

	var delegates, messages, reviews, runsDone atomic.Int32
	eng := fakeengine.New(domain.EngineAgy)
	eng.SetDynamic(func(rctx context.Context, spec app.RunSpec, _ app.PermissionFunc, out chan<- app.EngineEvent) {
		out <- app.EngineEvent{Type: "session", SessionID: "sess-" + spec.AgentSlug}
		ag, ok, _ := st.Read().GetAgent(rctx, spec.AgentID)
		if !ok {
			out <- app.EngineEvent{Type: "final", Text: "done"}
			return
		}
		ws, _ := st.Read().ListClaimedWakeupsByRun(rctx, spec.RunID)
		var convID, taskID string
		if len(ws) > 0 && ws[0].ConversationID != nil {
			convID = *ws[0].ConversationID
			if conv, cok, _ := st.Read().GetConversation(rctx, convID); cok && conv.TaskID != nil {
				taskID = *conv.TaskID
			}
		}
		if taskID == "" {
			out <- app.EngineEvent{Type: "final", Text: "done"}
			return
		}
		tk, tok, _ := st.Read().GetTask(rctx, taskID)
		if !tok || domain.TaskStatus(tk.Status).Final() {
			out <- app.EngineEvent{Type: "final", Text: "done"}
			return
		}

		tm, _, _ := st.Read().GetTeam(rctx, ag.TeamID)
		caller := tools.Caller{
			RunID:          spec.RunID,
			AgentID:        ag.ID,
			ConversationID: convID,
			TaskID:         &taskID,
			Origin:         tk.RootOrigin,
			Policy: domain.AgentPolicy{
				Scope:    domain.AgentScope{Slug: ag.Slug, TeamSlug: tm.Slug},
				Autonomy: domain.AutonomyAuto,
			},
		}

		switch {
		case ag.Slug == "chief-of-staff" && tk.Depth == 0:
			children, _ := st.Read().ListChildTasks(rctx, tk.ID)
			if len(children) == 0 {
				rRes := toolSvc.Delegate(rctx, caller, "researcher-000", "Research "+tk.Title, "Collect sources", "Cited notes", nil, false, nil)
				if !rRes.IsError {
					delegates.Add(1)
				}
				kids, _ := st.Read().ListChildTasks(rctx, tk.ID)
				var blockers []string
				if len(kids) > 0 {
					blockers = []string{kids[0].ID}
				}
				wRes := toolSvc.Delegate(rctx, caller, "writer-001", "Draft "+tk.Title, "Write article", "Polished draft", blockers, true, nil)
				if !wRes.IsError {
					delegates.Add(1)
				}
			} else {
				allDone := true
				for _, ch := range children {
					if !domain.TaskStatus(ch.Status).Final() {
						allDone = false
					}
				}
				if allDone {
					_ = toolSvc.TaskUpdate(rctx, caller, tk.ID, string(domain.TaskDone), "")
				}
			}
		case tk.Status == string(domain.TaskInReview) && tk.ReviewerAgentID != nil && *tk.ReviewerAgentID == ag.ID:
			approve := tk.ReviewRounds >= 1
			note := "Cần bổ sung dẫn chứng"
			if approve {
				note = "Đạt tiêu chuẩn"
			}
			if coordSvc.Review(rctx, tk.ID, ag.ID, approve, note) == nil {
				reviews.Add(1)
			}
		case strings.HasPrefix(ag.Slug, "writer-"):
			if tk.ReviewRounds == 0 {
				mRes := toolSvc.MessageAgent(rctx, caller, "researcher-000", "Đã nhận tài liệu nghiên cứu")
				if !mRes.IsError {
					messages.Add(1)
				}
			}
			_ = toolSvc.RequestReview(rctx, caller)
		default:
			_ = toolSvc.TaskUpdate(rctx, caller, tk.ID, string(domain.TaskDone), "")
		}
		out <- app.EngineEvent{Type: "final", Text: "ok", InputTokens: 50, OutputTokens: 25}
	})

	zeroCd := time.Duration(0)
	runSvc := runapp.NewService(runapp.Deps{
		Store:    st,
		Actors:   actors,
		Coord:    coordSvc,
		Engines:  map[domain.EngineName]app.Engine{domain.EngineAgy: eng},
		Vault:    vault,
		VaultDir: vaultDir,
		Cooldown: &zeroCd,
		Clock:    clk,
		IDs:      ids,
	})

	chief, _, _ := st.Read().GetAgentBySlug(ctx, "chief-of-staff")
	for g := range cfg.Goals {
		if _, err := coordSvc.Create(ctx, coord.NewTask{
			Title:       fmt.Sprintf("Goal Launch %d", g+1),
			Description: "Coordinate launch deliverables",
			Assignee:    &chief.ID,
			Origin:      domain.OriginOwner,
		}); err != nil {
			return MultiAgentResult{}, err
		}
	}

	caps := map[domain.EngineName]int{domain.EngineAgy: cfg.Slots}
	slots := limits.NewSlots(caps, nil)
	if err := drainUntilQuiescent(ctx, st, actors, runSvc, slots, nil, clk, &runsDone, 5000); err != nil {
		return MultiAgentResult{}, err
	}

	vs := invariants.Check(ctx, st,
		invariants.WithQuiescent(true),
		invariants.WithSlotCaps(caps),
		invariants.WithVaultDir(vaultDir),
	)
	return MultiAgentResult{
		Runs:       int(runsDone.Load()),
		Delegates:  int(delegates.Load()),
		Messages:   int(messages.Load()),
		Reviews:    int(reviews.Load()),
		Violations: vs,
	}, nil
}

func drainUntilQuiescent(
	ctx context.Context,
	st *sqlite.Store,
	actors *actor.Service,
	runSvc *runapp.Service,
	slots *limits.Slots,
	agentAllowed func(ctx context.Context, agentID, teamID domain.ID, now time.Time) (bool, string),
	clk app.Clock,
	runsCounter *atomic.Int32,
	maxIterations int,
) error {
	var wg sync.WaitGroup
	for range maxIterations {
		free := slots.Free()
		if free[domain.EngineAgy] > 0 {
			claimed, err := actors.ClaimBatch(ctx, actor.Capacity{
				Free:         free,
				AgentAllowed: agentAllowed,
			}, clk.Now())
			if err != nil {
				wg.Wait()
				return err
			}
			if len(claimed) > 0 {
				for _, c := range claimed {
					if !slots.Acquire(c.Engine) {
						_ = runSvc.Finalize(ctx, c.RunID, runapp.Outcome{Status: domain.RunQuotaStopped})
						continue
					}
					wg.Add(1)
					go func(cl actor.Claimed) {
						defer wg.Done()
						defer slots.Release(cl.Engine)
						_, _ = runSvc.Execute(ctx, cl)
						if runsCounter != nil {
							runsCounter.Add(1)
						}
					}(c)
				}
				continue
			}
		}
		wg.Wait()
		_, _, nonTerm, err := st.CountWakeupsStatusSummary(ctx)
		if err != nil {
			return err
		}
		if nonTerm == 0 {
			return nil
		}
		// Check if remaining wakeups belong only to agents blocked by hourly rate cap
		if agentAllowed != nil {
			claimedCheck, _ := actors.ClaimBatch(ctx, actor.Capacity{
				Free:         slots.Free(),
				AgentAllowed: agentAllowed,
			}, clk.Now())
			if len(claimedCheck) == 0 {
				return nil
			}
			for _, cl := range claimedCheck {
				_, _ = runSvc.Execute(ctx, cl)
				if runsCounter != nil {
					runsCounter.Add(1)
				}
			}
			continue
		}
		time.Sleep(2 * time.Millisecond)
	}
	wg.Wait()
	return nil
}
