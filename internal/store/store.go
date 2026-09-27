package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/NoRaincheck/fluffle/internal/db"

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

func (s *Store) tx() (*sql.Tx, *db.Queries, error) {
	tx, err := s.db.Begin()
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

func (s *Store) CreateChannel(name, repoAbsPath, repoRemote, repoHeadSHA, repoHeadBranch string, orphaned bool) (int64, error) {
	if strings.TrimSpace(name) == "" {
		return 0, invalid("channel name required")
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
	id, err := s.q.CreateChannel(context.Background(), db.CreateChannelParams{
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

func (s *Store) ListChannels(repoAbsPath string, includeOrphaned bool) ([]Channel, error) {
	ctx := context.Background()
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

type InboxMessage struct {
	Message
	ChannelName string `json:"channel_name"`
	ChannelID   int64  `json:"channel_id"`
	ThreadTitle string `json:"thread_title"`
}

func (s *Store) CreateThread(channelID int64, title string) (int64, error) {
	if strings.TrimSpace(title) == "" {
		return 0, invalid("title required")
	}
	n, err := s.q.CountChannelsByID(context.Background(), db.CountChannelsByIDParams{ID: channelID})
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, ErrNotFound
	}
	return s.q.CreateThread(context.Background(), db.CreateThreadParams{ChannelID: channelID, Title: title})
}

func (s *Store) ListThreads(channelID int64) ([]Thread, error) {
	rows, err := s.q.ListThreads(context.Background(), db.ListThreadsParams{ChannelID: channelID})
	if err != nil {
		return nil, err
	}
	var out []Thread
	for _, r := range rows {
		out = append(out, threadFromFields(r.ID, r.ChannelID, r.Title, r.CreatedAt))
	}
	return out, nil
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
	tx, q, err := s.tx()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	seq, _, err := s.appendMessage(q, threadID, name, authorType, role, content, createdAt, parentID)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return seq, nil
}

func (s *Store) AppendMessageByParentSeq(threadID, parentSeq int64, name, authorType, role, content, createdAt string) (int64, int64, error) {
	tx, q, err := s.tx()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	seq, id, err := s.appendMessageByParentSeq(q, threadID, parentSeq, name, authorType, role, content, createdAt)
	if err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return seq, id, nil
}

func (s *Store) appendMessage(q *db.Queries, threadID int64, name, authorType, role, content, createdAt string, parentID int64) (int64, int64, error) {
	ctx := context.Background()
	n, err := q.CountThreadsByID(ctx, db.CountThreadsByIDParams{ID: threadID})
	if err != nil {
		return 0, 0, err
	}
	if n == 0 {
		return 0, 0, ErrNotFound
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

func (s *Store) MessageIDBySeq(threadID, seq int64) (int64, error) {
	id, err := s.q.GetMessageIDByThreadSeq(context.Background(), db.GetMessageIDByThreadSeqParams{
		ThreadID: threadID,
		Seq:      seq,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}

func (s *Store) ListMessages(threadID int64, lastN int) ([]Message, error) {
	ctx := context.Background()
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

func (s *Store) ListMessagesAfter(threadID, afterSeq int64) ([]Message, error) {
	rows, err := s.q.ListMessagesAfter(context.Background(), db.ListMessagesAfterParams{
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

func inboxFromRow(r db.ListInboxRow) InboxMessage {
	return InboxMessage{
		Message: Message{
			ID:         r.ID,
			ThreadID:   r.ThreadID,
			Seq:        r.Seq,
			ParentID:   r.ParentID,
			Name:       r.Name,
			AuthorType: r.AuthorType,
			Role:       r.Role,
			Content:    r.Content,
			CreatedAt:  r.CreatedAt,
		},
		ChannelName: r.ChannelName,
		ChannelID:   r.ChannelID,
		ThreadTitle: r.ThreadTitle,
	}
}

func (s *Store) ListInbox(limit int) ([]InboxMessage, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := s.q.ListInbox(context.Background(), db.ListInboxParams{Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	out := []InboxMessage{}
	for _, r := range rows {
		out = append(out, inboxFromRow(r))
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

func (s *Store) AddReaction(messageID int64, emoji, name, authorType string) error {
	tx, q, err := s.tx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := s.messageThreadID(q, messageID); err != nil {
		return err
	}
	if _, _, err := s.addReaction(q, messageID, emoji, name, authorType, ""); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) messageThreadID(q *db.Queries, messageID int64) (int64, error) {
	if messageID <= 0 {
		return 0, ErrNotFound
	}
	threadID, err := q.GetMessageThreadIDByID(context.Background(), db.GetMessageThreadIDByIDParams{ID: messageID})
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	return threadID, nil
}

func (s *Store) ListReactions(threadID int64) ([]Reaction, error) {
	rows, err := s.q.ListReactions(context.Background(), db.ListReactionsParams{ThreadID: threadID})
	if err != nil {
		return nil, err
	}
	var out []Reaction
	for _, r := range rows {
		out = append(out, reactionFromRow(r))
	}
	return out, nil
}

func (s *Store) AddReactionBySeq(threadID, messageSeq int64, emoji, name, authorType string) error {
	tx, q, err := s.tx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, _, err := s.addReactionBySeq(q, threadID, messageSeq, emoji, name, authorType, ""); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) addReactionBySeq(q *db.Queries, threadID, messageSeq int64, emoji, name, authorType, createdAt string) (int64, int64, error) {
	messageID, err := q.GetMessageIDByThreadSeq(context.Background(), db.GetMessageIDByThreadSeqParams{
		ThreadID: threadID,
		Seq:      messageSeq,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, ErrNotFound
	}
	if err != nil {
		return 0, 0, err
	}
	return s.addReaction(q, messageID, emoji, name, authorType, createdAt)
}

func (s *Store) addReaction(q *db.Queries, messageID int64, emoji, name, authorType, createdAt string) (int64, int64, error) {
	ctx := context.Background()
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

func (s *Store) AppendBatch(threadID int64, events []AppendEvent) ([]AppendResult, error) {
	tx, q, err := s.tx()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for _, event := range events {
		if err := validateAppendEvent(event); err != nil {
			return nil, err
		}
	}
	nextSeq, err := nextThreadSeq(q, threadID)
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
			var parentID int64
			if parentSeq > 0 {
				parentID, err = q.GetMessageIDByThreadSeq(context.Background(), db.GetMessageIDByThreadSeqParams{
					ThreadID: threadID,
					Seq:      parentSeq,
				})
				if err != nil {
					if errors.Is(err, sql.ErrNoRows) {
						return nil, ErrNotFound
					}
					return nil, err
				}
			}
			seq, messageID, err := s.appendMessage(q, threadID, event.Name, event.AuthorType, event.Role, event.Content, event.CreatedAt, parentID)
			if err != nil {
				return nil, err
			}
			results = append(results, AppendResult{SourceSeq: event.SourceSeq, Seq: seq, MessageID: messageID})
		case "reaction":
			messageSeq := event.MessageSeq
			if mapped, ok := sourceSeqs[messageSeq]; ok {
				messageSeq = mapped
			}
			messageID, reactionID, err := s.addReactionBySeq(q, threadID, messageSeq, event.Emoji, event.Name, event.AuthorType, event.CreatedAt)
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

func (s *Store) appendMessageByParentSeq(q *db.Queries, threadID, parentSeq int64, name, authorType, role, content, createdAt string) (int64, int64, error) {
	if parentSeq < 0 {
		return 0, 0, invalid("parent_seq must be non-negative")
	}
	var parentID int64
	if parentSeq > 0 {
		var err error
		parentID, err = q.GetMessageIDByThreadSeq(context.Background(), db.GetMessageIDByThreadSeqParams{
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
	return s.appendMessage(q, threadID, name, authorType, role, content, createdAt, parentID)
}

func nextThreadSeq(q *db.Queries, threadID int64) (int64, error) {
	ctx := context.Background()
	n, err := q.CountThreadsByID(ctx, db.CountThreadsByIDParams{ID: threadID})
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, ErrNotFound
	}
	return nextMessageSeq(ctx, q, threadID)
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
