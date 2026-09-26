package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
	one := store.Session{ID: 5, AgentName: "reviewer", Status: store.SessionSucceeded, ReplyMode: "auto", StartedAt: strptr("2026-09-26T10:00:00Z"), FinishedAt: strptr("2026-09-26T10:00:12Z"), ReplyMessageID: int64ptr(9)}
	events := []store.SessionEvent{
		{Seq: 1, Type: store.SessionEventPrompt, Content: "the prompt"},
		{Seq: 2, Type: store.SessionEventStdout, Content: "the answer"},
		{Seq: 3, Type: store.SessionEventExit, Content: "0"},
	}
	m := sessionModel(t, []store.Session{one}, events)
	m.session = &one
	m.sessionEvents = events
	out := stripAnsi(m.renderSessionPreview(60, 20))
	for _, want := range []string{"SESSION", "reviewer", "succeeded", "stdout", "prompt", "the answer", "exit", "12s"} {
		if !strings.Contains(out, want) {
			t.Fatalf("preview missing %q\n---\n%s", want, out)
		}
	}
}

func TestRenderSessionPreviewOmitsTheDurationWhileRunning(t *testing.T) {
	one := store.Session{ID: 5, AgentName: "reviewer", Status: store.SessionRunning, ReplyMode: "auto",
		StartedAt: strptr("2026-09-26T10:00:00Z"), FinishedAt: strptr("2026-09-26T10:00:12Z")}
	m := sessionModel(t, []store.Session{one}, nil)
	m.session = &one
	header := strings.Split(stripAnsi(m.renderSessionPreview(80, 20)), "\n")[0]
	if strings.Contains(header, "12s") {
		t.Fatalf("a live session has no elapsed duration: %q", header)
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

func TestEnteringTheInboxBootstrapsTheSessionIndex(t *testing.T) {
	one := store.Session{ID: 5, TriggerMessageID: 42, Status: store.SessionSucceeded}
	m := sessionModel(t, []store.Session{one}, nil)
	m.inbox = []store.InboxMessage{{Message: store.Message{ID: 42, ThreadID: 7}, ChannelID: 1, ChannelName: "c", ThreadTitle: "t"}}
	cmd := m.maybeFetchPreview()
	if cmd == nil {
		t.Fatal("preview sync must fetch the thread's messages on entry")
	}
	var messages previewMessagesFetchedMsg
	for _, msg := range flattenMsgs(cmd()) {
		if pm, ok := msg.(previewMessagesFetchedMsg); ok {
			messages = pm
		}
	}
	if messages.threadID != 7 {
		t.Fatalf("preview did not fetch thread 7, got %+v", messages)
	}
	next, cmd := m.Update(messages)
	got := toModel(next)
	if got.previewThreadID != 7 {
		t.Fatalf("previewThreadID = %d", got.previewThreadID)
	}
	if cmd == nil {
		t.Fatal("entering a thread must fetch its sessions")
	}
	for _, msg := range flattenMsgs(cmd()) {
		sm, ok := msg.(sessionsFetchedMsg)
		if !ok {
			continue
		}
		if sm.err != nil {
			t.Fatalf("ListSessions = %v", sm.err)
		}
		if sm.threadID != 7 {
			t.Fatalf("sessions fetched for thread %d, want 7", sm.threadID)
		}
		if len(sm.sessions) != 1 || sm.sessions[0].ID != 5 {
			t.Fatalf("sessions = %+v", sm.sessions)
		}
		return
	}
	t.Fatal("entering a thread never fetched its sessions")
}

func TestAnActiveSessionDiscoveredOnEntryKeepsTheTickAlive(t *testing.T) {
	running := store.Session{ID: 3, TriggerMessageID: 42, Status: store.SessionRunning}
	m := sessionModel(t, []store.Session{running}, nil)
	m.inbox = []store.InboxMessage{{Message: store.Message{ID: 42, ThreadID: 7}, ChannelID: 1, ChannelName: "c", ThreadTitle: "t"}}
	next, _ := m.Update(previewMessagesFetchedMsg{threadID: 7})
	next, cmd := toModel(next).Update(sessionsFetchedMsg{threadID: 7, sessions: []store.Session{running}})
	got := toModel(next)
	if got.sessions[0].Status != store.SessionRunning {
		t.Fatal("the running session was not indexed")
	}
	if got.sessionPollThreadID != 7 {
		t.Fatalf("sessionPollThreadID = %d, want 7", got.sessionPollThreadID)
	}
	if cmd == nil {
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
	if out := stripAnsi(m.helpView()); !strings.Contains(out, "s session") {
		t.Fatalf("helpView must document the s key, got %q", out)
	}
}

func TestHelpOffersTheThreadKeyInSessionMode(t *testing.T) {
	m := sessionModel(t, nil, nil)
	m.previewMode = previewSession
	out := stripAnsi(m.helpView())
	if !strings.Contains(out, "s thread") {
		t.Fatalf("helpView must offer the way back, got %q", out)
	}
}

func assertPaneIsIntact(t *testing.T, out string, w int) {
	t.Helper()
	if !utf8.ValidString(out) {
		t.Fatal("the pane produced invalid utf-8")
	}
	for i, line := range strings.Split(out, "\n") {
		if got := lipgloss.Width(line); got > w {
			t.Fatalf("line %d width = %d, want <= %d: %q", i, got, w, line)
		}
	}
}

func TestRenderSessionPreviewBoundsALongAgentName(t *testing.T) {
	one := store.Session{ID: 5, AgentName: strings.Repeat("é", 500), Status: store.SessionSucceeded, ReplyMode: "auto"}
	m := sessionModel(t, []store.Session{one}, nil)
	m.session = &one
	assertPaneIsIntact(t, stripAnsi(m.renderSessionPreview(60, 20)), 60)
}

func TestRenderSessionPreviewScrubsTheErrorField(t *testing.T) {
	one := store.Session{ID: 5, AgentName: "reviewer", Status: store.SessionFailed, ReplyMode: "auto",
		Error: strptr("\x1b[31mboom\nsecond line\x1b[0m")}
	m := sessionModel(t, []store.Session{one}, nil)
	m.session = &one
	raw := m.renderSessionPreview(60, 20)
	if strings.Contains(raw, "\x1b") {
		t.Fatalf("the daemon-supplied error leaked escape codes into the header: %q", raw)
	}
	lines := strings.Split(raw, "\n")
	if len(lines) != 2 {
		t.Fatalf("the daemon-supplied error injected a line break: %q", raw)
	}
	if !strings.Contains(lines[0], "boom second") {
		t.Fatalf("the newline was not folded into a space: %q", lines[0])
	}
	assertPaneIsIntact(t, stripAnsi(raw), 60)
}

func TestEventWindowIsTopAnchoredAndBounded(t *testing.T) {
	one := store.Session{ID: 5, AgentName: "reviewer", Status: store.SessionSucceeded, ReplyMode: "auto"}
	events := make([]store.SessionEvent, 60)
	for i := range events {
		events[i] = store.SessionEvent{Seq: int64(i + 1), Type: store.SessionEventStdout, Content: fmt.Sprintf("chunk%d", i)}
	}
	m := sessionModel(t, []store.Session{one}, events)
	m.session = &one
	m.sessionEvents = events
	out := stripAnsi(m.renderSessionPreview(50, 8))
	lines := strings.Split(out, "\n")
	if len(lines) > 8 {
		t.Fatalf("rendered %d lines at h=8:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[1], "chunk0") {
		t.Fatalf("the window must be anchored on the first event:\n%s", out)
	}
	if !strings.Contains(lines[5], "chunk4") {
		t.Fatalf("h-3 events must be visible:\n%s", out)
	}
	if strings.Contains(out, "chunk5") {
		t.Fatalf("the window must stop after h-3 events:\n%s", out)
	}
}

func TestSessionKeyResolvesThroughAnActiveFilter(t *testing.T) {
	one := store.Session{ID: 5, TriggerMessageID: 42, Status: store.SessionSucceeded}
	m := sessionModel(t, []store.Session{one}, nil)
	m.inbox = []store.InboxMessage{
		{Message: store.Message{ID: 1, ThreadID: 7, CreatedAt: "2026-09-26T12:00:00Z"}, ChannelID: 1, ChannelName: "ui", ThreadTitle: "alpha"},
		{Message: store.Message{ID: 42, ThreadID: 8, CreatedAt: "2026-09-26T10:00:00Z"}, ChannelID: 2, ChannelName: "core", ThreadTitle: "beta"},
	}
	m.sessionsByMsg = map[int64]store.Session{42: one}
	if _, ok := m.sessionForCursor(); ok {
		t.Fatal("precondition: unfiltered, the cursor row must have no session")
	}
	m.inboxFilterChan = "core"
	if rows := m.inboxFilteredSorted(); len(rows) != 1 || rows[0].ID != 42 {
		t.Fatalf("filter setup failed: %+v", rows)
	}
	next, _, handled := m.handleSessionKey(keyRunes("s"))
	got := toModel(next)
	if !handled {
		t.Fatal("an active filter must not hide a session from s")
	}
	if got.session == nil || got.session.ID != 5 {
		t.Fatalf("session = %+v", got.session)
	}
}

func TestSessionKeyIsIgnoredOutsideTheInbox(t *testing.T) {
	one := store.Session{ID: 5, TriggerMessageID: 42, Status: store.SessionSucceeded}
	for _, view := range []viewKind{viewChannels, viewThreads, viewMessages} {
		m := sessionModel(t, []store.Session{one}, nil)
		m.inbox = []store.InboxMessage{{Message: store.Message{ID: 42, ThreadID: 7}, ChannelID: 1, ChannelName: "c", ThreadTitle: "t"}}
		m.sessionsByMsg = map[int64]store.Session{42: one}
		m.view = view
		if !m.previewVisible() {
			t.Fatalf("precondition: the pane is drawn in view %d", view)
		}
		next, _, handled := m.handleSessionKey(keyRunes("s"))
		if handled {
			t.Fatalf("s must be ignored in view %d", view)
		}
		if toModel(next).previewMode != previewThread {
			t.Fatalf("view %d left the model in session mode", view)
		}
	}
}

func TestPollStopsWhenThePaneIsHidden(t *testing.T) {
	m := sessionModel(t, nil, nil)
	m.sessions = []store.Session{{ID: 1, Status: store.SessionRunning}}
	if cmd := m.syncSessionTick(); cmd == nil {
		t.Fatal("precondition: with the pane on, a running session must poll")
	}
	m.preview = false
	if cmd := m.syncSessionTick(); cmd != nil {
		t.Fatal("a hidden pane must not keep polling")
	}
}

func TestTickRefetchesWithoutArmingASecondTimer(t *testing.T) {
	running := store.Session{ID: 3, TriggerMessageID: 42, Status: store.SessionRunning}
	m := sessionModel(t, []store.Session{running}, nil)
	m.sessions = []store.Session{running}
	m.sessionPollThreadID = 7
	_, cmd := m.Update(sessionTickMsg(time.Now()))
	if cmd == nil {
		t.Fatal("a tick with an active session must refetch")
	}
	msgs := flattenMsgs(cmd())
	for _, msg := range msgs {
		if _, ok := msg.(sessionTickMsg); ok {
			t.Fatal("the tick handler must not arm its own timer; the fetch carries the chain")
		}
	}
	found := false
	for _, msg := range msgs {
		if sm, ok := msg.(sessionsFetchedMsg); ok && sm.err == nil && sm.threadID == 7 {
			found = true
		}
	}
	if !found {
		t.Fatalf("the tick did not refetch sessions for thread 7: %+v", msgs)
	}
}

func TestTickPollsTheThreadItWasArmedFor(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/threads/", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]store.Session{})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	m := toModel(New(srv.URL))
	m.view = viewInbox
	m.width = 120
	m.height = 40
	m.preview = true
	m.sessions = []store.Session{{ID: 1, Status: store.SessionRunning}}
	m.sessionPollThreadID = 42
	_, cmd := m.Update(sessionTickMsg(time.Now()))
	if cmd == nil {
		t.Fatal("a tick with an active session must refetch")
	}
	cmd()
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 1 || paths[0] != "/v1/threads/42/sessions" {
		t.Fatalf("poll paths = %v, want the thread the chain was armed for", paths)
	}
}

func TestAFailingPollTerminatesTheChain(t *testing.T) {
	running := store.Session{ID: 3, Status: store.SessionRunning}
	m := sessionModel(t, nil, nil)
	m.sessions = []store.Session{running}
	m.sessionPollThreadID = 7
	m.status = "inbox — 3 messages · q quit"
	next, cmd := m.Update(sessionsFetchedMsg{threadID: 7, err: errors.New("BAD_JSONL: thread id must be a positive integer")})
	if cmd != nil {
		t.Fatal("a failed poll must not re-arm, or the loop never terminates")
	}
	got := toModel(next)
	if len(got.sessions) != 1 || got.sessions[0].Status != store.SessionRunning {
		t.Fatalf("a failed poll must leave the last known state alone: %+v", got.sessions)
	}
	if strings.Contains(got.status, "inbox —") {
		t.Fatalf("the error must be surfaced, status = %q", got.status)
	}
}

func TestLateSessionsResponseForAnAbandonedThreadIsDropped(t *testing.T) {
	stale := store.Session{ID: 1, TriggerMessageID: 10, Status: store.SessionRunning}
	fresh := store.Session{ID: 2, TriggerMessageID: 20, Status: store.SessionSucceeded}
	m := sessionModel(t, []store.Session{fresh}, nil)
	m.sessions = []store.Session{fresh}
	m.sessionsByMsg = map[int64]store.Session{20: fresh}
	m.previewThreadID = 2
	next, cmd := m.Update(sessionsFetchedMsg{threadID: 1, sessions: []store.Session{stale}})
	got := toModel(next)
	if cmd != nil {
		t.Fatal("a stale response must not arm the poll")
	}
	if _, ok := got.sessionsByMsg[10]; ok {
		t.Fatal("a stale response overwrote the index for the current thread")
	}
	if len(got.sessions) != 1 || got.sessions[0].ID != 2 {
		t.Fatalf("sessions = %+v", got.sessions)
	}
}

func TestSessionPaneFollowsTheCursor(t *testing.T) {
	a := store.Session{ID: 1, AgentName: "alpha", Status: store.SessionSucceeded, ReplyMode: "auto", TriggerMessageID: 10}
	b := store.Session{ID: 2, AgentName: "beta", Status: store.SessionSucceeded, ReplyMode: "auto", TriggerMessageID: 20}
	m := sessionModel(t, []store.Session{a, b}, nil)
	m.inbox = []store.InboxMessage{
		{Message: store.Message{ID: 10, ThreadID: 7, CreatedAt: "2026-09-26T12:00:00Z"}, ChannelID: 1, ChannelName: "c", ThreadTitle: "t"},
		{Message: store.Message{ID: 20, ThreadID: 8, CreatedAt: "2026-09-26T10:00:00Z"}, ChannelID: 1, ChannelName: "c", ThreadTitle: "u"},
	}
	m.sessionsByMsg = map[int64]store.Session{10: a, 20: b}
	m.session = &a
	m.sessionEvents = []store.SessionEvent{{Seq: 1, Type: store.SessionEventPrompt, Content: "prompt a"}}
	m.previewMode = previewSession
	if !strings.Contains(stripAnsi(m.renderSessionPreview(60, 20)), "alpha") {
		t.Fatal("the pane must show the cursor row's session")
	}
	m.cursor = 1
	out := stripAnsi(m.renderSessionPreview(60, 20))
	if !strings.Contains(out, "beta") {
		t.Fatalf("the pane must follow the cursor to the next thread:\n%s", out)
	}
	if strings.Contains(out, "alpha") {
		t.Fatalf("the pane kept showing the abandoned session:\n%s", out)
	}
}

func TestTickSurvivesAcrossAFetchAndStopsWhenTheFetchFails(t *testing.T) {
	running := store.Session{ID: 3, TriggerMessageID: 42, Status: store.SessionRunning}
	ended := store.Session{ID: 3, TriggerMessageID: 42, Status: store.SessionSucceeded}
	m := sessionModel(t, []store.Session{running}, nil)
	m.sessionPollThreadID = 7
	m.previewThreadID = 7
	m.sessions = []store.Session{running}
	next, cmd := m.Update(sessionTickMsg(time.Now()))
	if cmd == nil {
		t.Fatal("precondition: the tick must refetch")
	}
	for _, msg := range flattenMsgs(cmd()) {
		if sm, ok := msg.(sessionsFetchedMsg); ok {
			next, cmd = toModel(next).Update(sm)
		}
	}
	if cmd == nil {
		t.Fatal("a still-running session must keep the chain alive across the fetch")
	}
	_, cmd = toModel(next).Update(sessionsFetchedMsg{threadID: 7, err: errors.New("DAEMON_DOWN: connection refused")})
	if cmd != nil {
		t.Fatal("the chain must stop on a fetch failure")
	}
	_, cmd = toModel(next).Update(sessionsFetchedMsg{threadID: 7, sessions: []store.Session{ended}})
	if cmd != nil {
		t.Fatal("the chain must stop when the session finishes")
	}
}
