package main

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand/v2"
	"strings"
)

var sqlTxReadOnly = sql.TxOptions{ReadOnly: true}

// checkInvariants verifies 5 actor/mailbox invariants on a consistent readDB WAL snapshot.
func checkInvariants(ctx context.Context, st *store, nowMS int64) error {
	tx, err := st.readDB.BeginTx(ctx, &sqlTxReadOnly)
	if err != nil {
		return fmt.Errorf("begin invariant ro tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Invariant 1a: No agent has > 1 running run.
	var dupRunning int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM (
			SELECT agent_id FROM runs
			WHERE status = 'running' AND ended_at IS NULL
			GROUP BY agent_id HAVING COUNT(*) > 1
		)`).Scan(&dupRunning); err != nil {
		return err
	}
	if dupRunning > 0 {
		return fmt.Errorf("invariant 1a: %d agent(s) have >1 running run", dupRunning)
	}

	// Invariant 1b: Bijection between runs(status='running') and agents.active_run_id.
	var mismatchActive int
	if err := tx.QueryRowContext(ctx, `
		SELECT (
			SELECT COUNT(*) FROM runs r
			JOIN agents a ON a.id = r.agent_id
			WHERE r.status = 'running' AND r.ended_at IS NULL AND COALESCE(a.active_run_id, '') <> r.id
		) + (
			SELECT COUNT(*) FROM agents a
			LEFT JOIN runs r ON r.id = a.active_run_id AND r.status = 'running' AND r.ended_at IS NULL
			WHERE a.active_run_id IS NOT NULL AND (r.id IS NULL OR r.agent_id <> a.id)
		)`).Scan(&mismatchActive); err != nil {
		return err
	}
	if mismatchActive > 0 {
		return fmt.Errorf("invariant 1b: %d mismatch(es) between running runs and agents.active_run_id", mismatchActive)
	}

	// Invariant 2: No immediately-enqueued (`not_before = 0`) queued wakeup belongs to an agent with ready = 0,
	// and no agent has ready = 1 without at least one due queued wakeup (`not_before <= nowMS`).
	// (Delayed wakeups `not_before > 0` are promoted asynchronously by `promoteDueWakeups` scheduler ticks.)
	var readyMismatch int
	if err := tx.QueryRowContext(ctx, `
		SELECT (
			SELECT COUNT(*) FROM wakeups w
			JOIN agents a ON a.id = w.agent_id
			WHERE w.status = 'queued' AND w.not_before = 0 AND a.ready = 0
		) + (
			SELECT COUNT(*) FROM agents a
			WHERE a.ready = 1 AND NOT EXISTS (
				SELECT 1 FROM wakeups w
				WHERE w.agent_id = a.id AND w.status = 'queued' AND w.not_before <= ?
			)
		)`, nowMS).Scan(&readyMismatch); err != nil {
		return err
	}
	if readyMismatch > 0 {
		return fmt.Errorf("invariant 2: %d agent ready-state mismatch(es)", readyMismatch)
	}

	// Invariant 3: teams.running equals exact count of running runs for that team, and 0 <= running <= max_running.
	var teamMismatch int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM teams t
		LEFT JOIN (
			SELECT a.team_id, COUNT(*) AS c
			FROM runs r JOIN agents a ON a.id = r.agent_id
			WHERE r.status = 'running' AND r.ended_at IS NULL
			GROUP BY a.team_id
		) x ON x.team_id = t.id
		WHERE t.running <> COALESCE(x.c, 0) OR t.running > t.max_running OR t.running < 0`).Scan(&teamMismatch); err != nil {
		return err
	}
	if teamMismatch > 0 {
		return fmt.Errorf("invariant 3: %d team(s) have inconsistent running count", teamMismatch)
	}

	// Invariant 4: Every `claimed` wakeup belongs to a `running` run of the same agent, and every `running` run owns >= 1 `claimed` wakeup.
	var claimedMismatch int
	if err := tx.QueryRowContext(ctx, `
		SELECT (
			SELECT COUNT(*) FROM wakeups w
			LEFT JOIN runs r ON r.id = w.run_id AND r.status = 'running' AND r.ended_at IS NULL
			WHERE w.status = 'claimed' AND (w.run_id IS NULL OR r.id IS NULL OR r.agent_id <> w.agent_id)
		) + (
			SELECT COUNT(*) FROM runs r
			WHERE r.status = 'running' AND r.ended_at IS NULL AND NOT EXISTS (
				SELECT 1 FROM wakeups w WHERE w.run_id = r.id AND w.status = 'claimed' AND w.agent_id = r.agent_id
			)
		)`).Scan(&claimedMismatch); err != nil {
		return err
	}
	if claimedMismatch > 0 {
		return fmt.Errorf("invariant 4: %d claimed-wakeup <-> running-run mismatch(es)", claimedMismatch)
	}

	// Invariant 5: No `queued` or `dropped` wakeup retains a non-null `run_id`, and no `done` wakeup has a null `run_id`.
	var staleRunRef int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM wakeups
		WHERE (status IN ('queued','dropped') AND run_id IS NOT NULL)
		   OR (status = 'done' AND run_id IS NULL)`).Scan(&staleRunRef); err != nil {
		return err
	}
	if staleRunRef > 0 {
		return fmt.Errorf("invariant 5: %d wakeup(s) have invalid run_id for their status", staleRunRef)
	}
	return nil
}

func seedDataset(ctx context.Context, st *store, numTeams, numAgents, activeAgents, numWakeups, convsPerAgent, maxRunningPerTeam, delayedPct int, nowMS int64) error {
	if activeAgents <= 0 || activeAgents > numAgents {
		activeAgents = numAgents
	}
	tx, err := st.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	teamIDs := make([]string, numTeams)
	for i := range numTeams {
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
	for i := range numAgents {
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
		for c := range convsPerAgent {
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
		args := make([]any, 0, n*8)
		for j := range n {
			if j > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString("(?,?,?,?,?,?,'queued',?,?)")
			aIdx := rng.IntN(activeAgents)
			cIdx := rng.IntN(convsPerAgent)
			prio := priorities[rng.IntN(len(priorities))]
			wid := fmt.Sprintf("wake-%08d", offset+j+1)
			createdAt := nowMS - int64(numWakeups-(offset+j))
			notBefore := int64(0)
			if delayedPct > 0 && rng.IntN(100) < delayedPct {
				notBefore = nowMS + int64(25+rng.IntN(120))
			}
			args = append(args, wid, agentIDs[aIdx], convIDs[aIdx][cIdx], "message", prio, `{"ref":"m"}`, notBefore, createdAt)
		}
		if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
			return fmt.Errorf("batch insert wakeups at %d: %w", offset, err)
		}
	}

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
