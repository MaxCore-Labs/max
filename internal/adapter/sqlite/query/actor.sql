-- name: CountAgentQueuedWakeups :one
SELECT COUNT(*) FROM wakeups
WHERE agent_id = ? AND status = 'queued';

-- name: FindActiveWakeupByIdem :one
SELECT id FROM wakeups
WHERE idempotency_key = ? AND status IN ('queued', 'claimed')
LIMIT 1;

-- name: SetAgentReadyState :exec
UPDATE agents
SET ready = ?, ready_priority = ?, ready_since = ?
WHERE id = ?;
