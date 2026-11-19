package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type wakeInput struct {
	AgentID        string
	ConversationID string // optional; empty inserts NULL
	Kind           string
	Priority       int
	Payload        string
	IdempotencyKey string // optional
	NotBeforeMS    int64
	CreatedAtMS    int64
}

func isCoalescibleKind(kind string) bool {
	switch kind {
	case "message", "task_update", "heartbeat", "routine":
		return true
	default:
		return false
	}
}

// wake implements Spec 8.2.1 & 8.2.3 durable enqueue with idempotency, coalescing, and ready recomputation.
func (s *store) wake(ctx context.Context, in wakeInput) (string, bool, error) {
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback() }()

	if in.IdempotencyKey != "" {
		var existingID string
		err := tx.QueryRowContext(ctx, `
			SELECT id FROM wakeups
			WHERE idempotency_key = ? AND status IN ('queued','claimed')
			LIMIT 1`, in.IdempotencyKey,
		).Scan(&existingID)
		if err == nil {
			return existingID, true, nil
		}
		if err != sql.ErrNoRows {
			return "", false, err
		}
	}

	if isCoalescibleKind(in.Kind) && in.ConversationID != "" {
		var existingID string
		err := tx.QueryRowContext(ctx, `
			SELECT id FROM wakeups
			WHERE agent_id = ? AND status = 'queued' AND kind = ? AND conversation_id = ?
			LIMIT 1`, in.AgentID, in.Kind, in.ConversationID,
		).Scan(&existingID)
		if err == nil {
			if _, err := tx.ExecContext(ctx, `
				UPDATE wakeups
				SET coalesced_count = coalesced_count + 1,
				    priority = MAX(priority, ?),
				    not_before = MIN(not_before, ?)
				WHERE agent_id = ? AND id = ?`,
				in.Priority, in.NotBeforeMS, in.AgentID, existingID,
			); err != nil {
				return "", false, err
			}
			if err := s.recomputeAgentReadyTx(ctx, tx, in.AgentID, in.CreatedAtMS); err != nil {
				return "", false, err
			}
			if err := tx.Commit(); err != nil {
				return "", false, err
			}
			return existingID, true, nil
		}
		if err != sql.ErrNoRows {
			return "", false, err
		}
	}

	wid := newID()
	var convArg, idemArg any
	if in.ConversationID != "" {
		convArg = in.ConversationID
	}
	if in.IdempotencyKey != "" {
		idemArg = in.IdempotencyKey
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO wakeups (id, agent_id, conversation_id, kind, priority, payload, idempotency_key, status, not_before, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'queued', ?, ?)`,
		wid, in.AgentID, convArg, in.Kind, in.Priority, in.Payload, idemArg, in.NotBeforeMS, in.CreatedAtMS,
	); err != nil {
		return "", false, err
	}
	if err := s.recomputeAgentReadyTx(ctx, tx, in.AgentID, in.CreatedAtMS); err != nil {
		return "", false, err
	}
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	return wid, false, nil
}

// promoteDueWakeups scans `wakeups_due` (`WHERE status = 'queued' AND not_before > 0 AND not_before <= ?`)
// and recomputes `ready` for agents whose delayed wakeups have matured.
func (s *store) promoteDueWakeups(ctx context.Context, nowMS int64) (int, error) {
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `
		SELECT DISTINCT w.agent_id
		FROM wakeups w
		JOIN agents a ON a.id = w.agent_id
		WHERE w.status = 'queued' AND w.not_before > 0 AND w.not_before <= ?
		  AND (a.ready = 0 OR a.ready_priority < w.priority)`, nowMS)
	if err != nil {
		return 0, err
	}
	var agentIDs []string
	for rows.Next() {
		var aid string
		if err := rows.Scan(&aid); err != nil {
			_ = rows.Close()
			return 0, err
		}
		agentIDs = append(agentIDs, aid)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if len(agentIDs) == 0 {
		return 0, nil
	}
	for _, aid := range agentIDs {
		if err := s.recomputeAgentReadyTx(ctx, tx, aid, nowMS); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(agentIDs), nil
}

func (s *store) finishRun(ctx context.Context, cr claimedRun, cooldownMS int64, nowMS int64) (time.Duration, time.Duration, error) {
	tCall := time.Now()
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	tExec := time.Now()
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.StmtContext(ctx, s.stmtFinishRun).ExecContext(ctx, nowMS, cr.RunID); err != nil {
		return 0, 0, err
	}
	if _, err := tx.StmtContext(ctx, s.stmtFinishWakeups).ExecContext(ctx, cr.RunID); err != nil {
		return 0, 0, err
	}
	// Spec 8.2.4: interactive priority (>= 100) is exempt from cooldown.
	cdUntil := int64(0)
	if cr.Priority < 100 && cooldownMS > 0 {
		cdUntil = nowMS + cooldownMS
	}
	if _, err := tx.StmtContext(ctx, s.stmtFinishAgent).ExecContext(ctx, nowMS, cdUntil, cr.AgentID); err != nil {
		return 0, 0, err
	}
	if _, err := tx.StmtContext(ctx, s.stmtTeamDec).ExecContext(ctx, cr.TeamID); err != nil {
		return 0, 0, err
	}
	if err := s.recomputeAgentReadyTx(ctx, tx, cr.AgentID, nowMS); err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return time.Since(tCall), time.Since(tExec), nil
}

// finishAndRecycleBench completes the claimed runs while recycling their wakeups to the tail of `created_at`
// so queue size `Q` stays strictly constant across 1,000+ benchmark samples without pinning the same 8 agents.
func (s *store) finishAndRecycleBench(ctx context.Context, claimed []claimedRun, nowMS int64) error {
	if len(claimed) == 0 {
		return nil
	}
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for idx, cr := range claimed {
		ts := nowMS + int64(idx)
		if _, err := tx.StmtContext(ctx, s.stmtFinishRun).ExecContext(ctx, ts, cr.RunID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE wakeups
			SET status = 'queued', run_id = NULL, attempts = 0, created_at = ?
			WHERE run_id = ? AND status = 'claimed'`, ts, cr.RunID,
		); err != nil {
			return err
		}
		if _, err := tx.StmtContext(ctx, s.stmtFinishAgent).ExecContext(ctx, ts, int64(0), cr.AgentID); err != nil {
			return err
		}
		if _, err := tx.StmtContext(ctx, s.stmtTeamDec).ExecContext(ctx, cr.TeamID); err != nil {
			return err
		}
		if err := s.recomputeAgentReadyTx(ctx, tx, cr.AgentID, ts); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *store) checkpointPassive(ctx context.Context) error {
	_, err := s.writeDB.ExecContext(ctx, `PRAGMA wal_checkpoint(PASSIVE)`)
	return err
}

// recoverOnBoot implements Spec 8.2.5 startup recovery + Spec 8.2.3 poison-wakeup protection:
// - If `graceful == true` (SIGTERM shutdown, Spec 8.15): rolls back `attempts - 1` so intentional restarts never drop healthy work.
// - If `graceful == false` (crash recovery, Spec 8.2.5): counts the interrupted attempt; wakeups with `attempts >= 3` become `'dropped'`.
func (s *store) recoverOnBoot(ctx context.Context, nowMS int64, graceful bool) (interruptedRuns, requeuedWakeups, droppedWakeups int64, err error) {
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()

	// Collect affected agents BEFORE clearing runs/wakeups; uses `runs_active` partial index (`ended_at IS NULL`).
	rows, err := tx.QueryContext(ctx, `
		SELECT agent_id FROM runs WHERE status = 'running' AND ended_at IS NULL
		UNION
		SELECT DISTINCT agent_id FROM wakeups WHERE status = 'claimed'
		UNION
		SELECT id FROM agents WHERE active_run_id IS NOT NULL`)
	if err != nil {
		return 0, 0, 0, err
	}
	var agentIDs []string
	for rows.Next() {
		var aid string
		if err := rows.Scan(&aid); err != nil {
			_ = rows.Close()
			return 0, 0, 0, err
		}
		agentIDs = append(agentIDs, aid)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, 0, 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, 0, 0, err
	}

	// `AND ended_at IS NULL` ensures SQLite uses `runs_active` index instead of `SCAN runs`.
	resRuns, err := tx.ExecContext(ctx, `
		UPDATE runs SET status = 'interrupted', ended_at = ?
		WHERE status = 'running' AND ended_at IS NULL`, nowMS)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("mark interrupted runs: %w", err)
	}
	interruptedRuns, _ = resRuns.RowsAffected()

	if graceful {
		resWake, err := tx.ExecContext(ctx, `
			UPDATE wakeups
			SET status = 'queued', run_id = NULL,
			    attempts = CASE WHEN attempts > 0 THEN attempts - 1 ELSE 0 END
			WHERE status = 'claimed'`)
		if err != nil {
			return 0, 0, 0, err
		}
		requeuedWakeups, _ = resWake.RowsAffected()
	} else {
		resDrop, err := tx.ExecContext(ctx, `
			UPDATE wakeups
			SET status = 'dropped', run_id = NULL
			WHERE status = 'claimed' AND attempts >= ?`, maxWakeupAttempts)
		if err != nil {
			return 0, 0, 0, err
		}
		droppedWakeups, _ = resDrop.RowsAffected()

		resWake, err := tx.ExecContext(ctx, `
			UPDATE wakeups
			SET status = 'queued', run_id = NULL
			WHERE status = 'claimed'`)
		if err != nil {
			return 0, 0, 0, err
		}
		requeuedWakeups, _ = resWake.RowsAffected()
	}

	if _, err := tx.ExecContext(ctx, `UPDATE agents SET active_run_id = NULL WHERE active_run_id IS NOT NULL`); err != nil {
		return 0, 0, 0, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE teams SET running = COALESCE((
			SELECT COUNT(*) FROM runs r JOIN agents a ON a.id = r.agent_id
			WHERE a.team_id = teams.id AND r.status = 'running' AND r.ended_at IS NULL
		), 0)`); err != nil {
		return 0, 0, 0, err
	}
	for _, aid := range agentIDs {
		if err := s.recomputeAgentReadyTx(ctx, tx, aid, nowMS); err != nil {
			return 0, 0, 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, 0, err
	}
	return interruptedRuns, requeuedWakeups, droppedWakeups, nil
}
