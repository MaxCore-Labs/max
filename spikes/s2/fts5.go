package main

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const ftsSchemaSQL = `
CREATE TABLE IF NOT EXISTS episodic_memories (
  agent_id TEXT NOT NULL,
  id TEXT NOT NULL,
  conversation_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  content TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (agent_id, id)
) WITHOUT ROWID;

CREATE VIRTUAL TABLE IF NOT EXISTS episodic_fts USING fts5(
  agent_tok,
  content,
  memory_id UNINDEXED,
  tokenize = 'unicode61'
);
`

func agentToken(agentID string) string {
	var b strings.Builder
	b.WriteString("ag_")
	for i := 0; i < len(agentID); i++ {
		c := agentID[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			b.WriteByte(c)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

func openFTSStore(ctx context.Context, dbPath string) (*sql.DB, *sql.DB, error) {
	slash := filepath.ToSlash(dbPath)
	pragmas := "_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_pragma=cache_size(-65536)"
	wdb, err := sql.Open("sqlite", fmt.Sprintf("file:%s?%s&_txlock=immediate", slash, pragmas))
	if err != nil {
		return nil, nil, err
	}
	wdb.SetMaxOpenConns(1)
	if _, err := wdb.ExecContext(ctx, ftsSchemaSQL); err != nil {
		_ = wdb.Close()
		return nil, nil, err
	}
	rdb, err := sql.Open("sqlite", fmt.Sprintf("file:%s?%s", slash, pragmas))
	if err != nil {
		_ = wdb.Close()
		return nil, nil, err
	}
	rdb.SetMaxOpenConns(8)
	return wdb, rdb, nil
}

func runSQLiteFTS5Benchmark(ctx context.Context, baseDir string, totalMemories, numAgents, numSearches int) error {
	fmt.Printf("\n=== [Fallback Comparison] SQLite FTS5 EpisodicMemory (%d memories / %d agents) ===\n", totalMemories, numAgents)
	tmpDir := filepath.Join(baseDir, "tmp", "fts5")
	_ = os.RemoveAll(tmpDir)
	_ = os.MkdirAll(tmpDir, 0o700)
	defer func() { _ = os.RemoveAll(tmpDir) }()
	dbFile := filepath.Join(tmpDir, "episodic.db")

	rssBeforeMB := currentGoRSSMB()
	wdb, rdb, err := openFTSStore(ctx, dbFile)
	if err != nil {
		return err
	}

	topics := []string{
		"triển khai cổng duyệt quyền ACP cho tiến trình con",
		"tối ưu hóa truy vấn báo cáo doanh thu quý ba",
		"khắc phục lỗi rò rỉ kết nối mạng trên máy chủ biên",
		"đối soát hóa đơn thanh toán nhà cung cấp nội địa",
		"cấu hình lịch chạy định kỳ tổng hợp tin tức sáng",
	}

	tWrite := time.Now()
	const batch = 500
	for offset := 0; offset < totalMemories; offset += batch {
		n := min(batch, totalMemories-offset)
		tx, err := wdb.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		stmtMem, err := tx.PrepareContext(ctx, `INSERT INTO episodic_memories (agent_id, id, conversation_id, kind, content, created_at) VALUES (?, ?, ?, 'summary', ?, ?)`)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		stmtFTS, err := tx.PrepareContext(ctx, `INSERT INTO episodic_fts (agent_tok, content, memory_id) VALUES (?, ?, ?)`)
		if err != nil {
			_ = stmtMem.Close()
			_ = tx.Rollback()
			return err
		}

		nowMS := time.Now().UnixMilli()
		for j := 0; j < n; j++ {
			idx := offset + j
			aIdx := idx % numAgents
			agentID := fmt.Sprintf("agent-%05d", aIdx+1)
			memID := fmt.Sprintf("mem-%07d", idx+1)
			convID := fmt.Sprintf("conv-%05d-%d", aIdx+1, (idx/numAgents)%3+1)
			topic := topics[(aIdx+(idx/numAgents))%len(topics)]
			content := fmt.Sprintf("Tóm tắt run #%d của %s: %s mã kiểm chứng tok_%d_%d", idx+1, agentID, topic, aIdx+1, (idx/numAgents)+1)
			if idx == 0 {
				content = "Ký ức mẫu bất biến của agent-00001: khóa đối chiếu CANARY_ALPHA_9981 cho kiểm thử khởi động lại"
			}
			if _, err := stmtMem.ExecContext(ctx, agentID, memID, convID, content, nowMS); err != nil {
				_ = stmtFTS.Close()
				_ = stmtMem.Close()
				_ = tx.Rollback()
				return err
			}
			if _, err := stmtFTS.ExecContext(ctx, agentToken(agentID), content, memID); err != nil {
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
	_, _ = wdb.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
	writeDur := time.Since(tWrite)

	// Run 1,000 scoped smart-searches (agentId + limit 5)
	rng := rand.New(rand.NewPCG(99, 20261119))
	latencies := make([]float64, 0, numSearches)
	hits := 0
	queryWords := []string{"triển khai cổng", "doanh thu quý", "rò rỉ kết nối", "đối soát hóa đơn", "lịch chạy định kỳ"}

	for i := 0; i < numSearches; i++ {
		aIdx := rng.IntN(numAgents)
		agentID := fmt.Sprintf("agent-%05d", aIdx+1)
		q := queryWords[i%len(queryWords)]
		ftsExpr := fmt.Sprintf("agent_tok:%s AND (%s)", agentToken(agentID), q)

		t0 := time.Now()
		rows, err := rdb.QueryContext(ctx, `
			SELECT m.id, m.content
			FROM episodic_fts f
			JOIN episodic_memories m ON m.agent_id = ? AND m.id = f.memory_id
			WHERE episodic_fts MATCH ?
			ORDER BY bm25(episodic_fts)
			LIMIT 5`, agentID, ftsExpr)
		if err != nil {
			return err
		}
		count := 0
		for rows.Next() {
			var id, c string
			if err := rows.Scan(&id, &c); err == nil {
				count++
			}
		}
		_ = rows.Close()
		latencies = append(latencies, float64(time.Since(t0).Microseconds())/1000.0)
		if count > 0 {
			hits++
		}
	}
	rssAfterMB := currentGoRSSMB()

	// Close & reopen to verify disk persistence
	_ = rdb.Close()
	_ = wdb.Close()

	tReopen := time.Now()
	wdb2, rdb2, err := openFTSStore(ctx, dbFile)
	if err != nil {
		return err
	}
	var canaryID string
	err = rdb2.QueryRowContext(ctx, `
		SELECT memory_id FROM episodic_fts
		WHERE episodic_fts MATCH 'agent_tok:ag_agent_00001 AND CANARY_ALPHA_9981'
		LIMIT 1`).Scan(&canaryID)
	reopenMs := time.Since(tReopen).Milliseconds()
	_ = rdb2.Close()
	_ = wdb2.Close()
	if err != nil {
		return fmt.Errorf("canary lookup after reopen failed: %w", err)
	}

	fi, _ := os.Stat(dbFile)
	slices.Sort(latencies)
	p50 := latencies[len(latencies)*50/100]
	p95 := latencies[len(latencies)*95/100]
	p99 := latencies[len(latencies)*99/100]

	fmt.Printf("  Write %d memories: %.2fs (%.0f writes/s) | DB size: %.1f MB\n",
		totalMemories, writeDur.Seconds(), float64(totalMemories)/writeDur.Seconds(), float64(fi.Size())/(1024*1024))
	fmt.Printf("  Search (%d queries, limit=5): p50=%.3f ms | p95=%.3f ms | p99=%.3f ms | HitRate=%d/%d (%.1f%%)\n",
		numSearches, p50, p95, p99, hits, numSearches, 100*float64(hits)/float64(numSearches))
	fmt.Printf("  Go Process RSS Before=%.1f MB -> After=%.1f MB | Reopen+Canary(%s)=%d ms\n",
		rssBeforeMB, rssAfterMB, canaryID, reopenMs)
	return nil
}
