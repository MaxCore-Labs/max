package scenario

import (
	"context"
	"fmt"
	"os"
	"strings"
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
	"max/internal/app/scheduler"
	"max/internal/app/tools"
	"max/internal/domain"
	"max/internal/testutil/fakeengine"
	"max/internal/testutil/invariants"
)

// StormConfig configures RunStormSim.
type StormConfig struct {
	NormalAgents int
	Slots        int
}

// StormResult summarizes storm containment metrics.
type StormResult struct {
	TotalRuns       int
	InboxAlerts     int
	NormalTasksDone int
	Violations      []invariants.Violation
}

// RunStormSim pits 3 infinitely looping/messaging agents against coordination & hourly limits while normal agents execute alongside them.
func RunStormSim(ctx context.Context, cfg StormConfig) (StormResult, error) {
	if cfg.NormalAgents <= 0 {
		cfg.NormalAgents = 5
	}
	if cfg.Slots <= 0 {
		cfg.Slots = 4
	}

	tmpDir, err := os.MkdirTemp("", "max-sim-storm-*")
	if err != nil {
		return StormResult{}, err
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	st, err := sqlite.Open(ctx, tmpDir+"/max.sqlite3")
	if err != nil {
		return StormResult{}, err
	}
	defer func() { _ = st.Close() }()

	vaultDir := tmpDir + "/vault"
	vault := vaultgit.New(vaultDir)
	if err := vault.EnsureInstalled(ctx); err != nil {
		return StormResult{}, err
	}

	clk := app.SystemClock{}
	ids := app.UUIDv7Gen{}
	actors := actor.NewService(st, clk, ids)
	coordSvc := coord.NewService(coord.Deps{Store: st, Actors: actors, Clock: clk, IDs: ids})
	toolSvc := tools.NewService(st, vaultDir, clk, ids).WithCoord(coordSvc, actors)
	quota := limits.NewQuota(st, clk, limits.QuotaConfig{ShortHours: 5, BackgroundShare: 0.7, TZ: "UTC"})

	stormSlugs := []string{"storm-a", "storm-b", "storm-c", "storm-d"}
	var normalTaskIDs []string

	if err := st.WithTx(ctx, func(tx app.Tx) error {
		if err := tx.InsertTeam(ctx, app.TeamRecord{ID: "tm-storm", Slug: "storm", Name: "Storm", MaxRunning: cfg.Slots, Status: "active"}); err != nil {
			return err
		}
		if err := tx.InsertTeam(ctx, app.TeamRecord{ID: "tm-norm", Slug: "normal", Name: "Normal", MaxRunning: cfg.Slots, Status: "active"}); err != nil {
			return err
		}
		for _, slug := range stormSlugs {
			if err := tx.InsertAgent(ctx, app.AgentRecord{
				ID: slug, Slug: slug, Name: slug, TeamID: "tm-storm",
				EnginesJSON: `["agy"]`, EngineMask: 4, Autonomy: domain.AutonomyAuto, Browser: domain.BrowserNone,
				ConfigRev: "s1", Status: domain.AgentActive,
			}); err != nil {
				return err
			}
		}
		for i := range cfg.NormalAgents {
			slug := fmt.Sprintf("worker-%d", i+1)
			if err := tx.InsertAgent(ctx, app.AgentRecord{
				ID: slug, Slug: slug, Name: slug, TeamID: "tm-norm",
				EnginesJSON: `["agy"]`, EngineMask: 4, Autonomy: domain.AutonomyAuto, Browser: domain.BrowserNone,
				ConfigRev: "n1", Status: domain.AgentActive,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return StormResult{}, err
	}

	eng := fakeengine.New(domain.EngineAgy)
	eng.SetDynamic(func(rctx context.Context, spec app.RunSpec, _ app.PermissionFunc, out chan<- app.EngineEvent) {
		out <- app.EngineEvent{Type: "session", SessionID: "sess-" + spec.AgentID}
		ws, _ := st.Read().ListClaimedWakeupsByRun(rctx, spec.RunID)
		var convID, taskID string
		var maxHops int
		for _, w := range ws {
			if w.Hops > maxHops {
				maxHops = w.Hops
			}
			if convID == "" && w.ConversationID != nil {
				convID = *w.ConversationID
			}
		}
		if conv, ok, _ := st.Read().GetConversation(rctx, convID); ok && conv.TaskID != nil {
			taskID = *conv.TaskID
		}

		if strings.HasPrefix(spec.AgentID, "storm-") && taskID != "" {
			nextTarget := "storm-b"
			switch spec.AgentID {
			case "storm-b":
				nextTarget = "storm-c"
			case "storm-c":
				nextTarget = "storm-d"
			case "storm-d":
				nextTarget = "storm-a"
			}
			c := tools.Caller{
				RunID:          spec.RunID,
				AgentID:        spec.AgentID,
				ConversationID: convID,
				TaskID:         &taskID,
				Hops:           maxHops,
				Origin:         domain.OriginOwner,
				Policy: domain.AgentPolicy{
					Scope:    domain.AgentScope{Slug: spec.AgentID, TeamSlug: "storm"},
					Autonomy: domain.AutonomyAuto,
				},
			}
			// Attempt ring message chain (hits ErrHops at 20), pair chatter (hits ErrPairRate at 6), and recursive delegate (hits ErrDepth at 5)
			_ = toolSvc.MessageAgent(rctx, c, nextTarget, "ping ring loop")
			if spec.AgentID == "storm-a" {
				for range 3 {
					_ = toolSvc.MessageAgent(rctx, c, "storm-b", "pair spam")
				}
			}
			_ = toolSvc.Delegate(rctx, c, nextTarget, "Sub loop", "Loop", "Never", nil, false, nil)
		} else if taskID != "" {
			c := tools.Caller{
				RunID:          spec.RunID,
				AgentID:        spec.AgentID,
				ConversationID: convID,
				TaskID:         &taskID,
				Origin:         domain.OriginOwner,
				Policy: domain.AgentPolicy{
					Scope:    domain.AgentScope{Slug: spec.AgentID, TeamSlug: "normal"},
					Autonomy: domain.AutonomyAuto,
				},
			}
			_ = toolSvc.TaskUpdate(rctx, c, taskID, string(domain.TaskDone), "")
		}
		out <- app.EngineEvent{Type: "final", Text: "done", InputTokens: 20, OutputTokens: 10}
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

	stormAssignee := "storm-a"
	if _, err := coordSvc.Create(ctx, coord.NewTask{
		Title:    "Seed Storm Root",
		Assignee: &stormAssignee,
		Origin:   domain.OriginOwner,
	}); err != nil {
		return StormResult{}, err
	}
	for i := range cfg.NormalAgents {
		wID := fmt.Sprintf("worker-%d", i+1)
		tid, err := coordSvc.Create(ctx, coord.NewTask{
			Title:    fmt.Sprintf("Normal Task %d", i+1),
			Assignee: &wID,
			Origin:   domain.OriginOwner,
		})
		if err != nil {
			return StormResult{}, err
		}
		normalTaskIDs = append(normalTaskIDs, tid)
	}

	var totalRuns atomic.Int32
	caps := map[domain.EngineName]int{domain.EngineAgy: cfg.Slots}
	slots := limits.NewSlots(caps, nil)
	if err := drainUntilQuiescent(ctx, st, actors, runSvc, slots, quota.AgentAllowed, clk, &totalRuns, 500); err != nil {
		return StormResult{}, err
	}

	doneCount := 0
	for _, tid := range normalTaskIDs {
		if tk, ok, _ := st.Read().GetTask(ctx, tid); ok && tk.Status == string(domain.TaskDone) {
			doneCount++
		}
	}
	inboxItems, _ := st.Read().ListInboxItems(ctx, 100)
	// Note: storm agents paused by 12 runs/hr cap may still hold queued low-priority wakeups waiting for next hour,
	// so check non-quiescent structural invariants I1-I2, I4-I7, I13-I14.
	vs := invariants.Check(ctx, st, invariants.WithSlotCaps(caps), invariants.WithVaultDir(vaultDir))

	return StormResult{
		TotalRuns:       int(totalRuns.Load()),
		InboxAlerts:     len(inboxItems),
		NormalTasksDone: doneCount,
		Violations:      vs,
	}, nil
}

// MonitorSimResult summarizes the 24-hour sparse monitor simulation.
type MonitorSimResult struct {
	AgentsCreated   int
	HeartbeatsFired int
	MaxPerSecond    int
	Violations      []invariants.Violation
}

// RunMonitor24hSim bulk-instantiates count monitor agents and steps a virtual clock across 24 hours.
func RunMonitor24hSim(ctx context.Context, count int) (MonitorSimResult, error) {
	tmpDir, err := os.MkdirTemp("", "max-sim-mon-*")
	if err != nil {
		return MonitorSimResult{}, err
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	st, err := sqlite.Open(ctx, tmpDir+"/max.sqlite3")
	if err != nil {
		return MonitorSimResult{}, err
	}
	defer func() { _ = st.Close() }()

	vaultDir := tmpDir + "/vault"
	vault := vaultgit.New(vaultDir)
	if err := vault.EnsureInstalled(ctx); err != nil {
		return MonitorSimResult{}, err
	}

	clk := &manualClock{now: time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)}
	ids := app.UUIDv7Gen{}
	syncSvc := agentsync.NewService(st, vault, clk, ids)
	if _, _, err := syncSvc.SyncAll(ctx); err != nil {
		return MonitorSimResult{}, err
	}

	rows := make([]map[string]any, count)
	for i := range count {
		rows[i] = map[string]any{
			"target": fmt.Sprintf("Entity %d", i+1),
			"url":    fmt.Sprintf("https://example.com/p/%d", i+1),
		}
	}
	created, probs, err := syncSvc.InstantiateBulk(ctx, "monitor", "ops", "mon", rows)
	if err != nil || len(probs) > 0 {
		return MonitorSimResult{}, fmt.Errorf("bulk instantiate: err=%v probs=%+v", err, probs)
	}

	perSec := make(map[int64]int)
	maxSameSec := 0
	if err := st.WithTx(ctx, func(tx app.Tx) error {
		for _, slug := range created {
			ag, ok, _ := tx.GetAgentBySlug(ctx, slug)
			if !ok {
				continue
			}
			nextAt, err := scheduler.ComputeNextHeartbeat("0 */6 * * *", time.UTC, clk.Now(), ag.ID)
			if err != nil {
				return err
			}
			sec := nextAt.Unix()
			perSec[sec]++
			if perSec[sec] > maxSameSec {
				maxSameSec = perSec[sec]
			}
			ms := nextAt.UnixMilli()
			if err := tx.SetAgentHeartbeatSchedule(ctx, ag.ID, "0 */6 * * *", &ms); err != nil {
				return err
			}
			// Seed one assigned todo task on the first 10 monitors so S5 precondition wakes them cleanly
			if strings.HasSuffix(slug, "-1") || strings.HasSuffix(slug, "-2") {
				agID := ag.ID
				tid := ids.New()
				if err := tx.InsertTask(ctx, app.TaskRecord{
					ID: tid, RootID: tid, RootOrigin: domain.OriginOwner,
					Title: "Initial watch " + slug, Description: "Check baseline",
					Status: string(domain.TaskTodo), AssigneeAgentID: &agID,
					CreatedAt: clk.Now().UnixMilli(), UpdatedAt: clk.Now().UnixMilli(),
				}); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		return MonitorSimResult{}, err
	}

	actors := actor.NewService(st, clk, ids)
	coordSvc := coord.NewService(coord.Deps{Store: st, Actors: actors, Clock: clk, IDs: ids})
	sched := scheduler.New(st, actors, nil, nil).WithCoord(coordSvc).WithRoutines(vault, time.UTC, ids)
	eng := fakeengine.New(domain.EngineAgy,
		app.EngineEvent{Type: "session", SessionID: "s-mon"},
		app.EngineEvent{Type: "final", Text: "checked", InputTokens: 40, OutputTokens: 15},
	)
	zeroCd := time.Duration(0)
	runSvc := runapp.NewService(runapp.Deps{
		Store: st, Actors: actors, Coord: coordSvc,
		Engines: map[domain.EngineName]app.Engine{domain.EngineAgy: eng},
		Vault:   vault, VaultDir: vaultDir, Cooldown: &zeroCd, Clock: clk, IDs: ids,
	})

	caps := map[domain.EngineName]int{domain.EngineAgy: 4}
	slots := limits.NewSlots(caps, nil)
	var runsCount atomic.Int32

	// Advance 24h in 5-minute scheduler ticks (288 ticks)
	for range 288 {
		clk.Advance(5 * time.Minute)
		if err := sched.FireHeartbeats(ctx, clk.Now()); err != nil {
			return MonitorSimResult{}, err
		}
		if err := drainUntilQuiescent(ctx, st, actors, runSvc, slots, nil, clk, &runsCount, 50); err != nil {
			return MonitorSimResult{}, err
		}
	}

	vs := invariants.Check(ctx, st,
		invariants.WithQuiescent(true),
		invariants.WithSlotCaps(caps),
		invariants.WithVaultDir(vaultDir),
	)
	return MonitorSimResult{
		AgentsCreated:   len(created),
		HeartbeatsFired: int(runsCount.Load()),
		MaxPerSecond:    maxSameSec,
		Violations:      vs,
	}, nil
}

type manualClock struct {
	now time.Time
}

func (c *manualClock) Now() time.Time          { return c.now }
func (c *manualClock) Advance(d time.Duration) { c.now = c.now.Add(d) }
