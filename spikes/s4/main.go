package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func printExplainPlans(ctx context.Context, st *store, schemaVariant string) error {
	fmt.Printf("=== [Step 6] EXPLAIN QUERY PLAN Inspection (schema=%s) ===\n", schemaVariant)
	plans := []struct {
		name  string
		query string
		args  []any
	}{
		{
			name:  "1a. Candidate Agents (natural JOIN — default)",
			query: "EXPLAIN QUERY PLAN " + candidateSQLJoin,
			args:  []any{int64(1700000000000), 4, 24},
		},
		{
			name: "2. Agent Mailbox Single-Pass Scan (top + same-conv + nextReady)",
			query: `EXPLAIN QUERY PLAN SELECT id, conversation_id, priority, attempts, created_at
				FROM wakeups
				WHERE agent_id = ? AND status = 'queued' AND not_before <= ?
				ORDER BY priority DESC, created_at`,
			args: []any{"agent-00001", int64(1700000000000)},
		},
		{
			name: "3. Recompute Agent Ready",
			query: `EXPLAIN QUERY PLAN SELECT priority, created_at
				FROM wakeups
				WHERE agent_id = ? AND status = 'queued' AND not_before <= ?
				ORDER BY priority DESC, created_at LIMIT 1`,
			args: []any{"agent-00001", int64(1700000000000)},
		},
		{
			name: "4. Scheduler Promote Delayed Wakeups (`not_before > 0`)",
			query: `EXPLAIN QUERY PLAN SELECT DISTINCT w.agent_id
				FROM wakeups w JOIN agents a ON a.id = w.agent_id
				WHERE w.status = 'queued' AND w.not_before > 0 AND w.not_before <= ?
				  AND (a.ready = 0 OR a.ready_priority < w.priority)`,
			args: []any{int64(1700000000000)},
		},
		{
			name: "5. FinishRun Complete Claimed Wakeups",
			query: `EXPLAIN QUERY PLAN UPDATE wakeups SET status = 'done'
				WHERE run_id = ? AND status = 'claimed'`,
			args: []any{"run-00001"},
		},
		{
			name: "6. Boot Recovery Mark Interrupted Runs (exact UPDATE)",
			query: `EXPLAIN QUERY PLAN UPDATE runs SET status = 'interrupted', ended_at = ?
				WHERE status = 'running' AND ended_at IS NULL`,
			args: []any{int64(1700000000000)},
		},
	}
	if schemaVariant != "baseline" {
		plans = append(plans[:1], append([]struct {
			name  string
			query string
			args  []any
		}{{
			name:  "1b. Candidate Agents (INDEXED BY agents_ready)",
			query: "EXPLAIN QUERY PLAN " + candidateSQLIndexed,
			args:  []any{int64(1700000000000), 4, 24},
		}}, plans[1:]...)...)
	}

	for _, p := range plans {
		fmt.Printf("  [%s]\n", p.name)
		rows, err := st.readDB.QueryContext(ctx, p.query, p.args...)
		if err != nil {
			return fmt.Errorf("explain %s: %w", p.name, err)
		}
		for rows.Next() {
			var id, parent, notused int
			var detail string
			if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
				_ = rows.Close()
				return err
			}
			fmt.Printf("    |-- (%d<-%d) %s\n", id, parent, detail)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	return nil
}

func runWorkerMode(ctx context.Context, dbPath, schemaVariant string, forceIndexed bool) error {
	st, err := openStore(ctx, dbPath, schemaVariant)
	if err != nil {
		return err
	}
	defer func() { _ = st.close() }()

	if _, _, _, err := st.recoverOnBoot(ctx, time.Now().UnixMilli(), false); err != nil {
		return err
	}
	_, err = drainQueueLoop(ctx, st, 8, 10, 500, forceIndexed)
	return err
}

func main() {
	mode := flag.String("mode", "all", "all | scale-only | explain-only | chaos-only | worker")
	dbFlag := flag.String("db", "", "sqlite db path for worker mode")
	schemaFlag := flag.String("schema", "optimized", "optimized | baseline")
	distFlag := flag.String("dist", "both", "uniform | iso | both (for Step 4)")
	forceIndexed := flag.Bool("force-indexed", false, "use INDEXED BY agents_ready instead of natural JOIN + ANALYZE")
	samplesFlag := flag.Int("samples", 1000, "number of claim transactions per Q in Step 4")
	scaleRunsFlag := flag.Int("scale-runs", 1, "number of full repetitions of Step 4")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()

	if *mode == "worker" {
		if err := runWorkerMode(ctx, *dbFlag, *schemaFlag, *forceIndexed); err != nil {
			fmt.Fprintf(os.Stderr, "worker error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	baseDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "getwd: %v\n", err)
		os.Exit(1)
	}
	runScale := func() error {
		if *distFlag == "iso" || *distFlag == "both" {
			if err := measureClaimScale(ctx, baseDir, *schemaFlag, []int{1000, 100000, 1000000}, *samplesFlag, *scaleRunsFlag, true, *forceIndexed); err != nil {
				return err
			}
		}
		if *distFlag == "uniform" || *distFlag == "both" {
			if err := measureClaimScale(ctx, baseDir, *schemaFlag, []int{1000, 100000, 1000000}, *samplesFlag, *scaleRunsFlag, false, *forceIndexed); err != nil {
				return err
			}
		}
		return nil
	}
	if *mode == "scale-only" {
		if err := runScale(); err != nil {
			fmt.Fprintf(os.Stderr, "FAIL Step 4: %v\n", err)
			os.Exit(1)
		}
		return
	}

	selfExe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "executable: %v\n", err)
		os.Exit(1)
	}
	if *mode == "chaos-only" {
		if err := runCrashKillChaos(ctx, selfExe, baseDir, *schemaFlag, *forceIndexed); err != nil {
			fmt.Fprintf(os.Stderr, "FAIL Step 5: %v\n", err)
			os.Exit(1)
		}
		return
	}

	fmt.Printf("=== [Steps 1–3] 5,000 Agents (10 Teams) + 100,000 Wakeups Drain & Invariants (schema=%s, forceIndexed=%v) ===\n",
		*schemaFlag, *forceIndexed)
	mainTmp := filepath.Join(baseDir, "tmp", "main100k")
	_ = os.RemoveAll(mainTmp)
	if err := os.MkdirAll(mainTmp, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir tmp: %v\n", err)
		os.Exit(1)
	}
	db100k := filepath.Join(mainTmp, "s4_100k.db")

	st, err := openStore(ctx, db100k, *schemaFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open store: %v\n", err)
		os.Exit(1)
	}
	t0 := time.Now()
	// Seed 100k wakeups with 5% delayed wakeups (`not_before > now`) and run with 15ms scaled cooldown.
	if err := seedDataset(ctx, st, 10, 5000, 5000, 100000, 2, 2, 5, time.Now().UnixMilli()); err != nil {
		_ = st.close()
		fmt.Fprintf(os.Stderr, "seed 100k: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  Seeded 10 teams, 5,000 agents, 10,000 conversations, 100,000 wakeups (5%% delayed) in %.2fs\n", time.Since(t0).Seconds())

	if err := printExplainPlans(ctx, st, *schemaFlag); err != nil {
		_ = st.close()
		fmt.Fprintf(os.Stderr, "explain: %v\n", err)
		os.Exit(1)
	}
	if *mode == "explain-only" {
		_ = st.close()
		_ = os.RemoveAll(mainTmp)
		return
	}

	tDrain := time.Now()
	stats, err := drainQueueLoop(ctx, st, 8, 15, 1000, *forceIndexed)
	drainDur := time.Since(tDrain)
	var doneWakeups int
	countErr := st.readDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM wakeups WHERE status = 'done'`).Scan(&doneWakeups)
	_ = st.close()
	_ = os.RemoveAll(mainTmp)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL Steps 2-3: %v\n", err)
		os.Exit(1)
	}
	if countErr != nil || doneWakeups != 100000 {
		fmt.Fprintf(os.Stderr, "FAIL Steps 2-3 count: done=%d err=%v\n", doneWakeups, countErr)
		os.Exit(1)
	}
	fmt.Printf("  Drained %d wakeups across %d runs in %.2fs (promotedDelayedAgents=%d)\n",
		doneWakeups, stats.CompletedRuns, drainDur.Seconds(), stats.PromotedAgents)
	fmt.Printf("  Measured DB tx latency under 8-worker load -> claimWork exec: mean=%.3f ms, p95=%.3f ms (incl. lock-wait mean=%.3f ms) | finishRun exec: mean=%.3f ms, p95=%.3f ms\n",
		stats.ClaimExecMeanMS, stats.ClaimExecP95MS, stats.ClaimTotalMeanMS, stats.FinishExecMeanMS, stats.FinishExecP95MS)
	fmt.Printf("  Invariant checks passed: %d (%d violations)\n", stats.InvariantChecks, stats.Violations)

	if err := runScale(); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL Step 4: %v\n", err)
		os.Exit(1)
	}

	if err := runCrashKillChaos(ctx, selfExe, baseDir, *schemaFlag, *forceIndexed); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL Step 5: %v\n", err)
		os.Exit(1)
	}

	_ = os.RemoveAll(filepath.Join(baseDir, "tmp"))
	fmt.Println("\n=== ALL S4 BENCHMARKS & CHAOS TESTS PASSED ===")
}
