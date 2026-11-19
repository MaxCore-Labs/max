-- Copied from docs/superpowers/specs/2026-11-13-max-assistant-design.md Section 9
CREATE TABLE teams (
  id TEXT PRIMARY KEY, slug TEXT UNIQUE NOT NULL, name TEXT NOT NULL,
  mandate TEXT NOT NULL DEFAULT '{}',
  max_running INTEGER NOT NULL DEFAULT 2,
  running INTEGER NOT NULL DEFAULT 0 CHECK (running >= 0),
  quota_tokens_day INTEGER,
  status TEXT NOT NULL DEFAULT 'active'
);

CREATE TABLE agents (
  id TEXT PRIMARY KEY, slug TEXT UNIQUE NOT NULL, name TEXT NOT NULL,
  template_slug TEXT,
  team_id TEXT NOT NULL REFERENCES teams(id),
  manager_agent_id TEXT REFERENCES agents(id),
  engines TEXT NOT NULL DEFAULT '["agy"]',
  engine_mask INTEGER NOT NULL DEFAULT 4,
  allow_ungated_engine INTEGER NOT NULL DEFAULT 0,
  models TEXT NOT NULL DEFAULT '{}',
  scope_violations INTEGER NOT NULL DEFAULT 0,
  autonomy TEXT NOT NULL DEFAULT 'auto' CHECK (autonomy IN ('suggest','auto')),
  web INTEGER NOT NULL DEFAULT 0,
  browser TEXT NOT NULL DEFAULT 'none' CHECK (browser IN ('none','anon','owner')),
  shell INTEGER NOT NULL DEFAULT 0,
  skills TEXT NOT NULL DEFAULT '[]', params TEXT NOT NULL DEFAULT '{}',
  quota_tokens_day INTEGER,
  config_rev TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'active'
         CHECK (status IN ('active','paused','quota_paused','archived')),
  ready INTEGER NOT NULL DEFAULT 0,
  ready_priority INTEGER NOT NULL DEFAULT 0,
  ready_since INTEGER,
  active_run_id TEXT,
  cooldown_until INTEGER NOT NULL DEFAULT 0,
  last_run_at INTEGER,
  next_heartbeat_at INTEGER
);
CREATE INDEX agents_ready ON agents (ready_priority DESC, ready_since)
  WHERE ready = 1 AND active_run_id IS NULL AND status = 'active';
CREATE INDEX agents_heartbeat ON agents (next_heartbeat_at) WHERE next_heartbeat_at IS NOT NULL;
CREATE INDEX agents_team ON agents (team_id, status);

CREATE TABLE conversations (
  id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL REFERENCES agents(id),
  task_id TEXT, thread_id TEXT,
  sessions TEXT NOT NULL DEFAULT '{}',
  last_engine TEXT,
  waiting_for TEXT,
  turns_used INTEGER NOT NULL DEFAULT 0,
  closed_at INTEGER,
  UNIQUE (agent_id, task_id), UNIQUE (agent_id, thread_id)
);

CREATE TABLE wakeups (
  id TEXT NOT NULL,
  agent_id TEXT NOT NULL REFERENCES agents(id),
  conversation_id TEXT REFERENCES conversations(id),
  kind TEXT NOT NULL, priority INTEGER NOT NULL,
  payload TEXT NOT NULL,
  idempotency_key TEXT,
  status TEXT NOT NULL DEFAULT 'queued'
         CHECK (status IN ('queued','claimed','done','dropped')),
  run_id TEXT,
  coalesced_count INTEGER NOT NULL DEFAULT 0,
  attempts INTEGER NOT NULL DEFAULT 0,
  cause_run_id TEXT, hops INTEGER NOT NULL DEFAULT 0,
  not_before INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (agent_id, id)
) WITHOUT ROWID;
CREATE INDEX wakeups_due ON wakeups (not_before) WHERE status = 'queued' AND not_before > 0;
CREATE UNIQUE INDEX wakeups_idem ON wakeups (idempotency_key)
  WHERE idempotency_key IS NOT NULL AND status IN ('queued','claimed');
CREATE INDEX wakeups_mailbox ON wakeups (agent_id, priority DESC, created_at, conversation_id, not_before, attempts, id)
  WHERE status = 'queued';
CREATE INDEX wakeups_run ON wakeups (run_id, agent_id)
  WHERE status = 'claimed';

CREATE TABLE runs (
  id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL REFERENCES agents(id),
  conversation_id TEXT NOT NULL REFERENCES conversations(id),
  task_id TEXT,
  status TEXT NOT NULL CHECK (status IN
         ('running','succeeded','failed','cancelled','timed_out','turn_limit',
          'parked','quota_stopped','interrupted')),
  priority INTEGER NOT NULL, engine TEXT NOT NULL, model TEXT NOT NULL,
  pgid INTEGER,
  attempt INTEGER NOT NULL DEFAULT 1,
  turns INTEGER NOT NULL DEFAULT 0,
  input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0,
  notional_microusd INTEGER NOT NULL DEFAULT 0,
  touched_paths TEXT NOT NULL DEFAULT '[]',
  error TEXT, summary TEXT, vault_commit TEXT,
  started_at INTEGER NOT NULL, ended_at INTEGER
);
CREATE INDEX runs_agent ON runs (agent_id, started_at DESC);
CREATE INDEX runs_active ON runs (status) WHERE ended_at IS NULL;
