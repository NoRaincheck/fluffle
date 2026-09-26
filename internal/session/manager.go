package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/NoRaincheck/fluffle/internal/agentcfg"
	"github.com/NoRaincheck/fluffle/internal/jsonl"
	"github.com/NoRaincheck/fluffle/internal/runner"
	"github.com/NoRaincheck/fluffle/internal/store"
)

const (
	MaxConcurrentSessions = 4
	StreamFlushInterval   = 250 * time.Millisecond
	StreamFlushBytes      = 8 * 1024
	ReplyGracePeriod      = 2 * time.Second
	ReplyGracePoll        = 100 * time.Millisecond
)

var ErrTerminal = errors.New("session already finished")

type Manager struct {
	store  *store.Store
	agents *agentcfg.Loader
	runner runner.Runner

	sem    chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu           sync.Mutex
	running      map[int64]context.CancelFunc
	queued       map[int64]bool
	closed       bool
	shutdownOnce sync.Once
}

func NewManager(s *store.Store, agents *agentcfg.Loader, r runner.Runner) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		store:   s,
		agents:  agents,
		runner:  r,
		sem:     make(chan struct{}, MaxConcurrentSessions),
		ctx:     ctx,
		cancel:  cancel,
		running: map[int64]context.CancelFunc{},
		queued:  map[int64]bool{},
	}
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }

func stringPtr(s string) *string { return &s }

func int64ptr(v int64) *int64 { return &v }

func auditCommand(entry agentcfg.Entry) string {
	return strings.TrimSpace(entry.Command + " " + strings.Join(entry.Args, " "))
}

func (m *Manager) Start(threadID, triggerMessageID int64, names []string) {
	tc, err := m.store.ThreadContext(threadID)
	if err != nil {
		return
	}
	repoPath := ""
	if tc.RepoAbsPath != nil {
		repoPath = *tc.RepoAbsPath
	}
	set, err := m.agents.Resolve(repoPath)
	if err != nil {
		return
	}
	trigger, err := m.store.MessageByID(triggerMessageID)
	if err != nil {
		return
	}
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		entry, ok := set.Lookup(name)
		if !ok {
			continue
		}
		id, err := m.store.CreateSession(threadID, triggerMessageID, name, store.SessionQueued, entry.Reply, auditCommand(entry), tc.RepoAbsPath)
		if err != nil {
			continue
		}
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			m.store.FinishSession(id, store.SessionCanceled, nil, stringPtr("daemon shut down before start"), nowRFC3339())
			continue
		}
		m.queued[id] = true
		m.wg.Add(1)
		m.mu.Unlock()
		go m.dispatch(id, entry, tc, trigger)
	}
}

func (m *Manager) dispatch(id int64, entry agentcfg.Entry, tc store.ThreadContext, trigger store.Message) {
	defer m.wg.Done()
	select {
	case m.sem <- struct{}{}:
	case <-m.ctx.Done():
		m.dropQueued(id)
		m.store.FinishSession(id, store.SessionCanceled, nil, stringPtr("daemon shut down before start"), nowRFC3339())
		return
	}
	defer func() { <-m.sem }()

	runCtx, cancel := context.WithCancel(m.ctx)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		cancel()
		m.dropQueued(id)
		m.store.FinishSession(id, store.SessionCanceled, nil, stringPtr("daemon shut down before start"), nowRFC3339())
		return
	}
	delete(m.queued, id)
	m.running[id] = cancel
	m.mu.Unlock()
	defer func() {
		cancel()
		m.mu.Lock()
		delete(m.running, id)
		m.mu.Unlock()
	}()

	m.run(runCtx, id, entry, tc, trigger)
}

func (m *Manager) dropQueued(id int64) {
	m.mu.Lock()
	delete(m.queued, id)
	m.mu.Unlock()
}

func (m *Manager) run(ctx context.Context, id int64, entry agentcfg.Entry, tc store.ThreadContext, trigger store.Message) {
	if err := m.store.MarkSessionRunning(id, nowRFC3339()); err != nil {
		m.failIfStillQueued(id, err)
		return
	}
	prompt, err := buildPrompt(promptInput{
		Agent:         entry,
		Thread:        tc,
		History:       m.historyFor(tc.ThreadID),
		Trigger:       trigger,
		NeedsReplying: entry.Reply != "stdout",
	})
	if err != nil {
		m.failSession(id, err)
		return
	}
	m.store.AppendSessionEvent(id, store.SessionEventPrompt, prompt)

	writer := newStreamWriter(m.store, id)
	req := runner.Request{
		Command: entry.Command,
		Env:     envSlice(entry.Env),
		Timeout: time.Duration(entry.TimeoutSecs) * time.Second,
		OnChunk: writer.write,
	}
	req.Args, req.Stdin = deliverPrompt(entry.Args, prompt)
	if tc.RepoAbsPath != nil {
		req.Cwd = *tc.RepoAbsPath
	}

	result, runErr := m.runner.Run(ctx, req)
	writer.closeAll()

	if runErr != nil {
		m.store.AppendSessionEvent(id, store.SessionEventError, runErr.Error())
		if errors.Is(ctx.Err(), context.Canceled) {
			m.store.FinishSession(id, store.SessionCanceled, nil, stringPtr(runErr.Error()), nowRFC3339())
			return
		}
		m.store.FinishSession(id, store.SessionFailed, nil, stringPtr(runErr.Error()), nowRFC3339())
		return
	}

	m.store.AppendSessionEvent(id, store.SessionEventExit, fmt.Sprintf("%d", result.ExitCode))
	m.resolveReply(id, entry, tc, result)
}

func (m *Manager) failSession(id int64, err error) {
	m.store.AppendSessionEvent(id, store.SessionEventError, err.Error())
	m.store.FinishSession(id, store.SessionFailed, nil, stringPtr(err.Error()), nowRFC3339())
}

func (m *Manager) failIfStillQueued(id int64, err error) {
	if sess, getErr := m.store.GetSession(id); getErr == nil && sess.Status != store.SessionQueued {
		return
	}
	m.failSession(id, err)
}

func (m *Manager) historyFor(threadID int64) []jsonl.Line {
	messages, err := m.store.ListMessages(threadID, 0)
	if err != nil {
		return nil
	}
	reactions, err := m.store.ListReactions(threadID)
	if err != nil {
		reactions = nil
	}
	lines := make([]jsonl.Line, 0, len(messages)+len(reactions))
	for _, msg := range messages {
		lines = append(lines, jsonl.Line{
			Type:       "message",
			Seq:        msg.Seq,
			ParentSeq:  msg.ParentSeq.Int64,
			Role:       msg.Role,
			Name:       msg.Name,
			AuthorType: msg.AuthorType,
			Content:    msg.Content,
			Timestamp:  msg.CreatedAt,
		})
	}
	for _, r := range reactions {
		lines = append(lines, jsonl.Line{
			Type:       "reaction",
			MessageSeq: r.MessageSeq,
			Name:       r.Name,
			AuthorType: r.AuthorType,
			Emoji:      r.Emoji,
			Timestamp:  r.CreatedAt,
		})
	}
	return lines
}

func (m *Manager) Reconcile() error {
	_, err := m.store.ReconcileSessions(nowRFC3339())
	return err
}

// Shutdown refuses new work, cancels running sessions, and marks still-queued
// sessions canceled without waiting for them to unwind. It never waits on a
// session goroutine, so it is safe to call from any goroutine.
func (m *Manager) Shutdown() {
	m.shutdownOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		queued := make([]int64, 0, len(m.queued))
		for id := range m.queued {
			queued = append(queued, id)
		}
		m.queued = map[int64]bool{}
		m.mu.Unlock()

		m.cancel()
		for _, id := range queued {
			m.store.FinishSession(id, store.SessionCanceled, nil, stringPtr("daemon shut down before start"), nowRFC3339())
		}
	})
}

// ShutdownAndWait is Shutdown followed by a barrier: it does not return until
// every session row is terminal, for this caller and for any concurrent one.
// It must not be called from a session goroutine, which would wait on itself.
func (m *Manager) ShutdownAndWait() {
	m.Shutdown()
	m.wg.Wait()
}

func (m *Manager) Cancel(id int64) error {
	sess, err := m.store.GetSession(id)
	if err != nil {
		return err
	}
	switch sess.Status {
	case store.SessionSucceeded, store.SessionFailed, store.SessionCanceled:
		return ErrTerminal
	}
	m.mu.Lock()
	cancel, ok := m.running[id]
	m.mu.Unlock()
	if ok {
		cancel()
		return nil
	}
	m.store.FinishSession(id, store.SessionCanceled, nil, stringPtr("canceled"), nowRFC3339())
	return nil
}

func (m *Manager) resolveReply(id int64, entry agentcfg.Entry, tc store.ThreadContext, result runner.Result) {
	exit := int64ptr(int64(result.ExitCode))
	if entry.Reply == "stdout" {
		m.postReply(id, entry, tc, m.collectStdout(id), exit)
		return
	}
	if m.agentPosted(tc.ThreadID, entry.Name, id) {
		m.store.FinishSession(id, store.SessionSucceeded, exit, nil, nowRFC3339())
		return
	}
	if entry.Reply == "cli" {
		const msg = "agent did not reply (reply=cli)"
		m.store.AppendSessionEvent(id, store.SessionEventError, msg)
		m.store.FinishSession(id, store.SessionFailed, exit, stringPtr(msg), nowRFC3339())
		return
	}
	m.postReply(id, entry, tc, m.collectStdout(id), exit)
}

func (m *Manager) collectStdout(sessionID int64) string {
	events, err := m.store.ListSessionEvents(sessionID)
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, e := range events {
		if e.Type == store.SessionEventStdout {
			b.WriteString(e.Content)
		}
	}
	return strings.TrimSpace(b.String())
}

func (m *Manager) agentPosted(threadID int64, name string, sessionID int64) bool {
	sess, err := m.store.GetSession(sessionID)
	if err != nil {
		return false
	}
	since := nowRFC3339()
	if sess.StartedAt != nil {
		since = *sess.StartedAt
	}
	since = strings.TrimSuffix(since, "Z")
	deadline := time.Now().Add(ReplyGracePeriod)
	for {
		n, err := m.store.CountAgentMessagesSince(threadID, name, since)
		if err == nil && n > 0 {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(ReplyGracePoll)
	}
}

func (m *Manager) postReply(id int64, entry agentcfg.Entry, tc store.ThreadContext, content string, exit *int64) {
	if strings.TrimSpace(content) == "" {
		m.store.FinishSession(id, store.SessionSucceeded, exit, nil, nowRFC3339())
		return
	}
	sess, err := m.store.GetSession(id)
	if err != nil {
		m.store.FinishSession(id, store.SessionFailed, nil, stringPtr(err.Error()), nowRFC3339())
		return
	}
	seq, err := m.store.AppendMessageWithParent(tc.ThreadID, entry.Name, "agent", "assistant", content, sess.TriggerMessageID)
	if err != nil {
		m.store.FinishSession(id, store.SessionFailed, exit, stringPtr(err.Error()), nowRFC3339())
		return
	}
	replyID, err := m.store.MessageIDBySeq(tc.ThreadID, seq)
	if err != nil {
		m.store.FinishSession(id, store.SessionFailed, exit, stringPtr(err.Error()), nowRFC3339())
		return
	}
	m.store.SetSessionReply(id, replyID)
	m.store.FinishSession(id, store.SessionSucceeded, exit, nil, nowRFC3339())
}
