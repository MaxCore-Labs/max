-- +goose Up

CREATE TABLE teams (
  id TEXT PRIMARY KEY,
  slug TEXT UNIQUE NOT NULL,
  name TEXT NOT NULL,
  mandate TEXT NOT NULL DEFAULT '{}',
  max_running INTEGER NOT NULL DEFAULT 2,
  running INTEGER NOT NULL DEFAULT 0 CHECK (running >= 0),
  quota_tokens_day INTEGER,
  status TEXT NOT NULL DEFAULT 'active'
);

CREATE TABLE agents (
  id TEXT PRIMARY KEY,
  slug TEXT UNIQUE NOT NULL,
  name TEXT NOT NULL,
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
  skills TEXT NOT NULL DEFAULT '[]',
  params TEXT NOT NULL DEFAULT '{}',
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
  task_id TEXT,
  thread_id TEXT,
  sessions TEXT NOT NULL DEFAULT '{}',
  last_engine TEXT,
  waiting_for TEXT,
  turns_used INTEGER NOT NULL DEFAULT 0,
  closed_at INTEGER,
  UNIQUE (agent_id, task_id),
  UNIQUE (agent_id, thread_id)
);

CREATE TABLE wakeups (
  id TEXT NOT NULL,
  agent_id TEXT NOT NULL REFERENCES agents(id),
  conversation_id TEXT REFERENCES conversations(id),
  kind TEXT NOT NULL,
  priority INTEGER NOT NULL,
  payload TEXT NOT NULL,
  idempotency_key TEXT,
  status TEXT NOT NULL DEFAULT 'queued'
         CHECK (status IN ('queued','claimed','done','dropped')),
  run_id TEXT,
  coalesced_count INTEGER NOT NULL DEFAULT 0,
  attempts INTEGER NOT NULL DEFAULT 0,
  cause_run_id TEXT,
  hops INTEGER NOT NULL DEFAULT 0,
  not_before INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (agent_id, id)
) WITHOUT ROWID;
CREATE INDEX wakeups_due ON wakeups (not_before) WHERE status = 'queued' AND not_before > 0;
CREATE UNIQUE INDEX wakeups_idem ON wakeups (idempotency_key)
  WHERE idempotency_key IS NOT NULL AND status IN ('queued','claimed');
CREATE INDEX wakeups_mailbox ON wakeups (agent_id, priority DESC, created_at, conversation_id, not_before, attempts, id)
  WHERE status = 'queued';
CREATE INDEX wakeups_run ON wakeups (run_id)
  WHERE status = 'claimed';

CREATE TABLE goals (
  id TEXT PRIMARY KEY,
  title TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'active',
  created_at INTEGER NOT NULL
);

CREATE TABLE tasks (
  id TEXT PRIMARY KEY,
  goal_id TEXT REFERENCES goals(id),
  parent_id TEXT REFERENCES tasks(id),
  root_id TEXT NOT NULL,
  root_origin TEXT NOT NULL CHECK (root_origin IN ('owner','routine','agent','external')),
  depth INTEGER NOT NULL DEFAULT 0 CHECK (depth <= 5),
  title TEXT NOT NULL,
  description TEXT NOT NULL,
  done_criteria TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'todo' CHECK (status IN
         ('todo','in_progress','blocked','waiting_owner','in_review','done','cancelled')),
  blocked_reason TEXT CHECK (blocked_reason IN ('dependency','stalled','error','needs_owner')),
  assignee_agent_id TEXT REFERENCES agents(id),
  created_by_agent TEXT REFERENCES agents(id),
  checkout_run_id TEXT,
  needs_review INTEGER NOT NULL DEFAULT 0,
  reviewer_agent_id TEXT REFERENCES agents(id),
  review_rounds INTEGER NOT NULL DEFAULT 0,
  idle_runs INTEGER NOT NULL DEFAULT 0,
  due_at INTEGER,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  CHECK (reviewer_agent_id IS NULL OR reviewer_agent_id <> assignee_agent_id)
);
CREATE INDEX tasks_assignee ON tasks (assignee_agent_id, status);
CREATE INDEX tasks_parent ON tasks (parent_id);
CREATE INDEX tasks_goal ON tasks (goal_id, status);

CREATE TABLE task_blockers (
  task_id TEXT NOT NULL REFERENCES tasks(id),
  blocked_by TEXT NOT NULL REFERENCES tasks(id),
  PRIMARY KEY (task_id, blocked_by)
);

CREATE TABLE runs (
  id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL REFERENCES agents(id),
  conversation_id TEXT NOT NULL REFERENCES conversations(id),
  task_id TEXT,
  status TEXT NOT NULL CHECK (status IN
         ('running','succeeded','failed','cancelled','timed_out','turn_limit',
          'parked','quota_stopped','interrupted')),
  priority INTEGER NOT NULL,
  engine TEXT NOT NULL,
  model TEXT NOT NULL,
  pgid INTEGER,
  attempt INTEGER NOT NULL DEFAULT 1,
  turns INTEGER NOT NULL DEFAULT 0,
  input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  cached_read_tokens INTEGER NOT NULL DEFAULT 0,
  thought_tokens INTEGER NOT NULL DEFAULT 0,
  notional_microusd INTEGER NOT NULL DEFAULT 0,
  touched_paths TEXT NOT NULL DEFAULT '[]',
  error TEXT,
  summary TEXT,
  vault_commit TEXT,
  started_at INTEGER NOT NULL,
  ended_at INTEGER
);
CREATE INDEX runs_agent ON runs (agent_id, started_at DESC);
CREATE INDEX runs_active ON runs (status) WHERE ended_at IS NULL;

CREATE TABLE tool_calls (
  run_id TEXT NOT NULL,
  tool_use_id TEXT NOT NULL,
  conversation_id TEXT,
  tool_name TEXT NOT NULL,
  class TEXT NOT NULL,
  target_value TEXT,
  input_json TEXT,
  idempotency_key TEXT,
  status TEXT NOT NULL CHECK (status IN ('started','done','failed','ambiguous')),
  result TEXT,
  started_at INTEGER NOT NULL,
  ended_at INTEGER,
  PRIMARY KEY (run_id, tool_use_id)
);
CREATE UNIQUE INDEX tool_calls_idem ON tool_calls (idempotency_key)
  WHERE idempotency_key IS NOT NULL AND status IN ('started','done','ambiguous');
CREATE INDEX tool_calls_conv_target ON tool_calls (conversation_id, tool_name, target_value)
  WHERE status = 'done';

CREATE TABLE approvals (
  id TEXT PRIMARY KEY,
  run_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  conversation_id TEXT NOT NULL,
  tool_name TEXT NOT NULL,
  input TEXT NOT NULL,
  input_hash TEXT NOT NULL,
  preview TEXT NOT NULL,
  reason TEXT NOT NULL,
  by_max INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL CHECK (status IN ('pending','allowed','denied','expired','consumed')),
  decision_key TEXT UNIQUE,
  requested_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  decided_at INTEGER
);
CREATE INDEX approvals_pending ON approvals (agent_id, tool_name) WHERE status = 'pending';

CREATE TABLE grants (
  id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL REFERENCES agents(id),
  connection TEXT NOT NULL,
  tool TEXT NOT NULL,
  constraints TEXT NOT NULL DEFAULT '{}',
  allowed_root_origins TEXT NOT NULL DEFAULT '["owner","routine"]',
  max_per_day INTEGER,
  used_today INTEGER NOT NULL DEFAULT 0,
  used_day TEXT NOT NULL DEFAULT '',
  expires_at INTEGER,
  revoked_at INTEGER,
  created_at INTEGER NOT NULL
);

CREATE TABLE quota_ledger (
  engine TEXT NOT NULL,
  window TEXT NOT NULL CHECK (window IN ('short','week')),
  window_start INTEGER NOT NULL,
  tokens INTEGER NOT NULL DEFAULT 0,
  runs INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (engine, window, window_start)
);

CREATE TABLE quota_state (
  engine TEXT PRIMARY KEY,
  est_short_tokens INTEGER,
  est_week_tokens INTEGER,
  limited_until INTEGER,
  last_limit_at INTEGER,
  probe_attempts INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE usage_daily (
  day TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  team_id TEXT NOT NULL,
  engine TEXT NOT NULL,
  runs INTEGER NOT NULL DEFAULT 0,
  tokens INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (day, agent_id, engine)
);

CREATE TABLE leases (
  name TEXT NOT NULL,
  slot INTEGER NOT NULL,
  holder_run_id TEXT,
  acquired_at INTEGER,
  expires_at INTEGER,
  PRIMARY KEY (name, slot)
);

CREATE TABLE outbox (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  topic TEXT NOT NULL,
  payload TEXT NOT NULL,
  dedupe_key TEXT UNIQUE,
  urgency TEXT NOT NULL DEFAULT 'normal',
  attempts INTEGER NOT NULL DEFAULT 0,
  next_attempt_at INTEGER NOT NULL,
  sent_at INTEGER
);
CREATE INDEX outbox_due ON outbox (next_attempt_at) WHERE sent_at IS NULL;

CREATE TABLE threads (
  id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL REFERENCES agents(id),
  title TEXT NOT NULL,
  learned_through_msg TEXT,
  last_msg_at INTEGER,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX threads_agent ON threads (agent_id, updated_at DESC);

CREATE TABLE messages (
  id TEXT PRIMARY KEY,
  thread_id TEXT NOT NULL REFERENCES threads(id),
  role TEXT NOT NULL CHECK (role IN ('user','assistant','system')),
  content TEXT NOT NULL,
  attachments TEXT NOT NULL DEFAULT '[]',
  run_id TEXT,
  idempotency_key TEXT UNIQUE,
  created_at INTEGER NOT NULL
);
CREATE INDEX messages_thread ON messages (thread_id, created_at DESC, id DESC);

CREATE TABLE task_events (
  id TEXT PRIMARY KEY,
  task_id TEXT NOT NULL REFERENCES tasks(id),
  kind TEXT NOT NULL,
  actor_kind TEXT NOT NULL,
  actor_id TEXT,
  target_agent_id TEXT,
  body TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX task_events_task ON task_events (task_id, created_at, id);
CREATE INDEX task_events_pair ON task_events (task_id, actor_id, target_agent_id, created_at);

CREATE TABLE run_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  type TEXT NOT NULL,
  data TEXT NOT NULL DEFAULT '{}',
  created_at INTEGER NOT NULL
);
CREATE INDEX run_events_run ON run_events (run_id, id);

CREATE TABLE inbox_items (
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL,
  title TEXT NOT NULL,
  body TEXT NOT NULL DEFAULT '',
  ref_kind TEXT,
  ref_id TEXT,
  conversation_id TEXT,
  agent_id TEXT,
  choices TEXT NOT NULL DEFAULT '[]',
  urgency TEXT NOT NULL DEFAULT 'normal',
  dedupe_key TEXT UNIQUE,
  read_at INTEGER,
  resolved_at INTEGER,
  created_at INTEGER NOT NULL
);
CREATE INDEX inbox_unread ON inbox_items (created_at DESC) WHERE read_at IS NULL;

CREATE TABLE proposals (
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL CHECK (kind IN ('skill','routine','agent','instruction_edit')),
  agent_id TEXT,
  target_path TEXT,
  title TEXT NOT NULL,
  payload TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','rejected')),
  decided_at INTEGER,
  created_at INTEGER NOT NULL
);

CREATE TABLE routine_state (
  slug TEXT PRIMARY KEY,
  last_scheduled_min INTEGER,
  last_fired_at INTEGER,
  active_task_id TEXT,
  consecutive_failures INTEGER NOT NULL DEFAULT 0,
  auto_disabled INTEGER NOT NULL DEFAULT 0,
  bad_file_reported INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL
);

CREATE TABLE connections (
  name TEXT PRIMARY KEY,
  kind TEXT NOT NULL,
  config BLOB NOT NULL,
  tools TEXT NOT NULL DEFAULT '[]',
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE users (
  id TEXT PRIMARY KEY,
  username TEXT UNIQUE NOT NULL,
  password_hash TEXT NOT NULL,
  totp_secret BLOB,
  totp_enabled INTEGER NOT NULL DEFAULT 0,
  totp_pending BLOB,
  totp_failed_attempts INTEGER NOT NULL DEFAULT 0,
  totp_locked_until INTEGER,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE auth_sessions (
  token_hash TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id),
  expires_at INTEGER NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX auth_sessions_exp ON auth_sessions (expires_at);

CREATE TABLE kv (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE audit_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  ts INTEGER NOT NULL,
  actor TEXT NOT NULL,
  action TEXT NOT NULL,
  ref TEXT NOT NULL DEFAULT '',
  detail TEXT NOT NULL DEFAULT '{}'
);

CREATE TABLE engine_binaries (
  engine TEXT PRIMARY KEY,
  path TEXT NOT NULL,
  sha256 TEXT NOT NULL,
  version TEXT NOT NULL,
  confirmed_at INTEGER NOT NULL
);

-- +goose Down
DROP TABLE IF EXISTS engine_binaries;
DROP TABLE IF EXISTS audit_log;
DROP TABLE IF EXISTS kv;
DROP TABLE IF EXISTS auth_sessions;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS connections;
DROP TABLE IF EXISTS routine_state;
DROP TABLE IF EXISTS proposals;
DROP TABLE IF EXISTS inbox_items;
DROP TABLE IF EXISTS run_events;
DROP TABLE IF EXISTS task_events;
DROP TABLE IF EXISTS messages;
DROP TABLE IF EXISTS threads;
DROP TABLE IF EXISTS outbox;
DROP TABLE IF EXISTS leases;
DROP TABLE IF EXISTS usage_daily;
DROP TABLE IF EXISTS quota_state;
DROP TABLE IF EXISTS quota_ledger;
DROP TABLE IF EXISTS grants;
DROP TABLE IF EXISTS approvals;
DROP TABLE IF EXISTS tool_calls;
DROP TABLE IF EXISTS runs;
DROP TABLE IF EXISTS task_blockers;
DROP TABLE IF EXISTS tasks;
DROP TABLE IF EXISTS goals;
DROP TABLE IF EXISTS wakeups;
DROP TABLE IF EXISTS conversations;
DROP TABLE IF EXISTS agents;
DROP TABLE IF EXISTS teams;
