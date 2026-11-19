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
var schemaOptimizedSQL string

//go:embed schema_baseline.sql
var schemaBaselineSQL string

const maxWakeupAttempts = 3

type store struct {
	writeDB, readDB                                           *sql.DB
	stmtCandJoin, stmtCandIdx, stmtScanMailbox                *sql.Stmt
	stmtClaimOne, stmtClaimBatch20, stmtRecomputeSel          *sql.Stmt
	stmtAgentReadyOn, stmtAgentReadyOff, stmtAgentClaimUpdate *sql.Stmt
	stmtInsertRun, stmtTeamAddDelta                           *sql.Stmt
	stmtFinishRun, stmtFinishWakeups, stmtFinishAgent         *sql.Stmt
	stmtTeamDec                                               *sql.Stmt
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

// escapeSQLiteURIPath escapes special URI characters (%, ?, #, space) per https://www.sqlite.org/uri.html.
func escapeSQLiteURIPath(dbPath string) string {
	return strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23", " ", "%20").Replace(filepath.ToSlash(dbPath))
}

const (
	// Writer (1 conn): 128 MiB page cache, 512 MiB mmap, 64 MiB autocheckpoint ceiling + explicit PASSIVE checkpoints.
	writePragmas = "_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=cache_size(-131072)&_pragma=mmap_size(536870912)&_pragma=wal_autocheckpoint(16000)&_txlock=immediate"
	// Readers (8 conns): 4 MiB page cache each (32 MiB total ceiling) + shared OS page cache via 512 MiB mmap.
	// Total private SQLite page-cache ceiling across all 9 connections = 128 + 8*4 = 160 MiB.
	readPragmas = "_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=cache_size(-4096)&_pragma=mmap_size(536870912)&_pragma=query_only(1)"
)

const candidateSQLJoin = `
SELECT a.id, a.team_id, t.running, t.max_running
FROM agents a JOIN teams t ON t.id = a.team_id
WHERE a.ready = 1 AND a.active_run_id IS NULL AND a.status = 'active'
  AND (a.cooldown_until <= ? OR a.ready_priority >= 100)
  AND t.running < t.max_running
  AND (a.engine_mask & ?) != 0
ORDER BY a.ready_priority DESC, a.ready_since
LIMIT ?`

const candidateSQLIndexed = `
SELECT a.id, a.team_id, t.running, t.max_running
FROM agents a INDEXED BY agents_ready
JOIN teams t ON t.id = a.team_id
WHERE a.ready = 1 AND a.active_run_id IS NULL AND a.status = 'active'
  AND (a.cooldown_until <= ? OR a.ready_priority >= 100)
  AND t.running < t.max_running
  AND (a.engine_mask & ?) != 0
ORDER BY a.ready_priority DESC, a.ready_since
LIMIT ?`

func openStore(ctx context.Context, dbPath string, schemaVariant string) (*store, error) {
	escaped := escapeSQLiteURIPath(dbPath)
	writeDSN := fmt.Sprintf("file:%s?%s", escaped, writePragmas)
	readDSN := fmt.Sprintf("file:%s?%s", escaped, readPragmas)

	wdb, err := sql.Open("sqlite", writeDSN)
	if err != nil {
		return nil, fmt.Errorf("open write sqlite: %w", err)
	}
	wdb.SetMaxOpenConns(1)
	wdb.SetMaxIdleConns(1)

	ddl := schemaOptimizedSQL
	if schemaVariant == "baseline" {
		ddl = schemaBaselineSQL
	}
	if _, err := wdb.ExecContext(ctx, ddl); err != nil && !strings.Contains(err.Error(), "already exists") {
		_ = wdb.Close()
		return nil, fmt.Errorf("apply schema (%s): %w", schemaVariant, err)
	}

	rdb, err := sql.Open("sqlite", readDSN)
	if err != nil {
		_ = wdb.Close()
		return nil, fmt.Errorf("open read sqlite: %w", err)
	}
	rdb.SetMaxOpenConns(8)
	rdb.SetMaxIdleConns(8)

	st := &store{writeDB: wdb, readDB: rdb}
	if err := st.prepareStatements(ctx, schemaVariant); err != nil {
		_ = st.close()
		return nil, err
	}
	return st, nil
}

func (s *store) prepareStatements(ctx context.Context, schemaVariant string) error {
	var err error
	prep := func(q string) *sql.Stmt {
		if err != nil {
			return nil
		}
		var st *sql.Stmt
		st, err = s.writeDB.PrepareContext(ctx, q)
		return st
	}

	s.stmtCandJoin = prep(candidateSQLJoin)
	if schemaVariant != "baseline" {
		s.stmtCandIdx = prep(candidateSQLIndexed)
	} else {
		s.stmtCandIdx = prep(candidateSQLJoin)
	}
	s.stmtScanMailbox = prep(`SELECT id, conversation_id, priority, attempts, created_at FROM wakeups WHERE agent_id = ? AND status = 'queued' AND not_before <= ? ORDER BY priority DESC, created_at`)
	s.stmtClaimOne = prep(`UPDATE wakeups SET status = 'claimed', run_id = ?, attempts = attempts + 1 WHERE agent_id = ? AND id = ?`)
	s.stmtClaimBatch20 = prep(`UPDATE wakeups SET status = 'claimed', run_id = ?, attempts = attempts + 1 WHERE agent_id = ? AND id IN (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	s.stmtRecomputeSel = prep(`SELECT priority, created_at FROM wakeups WHERE agent_id = ? AND status = 'queued' AND not_before <= ? ORDER BY priority DESC, created_at LIMIT 1`)
	s.stmtAgentReadyOn = prep(`UPDATE agents SET ready = 1, ready_priority = ?, ready_since = ? WHERE id = ?`)
	s.stmtAgentReadyOff = prep(`UPDATE agents SET ready = 0, ready_priority = 0, ready_since = NULL WHERE id = ?`)
	s.stmtAgentClaimUpdate = prep(`UPDATE agents SET active_run_id = ?, ready = ?, ready_priority = ?, ready_since = ? WHERE id = ?`)
	s.stmtInsertRun = prep(`INSERT INTO runs (id, agent_id, conversation_id, status, priority, engine, model, attempt, started_at) VALUES (?, ?, ?, 'running', ?, 'agy', 'default', ?, ?)`)
	s.stmtTeamAddDelta = prep(`UPDATE teams SET running = running + ? WHERE id = ?`)
	s.stmtFinishRun = prep(`UPDATE runs SET status = 'succeeded', turns = 1, ended_at = ? WHERE id = ?`)
	s.stmtFinishWakeups = prep(`UPDATE wakeups SET status = 'done' WHERE run_id = ? AND status = 'claimed'`)
	s.stmtFinishAgent = prep(`UPDATE agents SET active_run_id = NULL, last_run_at = ?, cooldown_until = ? WHERE id = ?`)
	s.stmtTeamDec = prep(`UPDATE teams SET running = running - 1 WHERE id = ?`)
	if err != nil {
		return fmt.Errorf("prepare statements: %w", err)
	}
	return nil
}

func (s *store) close() error {
	stmts := []*sql.Stmt{
		s.stmtCandJoin, s.stmtCandIdx, s.stmtScanMailbox,
		s.stmtClaimOne, s.stmtClaimBatch20, s.stmtRecomputeSel,
		s.stmtAgentReadyOn, s.stmtAgentReadyOff, s.stmtAgentClaimUpdate,
		s.stmtInsertRun, s.stmtTeamAddDelta, s.stmtFinishRun,
		s.stmtFinishWakeups, s.stmtFinishAgent, s.stmtTeamDec,
	}
	for _, st := range stmts {
		if st != nil {
			_ = st.Close()
		}
	}
	var err1, err2 error
	if s.readDB != nil {
		err1 = s.readDB.Close()
	}
	if s.writeDB != nil {
		err2 = s.writeDB.Close()
	}
	if err1 != nil {
		return err1
	}
	return err2
}

func (s *store) recomputeAgentReadyTx(ctx context.Context, tx *sql.Tx, agentID string, nowMS int64) error {
	var prio int
	var createdAt int64
	err := tx.StmtContext(ctx, s.stmtRecomputeSel).QueryRowContext(ctx, agentID, nowMS).Scan(&prio, &createdAt)
	if err == sql.ErrNoRows {
		_, uerr := tx.StmtContext(ctx, s.stmtAgentReadyOff).ExecContext(ctx, agentID)
		return uerr
	}
	if err != nil {
		return err
	}
	_, uerr := tx.StmtContext(ctx, s.stmtAgentReadyOn).ExecContext(ctx, prio, createdAt, agentID)
	return uerr
}

// claimWork executes one BEGIN IMMEDIATE claim cycle per Spec 8.2.2 using a single-pass covering-index scan per agent.
func (s *store) claimWork(ctx context.Context, freeSlots int, engineMask int, nowMS int64, forceIndexed bool) ([]claimedRun, time.Duration, time.Duration, error) {
	if freeSlots <= 0 {
		return nil, 0, 0, nil
	}
	tCall := time.Now()
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("begin immediate: %w", err)
	}
	tExec := time.Now()
	defer func() { _ = tx.Rollback() }()

	candStmt := s.stmtCandJoin
	if forceIndexed {
		candStmt = s.stmtCandIdx
	}
	rows, err := tx.StmtContext(ctx, candStmt).QueryContext(ctx, nowMS, engineMask, freeSlots*3)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("select candidates: %w", err)
	}
	type cand struct {
		agentID, teamID     string
		running, maxRunning int
	}
	cands := make([]cand, 0, freeSlots*3)
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.agentID, &c.teamID, &c.running, &c.maxRunning); err != nil {
			_ = rows.Close()
			return nil, 0, 0, err
		}
		cands = append(cands, c)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, 0, 0, err
	}
	if err := rows.Close(); err != nil {
		return nil, 0, 0, err
	}

	teamAdded := make(map[string]int, len(cands))
	claimed := make([]claimedRun, 0, freeSlots)

	for _, c := range cands {
		if len(claimed) >= freeSlots {
			break
		}
		if c.running+teamAdded[c.teamID] >= c.maxRunning {
			continue
		}

		// Single-pass scan on covering index `wakeups_mailbox`: collects up to 20 same-conversation wakeups
		// AND identifies the exact next-highest remaining due wakeup (`nextReady`) in one sequential leaf walk.
		mRows, err := tx.StmtContext(ctx, s.stmtScanMailbox).QueryContext(ctx, c.agentID, nowMS)
		if err != nil {
			return nil, 0, 0, err
		}
		var (
			wIDs          = make([]string, 0, 20)
			firstConvNull sql.NullString
			topPrio       int
			firstAttempts int
			haveNextReady bool
			nextPrio      int
			nextSince     int64
		)
		for mRows.Next() {
			var wid string
			var convNull sql.NullString
			var prio, att int
			var createdAt int64
			if err := mRows.Scan(&wid, &convNull, &prio, &att, &createdAt); err != nil {
				_ = mRows.Close()
				return nil, 0, 0, err
			}
			if len(wIDs) == 0 {
				wIDs = append(wIDs, wid)
				firstConvNull = convNull
				topPrio = prio
				firstAttempts = att
				continue
			}
			if firstConvNull.Valid && firstConvNull.String != "" && convNull.Valid && convNull.String == firstConvNull.String && len(wIDs) < 20 {
				wIDs = append(wIDs, wid)
				continue
			}
			if !haveNextReady {
				haveNextReady = true
				nextPrio = prio
				nextSince = createdAt
			}
			if len(wIDs) >= 20 || !firstConvNull.Valid || firstConvNull.String == "" {
				break
			}
		}
		if err := mRows.Err(); err != nil {
			_ = mRows.Close()
			return nil, 0, 0, err
		}
		if err := mRows.Close(); err != nil {
			return nil, 0, 0, err
		}

		if len(wIDs) == 0 {
			if err := s.recomputeAgentReadyTx(ctx, tx, c.agentID, nowMS); err != nil {
				return nil, 0, 0, err
			}
			continue
		}

		convID := firstConvNull.String
		if !firstConvNull.Valid || convID == "" {
			convID = newID()
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO conversations (id, agent_id, task_id, thread_id) VALUES (?, ?, NULL, ?)`,
				convID, c.agentID, "sys:"+wIDs[0],
			); err != nil {
				return nil, 0, 0, fmt.Errorf("create fallback conversation: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE wakeups SET conversation_id = ? WHERE agent_id = ? AND id = ?`,
				convID, c.agentID, wIDs[0],
			); err != nil {
				return nil, 0, 0, err
			}
		}

		runID := newID()
		if _, err := tx.StmtContext(ctx, s.stmtInsertRun).ExecContext(ctx,
			runID, c.agentID, convID, topPrio, firstAttempts+1, nowMS,
		); err != nil {
			return nil, 0, 0, fmt.Errorf("insert run: %w", err)
		}

		if len(wIDs) == 1 {
			if _, err := tx.StmtContext(ctx, s.stmtClaimOne).ExecContext(ctx, runID, c.agentID, wIDs[0]); err != nil {
				return nil, 0, 0, fmt.Errorf("claim 1 wakeup: %w", err)
			}
		} else {
			var args [22]any
			args[0] = runID
			args[1] = c.agentID
			for i := range 20 {
				if i < len(wIDs) {
					args[i+2] = wIDs[i]
				} else {
					args[i+2] = wIDs[0]
				}
			}
			if _, err := tx.StmtContext(ctx, s.stmtClaimBatch20).ExecContext(ctx, args[:]...); err != nil {
				return nil, 0, 0, fmt.Errorf("claim wakeup batch: %w", err)
			}
		}

		var readyInt int
		var readySinceArg any
		if haveNextReady {
			readyInt = 1
			readySinceArg = nextSince
		}
		if _, err := tx.StmtContext(ctx, s.stmtAgentClaimUpdate).ExecContext(ctx,
			runID, readyInt, nextPrio, readySinceArg, c.agentID,
		); err != nil {
			return nil, 0, 0, err
		}

		teamAdded[c.teamID]++
		claimed = append(claimed, claimedRun{
			RunID:          runID,
			AgentID:        c.agentID,
			TeamID:         c.teamID,
			ConversationID: convID,
			Priority:       topPrio,
			WakeupIDs:      wIDs,
		})
	}

	for tid, delta := range teamAdded {
		if _, err := tx.StmtContext(ctx, s.stmtTeamAddDelta).ExecContext(ctx, delta, tid); err != nil {
			return nil, 0, 0, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, 0, 0, fmt.Errorf("commit claim: %w", err)
	}
	return claimed, time.Since(tCall), time.Since(tExec), nil
}
