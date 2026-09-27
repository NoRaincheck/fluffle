-- name: GetLastSessionEventSeq :one
SELECT seq FROM agent_session_events WHERE session_id = ? ORDER BY seq DESC LIMIT 1;

-- name: InsertSessionEvent :execlastid
INSERT INTO agent_session_events(session_id, seq, type, content)
VALUES(?, ?, ?, ?);

-- name: ListSessionEvents :many
SELECT id, session_id, seq, type, content, COALESCE(created_at, '') AS created_at
FROM agent_session_events
WHERE session_id = ?
ORDER BY seq ASC;
