package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"max/internal/adapter/engine/agy"
	"max/internal/adapter/sqlite"
	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/app/limits"
	runapp "max/internal/app/run"
	"max/internal/app/scheduler"
	"max/internal/domain"
	"max/internal/testutil/invariants"
)

type loadtestFlags struct {
	dataDir, fakecliBin                 string
	agents, wakeups, slots              int
	sleepMinMS, sleepMaxMS, chaosRounds int
	killEvery                           time.Duration
	workerOnly                          bool
}

func runLoadtest(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("loadtest", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var cfg loadtestFlags
	fs.StringVar(&cfg.dataDir, "data-dir", "", "SQLite directory (defaults to temporary directory)")
	fs.StringVar(&cfg.fakecliBin, "fakecli-bin", "", "prebuilt fakecli binary path")
	fs.IntVar(&cfg.agents, "agents", 200, "number of agents")
	fs.IntVar(&cfg.wakeups, "wakeups", 2000, "number of wakeups")
	fs.IntVar(&cfg.slots, "slots", 3, "concurrent CLI slots")
	fs.IntVar(&cfg.sleepMinMS, "sleep-min-ms", 5, "minimum fakecli sleep ms")
	fs.IntVar(&cfg.sleepMaxMS, "sleep-max-ms", 20, "maximum fakecli sleep ms")
	fs.DurationVar(&cfg.killEvery, "kill-every", 0, "if > 0, kill and restart worker every interval")
	fs.IntVar(&cfg.chaosRounds, "chaos-rounds", 24, "number of kill cycles when --kill-every > 0")
	fs.BoolVar(&cfg.workerOnly, "worker", false, "internal worker mode")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if cfg.dataDir == "" {
		tmp, err := os.MkdirTemp("", "max-loadtest-*")
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "mkdirtemp: %v\n", err)
			return 1
		}
		cfg.dataDir = tmp
		defer func() { _ = os.RemoveAll(cfg.dataDir) }()
	}

	if cfg.fakecliBin == "" {
		built, cleanupBin, err := buildFakeCLIBinary()
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "build fakecli: %v\n", err)
			return 1
		}
		defer cleanupBin()
		cfg.fakecliBin = built
	} else if abs, err := filepath.Abs(cfg.fakecliBin); err == nil {
		cfg.fakecliBin = abs
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if cfg.killEvery > 0 && !cfg.workerOnly {
		return runChaosSupervisor(ctx, cfg, stdout, stderr)
	}
	return runLoadtestWorker(ctx, cfg, stdout, stderr)
}

func buildFakeCLIBinary() (string, func(), error) {
	dir, err := os.MkdirTemp("", "max-fakecli-*")
	if err != nil {
		return "", func() {}, err
	}
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	outPath := filepath.Join(dir, "fakecli"+ext)
	cmd := exec.Command("go", "build", "-o", outPath, "max/internal/testutil/fakecli")
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.RemoveAll(dir)
		return "", func() {}, fmt.Errorf("%w: %s", err, string(out))
	}
	return outPath, func() { _ = os.RemoveAll(dir) }, nil
}

func runChaosSupervisor(ctx context.Context, cfg loadtestFlags, stdout, stderr io.Writer) int {
	if err := ensureLoadtestSeeded(ctx, cfg); err != nil {
		_, _ = fmt.Fprintf(stderr, "seed chaos db: %v\n", err)
		return 1
	}
	self, err := os.Executable()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "executable: %v\n", err)
		return 1
	}

	kills := 0
	for round := 0; round < cfg.chaosRounds; round++ {
		if ctx.Err() != nil {
			break
		}
		wArgs := []string{
			"loadtest", "--worker", "--data-dir", cfg.dataDir, "--fakecli-bin", cfg.fakecliBin,
			"--agents", strconv.Itoa(cfg.agents), "--wakeups", strconv.Itoa(cfg.wakeups),
			"--slots", strconv.Itoa(cfg.slots),
			"--sleep-min-ms", strconv.Itoa(cfg.sleepMinMS), "--sleep-max-ms", strconv.Itoa(cfg.sleepMaxMS),
		}
		child := exec.CommandContext(ctx, self, wArgs...)
		child.Stdout = stdout
		child.Stderr = stderr
		if err := child.Start(); err != nil {
			_, _ = fmt.Fprintf(stderr, "start worker: %v\n", err)
			return 1
		}

		doneCh := make(chan struct{})
		go func() { _ = child.Wait(); close(doneCh) }()

		select {
		case <-doneCh:
			round = cfg.chaosRounds
		case <-time.After(cfg.killEvery):
			_ = child.Process.Kill()
			<-doneCh
			kills++
		}
	}

	_, _ = fmt.Fprintf(stdout, "chaos supervisor executed %d kill(s); draining remaining wakeups...\n", kills)
	return runLoadtestWorker(ctx, cfg, stdout, stderr)
}

func ensureLoadtestSeeded(ctx context.Context, cfg loadtestFlags) error {
	_ = os.MkdirAll(cfg.dataDir, 0o700)
	st, err := sqlite.Open(ctx, filepath.Join(cfg.dataDir, "max.sqlite3"))
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	_, total, err := st.CountWakeupsDoneAndTotal(ctx)
	if err != nil {
		return err
	}
	if total > 0 {
		return nil
	}

	numTeams := max(10, cfg.slots*2)
	nowMs := time.Now().UnixMilli()
	idGen := app.UUIDv7Gen{}

	err = st.WithTx(ctx, func(tx app.Tx) error {
		for t := range numTeams {
			if err := tx.InsertTeam(ctx, app.TeamRecord{
				ID:         fmt.Sprintf("team-%03d", t),
				Slug:       fmt.Sprintf("team-%03d", t),
				Name:       fmt.Sprintf("Team %d", t),
				MaxRunning: max(4, cfg.slots),
				Status:     "active",
			}); err != nil {
				return err
			}
		}

		for a := range cfg.agents {
			agID := fmt.Sprintf("agent-%05d", a)
			convID := fmt.Sprintf("conv-%05d", a)
			if err := tx.InsertAgent(ctx, app.AgentRecord{
				ID:          agID,
				Slug:        agID,
				Name:        agID,
				TeamID:      fmt.Sprintf("team-%03d", a%numTeams),
				EnginesJSON: `["agy"]`,
				EngineMask:  4,
				Autonomy:    domain.AutonomyAuto,
				Browser:     domain.BrowserNone,
				ConfigRev:   "lt-1",
				Status:      domain.AgentActive,
			}); err != nil {
				return err
			}
			if err := tx.InsertConversation(ctx, app.ConversationRecord{
				ID:       convID,
				AgentID:  agID,
				Sessions: `{}`,
			}); err != nil {
				return err
			}
		}

		prios := []domain.Priority{domain.PrioInteractive, domain.PrioTask, domain.PrioRoutine, domain.PrioMaintenance}
		touchedAgents := make(map[string]struct{}, cfg.agents)
		for w := range cfg.wakeups {
			agIdx := w % cfg.agents
			agID := fmt.Sprintf("agent-%05d", agIdx)
			convID := fmt.Sprintf("conv-%05d", agIdx)
			prio := prios[rand.IntN(len(prios))]
			if err := tx.InsertWakeupRaw(ctx, app.WakeupRecord{
				ID:             idGen.New(),
				AgentID:        agID,
				ConversationID: &convID,
				Kind:           domain.WakeTaskAssigned,
				Priority:       prio,
				Payload:        `{"loadtest":true}`,
				Status:         "queued",
				CreatedAt:      nowMs + int64(w),
			}); err != nil {
				return err
			}
			touchedAgents[agID] = struct{}{}
		}
		for agID := range touchedAgents {
			if err := tx.RecomputeAgentReady(ctx, agID, nowMs+int64(cfg.wakeups)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return st.ExecRaw(ctx, `ANALYZE`)
}

func runLoadtestWorker(ctx context.Context, cfg loadtestFlags, stdout, stderr io.Writer) int {
	if err := ensureLoadtestSeeded(ctx, cfg); err != nil {
		_, _ = fmt.Fprintf(stderr, "seed loadtest: %v\n", err)
		return 1
	}

	st, err := sqlite.Open(ctx, filepath.Join(cfg.dataDir, "max.sqlite3"))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "open sqlite: %v\n", err)
		return 1
	}
	defer func() { _ = st.Close() }()

	clk := app.SystemClock{}
	ids := app.UUIDv7Gen{}
	actors := actor.NewService(st, clk, ids)
	if _, err := actors.Recover(ctx, clk.Now()); err != nil {
		_, _ = fmt.Fprintf(stderr, "recover: %v\n", err)
		return 1
	}

	if vs := invariants.Check(ctx, st, invariants.WithRecovered(true)); len(vs) > 0 {
		_, _ = fmt.Fprintf(stderr, "post-recovery invariant violations: %+v\n", vs)
		return 1
	}

	homeDir := filepath.Join(cfg.dataDir, "agent-home", "agy")
	_ = os.MkdirAll(homeDir, 0o700)
	vaultDir := filepath.Join(cfg.dataDir, "vault")
	_ = os.MkdirAll(vaultDir, 0o755)

	nopLog := slog.New(slog.NewTextHandler(io.Discard, nil))
	drv := agy.New(cfg.fakecliBin, homeDir, agy.ModeACP, nopLog).WithExtraEnv(
		fmt.Sprintf("FAKECLI_ACP_SLEEP_MIN_MS=%d", cfg.sleepMinMS),
		fmt.Sprintf("FAKECLI_ACP_SLEEP_MAX_MS=%d", cfg.sleepMaxMS),
	)

	cd := 10 * time.Millisecond
	runSvc := runapp.NewService(runapp.Deps{
		Store:    st,
		Actors:   actors,
		Engines:  map[domain.EngineName]app.Engine{domain.EngineAgy: drv},
		VaultDir: vaultDir,
		Cooldown: &cd,
		Clock:    clk,
		IDs:      ids,
	})

	caps := map[domain.EngineName]int{domain.EngineAgy: cfg.slots}
	slots := limits.NewSlots(caps, nil)
	sched := scheduler.New(st, actors, nil, nil)

	var (
		latMu         sync.Mutex
		claimDurs     []time.Duration
		runsCompleted atomic.Int64
		invViolations atomic.Int64
		wg            sync.WaitGroup
	)

	startWall := time.Now()
	for ctx.Err() == nil {
		free := slots.Free()
		if free[domain.EngineAgy] > 0 {
			t0 := time.Now()
			claimed, cerr := actors.ClaimBatch(ctx, actor.Capacity{Free: free}, t0)
			elapsed := time.Since(t0)
			if cerr == nil && len(claimed) > 0 {
				latMu.Lock()
				claimDurs = append(claimDurs, elapsed)
				latMu.Unlock()

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
						n := runsCompleted.Add(1)
						if n%500 == 0 {
							if vs := invariants.Check(ctx, st, invariants.WithSlotCaps(caps)); len(vs) > 0 {
								invViolations.Add(int64(len(vs)))
							}
						}
					}(c)
				}
				continue
			}
		}

		_, _, nonTerm, qerr := st.CountWakeupsStatusSummary(ctx)
		if qerr == nil && nonTerm == 0 {
			break
		}
		_ = sched.Tick(ctx, clk.Now())
		time.Sleep(15 * time.Millisecond)
	}
	wg.Wait()

	if ctx.Err() != nil {
		return 1
	}

	p50, p95, p99 := calcPercentiles(claimDurs)
	finalVs := invariants.Check(ctx, st,
		invariants.WithQuiescent(true),
		invariants.WithSlotCaps(caps),
		invariants.WithClaimLatencies(p95, p99),
	)

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	sysMiB := float64(mem.Sys) / (1024 * 1024)
	wallSec := max(0.001, time.Since(startWall).Seconds())
	doneW, dropW, nonTermW, _ := st.CountWakeupsStatusSummary(ctx)

	_, _ = fmt.Fprintf(stdout,
		"loadtest complete: agents=%d wakeups=%d (done=%d dropped=%d pending=%d) runs=%d wall=%.2fs throughput=%.1f wakeups/s claim_p50=%.2fms claim_p95=%.2fms claim_p99=%.2fms go_sys_mib=%.1f violations=%d\n",
		cfg.agents, cfg.wakeups, doneW, dropW, nonTermW, runsCompleted.Load(), wallSec,
		float64(doneW+dropW)/wallSec,
		float64(p50.Microseconds())/1000.0,
		float64(p95.Microseconds())/1000.0,
		float64(p99.Microseconds())/1000.0,
		sysMiB, int(invViolations.Load())+len(finalVs),
	)

	if invViolations.Load() > 0 || len(finalVs) > 0 {
		_, _ = fmt.Fprintf(stderr, "invariant violations detected: %+v\n", finalVs)
		return 1
	}
	return 0
}

func calcPercentiles(durs []time.Duration) (p50, p95, p99 time.Duration) {
	if len(durs) == 0 {
		return 0, 0, 0
	}
	cp := slices.Clone(durs)
	slices.Sort(cp)
	at := func(q float64) time.Duration {
		idx := int(float64(len(cp)-1) * q)
		return cp[idx]
	}
	return at(0.50), at(0.95), at(0.99)
}
