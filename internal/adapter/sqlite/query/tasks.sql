-- Canonical SQL queries for multi-agent task coordination (Phase 2).

-- name: CheckoutTaskAtomic :execrows
UPDATE tasks
SET status = 'in_progress',
    checkout_run_id = ?,
    updated_at = ?
WHERE id = ?
  AND assignee_agent_id = ?
  AND status IN ('todo', 'in_progress')
  AND checkout_run_id IS NULL;

-- name: InsertTaskBlocker :exec
INSERT OR IGNORE INTO task_blockers (task_id, blocked_by)
VALUES (?, ?);

-- name: ListDependents :many
SELECT t.*
FROM tasks t
JOIN task_blockers tb ON tb.task_id = t.id
WHERE tb.blocked_by = ?;

-- name: ListChildTasks :many
SELECT *
FROM tasks
WHERE parent_id = ?
ORDER BY created_at ASC, id ASC;

-- name: InsertTaskEvent :exec
INSERT INTO task_events (id, task_id, kind, actor_kind, actor_id, target_agent_id, body, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);
