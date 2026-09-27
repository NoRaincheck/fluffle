package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/NoRaincheck/fluffle/internal/db"
	"github.com/NoRaincheck/fluffle/internal/names"

	_ "modernc.org/sqlite"
)

var ErrConflict = errors.New("conflict")
var ErrNotFound = errors.New("not found")
var ErrInvalid = errors.New("invalid")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

type Store struct {
	db *sql.DB
	q  *db.Queries
}

func (s *Store) tx(ctx context.Context) (*sql.Tx, *db.Queries, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	return tx, s.q.WithTx(tx), nil
}

type Channel struct {
	ID                                                                    int64
	Name, RepoAbsPath, RepoRemote, RepoHeadSHA, RepoHeadBranch, CreatedAt string
	IsOrphaned                                                            bool
}

func Open(path string) (*Store, error) {
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	conn.SetMaxOpenConns(1)
	if err := prepare(conn); err != nil {
		conn.Close()
		return nil, err
	}
	return &Store{db: conn, q: db.New(conn)}, nil
}

func prepare(conn *sql.DB) error {
	if err := enableForeignKeys(conn); err != nil {
		return err
	}
	legacy, err := legacyRebuildRequired(conn)
	if err != nil {
		return err
	}
	if legacy {
		if err := dropLegacyTables(conn); err != nil {
			return err
		}
	}
	if err := migrateUp(conn, migrationFS); err != nil {
		return err
	}
	return dropArchivedAtColumns(conn)
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) CreateChannel(ctx context.Context, name, repoAbsPath, repoRemote, repoHeadSHA, repoHeadBranch string, orphaned bool) (int64, error) {
	if err := names.Slug(name); err != nil {
		return 0, invalid("%v", err)
	}
	var abs *string
	var isOrphan int64
	if orphaned {
		if repoAbsPath != "" {
			return 0, invalid("orphaned channel must not have repo path")
		}
		isOrphan = 1
	} else {
		if strings.TrimSpace(repoAbsPath) == "" {
			return 0, invalid("repo path required")
		}
		abs = &repoAbsPath
	}
	id, err := s.q.CreateChannel(ctx, db.CreateChannelParams{
		Name:           name,
		RepoAbsPath:    abs,
		RepoRemote:     nullIfEmpty(repoRemote),
		RepoHeadSHA:    nullIfEmpty(repoHeadSHA),
		RepoHeadBranch: nullIfEmpty(repoHeadBranch),
		IsOrphaned:     isOrphan,
	})
	if err != nil {
		return 0, classify(err)
	}
	return id, nil
}

func (s *Store) ListChannels(ctx context.Context, repoAbsPath string, includeOrphaned bool) ([]Channel, error) {
	switch {
	case repoAbsPath != "" && !includeOrphaned:
		rows, err := s.q.ListChannelsByRepoNonOrphaned(ctx, db.ListChannelsByRepoNonOrphanedParams{RepoAbsPath: &repoAbsPath})
		if err != nil {
			return nil, err
		}
		return channelsFromRows(rows, func(r db.ListChannelsByRepoNonOrphanedRow) Channel {
			return channelFromFields(r.ID, r.Name, r.RepoAbsPath, r.RepoRemote, r.RepoHeadSHA, r.RepoHeadBranch, r.IsOrphaned, r.CreatedAt)
		}), nil
	case repoAbsPath != "":
		rows, err := s.q.ListChannelsByRepo(ctx, db.ListChannelsByRepoParams{RepoAbsPath: &repoAbsPath})
		if err != nil {
			return nil, err
		}
		return channelsFromRows(rows, func(r db.ListChannelsByRepoRow) Channel {
			return channelFromFields(r.ID, r.Name, r.RepoAbsPath, r.RepoRemote, r.RepoHeadSHA, r.RepoHeadBranch, r.IsOrphaned, r.CreatedAt)
		}), nil
	case !includeOrphaned:
		rows, err := s.q.ListChannelsNonOrphaned(ctx)
		if err != nil {
			return nil, err
		}
		return channelsFromRows(rows, func(r db.ListChannelsNonOrphanedRow) Channel {
			return channelFromFields(r.ID, r.Name, r.RepoAbsPath, r.RepoRemote, r.RepoHeadSHA, r.RepoHeadBranch, r.IsOrphaned, r.CreatedAt)
		}), nil
	default:
		rows, err := s.q.ListChannelsAll(ctx)
		if err != nil {
			return nil, err
		}
		return channelsFromRows(rows, func(r db.ListChannelsAllRow) Channel {
			return channelFromFields(r.ID, r.Name, r.RepoAbsPath, r.RepoRemote, r.RepoHeadSHA, r.RepoHeadBranch, r.IsOrphaned, r.CreatedAt)
		}), nil
	}
}

func channelFromFields(id int64, name, repoAbsPath, repoRemote, repoHeadSHA, repoHeadBranch string, isOrphaned int64, createdAt string) Channel {
	return Channel{
		ID:             id,
		Name:           name,
		RepoAbsPath:    repoAbsPath,
		RepoRemote:     repoRemote,
		RepoHeadSHA:    repoHeadSHA,
		RepoHeadBranch: repoHeadBranch,
		IsOrphaned:     isOrphaned == 1,
		CreatedAt:      createdAt,
	}
}

func channelsFromRows[T any](rows []T, f func(T) Channel) []Channel {
	var out []Channel
	for _, r := range rows {
		out = append(out, f(r))
	}
	return out
}

func threadFromFields(id, channelID int64, title, createdAt string) Thread {
	return Thread{ID: id, ChannelID: channelID, Title: title, CreatedAt: createdAt}
}

func messageFromFields(id, threadID, seq int64, parentID sql.NullInt64, parentSeq *int64, name, authorType, role, content, createdAt string) Message {
	return Message{
		ID:         id,
		ThreadID:   threadID,
		Seq:        seq,
		ParentID:   parentID,
		ParentSeq:  nullInt64FromPtr(parentSeq),
		Name:       name,
		AuthorType: authorType,
		Role:       role,
		Content:    content,
		CreatedAt:  createdAt,
	}
}

func nullInt64FromPtr(v *int64) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *v, Valid: true}
}

func nullIfEmpty(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

type Thread struct {
	ID, ChannelID    int64
	Title, CreatedAt string
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

func (s *Store) CreateThread(ctx context.Context, channelID int64, title string) (int64, error) {
	if err := names.Slug(title); err != nil {
		return 0, invalid("%v", err)
	}
	id, err := s.q.CreateThread(ctx, db.CreateThreadParams{ChannelID: channelID, Title: title})
	if err != nil {
		return 0, classify(err)
	}
	return id, nil
}

func (s *Store) ListThreads(ctx context.Context, channelID int64) ([]Thread, error) {
	rows, err := s.q.ListThreads(ctx, db.ListThreadsParams{ChannelID: channelID})
	if err != nil {
		return nil, err
	}
	var out []Thread
	for _, r := range rows {
		out = append(out, threadFromFields(r.ID, r.ChannelID, r.Title, r.CreatedAt))
	}
	return out, nil
}

func (s *Store) AppendMessage(ctx context.Context, threadID int64, name, authorType, role, content string) (int64, error) {
	return s.AppendMessageWithParent(ctx, threadID, name, authorType, role, content, 0)
}

func (s *Store) AppendMessageWithParent(ctx context.Context, threadID int64, name, authorType, role, content string, parentID int64) (int64, error) {
	return s.AppendMessageAtWithParent(ctx, threadID, name, authorType, role, content, "", parentID)
}

func (s *Store) AppendMessageAt(ctx context.Context, threadID int64, name, authorType, role, content, createdAt string) (int64, error) {
	return s.AppendMessageAtWithParent(ctx, threadID, name, authorType, role, content, createdAt, 0)
}

func (s *Store) AppendMessageAtWithParent(ctx context.Context, threadID int64, name, authorType, role, content, createdAt string, parentID int64) (int64, error) {
	tx, q, err := s.tx(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	seq, _, err := s.appendMessage(ctx, q, threadID, name, authorType, role, content, createdAt, parentID)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return seq, nil
}

func (s *Store) AppendMessageByParentSeq(ctx context.Context, threadID, parentSeq int64, name, authorType, role, content, createdAt string) (int64, int64, error) {
	tx, q, err := s.tx(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	seq, id, err := s.appendMessageByParentSeq(ctx, q, threadID, parentSeq, name, authorType, role, content, createdAt)
	if err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return seq, id, nil
}

func (s *Store) appendMessage(ctx context.Context, q *db.Queries, threadID int64, name, authorType, role, content, createdAt string, parentID int64) (int64, int64, error) {
	if err := names.Name(name); err != nil {
		return 0, 0, invalid("%v", err)
	}
	seq, err := nextMessageSeq(ctx, q, threadID)
	if err != nil {
		return 0, 0, err
	}
	if parentID > 0 {
		parentThreadID, err := q.GetMessageThreadIDByID(ctx, db.GetMessageThreadIDByIDParams{ID: parentID})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return 0, 0, ErrNotFound
			}
			return 0, 0, err
		}
		if parentThreadID != threadID {
			return 0, 0, invalid("parent message not in this thread")
		}
	}
	var id int64
	if createdAt != "" {
		if _, err := time.Parse(time.RFC3339, createdAt); err != nil {
			return 0, 0, invalid("created_at %q is not RFC3339: %v", createdAt, err)
		}
		id, err = q.InsertMessage(ctx, db.InsertMessageParams{
			ThreadID:   threadID,
			Seq:        seq,
			ParentID:   nullIfInt64(parentID),
			Name:       name,
			AuthorType: authorType,
			Role:       role,
			Content:    content,
			CreatedAt:  createdAt,
		})
	} else {
		id, err = q.InsertMessageDefaultCreatedAt(ctx, db.InsertMessageDefaultCreatedAtParams{
			ThreadID:   threadID,
			Seq:        seq,
			ParentID:   nullIfInt64(parentID),
			Name:       name,
			AuthorType: authorType,
			Role:       role,
			Content:    content,
		})
	}
	if err != nil {
		return 0, 0, classify(err)
	}
	return seq, id, nil
}

func nullIfInt64(v int64) sql.NullInt64 {
	if v == 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: v, Valid: true}
}

func (s *Store) MessageIDBySeq(ctx context.Context, threadID, seq int64) (int64, error) {
	id, err := s.q.GetMessageIDByThreadSeq(ctx, db.GetMessageIDByThreadSeqParams{
		ThreadID: threadID,
		Seq:      seq,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}

func (s *Store) ListMessages(ctx context.Context, threadID int64, lastN int) ([]Message, error) {
	if lastN > 0 {
		rows, err := s.q.ListMessagesLastN(ctx, db.ListMessagesLastNParams{ThreadID: threadID, Limit: int64(lastN)})
		if err != nil {
			return nil, err
		}
		var out []Message
		for _, r := range rows {
			out = append(out, messageFromFields(r.ID, r.ThreadID, r.Seq, r.ParentID, r.ParentSeq, r.Name, r.AuthorType, r.Role, r.Content, r.CreatedAt))
		}
		return out, nil
	}
	rows, err := s.q.ListMessages(ctx, db.ListMessagesParams{ThreadID: threadID})
	if err != nil {
		return nil, err
	}
	var out []Message
	for _, r := range rows {
		out = append(out, messageFromFields(r.ID, r.ThreadID, r.Seq, r.ParentID, r.ParentSeq, r.Name, r.AuthorType, r.Role, r.Content, r.CreatedAt))
	}
	return out, nil
}

func (s *Store) ListMessagesAfter(ctx context.Context, threadID, afterSeq int64) ([]Message, error) {
	rows, err := s.q.ListMessagesAfter(ctx, db.ListMessagesAfterParams{
		ThreadID: threadID,
		Seq:      afterSeq,
	})
	if err != nil {
		return nil, err
	}
	var out []Message
	for _, r := range rows {
		out = append(out, messageFromFields(r.ID, r.ThreadID, r.Seq, r.ParentID, r.ParentSeq, r.Name, r.AuthorType, r.Role, r.Content, r.CreatedAt))
	}
	return out, nil
}

func (m Message) ParentIDValue() int64 {
	if m.ParentID.Valid {
		return m.ParentID.Int64
	}
	return 0
}

func messageFromDetailRow(r db.GetMessageByIDWithParentRow) Message {
	return messageFromFields(r.ID, r.ThreadID, r.Seq, r.ParentID, r.ParentSeq, r.Name, r.AuthorType, r.Role, r.Content, r.CreatedAt)
}

func reactionFromRow(r db.ListReactionsRow) Reaction {
	return Reaction{
		ID:         r.ID,
		MessageID:  r.MessageID,
		MessageSeq: r.MessageSeq,
		Emoji:      r.Emoji,
		Name:       r.Name,
		AuthorType: r.AuthorType,
		CreatedAt:  r.CreatedAt,
	}
}

// Granularity selects what one row of a ListRows result is.
const (
	GranularityMessage = "message"
	GranularityThread  = "thread"
	GranularityChannel = "channel"
)

const (
	defaultRowLimit = 200
	maxRowLimit     = 500
)

// Row is one line of the cross-channel feed. ID is the message, thread, or
// channel id depending on the granularity. ThreadID is 0 at channel
// granularity. Name is the author of the group's newest message, except at
// message granularity where it is that message's own author. Content is the
// whole representative message, not its first line: choosing a line is a
// rendering decision. Count is the group's message count, and 1 at message
// granularity.
type Row struct {
	ID       int64
	ThreadID int64
	Time     string
	Channel  string
	Thread   string
	Name     string
	Content  string
	Count    int
}

// ListRows returns the feed at one granularity, newest first. Grouping,
// representative selection, and reply counts are the query's job, so the
// caller holds no rule about which message represents a group.
func (s *Store) ListRows(ctx context.Context, granularity string, limit int) ([]Row, error) {
	if limit <= 0 {
		limit = defaultRowLimit
	}
	if limit > maxRowLimit {
		limit = maxRowLimit
	}
	switch granularity {
	case GranularityMessage:
		rows, err := s.q.ListMessageRows(ctx, db.ListMessageRowsParams{Limit: int64(limit)})
		if err != nil {
			return nil, err
		}
		out := make([]Row, 0, len(rows))
		for _, r := range rows {
			out = append(out, Row{
				ID: r.ID, ThreadID: r.ThreadID, Time: r.CreatedAt,
				Channel: r.Channel, Thread: r.Thread, Name: r.Name,
				Content: r.Content, Count: 1,
			})
		}
		return out, nil
	case GranularityThread:
		rows, err := s.q.ListThreadRows(ctx, db.ListThreadRowsParams{Limit: int64(limit)})
		if err != nil {
			return nil, err
		}
		out := make([]Row, 0, len(rows))
		for _, r := range rows {
			out = append(out, Row{
				ID: r.ID, ThreadID: r.ThreadID, Time: r.CreatedAt,
				Channel: r.Channel, Thread: r.Thread, Name: r.Name,
				Content: r.Content, Count: int(r.MsgCount),
			})
		}
		return out, nil
	case GranularityChannel:
		rows, err := s.q.ListChannelRows(ctx, db.ListChannelRowsParams{Limit: int64(limit)})
		if err != nil {
			return nil, err
		}
		out := make([]Row, 0, len(rows))
		for _, r := range rows {
			out = append(out, Row{
				ID: r.ID, ThreadID: r.ThreadID, Time: r.CreatedAt,
				Channel: r.Channel, Thread: r.Thread, Name: r.Name,
				Content: r.Content, Count: int(r.MsgCount),
			})
		}
		return out, nil
	default:
		return nil, invalid("granularity %q must be %s, %s, or %s",
			granularity, GranularityMessage, GranularityThread, GranularityChannel)
	}
}

func (s *Store) AddReaction(ctx context.Context, messageID int64, emoji, name, authorType string) error {
	_, _, err := s.addReaction(ctx, s.q, messageID, emoji, name, authorType, "")
	return err
}

func (s *Store) ListReactions(ctx context.Context, threadID int64) ([]Reaction, error) {
	rows, err := s.q.ListReactions(ctx, db.ListReactionsParams{ThreadID: threadID})
	if err != nil {
		return nil, err
	}
	var out []Reaction
	for _, r := range rows {
		out = append(out, reactionFromRow(r))
	}
	return out, nil
}

func (s *Store) AddReactionBySeq(ctx context.Context, threadID, messageSeq int64, emoji, name, authorType string) error {
	tx, q, err := s.tx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, _, err := s.addReactionBySeq(ctx, q, threadID, messageSeq, emoji, name, authorType, ""); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) addReactionBySeq(ctx context.Context, q *db.Queries, threadID, messageSeq int64, emoji, name, authorType, createdAt string) (int64, int64, error) {
	messageID, err := q.GetMessageIDByThreadSeq(ctx, db.GetMessageIDByThreadSeqParams{
		ThreadID: threadID,
		Seq:      messageSeq,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, ErrNotFound
	}
	if err != nil {
		return 0, 0, err
	}
	return s.addReaction(ctx, q, messageID, emoji, name, authorType, createdAt)
}

func (s *Store) addReaction(ctx context.Context, q *db.Queries, messageID int64, emoji, name, authorType, createdAt string) (int64, int64, error) {
	if err := names.Name(name); err != nil {
		return 0, 0, invalid("%v", err)
	}
	var (
		reactionID int64
		err        error
	)
	if createdAt != "" {
		if _, err := time.Parse(time.RFC3339, createdAt); err != nil {
			return 0, 0, invalid("created_at %q is not RFC3339: %v", createdAt, err)
		}
		reactionID, err = q.InsertReaction(ctx, db.InsertReactionParams{
			MessageID:  messageID,
			Emoji:      emoji,
			Name:       name,
			AuthorType: authorType,
			CreatedAt:  createdAt,
		})
	} else {
		reactionID, err = q.InsertReactionDefaultCreatedAt(ctx, db.InsertReactionDefaultCreatedAtParams{
			MessageID:  messageID,
			Emoji:      emoji,
			Name:       name,
			AuthorType: authorType,
		})
	}
	if err != nil {
		return 0, 0, classify(err)
	}
	return messageID, reactionID, nil
}

func (s *Store) AppendBatch(ctx context.Context, threadID int64, events []AppendEvent) ([]AppendResult, error) {
	tx, q, err := s.tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for _, event := range events {
		if err := validateAppendEvent(event); err != nil {
			return nil, err
		}
	}
	nextSeq, err := nextMessageSeq(ctx, q, threadID)
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
			seq, messageID, err := s.appendMessageByParentSeq(ctx, q, threadID, parentSeq, event.Name, event.AuthorType, event.Role, event.Content, event.CreatedAt)
			if err != nil {
				return nil, err
			}
			results = append(results, AppendResult{SourceSeq: event.SourceSeq, Seq: seq, MessageID: messageID})
		case "reaction":
			messageSeq := event.MessageSeq
			if mapped, ok := sourceSeqs[messageSeq]; ok {
				messageSeq = mapped
			}
			messageID, reactionID, err := s.addReactionBySeq(ctx, q, threadID, messageSeq, event.Emoji, event.Name, event.AuthorType, event.CreatedAt)
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
		return invalid("source_seq must be non-negative")
	}
	switch event.Type {
	case "message":
		if event.ParentSeq < 0 {
			return invalid("parent_seq must be non-negative")
		}
	case "reaction":
		if event.MessageSeq <= 0 {
			return invalid("message_seq must be positive")
		}
	default:
		return invalid("unknown event type %q", event.Type)
	}
	if event.CreatedAt != "" {
		if _, err := time.Parse(time.RFC3339, event.CreatedAt); err != nil {
			return invalid("created_at %q is not RFC3339: %v", event.CreatedAt, err)
		}
	}
	return nil
}

func (s *Store) appendMessageByParentSeq(ctx context.Context, q *db.Queries, threadID, parentSeq int64, name, authorType, role, content, createdAt string) (int64, int64, error) {
	if parentSeq < 0 {
		return 0, 0, invalid("parent_seq must be non-negative")
	}
	var parentID int64
	if parentSeq > 0 {
		var err error
		parentID, err = q.GetMessageIDByThreadSeq(ctx, db.GetMessageIDByThreadSeqParams{
			ThreadID: threadID,
			Seq:      parentSeq,
		})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return 0, 0, ErrNotFound
			}
			return 0, 0, err
		}
	}
	return s.appendMessage(ctx, q, threadID, name, authorType, role, content, createdAt, parentID)
}

func nextMessageSeq(ctx context.Context, q *db.Queries, threadID int64) (int64, error) {
	last, err := q.GetLastMessageSeq(ctx, db.GetLastMessageSeqParams{ThreadID: threadID})
	if errors.Is(err, sql.ErrNoRows) {
		return 1, nil
	}
	if err != nil {
		return 0, err
	}
	return last + 1, nil
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
			return nil, invalid("duplicate source_seq %d", event.SourceSeq)
		}
		if event.SourceSeq < lastSourceSeq {
			return nil, invalid("unsorted source_seq %d", event.SourceSeq)
		}
		mapped[event.SourceSeq] = destinationSeq
		lastSourceSeq = event.SourceSeq
	}
	return mapped, nil
}
