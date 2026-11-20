package scenario

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"slices"
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
	"max/internal/domain"
	"max/internal/testutil/fakeengine"
	"max/internal/testutil/invariants"
)

// Soak1000Result captures measured metrics from a 1,000-agent 14-day sparse monitor soak simulation (P4.04).
type Soak1000Result struct {
	AgentsCreated            int
	SimulatedDays            int
	SchedulerSweeps          int
	RunsExecuted             int
	TotalTokensUsed          int64
	MaxWakeupsSameSecond     int
	ClaimP50MS               float64
	ClaimP95MS               float64
	ClaimP99MS               float64
	OperationalInterventions int
	Violations               []invariants.Violation
}

// RunMonitor1000SoakSim bulk-creates count monitor agents (default 1,000) in the git vault and steps
// a virtual clock across days (default 14 days = 336 hourly sweeps) using fakeengine/fakecli with 0 live quota spent.
func RunMonitor1000SoakSim(ctx context.Context, count, days int) (Soak1000Result, error) {
	if count <= 0 {
		count = 1000
	}
	if days <= 0 {
		days = 14
	}

	tmpDir, err := os.MkdirTemp("", "max-soak-1000-*")
	if err != nil {
		return Soak1000Result{}, err
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	st, err := sqlite.Open(ctx, tmpDir+"/max.sqlite3")
	if err != nil {
		return Soak1000Result{}, err
	}
	defer func() { _ = st.Close() }()

	vaultDir := tmpDir + "/vault"
	vault := vaultgit.New(vaultDir)
	if err := vault.EnsureInstalled(ctx); err != nil {
		return Soak1000Result{}, err
	}

	clk := &manualClock{now: time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)}
	ids := app.UUIDv7Gen{}
	syncSvc := agentsync.NewService(st, vault, clk, ids)
	if _, _, err := syncSvc.SyncAll(ctx); err != nil {
		return Soak1000Result{}, err
	}

	rows := make([]map[string]any, count)
	for i := range count {
		rows[i] = map[string]any{
			"target": fmt.Sprintf("Tracked Target %04d", i+1),
			"url":    fmt.Sprintf("https://status.example.com/service/%04d", i+1),
		}
	}
	created, probs, err := syncSvc.InstantiateBulk(ctx, "monitor", "ops", "mon", rows)
	if err != nil || len(probs) > 0 {
		return Soak1000Result{}, fmt.Errorf("bulk instantiate %d monitors: err=%v probs=%d", count, err, len(probs))
	}

	const sparseCron = "0 9 * * 1"
	perSec := make(map[int64]int, count)
	maxSameSec := 0
	var activeAgentIDs []domain.ID

	if err := st.WithTx(ctx, func(tx app.Tx) error {
		if tm, ok, _ := tx.GetTeamBySlug(ctx, "ops"); ok {
			tm.MaxRunning = 4
			_ = tx.UpsertSyncedTeam(ctx, tm)
		}
		for idx, slug := range created {
			ag, ok, _ := tx.GetAgentBySlug(ctx, slug)
			if !ok {
				continue
			}
			nextAt, err := scheduler.ComputeNextHeartbeat(sparseCron, time.UTC, clk.Now(), ag.ID)
			if err != nil {
				return err
			}
			sec := nextAt.Unix()
			perSec[sec]++
			if perSec[sec] > maxSameSec {
				maxSameSec = perSec[sec]
			}
			ms := nextAt.UnixMilli()
			if err := tx.SetAgentHeartbeatSchedule(ctx, ag.ID, sparseCron, &ms); err != nil {
				return err
			}
			if idx < 40 {
				activeAgentIDs = append(activeAgentIDs, ag.ID)
			}
		}
		return nil
	}); err != nil {
		return Soak1000Result{}, err
	}

	actors := actor.NewService(st, clk, ids)
	coordSvc := coord.NewService(coord.Deps{Store: st, Actors: actors, Clock: clk, IDs: ids})
	quota := limits.NewQuota(st, clk, limits.QuotaConfig{ShortHours: 5, BackgroundShare: 0.7})
	sched := scheduler.New(st, actors, nil, nil).WithCoord(coordSvc).WithRoutines(vault, time.UTC, ids)

	eng := fakeengine.New(domain.EngineAgy,
		app.EngineEvent{Type: "session", SessionID: "s-soak"},
		app.EngineEvent{Type: "final", Text: "no significant change detected", InputTokens: 120, OutputTokens: 30},
	)
	zeroCd := time.Duration(0)
	runSvc := runapp.NewService(runapp.Deps{
		Store: st, Actors: actors, Coord: coordSvc,
		Engines: map[domain.EngineName]app.Engine{domain.EngineAgy: eng},
		Vault:   vault, VaultDir: vaultDir, Cooldown: &zeroCd, Clock: clk, IDs: ids,
	})

	caps := map[domain.EngineName]int{domain.EngineAgy: 3}
	slots := limits.NewSlots(caps, nil)
	var runsCount atomic.Int32
	var totalTokens atomic.Int64
	var claimSamples []float64

	totalHours := days * 24
	for h := range totalHours {
		// Seed new watch work each week before Monday 09:00 UTC so 40 active monitors execute across both weeks.
		if h%(24*7) == 0 {
			_ = st.WithTx(ctx, func(tx app.Tx) error {
				for _, agID := range activeAgentIDs {
					aID := agID
					tid := ids.New()
					if err := tx.InsertTask(ctx, app.TaskRecord{
						ID: tid, RootID: tid, RootOrigin: domain.OriginRoutine,
						Title: "Weekly diff check", Description: "Compare current snapshot",
						Status: string(domain.TaskTodo), AssigneeAgentID: &aID,
						CreatedAt: clk.Now().UnixMilli(), UpdatedAt: clk.Now().UnixMilli(),
					}); err != nil {
						return err
					}
				}
				return nil
			})
		}

		clk.Advance(time.Hour)
		if err := sched.FireHeartbeats(ctx, clk.Now()); err != nil {
			return Soak1000Result{}, err
		}

		for range 30 {
			tStart := time.Now()
			claimed, err := actors.ClaimBatch(ctx, actor.Capacity{
				Free:  slots.Free(),
				Admit: quota.Admit,
			}, clk.Now())
			durMS := float64(time.Since(tStart).Microseconds()) / 1000.0
			if err != nil {
				return Soak1000Result{}, err
			}
			if len(claimed) == 0 {
				break
			}
			claimSamples = append(claimSamples, durMS)
			for _, c := range claimed {
				slots.AcquirePriority(c.Engine, c.Priority)
				out, runErr := runSvc.Execute(ctx, c)
				slots.Release(c.Engine)
				if runErr != nil {
					return Soak1000Result{}, runErr
				}
				runsCount.Add(1)
				tok := out.InputTokens + out.OutputTokens
				totalTokens.Add(tok)
				_ = st.WithTx(ctx, func(tx app.Tx) error {
					return quota.Record(ctx, tx, c.Engine, c.AgentID, out.InputTokens, out.OutputTokens, clk.Now())
				})
			}
		}
	}

	inbox, _ := st.Read().ListInboxItems(ctx, 100)
	interventions := 0
	for _, it := range inbox {
		if it.Kind == "error" || it.Urgency == "high" {
			interventions++
		}
	}

	vs := invariants.Check(ctx, st,
		invariants.WithQuiescent(true),
		invariants.WithSlotCaps(caps),
		invariants.WithVaultDir(vaultDir),
	)

	slices.SortFunc(claimSamples, cmp.Compare[float64])
	return Soak1000Result{
		AgentsCreated:            len(created),
		SimulatedDays:            days,
		SchedulerSweeps:          totalHours,
		RunsExecuted:             int(runsCount.Load()),
		TotalTokensUsed:          totalTokens.Load(),
		MaxWakeupsSameSecond:     maxSameSec,
		ClaimP50MS:               percentileFloat(claimSamples, 0.50),
		ClaimP95MS:               percentileFloat(claimSamples, 0.95),
		ClaimP99MS:               percentileFloat(claimSamples, 0.99),
		OperationalInterventions: interventions,
		Violations:               vs,
	}, nil
}

func percentileFloat(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * q)
	return sorted[idx]
}
