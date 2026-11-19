package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

var sqlTxReadOnly = sql.TxOptions{ReadOnly: true}

func printExplainPlans(ctx context.Context, st *store) error {
	fmt.Println("=== [Step 6] EXPLAIN QUERY PLAN Inspection ===")
	plans := []struct {
		name  string
		query string
		args  []any
	}{
		{
			name:  "1a. Candidate Agents (natural JOIN)",
			query: "EXPLAIN QUERY PLAN " + candidateSQLJoin,
			args:  []any{int64(1700000000000), 4, 8},
		},
		{
			name:  "1b. Candidate Agents (INDEXED BY agents_ready)",
			query: "EXPLAIN QUERY PLAN " + candidateSQLIndexed,
			args:  []any{int64(1700000000000), 4, 8},
		},
		{
			name: "2. Agent Mailbox Top Wakeup",
			query: `EXPLAIN QUERY PLAN SELECT id, conversation_id, priority, attempts
				FROM wakeups
				WHERE agent_id = ? AND status = 'queued' AND not_before <= ?
				ORDER BY priority DESC, created_at LIMIT 1`,
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
			name: "4. FinishRun Complete Claimed Wakeups",
			query: `EXPLAIN QUERY PLAN UPDATE wakeups SET status = 'done'
				WHERE run_id = ? AND status = 'claimed'`,
			args: []any{"run-00001"},
		},
		{
			name:  "5. Boot Recovery Running Runs",
			query: `EXPLAIN QUERY PLAN SELECT id FROM runs WHERE status = 'running' AND ended_at IS NULL`,
			args:  nil,
		},
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
		_ = rows.Close()
	}
	return nil
}

func runWorkerMode(ctx context.Context, dbPath string, forceIndexed bool) error {
	st, err := openStore(ctx, dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = st.close() }()

	if _, _, err := st.recoverOnBoot(ctx, time.Now().UnixMilli()); err != nil {
		return err
	}
	_, _, err = drainQueueLoop(ctx, st, 8, 500, forceIndexed)
	return err
}

func main() {
	mode := flag.String("mode", "all", "all | worker | explain-only")
	dbFlag := flag.String("db", "", "sqlite db path for worker mode")
	forceIndexed := flag.Bool("force-indexed", true, "use INDEXED BY agents_ready on candidate query")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if *mode == "worker" {
		if err := runWorkerMode(ctx, *dbFlag, *forceIndexed); err != nil {
			os.Exit(1)
		}
		return
	}

	baseDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "getwd: %v\n", err)
		os.Exit(1)
	}
	if *mode == "scale-only" {
		if err := measureClaimScale(ctx, baseDir, []int{1000, 100000, 1000000}, *forceIndexed); err != nil {
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

	// Steps 1, 2, 3, and 6 (EXPLAIN QUERY PLAN) on 5,000 agents + 100,000 wakeups
	fmt.Println("=== [Steps 1–3] 5,000 Agents (10 Teams) + 100,000 Wakeups Drain & Invariants ===")
	mainTmp := filepath.Join(baseDir, "tmp", "main100k")
	_ = os.RemoveAll(mainTmp)
	_ = os.MkdirAll(mainTmp, 0o700)
	db100k := filepath.Join(mainTmp, "s4_100k.db")

	st, err := openStore(ctx, db100k)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open store: %v\n", err)
		os.Exit(1)
	}
	t0 := time.Now()
	if err := seedDataset(ctx, st, 10, 5000, 100000, 2, 2, time.Now().UnixMilli()); err != nil {
		_ = st.close()
		fmt.Fprintf(os.Stderr, "seed 100k: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  Seeded 10 teams, 5,000 agents, 10,000 conversations, 100,000 wakeups in %.2fs\n", time.Since(t0).Seconds())

	if err := printExplainPlans(ctx, st); err != nil {
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
	totalRuns, invChecks, err := drainQueueLoop(ctx, st, 8, 1000, *forceIndexed)
	drainDur := time.Since(tDrain)
	var doneWakeups int
	_ = st.readDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM wakeups WHERE status = 'done'`).Scan(&doneWakeups)
	_ = st.close()
	_ = os.RemoveAll(mainTmp)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL Steps 2-3: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  Drained %d wakeups across %d runs in %.2fs | Invariant checks passed: %d (0 violations)\n",
		doneWakeups, totalRuns, drainDur.Seconds(), invChecks)

	// Step 4: Q = 1,000 / 100,000 / 1,000,000 claim transaction p50/p95
	if err := measureClaimScale(ctx, baseDir, []int{1000, 100000, 1000000}, *forceIndexed); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL Step 4: %v\n", err)
		os.Exit(1)
	}

	// Step 5: 20 random OS process kills + Spec 8.2.5 recovery
	if err := runCrashKillChaos(ctx, selfExe, baseDir, *forceIndexed); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL Step 5: %v\n", err)
		os.Exit(1)
	}

	_ = os.RemoveAll(filepath.Join(baseDir, "tmp"))
	fmt.Println("\n=== ALL S4 BENCHMARKS & CHAOS TESTS PASSED ===")
}
