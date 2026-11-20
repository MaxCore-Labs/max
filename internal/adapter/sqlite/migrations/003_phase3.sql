-- +goose Up

CREATE TABLE episodic_memories (
  seq_id INTEGER PRIMARY KEY AUTOINCREMENT,
  id TEXT NOT NULL UNIQUE,
  author_agent_id TEXT NOT NULL,
  team_id TEXT NOT NULL DEFAULT '',
  scope TEXT NOT NULL DEFAULT 'private' CHECK (scope IN ('private','team')),
  conversation_id TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL DEFAULT 'episode',
  concepts TEXT NOT NULL DEFAULT '[]',
  content TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX episodic_memories_author_created ON episodic_memories (author_agent_id, created_at DESC, seq_id DESC);
CREATE INDEX episodic_memories_created ON episodic_memories (created_at, seq_id);

CREATE VIRTUAL TABLE episodic_fts USING fts5(
  scope_tok,
  content_norm,
  tokenize = 'unicode61 remove_diacritics 2'
);

-- +goose Down
DROP TABLE IF EXISTS episodic_fts;
DROP TABLE IF EXISTS episodic_memories;
