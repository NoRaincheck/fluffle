-- name: InsertMessage :execlastid
INSERT INTO messages(thread_id, seq, parent_id, name, author_type, role, content, created_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?);

-- name: InsertMessageDefaultCreatedAt :execlastid
INSERT INTO messages(thread_id, seq, parent_id, name, author_type, role, content)
VALUES(?, ?, ?, ?, ?, ?, ?);

-- name: GetMessageMaxSeq :one
SELECT MAX(seq) FROM messages WHERE thread_id = ?;

-- name: GetMessageThreadIDByID :one
SELECT thread_id FROM messages WHERE id = ?;

-- name: GetMessageIDByThreadSeq :one
SELECT id FROM messages WHERE thread_id = ? AND seq = ?;

-- name: ListMessages :many
SELECT m.id, m.thread_id, m.seq, m.parent_id, p.seq AS parent_seq, m.name, m.author_type, m.role, m.content,
       COALESCE(m.created_at, '') AS created_at
FROM messages m
LEFT JOIN messages p ON p.id = m.parent_id
WHERE m.thread_id = ?
ORDER BY m.seq ASC;

-- name: ListMessagesLastN :many
SELECT * FROM (
  SELECT * FROM (
    SELECT m.id, m.thread_id, m.seq, m.parent_id, p.seq AS parent_seq, m.name, m.author_type, m.role, m.content,
           COALESCE(m.created_at, '') AS created_at
    FROM messages m
    LEFT JOIN messages p ON p.id = m.parent_id
    WHERE m.thread_id = ?
    ORDER BY m.seq ASC
  ) ORDER BY seq DESC LIMIT ?
) ORDER BY seq ASC;

-- name: ListMessagesAfter :many
SELECT m.id, m.thread_id, m.seq, m.parent_id, p.seq AS parent_seq, m.name, m.author_type, m.role, m.content,
       COALESCE(m.created_at, '') AS created_at
FROM messages m
LEFT JOIN messages p ON p.id = m.parent_id
WHERE m.thread_id = ? AND m.seq > ?
ORDER BY m.seq ASC;

-- name: ListInbox :many
SELECT m.id, m.thread_id, m.seq, m.parent_id, m.name, m.author_type, m.role, m.content,
       COALESCE(m.created_at, '') AS created_at,
       c.name AS channel_name, c.id AS channel_id, t.title AS thread_title
FROM messages m
JOIN threads t ON t.id = m.thread_id
JOIN channels c ON c.id = t.channel_id
ORDER BY m.created_at DESC, m.id DESC
LIMIT ?;

-- name: CountAgentMessagesAfter :one
SELECT COUNT(*) FROM messages
WHERE thread_id = ? AND name = ? AND author_type = 'agent' AND seq > ?;

-- name: GetMessageByIDWithParent :one
SELECT m.id, m.thread_id, m.seq, m.parent_id, p.seq AS parent_seq, m.name, m.author_type, m.role, m.content,
       COALESCE(m.created_at, '') AS created_at
FROM messages m
LEFT JOIN messages p ON p.id = m.parent_id
WHERE m.id = ?;
