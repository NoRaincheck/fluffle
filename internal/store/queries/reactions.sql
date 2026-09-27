-- name: InsertReaction :execlastid
INSERT INTO reactions(message_id, emoji, name, author_type, created_at)
VALUES(?, ?, ?, ?, ?);

-- name: InsertReactionDefaultCreatedAt :execlastid
INSERT INTO reactions(message_id, emoji, name, author_type)
VALUES(?, ?, ?, ?);

-- name: ListReactions :many
SELECT r.id, r.message_id, m.seq AS message_seq, r.emoji, r.name, r.author_type,
       COALESCE(r.created_at, '') AS created_at
FROM reactions r
JOIN messages m ON m.id = r.message_id
WHERE m.thread_id = ?
ORDER BY m.seq ASC, r.created_at ASC, r.id ASC;
