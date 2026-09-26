package apiserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/NoRaincheck/fluffle/internal/agentcfg"
	"github.com/NoRaincheck/fluffle/internal/session"
	"github.com/NoRaincheck/fluffle/internal/store"
)

type recordingStarter struct {
	mu    sync.Mutex
	calls []string
}

func (r *recordingStarter) Start(threadID, triggerMessageID int64, names []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, fmt.Sprintf("%d:%d:%s", threadID, triggerMessageID, strings.Join(names, ",")))
}

func (r *recordingStarter) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.calls))
	copy(out, r.calls)
	return out
}

type stubCanceler struct {
	mu        sync.Mutex
	canceled  []int64
	returnErr error
}

func (c *stubCanceler) Cancel(id int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.returnErr != nil {
		return c.returnErr
	}
	c.canceled = append(c.canceled, id)
	return nil
}

func newTestLoader(t *testing.T, cfg string) *agentcfg.Loader {
	t.Helper()
	dir := t.TempDir()
	global := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(global, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	return agentcfg.NewLoader(global)
}

func newSessionHandler(t *testing.T, cfg string) (*store.Store, http.Handler, *recordingStarter, *stubCanceler, int64) {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	chID, _ := s.CreateChannel("c", "/repo", "", "", "", false)
	thID, _ := s.CreateThread(chID, "t")
	starter := &recordingStarter{}
	canceler := &stubCanceler{}
	h := NewHandlerWithDeps(s, Deps{Starter: starter, Agents: newTestLoader(t, cfg), Canceler: canceler})
	return s, h, starter, canceler, thID
}

func postThread(t *testing.T, h http.Handler, threadID int64, path, body, agentHeader string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/threads/%d/%s", threadID, path), strings.NewReader(body))
	if agentHeader != "" {
		req.Header.Set("X-Fluffle-Agent", agentHeader)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

const oneAgentCfg = "[[agents]]\nname=\"reviewer\"\ncommand=\"claude\"\n"

func TestLeadingMentionTriggersSession(t *testing.T) {
	_, h, starter, _, thID := newSessionHandler(t, oneAgentCfg)
	rec := postThread(t, h, thID, "messages", `{"name":"alice","role":"user","content":"@reviewer do xyz"}`, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	calls := starter.snapshot()
	if len(calls) != 1 {
		t.Fatalf("calls = %v", calls)
	}
	if want := fmt.Sprintf("%d:1:reviewer", thID); calls[0] != want {
		t.Fatalf("call = %q, want %q", calls[0], want)
	}
}

func TestNonLeadingMentionDoesNotTrigger(t *testing.T) {
	_, h, starter, _, thID := newSessionHandler(t, oneAgentCfg)
	postThread(t, h, thID, "messages", `{"name":"alice","role":"user","content":"don't @reviewer do that"}`, "")
	if len(starter.snapshot()) != 0 {
		t.Fatalf("calls = %v", starter.snapshot())
	}
}

func TestMultipleLeadingMentionsAreAllPassed(t *testing.T) {
	cfg := "[[agents]]\nname=\"a1\"\ncommand=\"x\"\n\n[[agents]]\nname=\"b2\"\ncommand=\"y\"\n"
	_, h, starter, _, thID := newSessionHandler(t, cfg)
	postThread(t, h, thID, "messages", `{"name":"alice","role":"user","content":"@a1 @b2 go"}`, "")
	calls := starter.snapshot()
	if len(calls) != 1 || calls[0] != fmt.Sprintf("%d:1:a1,b2", thID) {
		t.Fatalf("calls = %v", calls)
	}
}

func TestAgentAppendNeverTriggers(t *testing.T) {
	_, h, starter, _, thID := newSessionHandler(t, oneAgentCfg)
	rec := postThread(t, h, thID, "messages", `{"name":"probe","role":"assistant","content":"@reviewer ping"}`, "probe")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if len(starter.snapshot()) != 0 {
		t.Fatalf("agent append triggered a session: %v", starter.snapshot())
	}
}

func TestBatchEventsTriggerOncePerMentioningMessage(t *testing.T) {
	_, h, starter, _, thID := newSessionHandler(t, oneAgentCfg)
	rec := postThread(t, h, thID, "events", `{"events":[{"name":"alice","role":"user","content":"@reviewer go"}]}`, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if len(starter.snapshot()) != 1 {
		t.Fatalf("calls = %v", starter.snapshot())
	}
	rec2 := postThread(t, h, thID, "events", `{"events":[{"name":"a","role":"user","content":"@reviewer one"},{"name":"b","role":"user","content":"plain"},{"name":"c","role":"user","content":"@reviewer two"}]}`, "")
	if rec2.Code != http.StatusOK {
		t.Fatalf("batch status = %d body %s", rec2.Code, rec2.Body.String())
	}
	if len(starter.snapshot()) != 3 {
		t.Fatalf("calls = %v, want 3", starter.snapshot())
	}
}

func TestBatchReactionDoesNotRetriggerMentionedMessage(t *testing.T) {
	_, h, starter, _, thID := newSessionHandler(t, oneAgentCfg)
	rec := postThread(t, h, thID, "events", `{"events":[{"name":"alice","role":"user","content":"@reviewer go"},{"type":"reaction","message_seq":1,"emoji":"+1","name":"bob"}]}`, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	calls := starter.snapshot()
	if len(calls) != 1 {
		t.Fatalf("calls = %v, want 1: a reaction must not re-fire the target message's mention", calls)
	}
}

func TestAgentBatchNeverTriggers(t *testing.T) {
	_, h, starter, _, thID := newSessionHandler(t, oneAgentCfg)
	postThread(t, h, thID, "events", `{"events":[{"name":"probe","role":"assistant","content":"@reviewer go"}]}`, "probe")
	if len(starter.snapshot()) != 0 {
		t.Fatalf("agent batch triggered a session: %v", starter.snapshot())
	}
}

func TestZeroDepsHandlerIsSafeOnMention(t *testing.T) {
	s, h, _ := newTestHandlerWithThread(t)
	chans, _ := s.ListChannels("", false)
	thID, _ := s.CreateThread(chans[0].ID, "t")
	rec := postThread(t, h, thID, "messages", `{"name":"alice","role":"user","content":"@reviewer go"}`, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
}

func TestListThreadSessionsReturnsArray(t *testing.T) {
	_, h, _, _, thID := newSessionHandler(t, "")
	rec := serveRequest(t, h, http.MethodGet, fmt.Sprintf("/v1/threads/%d/sessions", thID), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("empty sessions must serialize as [], got %s", rec.Body.String())
	}
}

func TestListThreadSessionsReturnsRows(t *testing.T) {
	s, h, _, _, thID := newSessionHandler(t, "")
	seq, _ := s.AppendMessage(thID, "alice", "human", "user", "go")
	msgID, _ := s.MessageIDBySeq(thID, seq)
	if _, err := s.CreateSession(thID, msgID, "probe", store.SessionSucceeded, "stdout", "c", nil); err != nil {
		t.Fatal(err)
	}
	rec := serveRequest(t, h, http.MethodGet, fmt.Sprintf("/v1/threads/%d/sessions", thID), "")
	var out []store.Session
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].AgentName != "probe" {
		t.Fatalf("sessions = %+v", out)
	}
}

func TestListThreadSessionsMissingThreadIsNotFound(t *testing.T) {
	_, h, _, _, _ := newSessionHandler(t, "")
	rec := serveRequest(t, h, http.MethodGet, "/v1/threads/9999/sessions", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
}

func TestGetSessionIncludesEvents(t *testing.T) {
	s, h, _, _, thID := newSessionHandler(t, "")
	seq, _ := s.AppendMessage(thID, "alice", "human", "user", "go")
	msgID, _ := s.MessageIDBySeq(thID, seq)
	id, _ := s.CreateSession(thID, msgID, "probe", store.SessionSucceeded, "stdout", "c", nil)
	if _, _, err := s.AppendSessionEvent(id, store.SessionEventStdout, "hello"); err != nil {
		t.Fatal(err)
	}
	rec := serveRequest(t, h, http.MethodGet, fmt.Sprintf("/v1/sessions/%d", id), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Session store.Session        `json:"session"`
		Events  []store.SessionEvent `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Session.ID != id || len(out.Events) != 1 || out.Events[0].Content != "hello" {
		t.Fatalf("out = %+v", out)
	}
}

func TestGetSessionEmptyEventsIsArray(t *testing.T) {
	s, h, _, _, thID := newSessionHandler(t, "")
	seq, _ := s.AppendMessage(thID, "alice", "human", "user", "go")
	msgID, _ := s.MessageIDBySeq(thID, seq)
	id, _ := s.CreateSession(thID, msgID, "probe", store.SessionSucceeded, "stdout", "c", nil)
	rec := serveRequest(t, h, http.MethodGet, fmt.Sprintf("/v1/sessions/%d", id), "")
	if !strings.Contains(rec.Body.String(), `"events":[]`) {
		t.Fatalf("empty events must serialize as [], got %s", rec.Body.String())
	}
}

func TestGetSessionMissingIsSessionNotFound(t *testing.T) {
	_, h, _, _, _ := newSessionHandler(t, "")
	rec := serveRequest(t, h, http.MethodGet, "/v1/sessions/9999", "")
	assertErrorEnvelope(t, rec, http.StatusNotFound, "SESSION_NOT_FOUND")
}

func TestGetSessionRejectsBadID(t *testing.T) {
	_, h, _, _, _ := newSessionHandler(t, "")
	rec := serveRequest(t, h, http.MethodGet, "/v1/sessions/abc", "")
	assertErrorEnvelope(t, rec, http.StatusBadRequest, "BAD_JSONL")
}

func TestListAgentsMergesRepoOverGlobal(t *testing.T) {
	dir := t.TempDir()
	repoPath := filepath.Join(dir, "repo")
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoPath, ".flf.toml"), []byte("[[agents]]\nname=\"local\"\ncommand=\"c\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, h, _, _, _ := newSessionHandler(t, "[[agents]]\nname=\"shared\"\ncommand=\"g\"\nreply=\"cli\"\n")
	rec := serveRequest(t, h, http.MethodGet, "/v1/agents?repo="+repoPath, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Agents []agentListItem `json:"agents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	byName := map[string]agentListItem{}
	for _, a := range out.Agents {
		byName[a.Name] = a
	}
	if _, ok := byName["local"]; !ok {
		t.Fatalf("repo agent missing: %+v", out.Agents)
	}
	if byName["shared"].Reply != "cli" {
		t.Fatalf("global agent = %+v", byName["shared"])
	}
}

func TestListAgentsDoesNotEchoConfigError(t *testing.T) {
	dir := t.TempDir()
	repoPath := filepath.Join(dir, "repo")
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := "PRIVATE_KEY"
	if err := os.WriteFile(filepath.Join(repoPath, ".flf.toml"), []byte("[[agents]]\nname=\"probe\"\ncommand=\"c\"\n"+secret+"=\"p\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, h, _, _, _ := newSessionHandler(t, oneAgentCfg)
	rec := serveRequest(t, h, http.MethodGet, "/v1/agents?repo="+repoPath, "")
	assertErrorEnvelope(t, rec, http.StatusInternalServerError, "DAEMON_ERROR")
	if strings.Contains(rec.Body.String(), secret) {
		t.Fatalf("response leaks the config key %s: %s", secret, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), repoPath) {
		t.Fatalf("response leaks the repo path: %s", rec.Body.String())
	}
}

func TestListAgentsRejectsPost(t *testing.T) {
	_, h, _, _, _ := newSessionHandler(t, "")
	rec := serveRequest(t, h, http.MethodPost, "/v1/agents", "")
	assertErrorEnvelope(t, rec, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
}

func TestCancelSucceedsForHumans(t *testing.T) {
	s, h, _, canceler, thID := newSessionHandler(t, "")
	seq, _ := s.AppendMessage(thID, "alice", "human", "user", "go")
	msgID, _ := s.MessageIDBySeq(thID, seq)
	id, _ := s.CreateSession(thID, msgID, "probe", store.SessionRunning, "stdout", "c", nil)
	rec := serveRequest(t, h, http.MethodPost, fmt.Sprintf("/v1/sessions/%d/cancel", id), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if len(canceler.canceled) != 1 || canceler.canceled[0] != id {
		t.Fatalf("canceler calls = %v", canceler.canceled)
	}
}

func TestCancelRejectsAgents(t *testing.T) {
	s, h, _, _, thID := newSessionHandler(t, "")
	seq, _ := s.AppendMessage(thID, "alice", "human", "user", "go")
	msgID, _ := s.MessageIDBySeq(thID, seq)
	id, _ := s.CreateSession(thID, msgID, "probe", store.SessionRunning, "stdout", "c", nil)
	rec := postSession(t, h, id, "probe")
	assertErrorEnvelope(t, rec, http.StatusForbidden, "AGENT_FORBIDDEN")
}

func TestCancelFinishedSessionIsConflict(t *testing.T) {
	s, h, _, canceler, thID := newSessionHandler(t, "")
	seq, _ := s.AppendMessage(thID, "alice", "human", "user", "go")
	msgID, _ := s.MessageIDBySeq(thID, seq)
	id, _ := s.CreateSession(thID, msgID, "probe", store.SessionSucceeded, "stdout", "c", nil)
	canceler.returnErr = session.ErrTerminal
	rec := serveRequest(t, h, http.MethodPost, fmt.Sprintf("/v1/sessions/%d/cancel", id), "")
	assertErrorEnvelope(t, rec, http.StatusConflict, "SESSION_FINISHED")
}

func TestCancelMissingSessionIsNotFound(t *testing.T) {
	_, h, _, _, _ := newSessionHandler(t, "")
	rec := serveRequest(t, h, http.MethodPost, "/v1/sessions/9999/cancel", "")
	assertErrorEnvelope(t, rec, http.StatusNotFound, "SESSION_NOT_FOUND")
}

func postSession(t *testing.T, h http.Handler, sessionID int64, agentHeader string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/sessions/%d/cancel", sessionID), nil)
	req.Header.Set("X-Fluffle-Agent", agentHeader)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}
