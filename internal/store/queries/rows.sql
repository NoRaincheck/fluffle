-- name: ListMessageRows :many
SELECT m.id AS id,
       m.thread_id AS thread_id,
       COALESCE(m.created_at, '') AS created_at,
       c.name AS channel,
       t.title AS thread,
       m.name AS name,
       m.content AS content
FROM messages m
JOIN threads t ON t.id = m.thread_id
JOIN channels c ON c.id = t.channel_id
ORDER BY m.created_at DESC, m.id DESC
LIMIT ?;

-- name: ListThreadRows :many
SELECT t.id AS id,
       t.id AS thread_id,
       COALESCE((SELECT m.created_at FROM messages m
                 WHERE m.thread_id = t.id
                 ORDER BY m.created_at DESC, m.id DESC LIMIT 1), t.created_at) AS created_at,
       c.name AS channel,
       t.title AS thread,
       CAST(COALESCE((SELECT m.name FROM messages m
                      WHERE m.thread_id = t.id
                      ORDER BY m.created_at DESC, m.id DESC LIMIT 1), '') AS TEXT) AS name,
       CAST(COALESCE((SELECT m.content FROM messages m
                      WHERE m.thread_id = t.id
                      ORDER BY m.seq ASC LIMIT 1), '') AS TEXT) AS content,
       (SELECT COUNT(*) FROM messages m WHERE m.thread_id = t.id) AS msg_count
FROM threads t
JOIN channels c ON c.id = t.channel_id
ORDER BY created_at DESC, t.id DESC
LIMIT ?;

-- name: ListChannelRows :many
SELECT c.id AS id,
       0 AS thread_id,
       COALESCE((SELECT m.created_at FROM messages m JOIN threads t2 ON t2.id = m.thread_id
                 WHERE t2.channel_id = c.id
                 ORDER BY m.created_at DESC, m.id DESC LIMIT 1), c.created_at) AS created_at,
       c.name AS channel,
       '' AS thread,
       CAST(COALESCE((SELECT m.name FROM messages m JOIN threads t2 ON t2.id = m.thread_id
                      WHERE t2.channel_id = c.id
                      ORDER BY m.created_at DESC, m.id DESC LIMIT 1), '') AS TEXT) AS name,
       CAST(COALESCE((SELECT m.content FROM messages m JOIN threads t2 ON t2.id = m.thread_id
                      WHERE t2.channel_id = c.id
                      ORDER BY m.created_at DESC, m.id DESC LIMIT 1), '') AS TEXT) AS content,
       (SELECT COUNT(*) FROM messages m JOIN threads t2 ON t2.id = m.thread_id
        WHERE t2.channel_id = c.id) AS msg_count
FROM channels c
ORDER BY created_at DESC, c.id DESC
LIMIT ?;
