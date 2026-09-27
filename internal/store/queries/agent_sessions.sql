-- name: CreateSession :execlastid
INSERT INTO agent_sessions(thread_id, trigger_message_id, agent_name, status, reply_mode, command, cwd)
VALUES(?, ?, ?, ?, ?, ?, ?);

-- name: ListSessions :many
SELECT id, thread_id, trigger_message_id, agent_name, status, reply_mode, command,
       cwd, exit_code, error, reply_message_id, started_at, finished_at, created_at
FROM agent_sessions
WHERE thread_id = ?
ORDER BY id ASC;

-- name: GetSession :one
SELECT id, thread_id, trigger_message_id, agent_name, status, reply_mode, command,
       cwd, exit_code, error, reply_message_id, started_at, finished_at, created_at
FROM agent_sessions
WHERE id = ?;

-- name: MarkSessionRunning :execrows
UPDATE agent_sessions SET status = ?, started_at = ? WHERE id = ? AND status = ?;

-- name: SetSessionReply :execrows
UPDATE agent_sessions SET reply_message_id = ? WHERE id = ?;

-- name: FinishSession :execrows
UPDATE agent_sessions SET status = ?, exit_code = ?, error = ?, finished_at = ? WHERE id = ?;

-- name: CountSessionsByID :one
SELECT COUNT(*) FROM agent_sessions WHERE id = ?;

-- name: ReconcileSessions :execrows
UPDATE agent_sessions
SET status = ?, finished_at = ?, error = 'daemon restarted while ' || status
WHERE status IN (?, ?);

-- name: GetThreadContext :one
SELECT t.id AS thread_id, t.title AS thread_title, c.id AS channel_id, c.name AS channel_name, c.repo_abs_path
FROM threads t
JOIN channels c ON c.id = t.channel_id
WHERE t.id = ?;
