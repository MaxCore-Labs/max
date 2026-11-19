package main

import (
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type drainStats struct {
	CompletedRuns, InvariantChecks, Violations, PromotedAgents                           int64
	ClaimExecMeanMS, ClaimExecP95MS, ClaimTotalMeanMS, FinishExecMeanMS, FinishExecP95MS float64
}

func summarizeDurationsMS(samples []float64) (mean, p50, p95, p99, maxV float64) {
	if len(samples) == 0 {
		return 0, 0, 0, 0, 0
	}
	cp := slices.Clone(samples)
	slices.Sort(cp)
	var sum float64
	for _, v := range cp {
		sum += v
	}
	n := len(cp)
	return sum / float64(n), cp[n*50/100], cp[n*95/100], cp[n*99/100], cp[n-1]
}

func drainQueueLoop(ctx context.Context, st *store, totalSlots int, cooldownMS int64, checkEvery int64, forceIndexed bool) (drainStats, error) {
	sem := make(chan struct{}, totalSlots)
	var wg sync.WaitGroup
	var completedRuns, invariantChecks, violations, promotedTotal atomic.Int64
	var firstErr atomic.Pointer[error]

	var latMu sync.Mutex
	claimExecLats := make([]float64, 0, 4096)
	claimTotalLats := make([]float64, 0, 4096)
	finishExecLats := make([]float64, 0, 10020)

	setErr := func(e error) {
		firstErr.CompareAndSwap(nil, &e)
	}

	bgStop := make(chan struct{})
	var bgWG sync.WaitGroup
	bgWG.Add(1)
	go func() {
		defer bgWG.Done()
		schedTick := time.NewTicker(12 * time.Millisecond)
		ckptTick := time.NewTicker(250 * time.Millisecond)
		defer schedTick.Stop()
		defer ckptTick.Stop()
		for {
			select {
			case <-bgStop:
				return
			case <-ctx.Done():
				return
			case <-schedTick.C:
				if p, err := st.promoteDueWakeups(ctx, time.Now().UnixMilli()); err == nil && p > 0 {
					promotedTotal.Add(int64(p))
				}
			case <-ckptTick.C:
				_ = st.checkpointPassive(ctx)
			}
		}
	}()
	defer func() {
		close(bgStop)
		bgWG.Wait()
	}()

	for {
		if p := firstErr.Load(); p != nil {
			wg.Wait()
			return drainStats{Violations: violations.Load()}, *p
		}

		free := totalSlots - len(sem)
		if free <= 0 {
			time.Sleep(1 * time.Millisecond)
			continue
		}

		nowMS := time.Now().UnixMilli()
		claimed, totalDur, execDur, err := st.claimWork(ctx, free, 4, nowMS, forceIndexed)
		if err != nil {
			wg.Wait()
			return drainStats{}, err
		}
		if len(claimed) > 0 {
			latMu.Lock()
			claimTotalLats = append(claimTotalLats, float64(totalDur.Microseconds())/1000.0)
			claimExecLats = append(claimExecLats, float64(execDur.Microseconds())/1000.0)
			latMu.Unlock()
		}

		if len(claimed) == 0 {
			if len(sem) == 0 {
				if p, _ := st.promoteDueWakeups(ctx, time.Now().UnixMilli()); p > 0 {
					promotedTotal.Add(int64(p))
					continue
				}
				var remaining int
				if err := st.readDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM wakeups WHERE status IN ('queued','claimed')`).Scan(&remaining); err != nil {
					return drainStats{}, err
				}
				if remaining == 0 {
					break
				}
			}
			time.Sleep(2 * time.Millisecond)
			continue
		}

		for _, cr := range claimed {
			sem <- struct{}{}
			wg.Add(1)
			go func() {
				defer func() {
					<-sem
					wg.Done()
				}()
				time.Sleep(time.Duration(5+rand.IntN(46)) * time.Millisecond)

				_, fExec, err := st.finishRun(ctx, cr, cooldownMS, time.Now().UnixMilli())
				if err != nil {
					setErr(err)
					return
				}
				latMu.Lock()
				finishExecLats = append(finishExecLats, float64(fExec.Microseconds())/1000.0)
				latMu.Unlock()

				n := completedRuns.Add(1)
				if checkEvery > 0 && n%checkEvery == 0 {
					invariantChecks.Add(1)
					if err := checkInvariants(ctx, st, time.Now().UnixMilli()); err != nil {
						violations.Add(1)
						setErr(err)
					}
				}
			}()
		}
	}

	wg.Wait()
	if p := firstErr.Load(); p != nil {
		return drainStats{Violations: violations.Load()}, *p
	}
	invariantChecks.Add(1)
	if err := checkInvariants(ctx, st, time.Now().UnixMilli()); err != nil {
		violations.Add(1)
		return drainStats{Violations: violations.Load()}, err
	}

	cExecMean, _, cExecP95, _, _ := summarizeDurationsMS(claimExecLats)
	cTotalMean, _, _, _, _ := summarizeDurationsMS(claimTotalLats)
	fExecMean, _, fExecP95, _, _ := summarizeDurationsMS(finishExecLats)
	return drainStats{
		CompletedRuns:    completedRuns.Load(),
		InvariantChecks:  invariantChecks.Load(),
		Violations:       violations.Load(),
		PromotedAgents:   promotedTotal.Load(),
		ClaimExecMeanMS:  cExecMean,
		ClaimExecP95MS:   cExecP95,
		ClaimTotalMeanMS: cTotalMean,
		FinishExecMeanMS: fExecMean,
		FinishExecP95MS:  fExecP95,
	}, nil
}

// measureClaimScale benchmarks steady-state `claimWork` latency at fixed queue sizes `Q`
// (recycling claimed wakeups to the tail of `created_at` so `Q` stays strictly invariant across all samples).
func measureClaimScale(ctx context.Context, baseDir, schemaVariant string, qSizes []int, samples, scaleRuns int, isoBatch, forceIndexed bool) error {
	distName := "uniform-5k-agents"
	if isoBatch {
		distName = "iso-batch-160-rows/tx"
	}
	fmt.Printf("\n=== [Step 4] Claim Latency vs Queue Size Q (schema=%s, dist=%s, samples=%d, runs=%d, forceIndexed=%v) ===\n",
		schemaVariant, distName, samples, scaleRuns, forceIndexed)

	for runIdx := 1; runIdx <= scaleRuns; runIdx++ {
		p95ByQ := make(map[int]float64, len(qSizes))
		for _, q := range qSizes {
			dbDir := filepath.Join(baseDir, "tmp", fmt.Sprintf("scale_%s_%d_%d", distName[:3], runIdx, q))
			_ = os.RemoveAll(dbDir)
			if err := os.MkdirAll(dbDir, 0o700); err != nil {
				return err
			}
			dbFile := filepath.Join(dbDir, "scale.db")

			st, err := openStore(ctx, dbFile, schemaVariant)
			if err != nil {
				return err
			}
			tSeed := time.Now()
			nowMS := time.Now().UnixMilli()
			activeAgents := 5000
			convsPerAgent := 3
			if isoBatch {
				// Keep 5,000 defined agents in DB, concentrate active backlog so every Q claims ~160 wakeups/tx (20/agent * 8).
				convsPerAgent = 1
				activeAgents = min(5000, max(20, q/40))
			}
			if err := seedDataset(ctx, st, 10, 5000, activeAgents, q, convsPerAgent, 20, 0, nowMS); err != nil {
				_ = st.close()
				return err
			}
			_, _ = st.writeDB.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
			seedDur := time.Since(tSeed)

			var memBefore, memAfter runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&memBefore)

			latenciesMS := make([]float64, 0, samples)
			var totalWakeupsClaimed int64
			for i := range samples {
				stepNow := nowMS + int64(i*10)
				claimed, totalDur, _, err := st.claimWork(ctx, 8, 4, stepNow, forceIndexed)
				if err != nil {
					_ = st.close()
					return err
				}
				latenciesMS = append(latenciesMS, float64(totalDur.Microseconds())/1000.0)
				for _, cr := range claimed {
					totalWakeupsClaimed += int64(len(cr.WakeupIDs))
				}
				if err := st.finishAndRecycleBench(ctx, claimed, stepNow+1); err != nil {
					_ = st.close()
					return err
				}
				if (i+1)%100 == 0 {
					_ = st.checkpointPassive(ctx)
				}
			}
			runtime.ReadMemStats(&memAfter)
			_ = st.close()
			_ = os.RemoveAll(dbDir)

			_, p50, p95, p99, maxV := summarizeDurationsMS(latenciesMS)
			p95ByQ[q] = p95
			avgRows := float64(totalWakeupsClaimed) / float64(samples)
			gcPauseMS := float64(memAfter.PauseTotalNs-memBefore.PauseTotalNs) / 1e6
			fmt.Printf("  [Run %d/%d] Q=%-9d | seed=%5.2fs | avgWakeups/tx=%5.1f | p50=%6.3f ms | p95=%6.3f ms | p99=%6.3f ms | max=%6.3f ms | gcPause=%.2f ms\n",
				runIdx, scaleRuns, q, seedDur.Seconds(), avgRows, p50, p95, p99, maxV, gcPauseMS)

			if schemaVariant == "optimized" {
				if p95 >= 20.0 {
					return fmt.Errorf("run %d (%s) Q=%d failed p95 < 20 ms gate: got p95=%.3f ms", runIdx, distName, q, p95)
				}
				if p99 >= 50.0 {
					return fmt.Errorf("run %d (%s) Q=%d failed p99 < 50 ms gate: got p99=%.3f ms", runIdx, distName, q, p99)
				}
			}
		}

		if len(qSizes) >= 2 {
			qMin := qSizes[0]
			qMax := qSizes[len(qSizes)-1]
			ratio := p95ByQ[qMax] / max(p95ByQ[qMin], 0.001)
			delta := p95ByQ[qMax] - p95ByQ[qMin]
			fmt.Printf("  [Run %d/%d] Scaling Q=%d->%d: delta=+%.3f ms, ratio=%.2fx (p95_%d=%.3f ms, p95_%d=%.3f ms)\n",
				runIdx, scaleRuns, qMin, qMax, delta, ratio, qMax, p95ByQ[qMax], qMin, p95ByQ[qMin])
			if schemaVariant == "optimized" && delta >= 10.0 {
				return fmt.Errorf("run %d (%s) failed O(log Q) delta < 10 ms gate: p95(%d)=%.3f ms vs p95(%d)=%.3f ms (+%.3f ms)",
					runIdx, distName, qMax, p95ByQ[qMax], qMin, p95ByQ[qMin], delta)
			}
		}
	}
	return nil
}

func runCrashKillChaos(ctx context.Context, selfExe, baseDir, schemaVariant string, forceIndexed bool) error {
	fmt.Println("\n=== [Step 5] 20x Random OS Process Kill, Poison-Wakeup Cap & Spec 8.2.5 Recovery ===")
	tmpDir := filepath.Join(baseDir, "tmp", "chaos")
	_ = os.RemoveAll(tmpDir)
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return err
	}
	dbFile := filepath.Join(tmpDir, "chaos.db")

	st, err := openStore(ctx, dbFile, schemaVariant)
	if err != nil {
		return err
	}
	if err := seedDataset(ctx, st, 10, 5000, 5000, 20000, 2, 2, 5, time.Now().UnixMilli()); err != nil {
		_ = st.close()
		return err
	}
	_ = st.close()

	var totalInterrupted, totalRequeued, totalDropped int64
	for k := 1; k <= 20; k++ {
		args := []string{"-mode", "worker", "-db", dbFile, "-schema", schemaVariant}
		if forceIndexed {
			args = append(args, "-force-indexed=true")
		}
		cmd := exec.CommandContext(ctx, selfExe, args...)
		var stderrBuf bytes.Buffer
		cmd.Stderr = &stderrBuf
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("start worker #%d: %w", k, err)
		}

		var waitErr error
		waitCh := make(chan struct{})
		go func() {
			waitErr = cmd.Wait()
			close(waitCh)
		}()

		killDelay := time.Duration(40+rand.IntN(71)) * time.Millisecond
		select {
		case <-waitCh:
			if waitErr != nil {
				return fmt.Errorf("worker #%d crashed BEFORE kill: %v (stderr: %s)", k, waitErr, strings.TrimSpace(stderrBuf.String()))
			}
		case <-time.After(killDelay):
			_ = cmd.Process.Kill()
			<-waitCh
		}

		stCheck, err := openStore(ctx, dbFile, schemaVariant)
		if err != nil {
			return err
		}
		ir, rq, dr, err := stCheck.recoverOnBoot(ctx, time.Now().UnixMilli(), false)
		if err != nil {
			_ = stCheck.close()
			return fmt.Errorf("recover #%d: %w", k, err)
		}
		totalInterrupted += ir
		totalRequeued += rq
		totalDropped += dr
		if err := checkInvariants(ctx, stCheck, time.Now().UnixMilli()); err != nil {
			_ = stCheck.close()
			return fmt.Errorf("post-recovery #%d invariant failed: %w", k, err)
		}
		_ = stCheck.close()
	}

	stFinal, err := openStore(ctx, dbFile, schemaVariant)
	if err != nil {
		return err
	}
	defer func() {
		_ = stFinal.close()
		_ = os.RemoveAll(tmpDir)
	}()
	irLast, rqLast, drLast, err := stFinal.recoverOnBoot(ctx, time.Now().UnixMilli(), false)
	if err != nil {
		return err
	}
	totalInterrupted += irLast
	totalRequeued += rqLast
	totalDropped += drLast

	stats, err := drainQueueLoop(ctx, stFinal, 8, 10, 500, forceIndexed)
	if err != nil {
		return err
	}

	var doneCount, droppedCount, nonTerminalCount, badDropAttempts int
	if err := stFinal.readDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM wakeups WHERE status = 'done'`).Scan(&doneCount); err != nil {
		return err
	}
	if err := stFinal.readDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM wakeups WHERE status = 'dropped'`).Scan(&droppedCount); err != nil {
		return err
	}
	if err := stFinal.readDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM wakeups WHERE status NOT IN ('done','dropped')`).Scan(&nonTerminalCount); err != nil {
		return err
	}
	if err := stFinal.readDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM wakeups WHERE status = 'dropped' AND attempts < ?`, maxWakeupAttempts).Scan(&badDropAttempts); err != nil {
		return err
	}
	fmt.Printf("  Kills=20 | InterruptedRuns=%d | RequeuedWakeups=%d | DroppedPoisonWakeups=%d | FinalRuns=%d | InvariantChecks=%d | Done=%d | Dropped=%d | NonTerminal=%d\n",
		totalInterrupted, totalRequeued, totalDropped, stats.CompletedRuns, stats.InvariantChecks+20, doneCount, droppedCount, nonTerminalCount)
	if doneCount+droppedCount != 20000 || nonTerminalCount != 0 || badDropAttempts != 0 || int64(droppedCount) != totalDropped {
		return fmt.Errorf("chaos terminal invariant I3 failed: done=%d dropped=%d (tracked=%d, badAttempts=%d) nonTerminal=%d",
			doneCount, droppedCount, totalDropped, badDropAttempts, nonTerminalCount)
	}
	return nil
}
