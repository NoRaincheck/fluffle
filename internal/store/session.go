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

const sessionColumns = `id, thread_id, trigger_message_id, agent_name, status, reply_mode, command,
	cwd, exit_code, error, reply_message_id, started_at, finished_at, created_at`

type scanner interface {
	Scan(dest ...any) error
}

func scanSession(row scanner) (Session, error) {
	var s Session
	err := row.Scan(&s.ID, &s.ThreadID, &s.TriggerMessageID, &s.AgentName, &s.Status, &s.ReplyMode, &s.Command,
		&s.Cwd, &s.ExitCode, &s.Error, &s.ReplyMessageID, &s.StartedAt, &s.FinishedAt, &s.CreatedAt)
	return s, err
}

func (s *Store) CreateSession(threadID, triggerMessageID int64, agentName, status, replyMode, command string, cwd *string) (int64, error) {
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
	var msgThread int64
	if err := s.db.QueryRow(`SELECT thread_id FROM messages WHERE id = ?`, triggerMessageID).Scan(&msgThread); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	if msgThread != threadID {
		return 0, invalid("trigger message not in this thread")
	}
	res, err := s.db.Exec(`INSERT INTO agent_sessions(thread_id, trigger_message_id, agent_name, status, reply_mode, command, cwd) VALUES(?,?,?,?,?,?,?)`,
		threadID, triggerMessageID, agentName, status, replyMode, command, nullIfEmptyPtr(cwd))
	if err != nil {
		return 0, classify(err)
	}
	return res.LastInsertId()
}

func nullIfEmptyPtr(v *string) any {
	if v == nil || *v == "" {
		return nil
	}
	return *v
}

func (s *Store) ListSessions(threadID int64) ([]Session, error) {
	rows, err := s.db.Query(`SELECT `+sessionColumns+` FROM agent_sessions WHERE thread_id = ? ORDER BY id ASC`, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

func (s *Store) GetSession(id int64) (Session, error) {
	sess, err := scanSession(s.db.QueryRow(`SELECT `+sessionColumns+` FROM agent_sessions WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	return sess, err
}

func (s *Store) MarkSessionRunning(id int64, startedAt string) error {
	return s.execSessionUpdate(`UPDATE agent_sessions SET status = ?, started_at = ? WHERE id = ? AND status = ?`, SessionRunning, startedAt, id, SessionQueued)
}

func (s *Store) SetSessionReply(id int64, replyMessageID int64) error {
	return s.execSessionUpdate(`UPDATE agent_sessions SET reply_message_id = ? WHERE id = ?`, replyMessageID, id)
}

func (s *Store) FinishSession(id int64, status string, exitCode *int64, errMsg *string, finishedAt string) error {
	switch status {
	case SessionSucceeded, SessionFailed, SessionCanceled:
	default:
		return invalid("bad terminal session status %q", status)
	}
	return s.execSessionUpdate(`UPDATE agent_sessions SET status = ?, exit_code = ?, error = ?, finished_at = ? WHERE id = ?`,
		status, exitCode, nullIfEmptyPtr(errMsg), finishedAt, id)
}

func (s *Store) execSessionUpdate(query string, args ...any) error {
	res, err := s.db.Exec(query, args...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) AppendSessionEvent(sessionID int64, eventType, content string) (int64, int64, error) {
	switch eventType {
	case SessionEventPrompt, SessionEventStdout, SessionEventStderr, SessionEventExit, SessionEventError:
	default:
		return 0, 0, invalid("bad session event type %q", eventType)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM agent_sessions WHERE id = ?`, sessionID).Scan(&exists); err != nil {
		return 0, 0, err
	}
	if exists == 0 {
		return 0, 0, ErrNotFound
	}
	var maxSeq sql.NullInt64
	if err := tx.QueryRow(`SELECT MAX(seq) FROM agent_session_events WHERE session_id = ?`, sessionID).Scan(&maxSeq); err != nil {
		return 0, 0, err
	}
	seq := int64(1)
	if maxSeq.Valid {
		seq = maxSeq.Int64 + 1
	}
	res, err := tx.Exec(`INSERT INTO agent_session_events(session_id, seq, type, content) VALUES(?,?,?,?)`, sessionID, seq, eventType, content)
	if err != nil {
		return 0, 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return seq, id, nil
}

func (s *Store) ListSessionEvents(sessionID int64) ([]SessionEvent, error) {
	rows, err := s.db.Query(`SELECT id, session_id, seq, type, content, COALESCE(created_at,'') FROM agent_session_events WHERE session_id = ? ORDER BY seq ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SessionEvent{}
	for rows.Next() {
		var e SessionEvent
		if err := rows.Scan(&e.ID, &e.SessionID, &e.Seq, &e.Type, &e.Content, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) CountAgentMessagesAfter(threadID int64, name string, afterSeq int64) (int, error) {
	n, err := s.q.CountAgentMessagesAfter(context.Background(), db.CountAgentMessagesAfterParams{
		ThreadID: threadID,
		Name:     name,
		Seq:      afterSeq,
	})
	return int(n), err
}

func (s *Store) ThreadContext(threadID int64) (ThreadContext, error) {
	r, err := s.q.GetThreadContext(context.Background(), db.GetThreadContextParams{ID: threadID})
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

func (s *Store) MessageByID(id int64) (Message, error) {
	r, err := s.q.GetMessageByIDWithParent(context.Background(), db.GetMessageByIDWithParentParams{ID: id})
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	if err != nil {
		return Message{}, err
	}
	return messageFromDetailRow(r), nil
}

func (s *Store) ReconcileSessions(finishedAt string) (int, error) {
	res, err := s.db.Exec(`UPDATE agent_sessions
		SET status = ?, finished_at = ?, error = 'daemon restarted while ' || status
		WHERE status IN (?, ?)`, SessionCanceled, finishedAt, SessionQueued, SessionRunning)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}
