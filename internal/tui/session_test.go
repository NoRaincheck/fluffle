package tui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/NoRaincheck/fluffle/internal/store"
)

func strptr(s string) *string { return &s }

func int64ptr(v int64) *int64 { return &v }

func flattenMsgs(msg tea.Msg) []tea.Msg {
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return []tea.Msg{msg}
	}
	var out []tea.Msg
	for _, c := range batch {
		if c == nil {
			continue
		}
		out = append(out, flattenMsgs(c())...)
	}
	return out
}

func sessionServer(t *testing.T, sessions []store.Session, events []store.SessionEvent) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/threads/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sessions)
	})
	mux.HandleFunc("/v1/sessions/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"session": store.Session{ID: 1, AgentName: "reviewer", Status: store.SessionSucceeded, ReplyMode: "auto"},
			"events":  events,
		})
	})
	mux.HandleFunc("/v1/inbox", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]store.InboxMessage{})
	})
	mux.HandleFunc("/v1/channels", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]store.Channel{})
	})
	return httptest.NewServer(mux)
}

func sessionModel(t *testing.T, sessions []store.Session, events []store.SessionEvent) model {
	t.Helper()
	srv := sessionServer(t, sessions, events)
	t.Cleanup(srv.Close)
	m := toModel(New(srv.URL))
	m.view = viewInbox
	m.width = 120
	m.height = 40
	m.preview = true
	m.inboxLayout = inboxLayoutCompact
	return m
}

func TestSessionKeyRequiresASessionOnTheCursorRow(t *testing.T) {
	m := sessionModel(t, nil, nil)
	next, _, handled := m.handleSessionKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	got := toModel(next)
	if handled {
		t.Fatal("s must not be handled when the cursor row has no session")
	}
	if got.previewMode != previewThread {
		t.Fatalf("previewMode = %v", got.previewMode)
	}
}

func TestSessionKeyFlipsToSessionMode(t *testing.T) {
	one := store.Session{ID: 5, TriggerMessageID: 42, AgentName: "reviewer", Status: store.SessionSucceeded}
	m := sessionModel(t, []store.Session{one}, nil)
	m.inbox = []store.InboxMessage{{Message: store.Message{ID: 42}}}
	m.sessionsByMsg = map[int64]store.Session{42: one}
	next, _, handled := m.handleSessionKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	got := toModel(next)
	if !handled {
		t.Fatal("s should be handled when the cursor row has a session")
	}
	if got.previewMode != previewSession {
		t.Fatalf("previewMode = %v, want previewSession", got.previewMode)
	}
}

func TestSessionKeyFlipsBackToThread(t *testing.T) {
	one := store.Session{ID: 5, TriggerMessageID: 42}
	m := sessionModel(t, []store.Session{one}, nil)
	m.inbox = []store.InboxMessage{{Message: store.Message{ID: 42}}}
	m.sessionsByMsg = map[int64]store.Session{42: one}
	m.previewMode = previewSession
	next, _, handled := m.handleSessionKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	got := toModel(next)
	if !handled || got.previewMode != previewThread {
		t.Fatalf("handled = %v previewMode = %v", handled, got.previewMode)
	}
}

func TestSessionKeyReportsTheFallbackOnTheStatusLine(t *testing.T) {
	m := sessionModel(t, nil, nil)
	m.inbox = []store.InboxMessage{{Message: store.Message{ID: 7}}}
	next, _, handled := m.handleSessionKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	got := toModel(next)
	if handled {
		t.Fatal("s must not be handled when the cursor row has no session")
	}
	if !strings.Contains(got.status, "no session") {
		t.Fatalf("status = %q, want a no-session hint", got.status)
	}
	if got.previewMode != previewThread {
		t.Fatalf("previewMode = %v, want the thread pane to stay", got.previewMode)
	}
}

func TestSessionKeyIsIgnoredWhereThePaneIsHidden(t *testing.T) {
	one := store.Session{ID: 5, TriggerMessageID: 42, Status: store.SessionSucceeded}
	m := sessionModel(t, []store.Session{one}, nil)
	m.inbox = []store.InboxMessage{{Message: store.Message{ID: 42}}}
	m.sessionsByMsg = map[int64]store.Session{42: one}
	m.view = viewInboxDetail
	next, _, handled := m.handleSessionKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	got := toModel(next)
	if handled {
		t.Fatal("s must respect the previewVisible gate")
	}
	if got.previewMode != previewThread {
		t.Fatalf("previewMode = %v", got.previewMode)
	}
}

func TestRenderSessionPreviewShowsHeaderAndEvents(t *testing.T) {
	one := store.Session{ID: 5, AgentName: "reviewer", Status: store.SessionSucceeded, ReplyMode: "auto", StartedAt: strptr("t0"), FinishedAt: strptr("t1"), ReplyMessageID: int64ptr(9)}
	events := []store.SessionEvent{
		{Seq: 1, Type: store.SessionEventPrompt, Content: "the prompt"},
		{Seq: 2, Type: store.SessionEventStdout, Content: "the answer"},
		{Seq: 3, Type: store.SessionEventExit, Content: "0"},
	}
	m := sessionModel(t, []store.Session{one}, events)
	m.session = &one
	m.sessionEvents = events
	out := stripAnsi(m.renderSessionPreview(60, 20))
	for _, want := range []string{"SESSION", "reviewer", "succeeded", "stdout", "prompt", "the answer", "exit"} {
		if !strings.Contains(out, want) {
			t.Fatalf("preview missing %q\n---\n%s", want, out)
		}
	}
}

func TestRenderSessionPreviewShowsRunningStatus(t *testing.T) {
	one := store.Session{ID: 5, AgentName: "reviewer", Status: store.SessionRunning, ReplyMode: "auto"}
	m := sessionModel(t, []store.Session{one}, nil)
	m.session = &one
	out := stripAnsi(m.renderSessionPreview(60, 20))
	if !strings.Contains(out, "running") && !strings.Contains(out, "RUNNING") {
		t.Fatalf("running status not shown:\n%s", out)
	}
}

func TestRenderSessionPreviewShowsReplyLinkage(t *testing.T) {
	withReply := store.Session{ID: 5, AgentName: "reviewer", Status: store.SessionSucceeded, ReplyMode: "auto", ReplyMessageID: int64ptr(9)}
	m := sessionModel(t, []store.Session{withReply}, nil)
	m.session = &withReply
	if !strings.Contains(stripAnsi(m.renderSessionPreview(80, 20)), "9") {
		t.Fatal("reply message id not rendered")
	}
}

func TestRenderSessionPreviewHandlesNoSession(t *testing.T) {
	m := sessionModel(t, nil, nil)
	out := stripAnsi(m.renderSessionPreview(60, 20))
	if !strings.Contains(out, "no session") {
		t.Fatalf("empty state missing:\n%s", out)
	}
}

func TestRenderSessionPreviewSurvivesALargeEventBody(t *testing.T) {
	one := store.Session{ID: 5, AgentName: "reviewer", Status: store.SessionSucceeded, ReplyMode: "auto"}
	body := strings.Repeat("é→x", 90000) + "TAILMARKER"
	events := []store.SessionEvent{
		{Seq: 1, Type: store.SessionEventPrompt, Content: "look at this"},
		{Seq: 2, Type: store.SessionEventStdout, Content: body},
		{Seq: 3, Type: store.SessionEventExit, Content: "0"},
	}
	m := sessionModel(t, []store.Session{one}, events)
	m.session = &one
	m.sessionEvents = events
	const w = 60
	out := stripAnsi(m.renderSessionPreview(w, 20))
	if !utf8.ValidString(out) {
		t.Fatal("a large non-ASCII event body corrupted the pane")
	}
	for i, line := range strings.Split(out, "\n") {
		if got := lipgloss.Width(line); got > w {
			t.Fatalf("line %d width = %d, want <= %d: %q", i, got, w, line)
		}
	}
	if !strings.Contains(out, "look at this") {
		t.Fatalf("the small event was dropped:\n%s", out)
	}
	if !strings.Contains(out, store.SessionEventExit) {
		t.Fatalf("the tail event was dropped:\n%s", out)
	}
}

func TestRenderSessionPreviewTruncatesWholeRunes(t *testing.T) {
	one := store.Session{ID: 5, AgentName: "reviewer", Status: store.SessionSucceeded, ReplyMode: "auto"}
	events := []store.SessionEvent{{Seq: 1, Type: store.SessionEventStdout, Content: strings.Repeat("日", 40)}}
	m := sessionModel(t, []store.Session{one}, events)
	m.session = &one
	m.sessionEvents = events
	for _, w := range []int{20, 30, 47} {
		out := stripAnsi(m.renderSessionPreview(w, 12))
		if !utf8.ValidString(out) {
			t.Fatalf("width %d produced invalid utf-8:\n%q", w, out)
		}
		for i, line := range strings.Split(out, "\n") {
			if got := lipgloss.Width(line); got > w {
				t.Fatalf("width %d: line %d is %d wide: %q", w, i, got, line)
			}
		}
	}
}

func TestTickOnlySchedulesWhileASessionIsNonTerminal(t *testing.T) {
	m := sessionModel(t, nil, nil)
	m.sessions = []store.Session{{ID: 1, Status: store.SessionSucceeded}}
	if cmd := m.syncSessionTick(); cmd != nil {
		t.Fatal("all sessions terminal must not schedule a tick")
	}
	m.sessions = []store.Session{{ID: 1, Status: store.SessionSucceeded}, {ID: 2, Status: store.SessionRunning}}
	if cmd := m.syncSessionTick(); cmd == nil {
		t.Fatal("a running session must schedule a tick")
	}
}

func TestTickStopsAfterReachingTerminal(t *testing.T) {
	m := sessionModel(t, nil, nil)
	m.sessions = []store.Session{{ID: 1, Status: store.SessionRunning}}
	if cmd := m.syncSessionTick(); cmd == nil {
		t.Fatal("expected a tick while running")
	}
	msg := sessionsFetchedMsg{sessions: []store.Session{{ID: 1, Status: store.SessionSucceeded}}}
	next, cmd := m.applySessions(msg.sessions)
	got := toModel(next)
	if got.sessions[0].Status != store.SessionSucceeded {
		t.Fatalf("status = %q", got.sessions[0].Status)
	}
	if cmd != nil {
		t.Fatal("terminal sessions must not reschedule the tick")
	}
}

func TestTickHandlerRefetchesWhileActiveThenStops(t *testing.T) {
	m := sessionModel(t, nil, nil)
	m.sessions = []store.Session{{ID: 1, Status: store.SessionRunning}}
	next, cmd := m.Update(sessionTickMsg(time.Now()))
	if cmd == nil {
		t.Fatal("a tick while running must reschedule itself")
	}
	next, cmd = toModel(next).Update(sessionsFetchedMsg{sessions: []store.Session{{ID: 1, Status: store.SessionSucceeded}}})
	if cmd != nil {
		t.Fatal("terminal sessions must not reschedule the tick")
	}
	_, cmd = toModel(next).Update(sessionTickMsg(time.Now()))
	if cmd != nil {
		t.Fatal("the tick must go quiet once every session is terminal")
	}
}

func TestApplySessionsRebuildsTheMessageIndex(t *testing.T) {
	m := sessionModel(t, nil, nil)
	next, _ := m.applySessions([]store.Session{
		{ID: 1, TriggerMessageID: 10},
		{ID: 2, TriggerMessageID: 20},
	})
	got := toModel(next)
	if len(got.sessionsByMsg) != 2 {
		t.Fatalf("sessionsByMsg = %+v", got.sessionsByMsg)
	}
	if got.sessionsByMsg[20].ID != 2 {
		t.Fatalf("index = %+v", got.sessionsByMsg)
	}
}

func TestPreviewSyncFetchesSessionsForTheCursorThread(t *testing.T) {
	one := store.Session{ID: 5, TriggerMessageID: 42, Status: store.SessionSucceeded}
	m := sessionModel(t, []store.Session{one}, nil)
	m.inbox = []store.InboxMessage{{Message: store.Message{ID: 42, ThreadID: 7}, ChannelID: 1}}
	cmd := m.maybeFetchPreview()
	if cmd == nil {
		t.Fatal("preview sync must fetch sessions for the cursor thread")
	}
	for _, msg := range flattenMsgs(cmd()) {
		sm, ok := msg.(sessionsFetchedMsg)
		if !ok {
			continue
		}
		if sm.err != nil {
			t.Fatalf("ListSessions = %v", sm.err)
		}
		if len(sm.sessions) != 1 || sm.sessions[0].ID != 5 {
			t.Fatalf("sessions = %+v", sm.sessions)
		}
		return
	}
	t.Fatalf("preview sync never fetched sessions, got %T", cmd())
}

func TestAnActiveSessionDiscoveredOnEntryKeepsTheTickAlive(t *testing.T) {
	running := store.Session{ID: 3, TriggerMessageID: 42, Status: store.SessionRunning}
	m := sessionModel(t, []store.Session{running}, nil)
	m.inbox = []store.InboxMessage{{Message: store.Message{ID: 42, ThreadID: 7}, ChannelID: 1}}
	cmd := m.maybeFetchPreview()
	if cmd == nil {
		t.Fatal("preview sync must fetch sessions on entry")
	}
	var fetched sessionsFetchedMsg
	for _, msg := range flattenMsgs(cmd()) {
		if sm, ok := msg.(sessionsFetchedMsg); ok {
			fetched = sm
		}
	}
	next, tick := m.Update(fetched)
	if toModel(next).sessions[0].Status != store.SessionRunning {
		t.Fatal("the running session was not indexed")
	}
	if tick == nil {
		t.Fatal("a running session discovered on entry must start the tick")
	}
}

func TestPressingSThroughUpdateFlipsThePreviewPane(t *testing.T) {
	one := store.Session{ID: 5, TriggerMessageID: 42, Status: store.SessionSucceeded}
	m := sessionModel(t, []store.Session{one}, nil)
	m.inbox = []store.InboxMessage{{Message: store.Message{ID: 42, ThreadID: 7}, ChannelID: 1}}
	m.sessionsByMsg = map[int64]store.Session{42: one}
	next, _ := m.Update(keyRunes("s"))
	if toModel(next).previewMode != previewSession {
		t.Fatal("s through Update must reach handleSessionKey")
	}
}

func TestPreviewPaneRoutesToTheSessionRenderer(t *testing.T) {
	one := store.Session{ID: 5, AgentName: "reviewer", Status: store.SessionSucceeded, ReplyMode: "auto"}
	m := sessionModel(t, []store.Session{one}, nil)
	m.session = &one
	m.previewMode = previewSession
	if !strings.Contains(stripAnsi(m.renderPreview(60, 20)), "SESSION") {
		t.Fatalf("renderPreview must delegate to the session pane:\n%s", stripAnsi(m.renderPreview(60, 20)))
	}
	m.previewMode = previewThread
	if strings.Contains(stripAnsi(m.renderPreview(60, 20)), "SESSION  reviewer") {
		t.Fatal("the thread pane must come back in thread mode")
	}
}

func TestHelpMentionsTheSessionKey(t *testing.T) {
	m := sessionModel(t, nil, nil)
	if !strings.Contains(stripAnsi(m.helpView()), "s") {
		t.Fatal("helpView must document the s key")
	}
}
