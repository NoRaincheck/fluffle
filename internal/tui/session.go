package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/NoRaincheck/fluffle/internal/store"
)

const (
	sessionTickInterval = 500 * time.Millisecond
	sessionTypeWidth    = 8
)

type previewMode int

const (
	previewThread previewMode = iota
	previewSession
)

type sessionsFetchedMsg struct {
	threadID int64
	sessions []store.Session
	err      error
}

type sessionEventsFetchedMsg struct {
	session store.Session
	events  []store.SessionEvent
	err     error
}

type sessionTickMsg time.Time

func sessionTickCmd(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg { return sessionTickMsg(t) })
}

func (m *model) fetchSessions(threadID int64) tea.Cmd {
	return func() tea.Msg {
		sessions, err := m.api.ListSessions(nil, threadID)
		return sessionsFetchedMsg{threadID: threadID, sessions: sessions, err: err}
	}
}

func (m *model) fetchSessionEvents(sessionID int64) tea.Cmd {
	return func() tea.Msg {
		sess, events, err := m.api.GetSession(nil, sessionID)
		return sessionEventsFetchedMsg{session: sess, events: events, err: err}
	}
}

func (m *model) fetchSessionsForPreview() tea.Cmd {
	if m.view != viewInbox || m.previewThreadID == 0 {
		return nil
	}
	if m.sessionPollThreadID == m.previewThreadID {
		return nil
	}
	return m.fetchSessions(m.previewThreadID)
}

func (m *model) applySessions(sessions []store.Session) (tea.Model, tea.Cmd) {
	m.sessions = sessions
	m.sessionsByMsg = make(map[int64]store.Session, len(sessions))
	for _, s := range sessions {
		m.sessionsByMsg[s.TriggerMessageID] = s
	}
	if m.session != nil {
		if fresh, ok := m.sessionsByMsg[m.session.TriggerMessageID]; ok {
			fresh.ID = m.session.ID
			m.session = &fresh
		}
	}
	return m, m.syncSessionTick()
}

func isSessionLive(status string) bool {
	return status == store.SessionQueued || status == store.SessionRunning
}

func (m *model) anySessionActive() bool {
	for _, s := range m.sessions {
		if isSessionLive(s.Status) {
			return true
		}
	}
	return false
}

func (m *model) syncSessionTick() tea.Cmd {
	if !m.anySessionActive() || !m.previewVisible() {
		return nil
	}
	return sessionTickCmd(sessionTickInterval)
}

func (m *model) sessionForCursor() (store.Session, bool) {
	rows := m.inboxFilteredSorted()
	if m.cursor < 0 || m.cursor >= len(rows) {
		return store.Session{}, false
	}
	s, ok := m.sessionsByMsg[rows[m.cursor].ID]
	return s, ok
}

func (m *model) handleSessionKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if msg.Type != tea.KeyRunes || string(msg.Runes) != "s" {
		return m, nil, false
	}
	if m.view != viewInbox || !m.previewVisible() {
		return m, nil, false
	}
	if m.previewMode == previewSession {
		m.previewMode = previewThread
		m.session = nil
		m.sessionEvents = nil
		return m, nil, true
	}
	sess, ok := m.sessionForCursor()
	if !ok {
		m.status = "no session on this message"
		return m, nil, false
	}
	m.previewMode = previewSession
	m.session = &sess
	m.sessionEvents = nil
	return m, m.fetchSessionEvents(sess.ID), true
}

func (m model) renderSessionPreview(w, h int) string {
	if h < 3 {
		return ""
	}
	if w < minContentWidth {
		w = minContentWidth
	}
	sess := m.session
	if resolved, ok := m.sessionForCursor(); ok {
		sess = &resolved
	}
	if sess == nil {
		return lipgloss.NewStyle().Width(w).Render("  no session on this message")
	}
	s := *sess
	head := fmt.Sprintf("SESSION  %s · %s · %s · #%d", s.AgentName, s.Status, s.ReplyMode, s.ID)
	if !isSessionLive(s.Status) && s.StartedAt != nil && s.FinishedAt != nil {
		if d := sessionDuration(*s.StartedAt, *s.FinishedAt); d != "" {
			head += " · " + d
		}
	}
	if s.ReplyMessageID != nil {
		head += fmt.Sprintf(" · replied #%d", *s.ReplyMessageID)
	}
	if s.Error != nil {
		head += " · " + truncRunes(*s.Error, 40)
	}
	head = strings.ReplaceAll(stripAnsi(head), "\n", " ")
	lines := []string{truncRunes(head, w)}
	if len(m.sessionEvents) == 0 {
		if isSessionLive(s.Status) {
			lines = append(lines, "  (running…)")
		} else {
			lines = append(lines, "  (no events)")
		}
		return lipgloss.NewStyle().Width(w).Render(strings.Join(lines, "\n"))
	}

	rows := len(m.sessionEvents)
	end := min(h-3, rows)
	if end < 1 {
		end = 1
	}
	for i := 0; i < end; i++ {
		e := m.sessionEvents[i]
		content := strings.ReplaceAll(firstLine(e.Content), "\t", "    ")
		lines = append(lines, fmt.Sprintf("  %s %s", formatFixedName(e.Type, sessionTypeWidth), truncRunes(content, max(1, w-2-sessionTypeWidth-1))))
	}
	if end < rows {
		lines = append(lines, fmt.Sprintf("  … %d hidden …", rows-end))
	}
	return lipgloss.NewStyle().Width(w).Render(strings.Join(lines, "\n"))
}

func sessionDuration(startedAt, finishedAt string) string {
	start, err := time.Parse(time.RFC3339, startedAt)
	if err != nil {
		return ""
	}
	end, err := time.Parse(time.RFC3339, finishedAt)
	if err != nil {
		return ""
	}
	d := end.Sub(start)
	if d < 0 {
		d = 0
	}
	return d.Round(time.Second).String()
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}

func truncRunes(s string, maxLen int) string {
	if maxLen < 1 {
		return ""
	}
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= ellipsisReserve {
		return trimPartialRune(s, maxLen)
	}
	return trimPartialRune(s, maxLen-ellipsisReserve) + "..."
}

func trimPartialRune(s string, n int) string {
	if n > len(s) {
		n = len(s)
	}
	for n > 0 && n < len(s) && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
