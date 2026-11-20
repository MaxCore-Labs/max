package sqlite

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"

	"max/internal/app"
	"max/internal/domain"
	"max/internal/platform"
)

// MemoryEntry describes a structured episodic memory write in max.sqlite3.
type MemoryEntry struct {
	ID             domain.ID
	AuthorAgentID  domain.ID
	TeamID         domain.ID
	Scope          string // "private" | "team"
	ConversationID domain.ID
	Kind           string
	Concepts       []string
	Content        string
}

// Memory implements app.EpisodicMemory and app.TxMemoryWriter on the shared max.sqlite3 pool (S2 decision).
type Memory struct {
	store *Store
	clock app.Clock
	ids   app.IDGen
}

// NewMemory constructs a SQLite FTS5 EpisodicMemory sharing store's single writer + reader pools.
func NewMemory(store *Store, clock app.Clock, ids app.IDGen) *Memory {
	if clock == nil {
		clock = app.SystemClock{}
	}
	if ids == nil {
		ids = app.UUIDv7Gen{}
	}
	return &Memory{store: store, clock: clock, ids: ids}
}

// Healthy reports true when the shared SQLite pool is open.
func (m *Memory) Healthy() bool {
	return m != nil && m.store != nil && m.store.rdb != nil
}

// Remember stores a private redacted episodic memory for agentID.
func (m *Memory) Remember(ctx context.Context, agentID domain.ID, content string, concepts []string) error {
	return m.RememberEntry(ctx, MemoryEntry{
		AuthorAgentID: agentID,
		Scope:         "private",
		Concepts:      concepts,
		Content:       content,
	})
}

// RememberInTx writes a private episodic memory inside an existing SQLite write transaction.
func (m *Memory) RememberInTx(ctx context.Context, tx app.Tx, agentID, convID domain.ID, content string) error {
	stx, ok := tx.(*sqliteTx)
	if !ok {
		return m.RememberEntry(ctx, MemoryEntry{
			AuthorAgentID:  agentID,
			Scope:          "private",
			ConversationID: convID,
			Content:        content,
		})
	}
	var teamID domain.ID
	if ag, found, _ := stx.GetAgent(ctx, agentID); found {
		teamID = ag.TeamID
	}
	return m.insertWithRunner(ctx, stx.q, MemoryEntry{
		AuthorAgentID:  agentID,
		TeamID:         teamID,
		Scope:          "private",
		ConversationID: convID,
		Content:        content,
	})
}

// RememberEntry writes e into episodic_memories and episodic_fts (with rowid = seq_id) inside a single transaction.
func (m *Memory) RememberEntry(ctx context.Context, e MemoryEntry) error {
	return m.store.WithTx(ctx, func(tx app.Tx) error {
		stx := tx.(*sqliteTx)
		if e.TeamID == "" {
			if ag, found, _ := stx.GetAgent(ctx, e.AuthorAgentID); found {
				e.TeamID = ag.TeamID
			}
		}
		return m.insertWithRunner(ctx, stx.q, e)
	})
}

func (m *Memory) insertWithRunner(ctx context.Context, q sqlRunner, e MemoryEntry) error {
	clean := strings.TrimSpace(platform.Redact(e.Content))
	if clean == "" {
		return nil
	}
	id := e.ID
	if id == "" {
		id = m.ids.New()
	}
	scope := e.Scope
	if scope != "team" {
		scope = "private"
	}
	kind := e.Kind
	if kind == "" {
		kind = "episode"
	}
	conceptsBytes, _ := json.Marshal(e.Concepts)
	if len(e.Concepts) == 0 {
		conceptsBytes = []byte("[]")
	}
	nowMs := m.clock.Now().UnixMilli()

	res, err := q.ExecContext(ctx, `
		INSERT INTO episodic_memories (id, author_agent_id, team_id, scope, conversation_id, kind, concepts, content, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, e.AuthorAgentID, e.TeamID, scope, e.ConversationID, kind, string(conceptsBytes), clean, nowMs,
	)
	if err != nil {
		return fmt.Errorf("insert episodic_memories: %w", err)
	}
	seqID, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("episodic seq_id: %w", err)
	}

	scopeTok := agentToken(e.AuthorAgentID)
	if scope == "team" && e.TeamID != "" {
		scopeTok += " " + teamToken(e.TeamID)
	}
	if _, err := q.ExecContext(ctx, `
		INSERT INTO episodic_fts (rowid, scope_tok, content_norm) VALUES (?, ?, ?)`,
		seqID, scopeTok, foldVNForFTS(clean),
	); err != nil {
		return fmt.Errorf("insert episodic_fts: %w", err)
	}
	return nil
}

// Search executes a safe BM25-ranked FTS5 query scoped strictly to agentID's private and shared team memories.
func (m *Memory) Search(ctx context.Context, agentID domain.ID, query string, limit int) ([]app.MemoryHit, error) {
	if limit <= 0 {
		limit = 5
	}
	if limit > 50 {
		limit = 50
	}
	tokens := safeFTSTokens(query)
	if len(tokens) == 0 {
		return nil, nil
	}

	var teamID domain.ID
	if ag, found, err := m.store.Read().GetAgent(ctx, agentID); err == nil && found {
		teamID = ag.TeamID
	}

	scopeExpr := fmt.Sprintf(`scope_tok : "%s"`, agentToken(agentID))
	if teamID != "" {
		scopeExpr = fmt.Sprintf(`scope_tok : ("%s" OR "%s")`, agentToken(agentID), teamToken(teamID))
	}
	quoted := make([]string, len(tokens))
	for i, tok := range tokens {
		quoted[i] = `"` + tok + `"`
	}
	contentExpr := "content_norm : (" + strings.Join(quoted, " OR ") + ")"
	matchExpr := "(" + scopeExpr + ") AND (" + contentExpr + ")"

	rows, err := m.store.rdb.QueryContext(ctx, `
		SELECT m.id, m.author_agent_id, m.conversation_id, m.scope, m.content, bm25(episodic_fts) AS rank, m.created_at
		FROM episodic_fts f
		JOIN episodic_memories m ON m.seq_id = f.rowid
		WHERE episodic_fts MATCH ?
		  AND (m.author_agent_id = ? OR (m.scope = 'team' AND m.team_id = ? AND m.team_id <> ''))
		ORDER BY rank ASC, m.created_at DESC
		LIMIT ?`,
		matchExpr, agentID, teamID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("search episodic_fts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []app.MemoryHit
	for rows.Next() {
		var h app.MemoryHit
		var bm25Score float64
		if err := rows.Scan(&h.ID, &h.AuthorAgentID, &h.ConversationID, &h.Scope, &h.Content, &bm25Score, &h.CreatedAt); err != nil {
			return nil, err
		}
		h.Score = -bm25Score
		out = append(out, h)
	}
	return out, rows.Err()
}

// Prune deletes up to batchLimit memories older than cutoff or exceeding maxPerAgent by rowid/seq_id in O(log N).
func (m *Memory) Prune(ctx context.Context, cutoff time.Time, maxPerAgent, batchLimit int) (int, error) {
	if batchLimit <= 0 {
		batchLimit = 500
	}
	cutoffMs := cutoff.UnixMilli()
	var pruned int
	err := m.store.WithTx(ctx, func(tx app.Tx) error {
		stx := tx.(*sqliteTx)
		var seqIDs []int64
		rows, err := stx.q.QueryContext(ctx, `
			SELECT seq_id FROM (
				SELECT seq_id, created_at,
				       ROW_NUMBER() OVER (PARTITION BY author_agent_id ORDER BY created_at DESC, seq_id DESC) AS rn
				FROM episodic_memories
			)
			WHERE created_at < ? OR (? > 0 AND rn > ?)
			ORDER BY seq_id ASC
			LIMIT ?`, cutoffMs, maxPerAgent, maxPerAgent, batchLimit)
		if err != nil {
			return fmt.Errorf("query prune candidates: %w", err)
		}
		for rows.Next() {
			var sid int64
			if err := rows.Scan(&sid); err != nil {
				_ = rows.Close()
				return err
			}
			seqIDs = append(seqIDs, sid)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, sid := range seqIDs {
			if _, err := stx.q.ExecContext(ctx, `DELETE FROM episodic_fts WHERE rowid = ?`, sid); err != nil {
				return fmt.Errorf("delete episodic_fts rowid %d: %w", sid, err)
			}
			if _, err := stx.q.ExecContext(ctx, `DELETE FROM episodic_memories WHERE seq_id = ?`, sid); err != nil {
				return fmt.Errorf("delete episodic_memories seq_id %d: %w", sid, err)
			}
			pruned++
		}
		return nil
	})
	return pruned, err
}

// CountsForTest returns row counts in episodic_memories and episodic_fts for verification.
func (m *Memory) CountsForTest(ctx context.Context) (tableCount, ftsCount int, err error) {
	if err := m.store.rdb.QueryRowContext(ctx, `SELECT COUNT(*) FROM episodic_memories`).Scan(&tableCount); err != nil {
		return 0, 0, err
	}
	if err := m.store.rdb.QueryRowContext(ctx, `SELECT COUNT(*) FROM episodic_fts`).Scan(&ftsCount); err != nil {
		return 0, 0, err
	}
	return tableCount, ftsCount, nil
}

func agentToken(id domain.ID) string {
	return "ag" + hex.EncodeToString([]byte(id))
}

func teamToken(id domain.ID) string {
	return "tm" + hex.EncodeToString([]byte(id))
}

func foldVNForFTS(s string) string {
	return strings.NewReplacer("đ", "d", "Đ", "D").Replace(s)
}

func safeFTSTokens(q string) []string {
	folded := foldVNForFTS(q)
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range folded {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur.WriteRune(unicode.ToLower(r))
		} else {
			flush()
		}
	}
	flush()
	if len(out) > 24 {
		out = out[:24]
	}
	return out
}
