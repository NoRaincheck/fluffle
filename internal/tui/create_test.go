package tui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/NoRaincheck/fluffle/internal/store"
)

type capturedRequest struct {
	Method string
	Path   string
	Body   map[string]any
}

func captureServer(t *testing.T, captured *[]capturedRequest) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		mu.Lock()
		*captured = append(*captured, capturedRequest{Method: r.Method, Path: r.URL.Path, Body: body})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/threads"):
			json.NewEncoder(w).Encode(map[string]any{"id": 77})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/reactions"):
			json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/channels"):
			json.NewEncoder(w).Encode(map[string]any{"id": 55})
		case r.Method == http.MethodPost:
			json.NewEncoder(w).Encode(map[string]any{"seq": 1})
		default:
			json.NewEncoder(w).Encode([]any{})
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

func detailModel(t *testing.T, base string) model {
	t.Helper()
	m := toModel(New(base))
	nm, _ := m.Update(keyType(24))
	m = toModel(nm)
	nm, _ = m.Update(messagesFetchedMsg{threadID: 9, messages: []store.Message{
		{ID: 101, ThreadID: 9, Seq: 1, Name: "alice", AuthorType: "human", Role: "user", Content: "op"},
		{ID: 102, ThreadID: 9, Seq: 2, Name: "bob", AuthorType: "human", Role: "user", Content: "first reply"},
		{ID: 103, ThreadID: 9, Seq: 3, Name: "carol", AuthorType: "human", Role: "user", Content: "second reply"},
	}})
	m = toModel(nm)
	m.view = viewInboxDetail
	m.detailThreadID = 9
	m.selectedThread = &store.Thread{ID: 9, ChannelID: 4, Title: "auth"}
	m.selectedChannel = &store.Channel{ID: 4, Name: "general"}
	return m
}

func TestNInDetailOpensNewThreadCompose(t *testing.T) {
	m := detailModel(t, "http://127.0.0.1:0")
	nm, _ := m.Update(keyRunes("n"))
	m = toModel(nm)
	if !m.compose.IsActive() {
		t.Fatal("n should open the compose modal for a new thread")
	}
	if m.compose.state.mode != composeModeNewThread {
		t.Fatalf("compose mode = %v, want composeModeNewThread", m.compose.state.mode)
	}
}

func TestNewThreadComposeCreatesThread(t *testing.T) {
	var got []capturedRequest
	ts := captureServer(t, &got)
	m := detailModel(t, ts.URL)
	nm, _ := m.Update(keyRunes("n"))
	m = toModel(nm)
	nm, cmd := m.Update(composeSendMsg{text: "schema migration", mode: composeModeNewThread})
	m = toModel(nm)
	if cmd == nil {
		t.Fatal("expected a command announcing the created thread")
	}
	nm, cmd = m.Update(cmd())
	m = toModel(nm)
	if m.status != `thread "schema migration" created` {
		t.Fatalf("status = %q, want the created-thread confirmation", m.status)
	}
	if m.compose.IsActive() {
		t.Fatal("compose should close after the thread is created")
	}
	if len(got) != 1 {
		t.Fatalf("requests = %+v, want exactly one thread creation", got)
	}
	if got[0].Method != http.MethodPost || got[0].Path != "/v1/channels/4/threads" {
		t.Fatalf("request = %+v, want POST /v1/channels/4/threads", got[0])
	}
	if got[0].Body["Title"] != "schema migration" {
		t.Fatalf("title = %v, want %q", got[0].Body["Title"], "schema migration")
	}
}

func TestNewThreadComposeRejectsEmptyTitle(t *testing.T) {
	var got []capturedRequest
	ts := captureServer(t, &got)
	m := detailModel(t, ts.URL)
	nm, _ := m.Update(keyRunes("n"))
	m = toModel(nm)
	nm, cmd := m.Update(composeSendMsg{text: "", mode: composeModeNewThread})
	m = toModel(nm)
	if cmd != nil {
		t.Fatal("an empty title must not produce a command")
	}
	if len(got) != 0 {
		t.Fatalf("requests = %+v, want none for an empty title", got)
	}
	if !m.compose.IsActive() {
		t.Fatal("compose should stay open so the title can be corrected")
	}
}

func TestShiftCOpensNewChannelCompose(t *testing.T) {
	m := detailModel(t, "http://127.0.0.1:0")
	nm, _ := m.Update(keyRunes("C"))
	m = toModel(nm)
	if !m.compose.IsActive() {
		t.Fatal("C should open the compose modal for a new channel")
	}
	if m.compose.state.mode != composeModeNewChannel {
		t.Fatalf("compose mode = %v, want composeModeNewChannel", m.compose.state.mode)
	}
}

func TestNewChannelComposeCreatesChannel(t *testing.T) {
	var got []capturedRequest
	ts := captureServer(t, &got)
	m := detailModel(t, ts.URL)
	nm, _ := m.Update(keyRunes("C"))
	m = toModel(nm)
	nm, cmd := m.Update(composeSendMsg{text: "auth-refactor", mode: composeModeNewChannel})
	m = toModel(nm)
	if cmd == nil {
		t.Fatal("expected a command announcing the created channel")
	}
	nm, _ = m.Update(cmd())
	m = toModel(nm)
	if m.status != `channel "auth-refactor" created` {
		t.Fatalf("status = %q, want the created-channel confirmation", m.status)
	}
	if m.compose.IsActive() {
		t.Fatal("compose should close after the channel is created")
	}
	if len(got) != 1 {
		t.Fatalf("requests = %+v, want exactly one channel creation", got)
	}
	if got[0].Method != http.MethodPost || got[0].Path != "/v1/channels" {
		t.Fatalf("request = %+v, want POST /v1/channels", got[0])
	}
	if got[0].Body["Name"] != "auth-refactor" {
		t.Fatalf("name = %v, want %q", got[0].Body["Name"], "auth-refactor")
	}
}

func TestEInDetailOpensReactComposeOnCursorMessage(t *testing.T) {
	m := detailModel(t, "http://127.0.0.1:0")
	m.detailCursor = 1
	nm, _ := m.Update(keyRunes("e"))
	m = toModel(nm)
	if !m.compose.IsActive() {
		t.Fatal("e should open the compose modal for a reaction")
	}
	if m.compose.state.mode != composeModeReact {
		t.Fatalf("compose mode = %v, want composeModeReact", m.compose.state.mode)
	}
	if m.compose.state.threadID != 9 {
		t.Fatalf("compose thread = %d, want 9", m.compose.state.threadID)
	}
	if m.compose.state.parentID != 103 {
		t.Fatalf("compose target message = %d, want 103 (the message under the cursor)", m.compose.state.parentID)
	}
}

func TestReactComposeAddsReactionToCursorMessage(t *testing.T) {
	var got []capturedRequest
	ts := captureServer(t, &got)
	m := detailModel(t, ts.URL)
	m.detailCursor = 0
	nm, _ := m.Update(keyRunes("e"))
	m = toModel(nm)
	nm, _ = m.Update(composeSendMsg{text: "👀", mode: composeModeReact})
	m = toModel(nm)
	if m.compose.IsActive() {
		t.Fatal("compose should close after the reaction is added")
	}
	if len(got) != 1 {
		t.Fatalf("requests = %+v, want exactly one reaction", got)
	}
	if got[0].Method != http.MethodPost || got[0].Path != "/v1/messages/102/reactions" {
		t.Fatalf("request = %+v, want POST /v1/messages/102/reactions", got[0])
	}
	if got[0].Body["Emoji"] != "👀" {
		t.Fatalf("emoji = %v, want 👀", got[0].Body["Emoji"])
	}
}

func TestEOutsideDetailDoesNotOpenReactCompose(t *testing.T) {
	m := toModel(New("http://127.0.0.1:0"))
	nm, _ := m.Update(keyType(24))
	m = toModel(nm)
	nm, _ = m.Update(inboxFetchedMsg{inbox: []store.InboxMessage{{Message: store.Message{ID: 1, ThreadID: 9, Content: "hi"}, ChannelID: 4}}})
	m = toModel(nm)
	nm, _ = m.Update(keyRunes("e"))
	m = toModel(nm)
	if m.compose.IsActive() {
		t.Fatal("e must not open react compose outside the detail view, where no message is under the cursor")
	}
}
