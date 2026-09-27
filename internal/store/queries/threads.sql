-- name: CreateThread :execlastid
INSERT INTO threads(channel_id, title) VALUES(?, ?);

-- name: ListThreads :many
SELECT id, channel_id, title, COALESCE(created_at, '') AS created_at
FROM threads
WHERE channel_id = ?
ORDER BY id;

-- name: CountThreadsByID :one
SELECT COUNT(*) FROM threads WHERE id = ?;
