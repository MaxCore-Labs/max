package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func checkInvariants(ctx context.Context, st *store, nowMS int64) error {
	// Read inside writeDB serialized boundary or readDB snapshot?
	// Using a read-only transaction on readDB gives a consistent point-in-time WAL snapshot!
	tx, err := st.readDB.BeginTx(ctx, &sqlTxReadOnly)
	if err != nil {
		return fmt.Errorf("begin invariant ro tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Invariant 1: No agent has > 1 running run, and active_run_id matches running run
	var dupRunning int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM (
			SELECT agent_id FROM runs WHERE status = 'running' GROUP BY agent_id HAVING COUNT(*) > 1
		)`).Scan(&dupRunning); err != nil {
		return err
	}
	if dupRunning > 0 {
		return fmt.Errorf("invariant violation: %d agent(s) have >1 running run", dupRunning)
	}

	var mismatchActive int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM runs r
		JOIN agents a ON a.id = r.agent_id
		WHERE r.status = 'running' AND COALESCE(a.active_run_id, '') <> r.id`).Scan(&mismatchActive); err != nil {
		return err
	}
	if mismatchActive > 0 {
		return fmt.Errorf("invariant violation: %d running run(s) mismatch agents.active_run_id", mismatchActive)
	}

	// Invariant 2: No due queued wakeup belongs to an agent with ready = 0
	var orphanQueued int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM wakeups w
		JOIN agents a ON a.id = w.agent_id
		WHERE w.status = 'queued' AND w.not_before <= ? AND a.ready = 0`, nowMS).Scan(&orphanQueued); err != nil {
		return err
	}
	if orphanQueued > 0 {
		return fmt.Errorf("invariant violation: %d queued wakeup(s) belong to agent with ready=0", orphanQueued)
	}

	// Invariant 3: teams.running equals exact count of running runs for that team, and <= max_running
	var teamMismatch int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM teams t
		LEFT JOIN (
			SELECT a.team_id, COUNT(*) AS c
			FROM runs r JOIN agents a ON a.id = r.agent_id
			WHERE r.status = 'running'
			GROUP BY a.team_id
		) x ON x.team_id = t.id
		WHERE t.running <> COALESCE(x.c, 0) OR t.running > t.max_running OR t.running < 0`).Scan(&teamMismatch); err != nil {
		return err
	}
	if teamMismatch > 0 {
		return fmt.Errorf("invariant violation: %d team(s) have inconsistent running count", teamMismatch)
	}
	return nil
}

func seedDataset(ctx context.Context, st *store, numTeams, numAgents, numWakeups, convsPerAgent int, maxRunningPerTeam int, nowMS int64) error {
	tx, err := st.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	teamIDs := make([]string, numTeams)
	for i := 0; i < numTeams; i++ {
		tid := fmt.Sprintf("team-%02d", i+1)
		teamIDs[i] = tid
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO teams (id, slug, name, max_running) VALUES (?, ?, ?, ?)`,
			tid, tid, fmt.Sprintf("Team %d", i+1), maxRunningPerTeam,
		); err != nil {
			return err
		}
	}

	agentIDs := make([]string, numAgents)
	convIDs := make([][]string, numAgents)
	for i := 0; i < numAgents; i++ {
		aid := fmt.Sprintf("agent-%05d", i+1)
		agentIDs[i] = aid
		tid := teamIDs[i%numTeams]
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO agents (id, slug, name, team_id, config_rev)
			VALUES (?, ?, ?, ?, 'rev-1')`,
			aid, aid, aid, tid,
		); err != nil {
			return err
		}
		convIDs[i] = make([]string, convsPerAgent)
		for c := 0; c < convsPerAgent; c++ {
			cid := fmt.Sprintf("conv-%05d-%d", i+1, c+1)
			convIDs[i][c] = cid
			tidRef := fmt.Sprintf("task-%05d-%d", i+1, c+1)
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO conversations (id, agent_id, task_id) VALUES (?, ?, ?)`,
				cid, aid, tidRef,
			); err != nil {
				return err
			}
		}
	}

	priorities := []int{100, 50, 30, 10}
	rng := rand.New(rand.NewPCG(42, 20261119))
	const batchSize = 500

	for offset := 0; offset < numWakeups; offset += batchSize {
		n := min(batchSize, numWakeups-offset)
		var sb strings.Builder
		sb.WriteString("INSERT INTO wakeups (id, agent_id, conversation_id, kind, priority, payload, status, not_before, created_at) VALUES ")
		args := make([]any, 0, n*7)
		for j := 0; j < n; j++ {
			if j > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString("(?,?,?,?,?,?,'queued',0,?)")
			aIdx := rng.IntN(numAgents)
			cIdx := rng.IntN(convsPerAgent)
			prio := priorities[rng.IntN(len(priorities))]
			wid := fmt.Sprintf("wake-%08d", offset+j+1)
			createdAt := nowMS - int64(numWakeups-(offset+j))
			args = append(args, wid, agentIDs[aIdx], convIDs[aIdx][cIdx], "message", prio, `{"ref":"m"}`, createdAt)
		}
		if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
			return fmt.Errorf("batch insert wakeups at %d: %w", offset, err)
		}
	}

	// Derive initial ready state for all agents in one set-based SQL statement
	if _, err := tx.ExecContext(ctx, `
		UPDATE agents
		SET
			ready = CASE WHEN EXISTS (
				SELECT 1 FROM wakeups w WHERE w.agent_id = agents.id AND w.status = 'queued' AND w.not_before <= ?
			) THEN 1 ELSE 0 END,
			ready_priority = COALESCE((
				SELECT w.priority FROM wakeups w
				WHERE w.agent_id = agents.id AND w.status = 'queued' AND w.not_before <= ?
				ORDER BY w.priority DESC, w.created_at LIMIT 1
			), 0),
			ready_since = (
				SELECT w.created_at FROM wakeups w
				WHERE w.agent_id = agents.id AND w.status = 'queued' AND w.not_before <= ?
				ORDER BY w.priority DESC, w.created_at LIMIT 1
			)`, nowMS, nowMS, nowMS); err != nil {
		return fmt.Errorf("init agent ready state: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `ANALYZE`); err != nil {
		return err
	}
	return tx.Commit()
}

func drainQueueLoop(ctx context.Context, st *store, totalSlots int, checkEvery int64, forceIndexed bool) (int64, int64, error) {
	sem := make(chan struct{}, totalSlots)
	var wg sync.WaitGroup
	var completedRuns atomic.Int64
	var invariantChecks atomic.Int64
	var firstErr atomic.Pointer[error]

	setErr := func(e error) {
		firstErr.CompareAndSwap(nil, &e)
	}

	for {
		if p := firstErr.Load(); p != nil {
			wg.Wait()
			return completedRuns.Load(), invariantChecks.Load(), *p
		}

		// Count free slots without blocking first
		free := totalSlots - len(sem)
		if free <= 0 {
			time.Sleep(1 * time.Millisecond)
			continue
		}

		nowMS := time.Now().UnixMilli()
		claimed, _, err := st.claimWork(ctx, free, 4, nowMS, forceIndexed)
		if err != nil {
			wg.Wait()
			return completedRuns.Load(), invariantChecks.Load(), err
		}

		if len(claimed) == 0 {
			if len(sem) == 0 {
				var remaining int
				if err := st.readDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM wakeups WHERE status IN ('queued','claimed')`).Scan(&remaining); err != nil {
					return completedRuns.Load(), invariantChecks.Load(), err
				}
				if remaining == 0 {
					break
				}
			}
			time.Sleep(2 * time.Millisecond)
			continue
		}

		for _, cr := range claimed {
			cr := cr
			sem <- struct{}{}
			wg.Add(1)
			go func() {
				defer func() {
					<-sem
					wg.Done()
				}()
				// Fake engine: sleep 5–50 ms
				sleepDur := time.Duration(5+rand.IntN(46)) * time.Millisecond
				time.Sleep(sleepDur)

				if err := st.finishRun(ctx, cr, 0, time.Now().UnixMilli()); err != nil {
					setErr(err)
					return
				}
				n := completedRuns.Add(1)
				if checkEvery > 0 && n%checkEvery == 0 {
					invariantChecks.Add(1)
					if err := checkInvariants(ctx, st, time.Now().UnixMilli()); err != nil {
						setErr(err)
					}
				}
			}()
		}
	}

	wg.Wait()
	if p := firstErr.Load(); p != nil {
		return completedRuns.Load(), invariantChecks.Load(), *p
	}
	invariantChecks.Add(1)
	if err := checkInvariants(ctx, st, time.Now().UnixMilli()); err != nil {
		return completedRuns.Load(), invariantChecks.Load(), err
	}
	return completedRuns.Load(), invariantChecks.Load(), nil
}

func measureClaimScale(ctx context.Context, baseDir string, qSizes []int, forceIndexed bool) error {
	fmt.Println("\n=== [Step 4] Claim Transaction Latency vs Queue Size Q ===")
	for _, q := range qSizes {
		dbFile := filepath.Join(baseDir, "tmp", fmt.Sprintf("scale_%d.db", q))
		_ = os.RemoveAll(filepath.Dir(dbFile))
		_ = os.MkdirAll(filepath.Dir(dbFile), 0o700)

		st, err := openStore(ctx, dbFile)
		if err != nil {
			return err
		}
		tSeed := time.Now()
		nowMS := time.Now().UnixMilli()
		if err := seedDataset(ctx, st, 10, 5000, q, 3, 20, nowMS); err != nil {
			_ = st.close()
			return err
		}
		// Flush bulk-seed WAL frames before benchmarking steady-state claim latency
		_, _ = st.writeDB.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
		var jMode string
		_ = st.writeDB.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&jMode)
		seedDur := time.Since(tSeed)

		const samples = 100
		latenciesMS := make([]float64, 0, samples)
		for i := 0; i < samples; i++ {
			claimed, dur, err := st.claimWork(ctx, 8, 4, time.Now().UnixMilli(), forceIndexed)
			if err != nil {
				_ = st.close()
				return err
			}
			latenciesMS = append(latenciesMS, float64(dur.Microseconds())/1000.0)
			// Immediately finish claimed runs outside measurement so slots/teams stay open across 100 samples
			for _, cr := range claimed {
				if err := st.finishRun(ctx, cr, 0, time.Now().UnixMilli()); err != nil {
					_ = st.close()
					return err
				}
			}
		}
		_ = st.close()
		_ = os.RemoveAll(filepath.Dir(dbFile))

		slices.Sort(latenciesMS)
		p50 := latenciesMS[len(latenciesMS)*50/100]
		p95 := latenciesMS[len(latenciesMS)*95/100]
		p99 := latenciesMS[len(latenciesMS)*99/100]
		fmt.Printf("  Q=%-9d (mode=%s) | seed=%6.2fs | samples=%d (8 agents/tx) | p50=%.3f ms | p95=%.3f ms | p99=%.3f ms\n",
			q, jMode, seedDur.Seconds(), samples, p50, p95, p99)
	}
	return nil
}

func runCrashKillChaos(ctx context.Context, selfExe, baseDir string, forceIndexed bool) error {
	fmt.Println("\n=== [Step 5] 20x Random OS Process Kill & Spec 8.2.5 Recovery ===")
	tmpDir := filepath.Join(baseDir, "tmp", "chaos")
	_ = os.RemoveAll(tmpDir)
	_ = os.MkdirAll(tmpDir, 0o700)
	dbFile := filepath.Join(tmpDir, "chaos.db")

	st, err := openStore(ctx, dbFile)
	if err != nil {
		return err
	}
	// Seed 5,000 agents and 20,000 wakeups so 20 kills happen mid-flight and final drain finishes fast
	if err := seedDataset(ctx, st, 10, 5000, 20000, 2, 2, time.Now().UnixMilli()); err != nil {
		_ = st.close()
		return err
	}
	_ = st.close()

	var totalInterrupted, totalRequeued int64
	for k := 1; k <= 20; k++ {
		args := []string{"-mode", "worker", "-db", dbFile}
		if forceIndexed {
			args = append(args, "-force-indexed=true")
		}
		cmd := exec.CommandContext(ctx, selfExe, args...)
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("start worker #%d: %w", k, err)
		}
		// Let worker claim & run goroutines for 40–110 ms, then hard-kill OS process!
		time.Sleep(time.Duration(40+rand.IntN(71)) * time.Millisecond)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()

		// Open DB, run Spec 8.2.5 recovery, and immediately verify all 3 invariants!
		stCheck, err := openStore(ctx, dbFile)
		if err != nil {
			return err
		}
		ir, rq, err := stCheck.recoverOnBoot(ctx, time.Now().UnixMilli())
		if err != nil {
			_ = stCheck.close()
			return fmt.Errorf("recover #%d: %w", k, err)
		}
		totalInterrupted += ir
		totalRequeued += rq
		if err := checkInvariants(ctx, stCheck, time.Now().UnixMilli()); err != nil {
			_ = stCheck.close()
			return fmt.Errorf("post-recovery #%d invariant failed: %w", k, err)
		}
		_ = stCheck.close()
	}

	// Final boot #21: drain all remaining wakeups to completion
	stFinal, err := openStore(ctx, dbFile)
	if err != nil {
		return err
	}
	defer func() {
		_ = stFinal.close()
		_ = os.RemoveAll(tmpDir)
	}()
	if _, _, err := stFinal.recoverOnBoot(ctx, time.Now().UnixMilli()); err != nil {
		return err
	}
	runsLeft, checks, err := drainQueueLoop(ctx, stFinal, 8, 500, forceIndexed)
	if err != nil {
		return err
	}

	var doneCount, nonDoneCount int
	if err := stFinal.readDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM wakeups WHERE status = 'done'`).Scan(&doneCount); err != nil {
		return err
	}
	if err := stFinal.readDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM wakeups WHERE status <> 'done'`).Scan(&nonDoneCount); err != nil {
		return err
	}
	fmt.Printf("  Kills=20 | InterruptedRunsRecovered=%d | RequeuedWakeups=%d | FinalRuns=%d | InvariantChecks=%d | Done=%d | NonDone=%d\n",
		totalInterrupted, totalRequeued, runsLeft, checks+20, doneCount, nonDoneCount)
	if doneCount != 20000 || nonDoneCount != 0 {
		return fmt.Errorf("expected done=20000 nonDone=0, got done=%d nonDone=%d", doneCount, nonDoneCount)
	}
	return nil
}
