-- name: CreateChannel :execlastid
INSERT INTO channels(name, repo_abs_path, repo_remote, repo_head_sha, repo_head_branch, is_orphaned)
VALUES(?, ?, ?, ?, ?, ?);

-- name: ListChannelsAll :many
SELECT id, name,
       COALESCE(repo_abs_path, '') AS repo_abs_path,
       COALESCE(repo_remote, '') AS repo_remote,
       COALESCE(repo_head_sha, '') AS repo_head_sha,
       COALESCE(repo_head_branch, '') AS repo_head_branch,
       is_orphaned,
       COALESCE(created_at, '') AS created_at
FROM channels
ORDER BY id;

-- name: ListChannelsByRepo :many
SELECT id, name,
       COALESCE(repo_abs_path, '') AS repo_abs_path,
       COALESCE(repo_remote, '') AS repo_remote,
       COALESCE(repo_head_sha, '') AS repo_head_sha,
       COALESCE(repo_head_branch, '') AS repo_head_branch,
       is_orphaned,
       COALESCE(created_at, '') AS created_at
FROM channels
WHERE repo_abs_path = ?
ORDER BY id;

-- name: ListChannelsNonOrphaned :many
SELECT id, name,
       COALESCE(repo_abs_path, '') AS repo_abs_path,
       COALESCE(repo_remote, '') AS repo_remote,
       COALESCE(repo_head_sha, '') AS repo_head_sha,
       COALESCE(repo_head_branch, '') AS repo_head_branch,
       is_orphaned,
       COALESCE(created_at, '') AS created_at
FROM channels
WHERE is_orphaned = 0
ORDER BY id;

-- name: ListChannelsByRepoNonOrphaned :many
SELECT id, name,
       COALESCE(repo_abs_path, '') AS repo_abs_path,
       COALESCE(repo_remote, '') AS repo_remote,
       COALESCE(repo_head_sha, '') AS repo_head_sha,
       COALESCE(repo_head_branch, '') AS repo_head_branch,
       is_orphaned,
       COALESCE(created_at, '') AS created_at
FROM channels
WHERE repo_abs_path = ? AND is_orphaned = 0
ORDER BY id;
