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

type Thread struct {
	ID, ChannelID    int64
	Title, CreatedAt string
	ArchivedAt       *string
}

type Message struct {
	ID, ThreadID, Seq                            int64
	Author, AuthorType, Role, Content, CreatedAt string
}

func (s *Store) CreateThread(channelID int64, title string) (int64, error) {
	if strings.TrimSpace(title) == "" {
		return 0, errors.New("title required")
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM channels WHERE id = ? AND archived_at IS NULL`, channelID).Scan(&n); err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, ErrNotFound
	}
	res, err := s.db.Exec(`INSERT INTO threads(channel_id, title) VALUES(?,?)`, channelID, title)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) ListThreads(channelID int64) ([]Thread, error) {
	rows, err := s.db.Query(`SELECT id, channel_id, title, COALESCE(created_at,''), archived_at FROM threads WHERE channel_id = ? AND archived_at IS NULL ORDER BY id`, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Thread
	for rows.Next() {
		var th Thread
		if err := rows.Scan(&th.ID, &th.ChannelID, &th.Title, &th.CreatedAt, &th.ArchivedAt); err != nil {
			return nil, err
		}
		out = append(out, th)
	}
	return out, rows.Err()
}

func (s *Store) AppendMessage(threadID int64, author, authorType, role, content string) (int64, error) {
	if strings.TrimSpace(author) == "" || strings.TrimSpace(content) == "" {
		return 0, errors.New("author and content required")
	}
	if authorType != "human" && authorType != "agent" {
		return 0, errors.New("bad author_type")
	}
	if role != "user" && role != "assistant" && role != "system" {
		return 0, errors.New("bad role")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM threads WHERE id = ? AND archived_at IS NULL`, threadID).Scan(&n); err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, ErrNotFound
	}
	var maxSeq sql.NullInt64
	if err := tx.QueryRow(`SELECT MAX(seq) FROM messages WHERE thread_id = ?`, threadID).Scan(&maxSeq); err != nil {
		return 0, err
	}
	seq := int64(1)
	if maxSeq.Valid {
		seq = maxSeq.Int64 + 1
	}
	if _, err := tx.Exec(`INSERT INTO messages(thread_id, seq, author, author_type, role, content) VALUES(?,?,?,?,?,?)`, threadID, seq, author, authorType, role, content); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return seq, nil
}

func (s *Store) ListMessages(threadID int64, lastN int) ([]Message, error) {
	q := `SELECT id, thread_id, seq, author, author_type, role, content, COALESCE(created_at,'') FROM messages WHERE thread_id = ? ORDER BY seq ASC`
	if lastN > 0 {
		q = `SELECT * FROM (` + q + `) ORDER BY seq DESC LIMIT ?`
		q = `SELECT * FROM (` + q + `) ORDER BY seq ASC`
		rows, err := s.db.Query(q, threadID, lastN)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		return scanMessages(rows)
	}
	rows, err := s.db.Query(q, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

func scanMessages(rows *sql.Rows) ([]Message, error) {
	var out []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.ThreadID, &m.Seq, &m.Author, &m.AuthorType, &m.Role, &m.Content, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) AddReaction(messageID int64, emoji, author, authorType string) error {
	if strings.TrimSpace(emoji) == "" || strings.TrimSpace(author) == "" {
		return errors.New("emoji and author required")
	}
	if authorType != "human" && authorType != "agent" {
		return errors.New("bad author_type")
	}
	if _, err := s.db.Exec(`INSERT INTO reactions(message_id, emoji, author, author_type) VALUES(?,?,?,?)`, messageID, emoji, author, authorType); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return ErrConflict
		}
		return err
	}
	return nil
}
