package store

import (
	"database/sql"
	"errors"
	"strings"

	_ "modernc.org/sqlite"
)

var ErrConflict = errors.New("conflict")
var ErrNotFound = errors.New("not found")

const schema = `
CREATE TABLE IF NOT EXISTS channels(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL CHECK(length(trim(name)) > 0),
  repo_abs_path TEXT,
  repo_remote TEXT,
  repo_head_sha TEXT,
  is_orphaned INTEGER NOT NULL DEFAULT 0 CHECK(is_orphaned IN (0,1)),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  archived_at TEXT,
  CHECK ((is_orphaned = 1 AND repo_abs_path IS NULL)
      OR (is_orphaned = 0 AND repo_abs_path IS NOT NULL)),
  UNIQUE(name, repo_abs_path)
);
CREATE UNIQUE INDEX IF NOT EXISTS uniq_orphan_channel_name ON channels(name) WHERE is_orphaned = 1;
CREATE TABLE IF NOT EXISTS threads(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  channel_id INTEGER NOT NULL REFERENCES channels(id),
  title TEXT NOT NULL CHECK(length(trim(title)) > 0),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  archived_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_threads_channel ON threads(channel_id, id);
CREATE TABLE IF NOT EXISTS messages(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  thread_id INTEGER NOT NULL REFERENCES threads(id),
  seq INTEGER NOT NULL,
  author TEXT NOT NULL CHECK(length(trim(author)) > 0),
  author_type TEXT NOT NULL CHECK(author_type IN ('human','agent')),
  role TEXT NOT NULL CHECK(role IN ('user','assistant','system')),
  content TEXT NOT NULL CHECK(length(trim(content)) > 0),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  UNIQUE(thread_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_messages_thread_seq ON messages(thread_id, seq);
CREATE TABLE IF NOT EXISTS reactions(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  message_id INTEGER NOT NULL REFERENCES messages(id),
  emoji TEXT NOT NULL CHECK(length(trim(emoji)) > 0),
  author TEXT NOT NULL CHECK(length(trim(author)) > 0),
  author_type TEXT NOT NULL CHECK(author_type IN ('human','agent')),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  UNIQUE(message_id, emoji, author)
);
`

type Store struct{ db *sql.DB }

type Channel struct {
	ID                                                    int64
	Name, RepoAbsPath, RepoRemote, RepoHeadSHA, CreatedAt string
	IsOrphaned                                            bool
	ArchivedAt                                            *string
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) CreateChannel(name, repoAbsPath, repoRemote, repoHeadSHA string, orphaned bool) (int64, error) {
	if strings.TrimSpace(name) == "" {
		return 0, errors.New("channel name required")
	}
	var abs any
	var isOrphan int
	if orphaned {
		if repoAbsPath != "" {
			return 0, errors.New("orphaned channel must not have repo path")
		}
		abs = nil
		isOrphan = 1
	} else {
		if strings.TrimSpace(repoAbsPath) == "" {
			return 0, errors.New("repo path required")
		}
		abs = repoAbsPath
	}
	res, err := s.db.Exec(`INSERT INTO channels(name, repo_abs_path, repo_remote, repo_head_sha, is_orphaned) VALUES(?,?,?,?,?)`, name, abs, nullIfEmpty(repoRemote), nullIfEmpty(repoHeadSHA), isOrphan)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return 0, ErrConflict
		}
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) ListChannels(repoAbsPath string, includeOrphaned bool) ([]Channel, error) {
	q := `SELECT id, name, COALESCE(repo_abs_path,''), COALESCE(repo_remote,''), COALESCE(repo_head_sha,''), is_orphaned, COALESCE(created_at,''), archived_at FROM channels WHERE archived_at IS NULL`
	args := []any{}
	if repoAbsPath != "" {
		q += ` AND repo_abs_path = ?`
		args = append(args, repoAbsPath)
	}
	if !includeOrphaned {
		q += ` AND is_orphaned = 0`
	}
	q += ` ORDER BY id`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Channel
	for rows.Next() {
		var c Channel
		var isOrphan int
		if err := rows.Scan(&c.ID, &c.Name, &c.RepoAbsPath, &c.RepoRemote, &c.RepoHeadSHA, &isOrphan, &c.CreatedAt, &c.ArchivedAt); err != nil {
			return nil, err
		}
		c.IsOrphaned = isOrphan == 1
		out = append(out, c)
	}
	return out, rows.Err()
}

func nullIfEmpty(v string) any {
	if v == "" {
		return nil
	}
	return v
}
