package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/NoRaincheck/fluffle/internal/db"
)

const (
	SessionQueued    = "queued"
	SessionRunning   = "running"
	SessionSucceeded = "succeeded"
	SessionFailed    = "failed"
	SessionCanceled  = "canceled"
)

const (
	SessionEventPrompt = "prompt"
	SessionEventStdout = "stdout"
	SessionEventStderr = "stderr"
	SessionEventExit   = "exit"
	SessionEventError  = "error"
)

type Session struct {
	ID               int64
	ThreadID         int64
	TriggerMessageID int64
	AgentName        string
	Status           string
	ReplyMode        string
	Command          string
	Cwd              *string
	ExitCode         *int64
	Error            *string
	ReplyMessageID   *int64
	StartedAt        *string
	FinishedAt       *string
	CreatedAt        string
}

type SessionEvent struct {
	ID        int64
	SessionID int64
	Seq       int64
	Type      string
	Content   string
	CreatedAt string
}

type ThreadContext struct {
	ThreadID    int64
	ThreadTitle string
	ChannelID   int64
	ChannelName string
	RepoAbsPath *string
}

func (s *Store) CreateSession(ctx context.Context, threadID, triggerMessageID int64, agentName, status, replyMode, command string, cwd *string) (int64, error) {
	if strings.TrimSpace(agentName) == "" || strings.TrimSpace(command) == "" {
		return 0, invalid("agent_name and command required")
	}
	switch status {
	case SessionQueued, SessionRunning, SessionSucceeded, SessionFailed, SessionCanceled:
	default:
		return 0, invalid("bad session status %q", status)
	}
	switch replyMode {
	case "stdout", "cli", "auto":
	default:
		return 0, invalid("bad reply mode %q", replyMode)
	}
	msgThread, err := s.q.GetMessageThreadIDByID(ctx, db.GetMessageThreadIDByIDParams{ID: triggerMessageID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	if msgThread != threadID {
		return 0, invalid("trigger message not in this thread")
	}
	id, err := s.q.CreateSession(ctx, db.CreateSessionParams{
		ThreadID:         threadID,
		TriggerMessageID: triggerMessageID,
		AgentName:        agentName,
		Status:           status,
		ReplyMode:        replyMode,
		Command:          command,
		Cwd:              nullIfEmptyPtr(cwd),
	})
	if err != nil {
		return 0, classify(err)
	}
	return id, nil
}

func nullIfEmptyPtr(v *string) *string {
	if v == nil || *v == "" {
		return nil
	}
	return v
}

func (s *Store) ListSessions(ctx context.Context, threadID int64) ([]Session, error) {
	rows, err := s.q.ListSessions(ctx, db.ListSessionsParams{ThreadID: threadID})
	if err != nil {
		return nil, err
	}
	out := []Session{}
	for _, r := range rows {
		out = append(out, sessionFromRow(r))
	}
	return out, nil
}

func (s *Store) GetSession(ctx context.Context, id int64) (Session, error) {
	r, err := s.q.GetSession(ctx, db.GetSessionParams{ID: id})
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, err
	}
	return sessionFromRow(r), nil
}

func sessionFromRow(r db.AgentSession) Session {
	return Session{
		ID:               r.ID,
		ThreadID:         r.ThreadID,
		TriggerMessageID: r.TriggerMessageID,
		AgentName:        r.AgentName,
		Status:           r.Status,
		ReplyMode:        r.ReplyMode,
		Command:          r.Command,
		Cwd:              r.Cwd,
		ExitCode:         r.ExitCode,
		Error:            r.Error,
		ReplyMessageID:   r.ReplyMessageID,
		StartedAt:        r.StartedAt,
		FinishedAt:       r.FinishedAt,
		CreatedAt:        r.CreatedAt,
	}
}

func (s *Store) MarkSessionRunning(ctx context.Context, id int64, startedAt string) error {
	n, err := s.q.MarkSessionRunning(ctx, db.MarkSessionRunningParams{
		Status:    SessionRunning,
		StartedAt: &startedAt,
		ID:        id,
		Status_2:  SessionQueued,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetSessionReply(ctx context.Context, id int64, replyMessageID int64) error {
	n, err := s.q.SetSessionReply(ctx, db.SetSessionReplyParams{
		ReplyMessageID: &replyMessageID,
		ID:             id,
	})
	if err != nil {
		return classify(err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) FinishSession(ctx context.Context, id int64, status string, exitCode *int64, errMsg *string, finishedAt string) error {
	switch status {
	case SessionSucceeded, SessionFailed, SessionCanceled:
	default:
		return invalid("bad terminal session status %q", status)
	}
	n, err := s.q.FinishSession(ctx, db.FinishSessionParams{
		Status:     status,
		ExitCode:   exitCode,
		Error:      nullIfEmptyPtr(errMsg),
		FinishedAt: &finishedAt,
		ID:         id,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) AppendSessionEvent(ctx context.Context, sessionID int64, eventType, content string) (int64, int64, error) {
	switch eventType {
	case SessionEventPrompt, SessionEventStdout, SessionEventStderr, SessionEventExit, SessionEventError:
	default:
		return 0, 0, invalid("bad session event type %q", eventType)
	}
	tx, q, err := s.tx(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	seq, err := nextSessionEventSeq(ctx, q, sessionID)
	if err != nil {
		return 0, 0, err
	}
	id, err := q.InsertSessionEvent(ctx, db.InsertSessionEventParams{
		SessionID: sessionID,
		Seq:       seq,
		Type:      eventType,
		Content:   content,
	})
	if err != nil {
		return 0, 0, classify(err)
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return seq, id, nil
}

func nextSessionEventSeq(ctx context.Context, q *db.Queries, sessionID int64) (int64, error) {
	last, err := q.GetLastSessionEventSeq(ctx, db.GetLastSessionEventSeqParams{SessionID: sessionID})
	if errors.Is(err, sql.ErrNoRows) {
		return 1, nil
	}
	if err != nil {
		return 0, err
	}
	return last + 1, nil
}

func (s *Store) ListSessionEvents(ctx context.Context, sessionID int64) ([]SessionEvent, error) {
	rows, err := s.q.ListSessionEvents(ctx, db.ListSessionEventsParams{SessionID: sessionID})
	if err != nil {
		return nil, err
	}
	out := []SessionEvent{}
	for _, r := range rows {
		out = append(out, SessionEvent{
			ID:        r.ID,
			SessionID: r.SessionID,
			Seq:       r.Seq,
			Type:      r.Type,
			Content:   r.Content,
			CreatedAt: r.CreatedAt,
		})
	}
	return out, nil
}

func (s *Store) CountAgentMessagesAfter(ctx context.Context, threadID int64, name string, afterSeq int64) (int, error) {
	n, err := s.q.CountAgentMessagesAfter(ctx, db.CountAgentMessagesAfterParams{
		ThreadID: threadID,
		Name:     name,
		Seq:      afterSeq,
	})
	return int(n), err
}

func (s *Store) ThreadContext(ctx context.Context, threadID int64) (ThreadContext, error) {
	r, err := s.q.GetThreadContext(ctx, db.GetThreadContextParams{ID: threadID})
	if errors.Is(err, sql.ErrNoRows) {
		return ThreadContext{}, ErrNotFound
	}
	if err != nil {
		return ThreadContext{}, err
	}
	return ThreadContext{
		ThreadID:    r.ThreadID,
		ThreadTitle: r.ThreadTitle,
		ChannelID:   r.ChannelID,
		ChannelName: r.ChannelName,
		RepoAbsPath: r.RepoAbsPath,
	}, nil
}

func (s *Store) MessageByID(ctx context.Context, id int64) (Message, error) {
	r, err := s.q.GetMessageByIDWithParent(ctx, db.GetMessageByIDWithParentParams{ID: id})
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	if err != nil {
		return Message{}, err
	}
	return messageFromDetailRow(r), nil
}

func (s *Store) ReconcileSessions(ctx context.Context, finishedAt string) (int, error) {
	n, err := s.q.ReconcileSessions(ctx, db.ReconcileSessionsParams{
		Status:     SessionCanceled,
		FinishedAt: &finishedAt,
		Status_2:   SessionQueued,
		Status_3:   SessionRunning,
	})
	return int(n), err
}
