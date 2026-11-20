package main

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"time"

	_ "modernc.org/sqlite"
)

const ftsSchemaSQL = `
CREATE TABLE IF NOT EXISTS episodic_memories (
  seq_id INTEGER PRIMARY KEY,
  id TEXT NOT NULL UNIQUE,
  author_agent_id TEXT NOT NULL,
  team_id TEXT NOT NULL,
  scope TEXT NOT NULL CHECK (scope IN ('private', 'team')),
  conversation_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  content TEXT NOT NULL,
  created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_episodic_agent_created
  ON episodic_memories(author_agent_id, created_at DESC, seq_id DESC);
CREATE INDEX IF NOT EXISTS idx_episodic_team_created
  ON episodic_memories(team_id, scope, created_at DESC);

CREATE VIRTUAL TABLE IF NOT EXISTS episodic_fts USING fts5(
  scope_tok,
  content_norm,
  tokenize = 'unicode61 remove_diacritics 2'
);
`

// agentToken encodes agentID injectively into a single alphanumeric token (no punctuation split).
func agentToken(agentID string) string {
	return "ag" + hex.EncodeToString([]byte(agentID))
}

// teamToken encodes teamID injectively into a single alphanumeric token.
func teamToken(teamID string) string {
	return "tm" + hex.EncodeToString([]byte(teamID))
}

func scopeTokens(authorAgentID, teamID, scope string) string {
	if scope == "team" && teamID != "" {
		return agentToken(authorAgentID) + " " + teamToken(teamID)
	}
	return agentToken(authorAgentID)
}

type episodicHit struct {
	ID            string
	AuthorAgentID string
	Scope         string
	Content       string
}

// openFTSStore bounds standalone benchmark SQLite page cache to <= 96 MiB (1 writer * 32 MiB + 4 readers * 16 MiB).
// In production (P3.02), episodic tables live inside max.sqlite3 and share P1.04's single Store pool.
func openFTSStore(ctx context.Context, dbPath string) (*sql.DB, *sql.DB, error) {
	slash := filepath.ToSlash(dbPath)
	base := "_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)"
	wdb, err := sql.Open("sqlite", fmt.Sprintf("file:%s?%s&_pragma=cache_size(-32768)&_txlock=immediate", slash, base))
	if err != nil {
		return nil, nil, err
	}
	wdb.SetMaxOpenConns(1)
	if _, err := wdb.ExecContext(ctx, ftsSchemaSQL); err != nil {
		_ = wdb.Close()
		return nil, nil, err
	}
	rdb, err := sql.Open("sqlite", fmt.Sprintf("file:%s?%s&_pragma=cache_size(-16384)", slash, base))
	if err != nil {
		_ = wdb.Close()
		return nil, nil, err
	}
	rdb.SetMaxOpenConns(4)
	return wdb, rdb, nil
}

func insertEpisodicTx(ctx context.Context, tx *sql.Tx, m synthMemory, createdAt int64) error {
	res, err := tx.ExecContext(ctx,
		`INSERT INTO episodic_memories (id, author_agent_id, team_id, scope, conversation_id, kind, content, created_at)
		 VALUES (?, ?, ?, ?, ?, 'summary', ?, ?)`,
		m.MemID, m.AgentID, m.TeamID, m.Scope, m.ConvID, m.Content, createdAt,
	)
	if err != nil {
		return err
	}
	rowID, err := res.LastInsertId()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO episodic_fts (rowid, scope_tok, content_norm) VALUES (?, ?, ?)`,
		rowID, scopeTokens(m.AgentID, m.TeamID, m.Scope), foldVNForFTS(m.Content),
	)
	return err
}

func searchEpisodic(ctx context.Context, rdb *sql.DB, callerAgentID, callerTeamID string, includeTeam bool, rawQuery string, matchAll bool, limit int) ([]episodicHit, error) {
	qExpr := sanitizeFTSQuery(rawQuery, matchAll)
	var (
		rows *sql.Rows
		err  error
	)
	if includeTeam && callerTeamID != "" {
		ftsExpr := fmt.Sprintf("(scope_tok:%s OR scope_tok:%s) AND (%s)", agentToken(callerAgentID), teamToken(callerTeamID), qExpr)
		rows, err = rdb.QueryContext(ctx, `
			SELECT m.id, m.author_agent_id, m.scope, m.content
			FROM episodic_fts f
			JOIN episodic_memories m ON m.seq_id = f.rowid
			WHERE episodic_fts MATCH ?
			  AND (m.author_agent_id = ? OR (m.scope = 'team' AND m.team_id = ?))
			ORDER BY bm25(episodic_fts)
			LIMIT ?`, ftsExpr, callerAgentID, callerTeamID, limit)
	} else {
		ftsExpr := fmt.Sprintf("scope_tok:%s AND (%s)", agentToken(callerAgentID), qExpr)
		rows, err = rdb.QueryContext(ctx, `
			SELECT m.id, m.author_agent_id, m.scope, m.content
			FROM episodic_fts f
			JOIN episodic_memories m ON m.seq_id = f.rowid
			WHERE episodic_fts MATCH ?
			  AND m.author_agent_id = ?
			ORDER BY bm25(episodic_fts)
			LIMIT ?`, ftsExpr, callerAgentID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	hits := make([]episodicHit, 0, limit)
	for rows.Next() {
		var h episodicHit
		if err := rows.Scan(&h.ID, &h.AuthorAgentID, &h.Scope, &h.Content); err != nil {
			return nil, err
		}
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return hits, nil
}

// pruneEpisodicMemories deletes expired or excess memories by integer `rowid` (`seq_id`)
// across both episodic_fts and episodic_memories in O(log N) per row inside one transaction.
func pruneEpisodicMemories(ctx context.Context, wdb *sql.DB, cutoffMs int64, maxPerAgent int) (int64, error) {
	tx, err := wdb.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `
		SELECT seq_id FROM (
			SELECT seq_id, created_at,
			       ROW_NUMBER() OVER (PARTITION BY author_agent_id ORDER BY created_at DESC, seq_id DESC) AS rn
			FROM episodic_memories
		)
		WHERE created_at < ? OR rn > ?`, cutoffMs, maxPerAgent)
	if err != nil {
		return 0, err
	}
	var seqIDs []int64
	for rows.Next() {
		var sid int64
		if err := rows.Scan(&sid); err != nil {
			_ = rows.Close()
			return 0, err
		}
		seqIDs = append(seqIDs, sid)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	_ = rows.Close()

	stmtFTS, err := tx.PrepareContext(ctx, `DELETE FROM episodic_fts WHERE rowid = ?`)
	if err != nil {
		return 0, err
	}
	defer func() { _ = stmtFTS.Close() }()
	stmtMem, err := tx.PrepareContext(ctx, `DELETE FROM episodic_memories WHERE seq_id = ?`)
	if err != nil {
		return 0, err
	}
	defer func() { _ = stmtMem.Close() }()

	for _, sid := range seqIDs {
		if _, err := stmtFTS.ExecContext(ctx, sid); err != nil {
			return 0, err
		}
		if _, err := stmtMem.ExecContext(ctx, sid); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int64(len(seqIDs)), nil
}

func runSQLiteFTS5Benchmark(ctx context.Context, baseDir string, totalMemories, numAgents, numSearches int) error {
	fmt.Printf("\n=== [Fallback Comparison] SQLite FTS5 EpisodicMemory (%d memories / %d agents) ===\n", totalMemories, numAgents)
	tmpDir, err := os.MkdirTemp("", "max-s2-fts5-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()
	dbFile := filepath.Join(tmpDir, "episodic.db")

	pssBefore := currentSelfPSSMB()
	wdb, rdb, err := openFTSStore(ctx, dbFile)
	if err != nil {
		return err
	}
	defer func() {
		_ = rdb.Close()
		_ = wdb.Close()
	}()

	// 1a. Bulk load totalMemories (500 rows/tx) with oldest 500 rows marked at timestamp 1000 for prune test
	tBulk := time.Now()
	const batch = 500
	baseNowMs := time.Now().UnixMilli()
	for offset := 0; offset < totalMemories; offset += batch {
		n := min(batch, totalMemories-offset)
		tx, err := wdb.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		stmtMem, err := tx.PrepareContext(ctx, `INSERT INTO episodic_memories (id, author_agent_id, team_id, scope, conversation_id, kind, content, created_at) VALUES (?, ?, ?, ?, ?, 'summary', ?, ?)`)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		stmtFTS, err := tx.PrepareContext(ctx, `INSERT INTO episodic_fts (rowid, scope_tok, content_norm) VALUES (?, ?, ?)`)
		if err != nil {
			_ = stmtMem.Close()
			_ = tx.Rollback()
			return err
		}
		for j := 0; j < n; j++ {
			idx := offset + j
			m := buildSynthMemory(idx, numAgents)
			ts := baseNowMs + int64(idx)
			// Mark indices 1..500 (excluding canary idx=0) as expired so we measure pruning 500 rows at N=200k.
			if idx >= 1 && idx <= 500 {
				ts = 1000
			}
			res, err := stmtMem.ExecContext(ctx, m.MemID, m.AgentID, m.TeamID, m.Scope, m.ConvID, m.Content, ts)
			if err != nil {
				_ = stmtFTS.Close()
				_ = stmtMem.Close()
				_ = tx.Rollback()
				return err
			}
			rowID, err := res.LastInsertId()
			if err != nil {
				_ = stmtFTS.Close()
				_ = stmtMem.Close()
				_ = tx.Rollback()
				return err
			}
			if _, err := stmtFTS.ExecContext(ctx, rowID, scopeTokens(m.AgentID, m.TeamID, m.Scope), foldVNForFTS(m.Content)); err != nil {
				_ = stmtFTS.Close()
				_ = stmtMem.Close()
				_ = tx.Rollback()
				return err
			}
		}
		_ = stmtFTS.Close()
		_ = stmtMem.Close()
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	bulkDur := time.Since(tBulk)

	// 1b. Fair production write mode: 1,000 individual 1-row transactions on populated DB
	const singleTxWrites = 1000
	tSingle := time.Now()
	for k := 0; k < singleTxWrites; k++ {
		m := buildSynthMemory(totalMemories+k, numAgents)
		tx, err := wdb.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if err := insertEpisodicTx(ctx, tx, m, baseNowMs+int64(totalMemories+k)); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	singleDur := time.Since(tSingle)

	// 2. Scoped searches (skipping pruned indices 1..500 not yet pruned; run searches BEFORE pruning)
	rng := rand.New(rand.NewPCG(77, 20261119))
	latencies := make([]float64, 0, numSearches)
	half := numSearches / 2
	topicHits := 0
	recall5Hits := 0

	for i := 0; i < numSearches; i++ {
		aIdx := rng.IntN(numAgents)
		agentID := fmt.Sprintf("agent-%05d", aIdx+1)
		var q, wantMemID string
		if i < half {
			q = topicQueryWords[aIdx%len(topicQueryWords)]
			if aIdx == 0 {
				q = canaryToken
			}
		} else {
			q = fmt.Sprintf("utoka%05dr001", aIdx+1)
			wantMemID = fmt.Sprintf("mem-%07d", aIdx+1)
		}
		t0 := time.Now()
		hits, err := searchEpisodic(ctx, rdb, agentID, "", false, q, true, 5)
		latencies = append(latencies, float64(time.Since(t0).Microseconds())/1000.0)
		if err != nil {
			return err
		}
		if i < half {
			if len(hits) > 0 {
				topicHits++
			}
		} else {
			for _, h := range hits {
				if h.ID == wantMemID {
					recall5Hits++
					break
				}
			}
		}
	}

	// 3. Measure retention prune of 500 expired rows by rowid on the 201k-row DB
	tPrune := time.Now()
	pruned, err := pruneEpisodicMemories(ctx, wdb, 5000, 100000)
	if err != nil {
		return fmt.Errorf("pruneEpisodicMemories: %w", err)
	}
	pruneMs := float64(time.Since(tPrune).Microseconds()) / 1000.0

	_, _ = wdb.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
	pssAfter := currentSelfPSSMB()
	peakRSS := peakSelfRSSMB()
	fi, err := os.Stat(dbFile)
	if err != nil {
		return fmt.Errorf("stat %s: %w", dbFile, err)
	}

	// 4. True SIGKILL crash + fresh reopen test
	crashRows, crashReopenMs, err := verifySIGKILLPersistence(ctx, filepath.Join(tmpDir, "crash.db"))
	if err != nil {
		return fmt.Errorf("SIGKILL persistence test: %w", err)
	}

	slices.Sort(latencies)
	p50 := latencies[len(latencies)*50/100]
	p95 := latencies[len(latencies)*95/100]
	p99 := latencies[len(latencies)*99/100]

	fmt.Printf("  Bulk Write (%d rows, 500/tx): %.2fs (%.0f rows/s) | 1-Row/Tx (%d tx): %.2fs (%.0f tx/s) | DB=%.1f MB\n",
		totalMemories, bulkDur.Seconds(), float64(totalMemories)/bulkDur.Seconds(),
		singleTxWrites, singleDur.Seconds(), float64(singleTxWrites)/singleDur.Seconds(),
		float64(fi.Size())/(1024*1024))
	fmt.Printf("  Search (%d queries, limit=5): p50=%.3f ms | p95=%.3f ms | p99=%.3f ms | TopicHalf=%d/%d (%.1f%%) | UniqueRecall@5=%d/%d (%.1f%%)\n",
		numSearches, p50, p95, p99, topicHits, half, 100*float64(topicHits)/float64(half), recall5Hits, numSearches-half, 100*float64(recall5Hits)/float64(numSearches-half))
	fmt.Printf("  Retention Prune (by rowid at N=%d): deleted=%d rows in %.2f ms\n", totalMemories+singleTxWrites, pruned, pruneMs)
	fmt.Printf("  Self Memory: PSS/Private Before=%.1f MiB -> After=%.1f MiB | Peak RSS (VmHWM/PeakWS)=%.1f MiB\n",
		pssBefore, pssAfter, peakRSS)
	fmt.Printf("  SIGKILL Crash Test (no close/checkpoint): %d/200 committed rows + canary recovered in %d ms\n",
		crashRows, crashReopenMs)
	_ = baseDir
	return nil
}
