package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"

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
  repo_head_branch TEXT,
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
  parent_id INTEGER REFERENCES messages(id),
  name TEXT NOT NULL CHECK(length(trim(name)) > 0),
  author_type TEXT NOT NULL CHECK(author_type IN ('human','agent')),
  role TEXT NOT NULL CHECK(role IN ('user','assistant','system')),
  content TEXT NOT NULL CHECK(length(trim(content)) > 0),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  UNIQUE(thread_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_messages_parent ON messages(parent_id);
CREATE INDEX IF NOT EXISTS idx_messages_thread_seq ON messages(thread_id, seq);
CREATE TABLE IF NOT EXISTS reactions(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  message_id INTEGER NOT NULL REFERENCES messages(id),
  emoji TEXT NOT NULL CHECK(length(trim(emoji)) > 0),
  name TEXT NOT NULL CHECK(length(trim(name)) > 0),
  author_type TEXT NOT NULL CHECK(author_type IN ('human','agent')),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  UNIQUE(message_id, emoji, name)
);
`

type Store struct{ db *sql.DB }

type Channel struct {
	ID                                                                    int64
	Name, RepoAbsPath, RepoRemote, RepoHeadSHA, RepoHeadBranch, CreatedAt string
	IsOrphaned                                                            bool
	ArchivedAt                                                            *string
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := migrate(db, schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func migrate(db *sql.DB, schema string) error {
	rows, err := db.Query(`PRAGMA table_info(messages)`)
	if err != nil {
		_, err := db.Exec(schema)
		return err
	}
	found := false
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var dfltValue sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dfltValue, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == "parent_id" {
			found = true
			break
		}
	}
	rows.Close()
	if found {
		_, err := db.Exec(schema)
		return err
	}
	for _, tbl := range []string{"reactions", "messages", "threads", "channels"} {
		db.Exec("DROP TABLE IF EXISTS " + tbl)
	}
	_, err = db.Exec(schema)
	return err
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) CreateChannel(name, repoAbsPath, repoRemote, repoHeadSHA, repoHeadBranch string, orphaned bool) (int64, error) {
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
	res, err := s.db.Exec(`INSERT INTO channels(name, repo_abs_path, repo_remote, repo_head_sha, repo_head_branch, is_orphaned) VALUES(?,?,?,?,?,?)`, name, abs, nullIfEmpty(repoRemote), nullIfEmpty(repoHeadSHA), nullIfEmpty(repoHeadBranch), isOrphan)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return 0, ErrConflict
		}
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) ListChannels(repoAbsPath string, includeOrphaned bool) ([]Channel, error) {
	q := `SELECT id, name, COALESCE(repo_abs_path,''), COALESCE(repo_remote,''), COALESCE(repo_head_sha,''), COALESCE(repo_head_branch,''), is_orphaned, COALESCE(created_at,''), archived_at FROM channels WHERE archived_at IS NULL`
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
		if err := rows.Scan(&c.ID, &c.Name, &c.RepoAbsPath, &c.RepoRemote, &c.RepoHeadSHA, &c.RepoHeadBranch, &isOrphan, &c.CreatedAt, &c.ArchivedAt); err != nil {
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
	ID, ThreadID, Seq                          int64
	ParentID                                   sql.NullInt64
	ParentSeq                                  sql.NullInt64 `json:"-"`
	Name, AuthorType, Role, Content, CreatedAt string
}

type Reaction struct {
	ID         int64
	MessageID  int64
	MessageSeq int64
	Emoji      string
	Name       string
	AuthorType string
	CreatedAt  string
}

type AppendEvent struct {
	Type       string
	SourceSeq  int64
	ParentSeq  int64
	MessageSeq int64
	Name       string
	AuthorType string
	Role       string
	Content    string
	Emoji      string
	CreatedAt  string
}

type AppendResult struct {
	SourceSeq  int64
	Seq        int64
	MessageID  int64
	ReactionID int64
}

type InboxMessage struct {
	Message
	ChannelName string `json:"channel_name"`
	ChannelID   int64  `json:"channel_id"`
	ThreadTitle string `json:"thread_title"`
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

func (s *Store) AppendMessage(threadID int64, name, authorType, role, content string) (int64, error) {
	return s.AppendMessageWithParent(threadID, name, authorType, role, content, 0)
}

func (s *Store) AppendMessageWithParent(threadID int64, name, authorType, role, content string, parentID int64) (int64, error) {
	return s.AppendMessageAtWithParent(threadID, name, authorType, role, content, "", parentID)
}

func (s *Store) AppendMessageAt(threadID int64, name, authorType, role, content, createdAt string) (int64, error) {
	return s.AppendMessageAtWithParent(threadID, name, authorType, role, content, createdAt, 0)
}

func (s *Store) AppendMessageAtWithParent(threadID int64, name, authorType, role, content, createdAt string, parentID int64) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	seq, _, err := appendMessageTx(tx, threadID, name, authorType, role, content, createdAt, parentID)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return seq, nil
}

func (s *Store) AppendMessageByParentSeq(threadID, parentSeq int64, name, authorType, role, content, createdAt string) (int64, int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	seq, id, err := appendMessageByParentSeqTx(tx, threadID, parentSeq, name, authorType, role, content, createdAt)
	if err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return seq, id, nil
}

func appendMessageByParentSeqTx(tx *sql.Tx, threadID, parentSeq int64, name, authorType, role, content, createdAt string) (int64, int64, error) {
	if parentSeq < 0 {
		return 0, 0, errors.New("parent_seq must be non-negative")
	}
	var parentID int64
	if parentSeq > 0 {
		var err error
		parentID, err = messageIDBySeqTx(tx, threadID, parentSeq)
		if err != nil {
			return 0, 0, err
		}
	}
	return appendMessageTx(tx, threadID, name, authorType, role, content, createdAt, parentID)
}

func appendMessageTx(tx *sql.Tx, threadID int64, name, authorType, role, content, createdAt string, parentID int64) (int64, int64, error) {
	if err := validateMessage(name, authorType, role, content); err != nil {
		return 0, 0, err
	}
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM threads WHERE id = ? AND archived_at IS NULL`, threadID).Scan(&n); err != nil {
		return 0, 0, err
	}
	if n == 0 {
		return 0, 0, ErrNotFound
	}
	var maxSeq sql.NullInt64
	if err := tx.QueryRow(`SELECT MAX(seq) FROM messages WHERE thread_id = ?`, threadID).Scan(&maxSeq); err != nil {
		return 0, 0, err
	}
	seq := int64(1)
	if maxSeq.Valid {
		seq = maxSeq.Int64 + 1
	}
	if parentID > 0 {
		var parentThreadID int64
		if err := tx.QueryRow(`SELECT thread_id FROM messages WHERE id = ?`, parentID).Scan(&parentThreadID); err != nil {
			return 0, 0, ErrNotFound
		}
		if parentThreadID != threadID {
			return 0, 0, errors.New("parent message not in this thread")
		}
	}
	var res sql.Result
	var err error
	if createdAt != "" {
		if _, err := time.Parse(time.RFC3339, createdAt); err != nil {
			return 0, 0, err
		}
		res, err = tx.Exec(`INSERT INTO messages(thread_id, seq, parent_id, name, author_type, role, content, created_at) VALUES(?,?,?,?,?,?,?,?)`, threadID, seq, nullIfInt64(parentID), name, authorType, role, content, createdAt)
		if err != nil {
			return 0, 0, err
		}
	} else {
		res, err = tx.Exec(`INSERT INTO messages(thread_id, seq, parent_id, name, author_type, role, content) VALUES(?,?,?,?,?,?,?)`, threadID, seq, nullIfInt64(parentID), name, authorType, role, content)
		if err != nil {
			return 0, 0, err
		}
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, 0, err
	}
	return seq, id, nil
}

func validateMessage(name, authorType, role, content string) error {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(content) == "" {
		return errors.New("name and content required")
	}
	if authorType != "human" && authorType != "agent" {
		return errors.New("bad author_type")
	}
	if role != "user" && role != "assistant" && role != "system" {
		return errors.New("bad role")
	}
	return nil
}

func nullIfInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func (s *Store) MessageIDBySeq(threadID, seq int64) (int64, error) {
	var id int64
	err := s.db.QueryRow(`SELECT id FROM messages WHERE thread_id = ? AND seq = ?`, threadID, seq).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	return id, nil
}

func messageIDBySeqTx(tx *sql.Tx, threadID, seq int64) (int64, error) {
	var id int64
	err := tx.QueryRow(`SELECT id FROM messages WHERE thread_id = ? AND seq = ?`, threadID, seq).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Store) ListMessages(threadID int64, lastN int) ([]Message, error) {
	q := `SELECT m.id, m.thread_id, m.seq, m.parent_id, p.seq, m.name, m.author_type, m.role, m.content, COALESCE(m.created_at,'') FROM messages m LEFT JOIN messages p ON p.id = m.parent_id WHERE m.thread_id = ? ORDER BY m.seq ASC`
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

func (s *Store) ListMessagesAfter(threadID, afterSeq int64) ([]Message, error) {
	rows, err := s.db.Query(`SELECT m.id, m.thread_id, m.seq, m.parent_id, p.seq, m.name, m.author_type, m.role, m.content, COALESCE(m.created_at,'') FROM messages m LEFT JOIN messages p ON p.id = m.parent_id WHERE m.thread_id = ? AND m.seq > ? ORDER BY m.seq ASC`, threadID, afterSeq)
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
		if err := rows.Scan(&m.ID, &m.ThreadID, &m.Seq, &m.ParentID, &m.ParentSeq, &m.Name, &m.AuthorType, &m.Role, &m.Content, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (m Message) ParentIDValue() int64 {
	if m.ParentID.Valid {
		return m.ParentID.Int64
	}
	return 0
}

func (s *Store) ListMessagesByParent(threadID int64, parentID int64) ([]Message, error) {
	q := `SELECT m.id, m.thread_id, m.seq, m.parent_id, p.seq, m.name, m.author_type, m.role, m.content, COALESCE(m.created_at,'') FROM messages m LEFT JOIN messages p ON p.id = m.parent_id WHERE m.thread_id = ? AND m.parent_id = ? ORDER BY m.seq ASC`
	rows, err := s.db.Query(q, threadID, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

func (s *Store) ListInbox(limit int) ([]InboxMessage, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 200 {
		limit = 200
	}
	q := `SELECT m.id, m.thread_id, m.seq, m.parent_id, m.name, m.author_type, m.role, m.content, COALESCE(m.created_at,''),
                 c.name, c.id, t.title
          FROM messages m
          JOIN threads t ON t.id = m.thread_id
          JOIN channels c ON c.id = t.channel_id
          WHERE c.archived_at IS NULL AND t.archived_at IS NULL
          ORDER BY m.created_at DESC, m.id DESC LIMIT ?`
	rows, err := s.db.Query(q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InboxMessage
	for rows.Next() {
		var im InboxMessage
		if err := rows.Scan(&im.ID, &im.ThreadID, &im.Seq, &im.ParentID, &im.Name, &im.AuthorType, &im.Role, &im.Content, &im.CreatedAt, &im.ChannelName, &im.ChannelID, &im.ThreadTitle); err != nil {
			return nil, err
		}
		out = append(out, im)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	if out == nil {
		out = []InboxMessage{}
	}
	return out, nil
}

func (s *Store) CountReplies(threadID int64, parentID int64) (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE thread_id = ? AND parent_id = ?`, threadID, parentID).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func (s *Store) AddReaction(messageID int64, emoji, name, authorType string) error {
	if err := validateReaction(emoji, name, authorType); err != nil {
		return err
	}
	if _, err := s.db.Exec(`INSERT INTO reactions(message_id, emoji, name, author_type) VALUES(?,?,?,?)`, messageID, emoji, name, authorType); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return ErrConflict
		}
		return err
	}
	return nil
}

func validateReaction(emoji, name, authorType string) error {
	if strings.TrimSpace(emoji) == "" || strings.TrimSpace(name) == "" {
		return errors.New("emoji and name required")
	}
	if authorType != "human" && authorType != "agent" {
		return errors.New("bad author_type")
	}
	return nil
}

func (s *Store) ListReactions(threadID int64) ([]Reaction, error) {
	rows, err := s.db.Query(`SELECT r.id, r.message_id, m.seq, r.emoji, r.name, r.author_type, COALESCE(r.created_at,'') FROM reactions r JOIN messages m ON m.id = r.message_id WHERE m.thread_id = ? ORDER BY m.seq ASC, r.created_at ASC, r.id ASC`, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Reaction
	for rows.Next() {
		var reaction Reaction
		if err := rows.Scan(&reaction.ID, &reaction.MessageID, &reaction.MessageSeq, &reaction.Emoji, &reaction.Name, &reaction.AuthorType, &reaction.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, reaction)
	}
	return out, rows.Err()
}

func (s *Store) AddReactionBySeq(threadID, messageSeq int64, emoji, name, authorType string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, _, err := addReactionBySeqTx(tx, threadID, messageSeq, emoji, name, authorType, ""); err != nil {
		return err
	}
	return tx.Commit()
}

func addReactionBySeqTx(tx *sql.Tx, threadID, messageSeq int64, emoji, name, authorType, createdAt string) (int64, int64, error) {
	if err := validateReaction(emoji, name, authorType); err != nil {
		return 0, 0, err
	}
	messageID, err := messageIDBySeqTx(tx, threadID, messageSeq)
	if err != nil {
		return 0, 0, err
	}
	var res sql.Result
	if createdAt != "" {
		if _, err := time.Parse(time.RFC3339, createdAt); err != nil {
			return 0, 0, err
		}
		res, err = tx.Exec(`INSERT INTO reactions(message_id, emoji, name, author_type, created_at) VALUES(?,?,?,?,?)`, messageID, emoji, name, authorType, createdAt)
	} else {
		res, err = tx.Exec(`INSERT INTO reactions(message_id, emoji, name, author_type) VALUES(?,?,?,?)`, messageID, emoji, name, authorType)
	}
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return 0, 0, ErrConflict
		}
		return 0, 0, err
	}
	reactionID, err := res.LastInsertId()
	if err != nil {
		return 0, 0, err
	}
	return messageID, reactionID, nil
}

func (s *Store) AppendBatch(threadID int64, events []AppendEvent) ([]AppendResult, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for _, event := range events {
		if err := validateAppendEvent(event); err != nil {
			return nil, err
		}
	}
	nextSeq, err := nextThreadSeqTx(tx, threadID)
	if err != nil {
		return nil, err
	}
	sourceSeqs, err := buildSourceSequenceMap(events, nextSeq)
	if err != nil {
		return nil, err
	}
	results := make([]AppendResult, 0, len(events))
	for _, event := range events {
		switch event.Type {
		case "message":
			parentSeq := event.ParentSeq
			if mapped, ok := sourceSeqs[parentSeq]; ok {
				parentSeq = mapped
			}
			seq, messageID, err := appendMessageByParentSeqTx(tx, threadID, parentSeq, event.Name, event.AuthorType, event.Role, event.Content, event.CreatedAt)
			if err != nil {
				return nil, err
			}
			results = append(results, AppendResult{SourceSeq: event.SourceSeq, Seq: seq, MessageID: messageID})
		case "reaction":
			messageSeq := event.MessageSeq
			if mapped, ok := sourceSeqs[messageSeq]; ok {
				messageSeq = mapped
			}
			messageID, reactionID, err := addReactionBySeqTx(tx, threadID, messageSeq, event.Emoji, event.Name, event.AuthorType, event.CreatedAt)
			if err != nil {
				return nil, err
			}
			results = append(results, AppendResult{SourceSeq: event.SourceSeq, MessageID: messageID, ReactionID: reactionID})
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return results, nil
}

func validateAppendEvent(event AppendEvent) error {
	if event.SourceSeq < 0 {
		return errors.New("source_seq must be non-negative")
	}
	switch event.Type {
	case "message":
		if event.ParentSeq < 0 {
			return errors.New("parent_seq must be non-negative")
		}
		if err := validateMessage(event.Name, event.AuthorType, event.Role, event.Content); err != nil {
			return err
		}
	case "reaction":
		if event.MessageSeq <= 0 {
			return errors.New("message_seq must be positive")
		}
		if err := validateReaction(event.Emoji, event.Name, event.AuthorType); err != nil {
			return err
		}
	default:
		return errors.New("unknown event type")
	}
	if event.CreatedAt != "" {
		if _, err := time.Parse(time.RFC3339, event.CreatedAt); err != nil {
			return err
		}
	}
	return nil
}

func nextThreadSeqTx(tx *sql.Tx, threadID int64) (int64, error) {
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
	if maxSeq.Valid {
		return maxSeq.Int64 + 1, nil
	}
	return 1, nil
}

func buildSourceSequenceMap(events []AppendEvent, nextSeq int64) (map[int64]int64, error) {
	mapped := make(map[int64]int64)
	var lastSourceSeq int64
	for _, event := range events {
		if event.Type != "message" {
			continue
		}
		destinationSeq := nextSeq
		nextSeq++
		if event.SourceSeq == 0 {
			continue
		}
		if _, exists := mapped[event.SourceSeq]; exists {
			return nil, errors.New("duplicate source_seq")
		}
		if event.SourceSeq < lastSourceSeq {
			return nil, errors.New("unsorted source_seq")
		}
		mapped[event.SourceSeq] = destinationSeq
		lastSourceSeq = event.SourceSeq
	}
	return mapped, nil
}
