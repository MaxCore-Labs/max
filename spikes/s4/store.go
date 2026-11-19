package main

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

type store struct {
	writeDB *sql.DB
	readDB  *sql.DB
}

type claimedRun struct {
	RunID          string
	AgentID        string
	TeamID         string
	ConversationID string
	Priority       int
	WakeupIDs      []string
}

func newID() string {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.NewString()
	}
	return id.String()
}

func openStore(ctx context.Context, dbPath string) (*store, error) {
	slash := filepath.ToSlash(dbPath)
	basePragmas := "_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=cache_size(-131072)&_pragma=mmap_size(536870912)&_pragma=wal_autocheckpoint(4000)"
	writeDSN := fmt.Sprintf("file:%s?%s&_txlock=immediate", slash, basePragmas)
	readDSN := fmt.Sprintf("file:%s?%s", slash, basePragmas)

	wdb, err := sql.Open("sqlite", writeDSN)
	if err != nil {
		return nil, fmt.Errorf("open write sqlite: %w", err)
	}
	wdb.SetMaxOpenConns(1)
	wdb.SetMaxIdleConns(1)

	if _, err := wdb.ExecContext(ctx, schemaSQL); err != nil && !strings.Contains(err.Error(), "already exists") {
		_ = wdb.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}

	rdb, err := sql.Open("sqlite", readDSN)
	if err != nil {
		_ = wdb.Close()
		return nil, fmt.Errorf("open read sqlite: %w", err)
	}
	rdb.SetMaxOpenConns(8)
	rdb.SetMaxIdleConns(8)

	return &store{writeDB: wdb, readDB: rdb}, nil
}

func (s *store) close() error {
	err1 := s.readDB.Close()
	err2 := s.writeDB.Close()
	if err1 != nil {
		return err1
	}
	return err2
}

func recomputeAgentReadyTx(ctx context.Context, tx *sql.Tx, agentID string, nowMS int64) error {
	var prio int
	var createdAt int64
	err := tx.QueryRowContext(ctx, `
		SELECT priority, created_at
		FROM wakeups
		WHERE agent_id = ? AND status = 'queued' AND not_before <= ?
		ORDER BY priority DESC, created_at
		LIMIT 1`, agentID, nowMS,
	).Scan(&prio, &createdAt)
	if err == sql.ErrNoRows {
		_, uerr := tx.ExecContext(ctx, `
			UPDATE agents
			SET ready = 0, ready_priority = 0, ready_since = NULL
			WHERE id = ?`, agentID)
		return uerr
	}
	if err != nil {
		return err
	}
	_, uerr := tx.ExecContext(ctx, `
		UPDATE agents
		SET ready = 1, ready_priority = ?, ready_since = ?
		WHERE id = ?`, prio, createdAt, agentID)
	return uerr
}

const candidateSQLJoin = `
SELECT a.id, a.team_id
FROM agents a JOIN teams t ON t.id = a.team_id
WHERE a.ready = 1 AND a.active_run_id IS NULL AND a.status = 'active'
  AND a.cooldown_until <= ?
  AND t.running < t.max_running
  AND (a.engine_mask & ?) != 0
ORDER BY a.ready_priority DESC, a.ready_since
LIMIT ?`

const candidateSQLIndexed = `
SELECT a.id, a.team_id
FROM agents a INDEXED BY agents_ready
JOIN teams t ON t.id = a.team_id
WHERE a.ready = 1 AND a.active_run_id IS NULL AND a.status = 'active'
  AND a.cooldown_until <= ?
  AND t.running < t.max_running
  AND (a.engine_mask & ?) != 0
ORDER BY a.ready_priority DESC, a.ready_since
LIMIT ?`

// claimWork executes one BEGIN IMMEDIATE claim cycle per Spec 8.2.2.
func (s *store) claimWork(ctx context.Context, freeSlots int, engineMask int, nowMS int64, forceIndexed bool) ([]claimedRun, time.Duration, error) {
	if freeSlots <= 0 {
		return nil, 0, nil
	}
	t0 := time.Now()
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("begin immediate: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	query := candidateSQLJoin
	if forceIndexed {
		query = candidateSQLIndexed
	}
	// Over-fetch slightly (freeSlots*3) in case multiple candidates belong to a team hitting max_running in the same tx.
	rows, err := tx.QueryContext(ctx, query, nowMS, engineMask, freeSlots*3)
	if err != nil {
		return nil, 0, fmt.Errorf("select candidates: %w", err)
	}
	type cand struct{ agentID, teamID string }
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.agentID, &c.teamID); err != nil {
			_ = rows.Close()
			return nil, 0, err
		}
		cands = append(cands, c)
	}
	if err := rows.Close(); err != nil {
		return nil, 0, err
	}

	claimed := make([]claimedRun, 0, freeSlots)
	for _, c := range cands {
		if len(claimed) >= freeSlots {
			break
		}
		// Re-check team cap after prior claims in the same transaction (Spec 8.2.2 step 3)
		var running, maxRunning int
		if err := tx.QueryRowContext(ctx, `SELECT running, max_running FROM teams WHERE id = ?`, c.teamID).Scan(&running, &maxRunning); err != nil {
			return nil, 0, err
		}
		if running >= maxRunning {
			continue
		}

		// Step 1: Highest priority queued wakeup for this agent
		var firstID, convID string
		var topPrio, firstAttempts int
		err := tx.QueryRowContext(ctx, `
			SELECT id, conversation_id, priority, attempts
			FROM wakeups
			WHERE agent_id = ? AND status = 'queued' AND not_before <= ?
			ORDER BY priority DESC, created_at
			LIMIT 1`, c.agentID, nowMS,
		).Scan(&firstID, &convID, &topPrio, &firstAttempts)
		if err == sql.ErrNoRows {
			if err := recomputeAgentReadyTx(ctx, tx, c.agentID, nowMS); err != nil {
				return nil, 0, err
			}
			continue
		}
		if err != nil {
			return nil, 0, err
		}

		// Step 2: Up to 19 more queued wakeups of the SAME conversation
		wRows, err := tx.QueryContext(ctx, `
			SELECT id
			FROM wakeups
			WHERE agent_id = ? AND status = 'queued' AND not_before <= ?
			  AND conversation_id = ? AND id <> ?
			ORDER BY priority DESC, created_at
			LIMIT 19`, c.agentID, nowMS, convID, firstID)
		if err != nil {
			return nil, 0, err
		}
		wIDs := []string{firstID}
		for wRows.Next() {
			var wid string
			if err := wRows.Scan(&wid); err != nil {
				_ = wRows.Close()
				return nil, 0, err
			}
			wIDs = append(wIDs, wid)
		}
		_ = wRows.Close()

		// Step 4: Create run, mark wakeups claimed, update agent & team
		runID := newID()
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO runs (id, agent_id, conversation_id, status, priority, engine, model, attempt, started_at)
			VALUES (?, ?, ?, 'running', ?, 'agy', 'default', ?, ?)`,
			runID, c.agentID, convID, topPrio, firstAttempts+1, nowMS,
		); err != nil {
			return nil, 0, fmt.Errorf("insert run: %w", err)
		}

		placeholders := strings.Repeat("?,", len(wIDs))
		placeholders = placeholders[:len(placeholders)-1]
		args := make([]any, 0, len(wIDs)+2)
		args = append(args, runID, c.agentID)
		for _, id := range wIDs {
			args = append(args, id)
		}
		if _, err := tx.ExecContext(ctx,
			fmt.Sprintf(`UPDATE wakeups SET status = 'claimed', run_id = ?, attempts = attempts + 1 WHERE agent_id = ? AND id IN (%s)`, placeholders),
			args...,
		); err != nil {
			return nil, 0, fmt.Errorf("claim wakeups: %w", err)
		}

		if _, err := tx.ExecContext(ctx, `UPDATE agents SET active_run_id = ? WHERE id = ?`, runID, c.agentID); err != nil {
			return nil, 0, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE teams SET running = running + 1 WHERE id = ?`, c.teamID); err != nil {
			return nil, 0, err
		}
		if err := recomputeAgentReadyTx(ctx, tx, c.agentID, nowMS); err != nil {
			return nil, 0, err
		}

		claimed = append(claimed, claimedRun{
			RunID:          runID,
			AgentID:        c.agentID,
			TeamID:         c.teamID,
			ConversationID: convID,
			Priority:       topPrio,
			WakeupIDs:      wIDs,
		})
	}

	if err := tx.Commit(); err != nil {
		return nil, 0, fmt.Errorf("commit claim: %w", err)
	}
	return claimed, time.Since(t0), nil
}

func (s *store) finishRun(ctx context.Context, cr claimedRun, cooldownMS int64, nowMS int64) error {
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
		UPDATE runs SET status = 'succeeded', turns = 1, ended_at = ? WHERE id = ?`,
		nowMS, cr.RunID,
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE wakeups SET status = 'done' WHERE run_id = ? AND status = 'claimed'`,
		cr.RunID,
	); err != nil {
		return err
	}
	cdUntil := int64(0)
	if cr.Priority < 100 && cooldownMS > 0 {
		cdUntil = nowMS + cooldownMS
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE agents SET active_run_id = NULL, last_run_at = ?, cooldown_until = ? WHERE id = ?`,
		nowMS, cdUntil, cr.AgentID,
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE teams SET running = running - 1 WHERE id = ?`,
		cr.TeamID,
	); err != nil {
		return err
	}
	if err := recomputeAgentReadyTx(ctx, tx, cr.AgentID, nowMS); err != nil {
		return err
	}
	return tx.Commit()
}

// recoverOnBoot implements Spec 8.2.5 startup crash recovery.
func (s *store) recoverOnBoot(ctx context.Context, nowMS int64) (int64, int64, error) {
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()

	resRuns, err := tx.ExecContext(ctx, `
		UPDATE runs SET status = 'interrupted', ended_at = ? WHERE status = 'running'`, nowMS)
	if err != nil {
		return 0, 0, err
	}
	interruptedRuns, _ := resRuns.RowsAffected()

	// Find affected agents before clearing claimed wakeups
	rows, err := tx.QueryContext(ctx, `
		SELECT DISTINCT agent_id FROM wakeups WHERE status = 'claimed'
		UNION
		SELECT id FROM agents WHERE active_run_id IS NOT NULL`)
	if err != nil {
		return 0, 0, err
	}
	var agentIDs []string
	for rows.Next() {
		var aid string
		if err := rows.Scan(&aid); err != nil {
			_ = rows.Close()
			return 0, 0, err
		}
		agentIDs = append(agentIDs, aid)
	}
	_ = rows.Close()

	resWake, err := tx.ExecContext(ctx, `
		UPDATE wakeups SET status = 'queued', run_id = NULL WHERE status = 'claimed'`)
	if err != nil {
		return 0, 0, err
	}
	requeuedWakeups, _ := resWake.RowsAffected()

	if _, err := tx.ExecContext(ctx, `UPDATE agents SET active_run_id = NULL WHERE active_run_id IS NOT NULL`); err != nil {
		return 0, 0, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE teams SET running = COALESCE((
			SELECT COUNT(*) FROM runs r JOIN agents a ON a.id = r.agent_id
			WHERE a.team_id = teams.id AND r.status = 'running'
		), 0)`); err != nil {
		return 0, 0, err
	}
	for _, aid := range agentIDs {
		if err := recomputeAgentReadyTx(ctx, tx, aid, nowMS); err != nil {
			return 0, 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return interruptedRuns, requeuedWakeups, nil
}
