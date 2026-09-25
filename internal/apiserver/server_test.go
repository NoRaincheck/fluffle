package apiserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NoRaincheck/fluffle/internal/store"
)

func TestAgentCannotCreateChannelButCanAppend(t *testing.T) {
	s, _ := store.Open(":memory:")
	defer s.Close()
	h := NewHandler(s)
	req := httptest.NewRequest("POST", "/v1/channels", strings.NewReader(`{"name":"x"}`))
	req.Header.Set("X-Fluffle-Agent", "pi")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403 got %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "AGENT_FORBIDDEN") {
		t.Fatalf("body %s", rec.Body.String())
	}
}

func TestExportImportRoundTrip(t *testing.T) {
	s, _ := store.Open(":memory:")
	defer s.Close()
	h := NewHandler(s)
	ch, _ := s.CreateChannel("c", "/r", "", "", "", false)
	th, _ := s.CreateThread(ch, "t")
	s.AppendMessage(th, "alice", "human", "user", "hello")
	req := httptest.NewRequest("GET", fmt.Sprintf("/v1/threads/%d/messages", th), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code %d", rec.Code)
	}
	var msgs []store.Message
	if err := json.NewDecoder(rec.Body).Decode(&msgs); err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Seq != 1 {
		t.Fatalf("%+v", msgs)
	}
}

func TestListChannelsEmptyReturnsArray(t *testing.T) {
	s, _ := store.Open(":memory:")
	defer s.Close()
	h := NewHandler(s)
	req := httptest.NewRequest("GET", "/v1/channels?include-orphaned=1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("want 200 got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("want application/json got %q", ct)
	}
	body := strings.TrimSpace(rec.Body.String())
	if body != "[]" {
		t.Fatalf("want [] got %q", body)
	}
}

func TestListThreadsEmptyReturnsArray(t *testing.T) {
	s, _ := store.Open(":memory:")
	defer s.Close()
	h := NewHandler(s)
	ch, _ := s.CreateChannel("c", "/r", "", "", "", false)
	req := httptest.NewRequest("GET", fmt.Sprintf("/v1/channels/%d/threads", ch), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("want 200 got %d", rec.Code)
	}
	assertJSONContentType(t, rec)
	body := strings.TrimSpace(rec.Body.String())
	if body != "[]" {
		t.Fatalf("want [] got %q", body)
	}
}

func TestListMessagesEmptyReturnsArray(t *testing.T) {
	s, _ := store.Open(":memory:")
	defer s.Close()
	h := NewHandler(s)
	ch, _ := s.CreateChannel("c", "/r", "", "", "", false)
	th, _ := s.CreateThread(ch, "t")
	req := httptest.NewRequest("GET", fmt.Sprintf("/v1/threads/%d/messages", th), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("want 200 got %d", rec.Code)
	}
	assertJSONContentType(t, rec)
	body := strings.TrimSpace(rec.Body.String())
	if body != "[]" {
		t.Fatalf("want [] got %q", body)
	}
}

func TestListMessagesAfterSequenceReturnsStrictlyNewerMessages(t *testing.T) {
	s, h, threadID := newTestHandlerWithThread(t)
	for _, content := range []string{"first", "second", "third"} {
		if _, err := s.AppendMessage(threadID, "alice", "human", "user", content); err != nil {
			t.Fatal(err)
		}
	}

	rec := serveRequest(t, h, http.MethodGet, fmt.Sprintf("/v1/threads/%d/messages?after_seq=1", threadID), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	assertJSONContentType(t, rec)
	var messages []store.Message
	decodeResponse(t, rec, &messages)
	if len(messages) != 2 || messages[0].Seq != 2 || messages[1].Seq != 3 {
		t.Fatalf("messages = %+v, want sequences [2 3]", messages)
	}
	if strings.Contains(rec.Body.String(), "ParentSeq") {
		t.Fatalf("message response changed shape: %s", rec.Body.String())
	}
}

func TestListMessagesRejectsInvalidAfterSequence(t *testing.T) {
	_, h, threadID := newTestHandlerWithThread(t)
	for _, test := range []struct {
		name  string
		query string
	}{
		{name: "empty", query: "after_seq="},
		{name: "text", query: "after_seq=next"},
		{name: "decimal", query: "after_seq=1.5"},
		{name: "negative", query: "after_seq=-1"},
		{name: "overflow", query: "after_seq=9223372036854775808"},
		{name: "duplicate", query: "after_seq=1&after_seq=2"},
	} {
		t.Run(test.name, func(t *testing.T) {
			rec := serveRequest(t, h, http.MethodGet, fmt.Sprintf("/v1/threads/%d/messages?%s", threadID, test.query), "")
			assertErrorEnvelope(t, rec, http.StatusBadRequest, "BAD_JSONL")
		})
	}
}

func TestListMessagesRejectsCursorWithLast(t *testing.T) {
	_, h, threadID := newTestHandlerWithThread(t)
	rec := serveRequest(t, h, http.MethodGet, fmt.Sprintf("/v1/threads/%d/messages?after_seq=1&last=1", threadID), "")
	assertErrorEnvelope(t, rec, http.StatusBadRequest, "BAD_JSONL")
}

func TestListMessagesAfterZeroUsesExistingReadBehavior(t *testing.T) {
	s, h, threadID := newTestHandlerWithThread(t)
	for _, content := range []string{"first", "second", "third"} {
		if _, err := s.AppendMessage(threadID, "alice", "human", "user", content); err != nil {
			t.Fatal(err)
		}
	}

	for _, test := range []struct {
		query string
		want  []int64
	}{
		{query: "", want: []int64{1, 2, 3}},
		{query: "after_seq=0", want: []int64{1, 2, 3}},
		{query: "last=2", want: []int64{2, 3}},
	} {
		rec := serveRequest(t, h, http.MethodGet, fmt.Sprintf("/v1/threads/%d/messages?%s", threadID, test.query), "")
		if rec.Code != http.StatusOK {
			t.Fatalf("query %q status = %d body %s", test.query, rec.Code, rec.Body.String())
		}
		var messages []store.Message
		decodeResponse(t, rec, &messages)
		if len(messages) != len(test.want) {
			t.Fatalf("query %q messages = %+v, want %v", test.query, messages, test.want)
		}
		for i, seq := range test.want {
			if messages[i].Seq != seq {
				t.Fatalf("query %q message %d sequence = %d, want %d", test.query, i, messages[i].Seq, seq)
			}
		}
	}
}

func TestListMessagesAfterCursorAtEndReturnsArray(t *testing.T) {
	s, h, threadID := newTestHandlerWithThread(t)
	if _, err := s.AppendMessage(threadID, "alice", "human", "user", "only"); err != nil {
		t.Fatal(err)
	}

	rec := serveRequest(t, h, http.MethodGet, fmt.Sprintf("/v1/threads/%d/messages?after_seq=1", threadID), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	assertJSONContentType(t, rec)
	if body := strings.TrimSpace(rec.Body.String()); body != "[]" {
		t.Fatalf("body = %q, want []", body)
	}
}

func TestListThreadReactionsReturnsMessageAndTimeOrder(t *testing.T) {
	s, h, threadID := newTestHandlerWithThread(t)
	firstSeq, err := s.AppendMessage(threadID, "alice", "human", "user", "first")
	if err != nil {
		t.Fatal(err)
	}
	secondSeq, err := s.AppendMessage(threadID, "reviewer", "agent", "assistant", "second")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendBatch(threadID, []store.AppendEvent{
		{Type: "reaction", MessageSeq: secondSeq, Name: "carol", AuthorType: "human", Emoji: "second-late", CreatedAt: "2026-09-25T12:02:00Z"},
		{Type: "reaction", MessageSeq: firstSeq, Name: "alice", AuthorType: "human", Emoji: "first-message", CreatedAt: "2026-09-25T12:03:00Z"},
		{Type: "reaction", MessageSeq: secondSeq, Name: "bob", AuthorType: "human", Emoji: "second-early", CreatedAt: "2026-09-25T12:01:00Z"},
	}); err != nil {
		t.Fatal(err)
	}

	rec := serveRequest(t, h, http.MethodGet, fmt.Sprintf("/v1/threads/%d/reactions", threadID), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	assertJSONContentType(t, rec)
	var reactions []store.Reaction
	decodeResponse(t, rec, &reactions)
	want := []struct {
		seq   int64
		emoji string
	}{
		{seq: 1, emoji: "first-message"},
		{seq: 2, emoji: "second-early"},
		{seq: 2, emoji: "second-late"},
	}
	if len(reactions) != len(want) {
		t.Fatalf("reactions = %+v", reactions)
	}
	for i, expected := range want {
		if reactions[i].MessageSeq != expected.seq || reactions[i].Emoji != expected.emoji {
			t.Fatalf("reaction %d = %+v, want sequence %d emoji %q", i, reactions[i], expected.seq, expected.emoji)
		}
	}
}

func TestListThreadReactionsEmptyReturnsArray(t *testing.T) {
	_, h, threadID := newTestHandlerWithThread(t)
	rec := serveRequest(t, h, http.MethodGet, fmt.Sprintf("/v1/threads/%d/reactions", threadID), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	assertJSONContentType(t, rec)
	if body := strings.TrimSpace(rec.Body.String()); body != "[]" {
		t.Fatalf("body = %q, want []", body)
	}
}

func TestMessagePostResolvesParentSequence(t *testing.T) {
	s, h, threadID := newTestHandlerWithThread(t)
	parentSeq, err := s.AppendMessage(threadID, "alice", "human", "user", "parent")
	if err != nil {
		t.Fatal(err)
	}
	parentID, err := s.MessageIDBySeq(threadID, parentSeq)
	if err != nil {
		t.Fatal(err)
	}

	rec := serveRequest(t, h, http.MethodPost, fmt.Sprintf("/v1/threads/%d/messages", threadID), `{"name":"reviewer","role":"assistant","content":"reply","parent_seq":1,"created_at":"2026-09-25T13:00:00Z"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Seq int64 `json:"seq"`
	}
	decodeResponse(t, rec, &result)
	if result.Seq != 2 {
		t.Fatalf("seq = %d, want 2", result.Seq)
	}
	messages, err := s.ListMessagesAfter(threadID, parentSeq)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].ParentIDValue() != parentID || !messages[0].ParentSeq.Valid || messages[0].ParentSeq.Int64 != parentSeq {
		t.Fatalf("messages = %+v, want parent id %d sequence %d", messages, parentID, parentSeq)
	}
}

func TestMessagePostRejectsCrossThreadParentSequence(t *testing.T) {
	s, h, threadID := newTestHandlerWithThread(t)
	otherThreadID := newTestThread(t, s, "other")
	if _, err := s.AppendMessage(otherThreadID, "alice", "human", "user", "other parent"); err != nil {
		t.Fatal(err)
	}

	rec := serveRequest(t, h, http.MethodPost, fmt.Sprintf("/v1/threads/%d/messages", threadID), `{"name":"reviewer","role":"assistant","content":"reply","parent_seq":1}`)
	assertErrorEnvelope(t, rec, http.StatusNotFound, "THREAD_NOT_FOUND")
	messages, err := s.ListMessages(threadID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Fatalf("invalid parent created messages: %+v", messages)
	}
}

func TestLiveMessageAuthorTypeComesOnlyFromHeader(t *testing.T) {
	s, h, threadID := newTestHandlerWithThread(t)
	humanRec := serveRequest(t, h, http.MethodPost, fmt.Sprintf("/v1/threads/%d/messages", threadID), `{"name":"alice","agent_id":"spoofed","author_type":"agent","role":"user","content":"human request"}`)
	if humanRec.Code != http.StatusOK {
		t.Fatalf("human status = %d body %s", humanRec.Code, humanRec.Body.String())
	}
	agentReq := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/threads/%d/messages", threadID), strings.NewReader(`{"name":"reviewer","agent_id":"ignored","author_type":"human","role":"assistant","content":"agent request"}`))
	agentReq.Header.Set("X-Fluffle-Agent", "reviewer")
	agentRec := httptest.NewRecorder()
	h.ServeHTTP(agentRec, agentReq)
	if agentRec.Code != http.StatusOK {
		t.Fatalf("agent status = %d body %s", agentRec.Code, agentRec.Body.String())
	}

	messages, err := s.ListMessages(threadID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].AuthorType != "human" || messages[1].AuthorType != "agent" {
		t.Fatalf("author types = %+v, want [human agent]", messages)
	}
}

func TestThreadScopedReactionUsesSequenceWithinThread(t *testing.T) {
	s, h, threadID := newTestHandlerWithThread(t)
	otherThreadID := newTestThread(t, s, "other")
	if _, err := s.AppendMessage(otherThreadID, "alice", "human", "user", "other one"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(otherThreadID, "alice", "human", "user", "other two"); err != nil {
		t.Fatal(err)
	}

	crossThread := serveRequest(t, h, http.MethodPost, fmt.Sprintf("/v1/threads/%d/messages/2/reactions", threadID), `{"emoji":"👀","name":"reviewer"}`)
	assertErrorEnvelope(t, crossThread, http.StatusNotFound, "THREAD_NOT_FOUND")
	reactions, err := s.ListReactions(threadID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reactions) != 0 {
		t.Fatalf("cross-thread target created reactions: %+v", reactions)
	}

	if _, err := s.AppendMessage(threadID, "alice", "human", "user", "target"); err != nil {
		t.Fatal(err)
	}
	rec := serveRequest(t, h, http.MethodPost, fmt.Sprintf("/v1/threads/%d/messages/1/reactions", threadID), `{"emoji":"👀","name":"reviewer"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	var result struct {
		OK bool `json:"ok"`
	}
	decodeResponse(t, rec, &result)
	if !result.OK {
		t.Fatal("ok = false")
	}
}

func TestLiveReactionAuthorTypeComesOnlyFromHeader(t *testing.T) {
	s, h, threadID := newTestHandlerWithThread(t)
	firstSeq, err := s.AppendMessage(threadID, "alice", "human", "user", "first")
	if err != nil {
		t.Fatal(err)
	}
	secondSeq, err := s.AppendMessage(threadID, "alice", "human", "user", "second")
	if err != nil {
		t.Fatal(err)
	}
	firstID, err := s.MessageIDBySeq(threadID, firstSeq)
	if err != nil {
		t.Fatal(err)
	}

	humanRec := serveRequest(t, h, http.MethodPost, fmt.Sprintf("/v1/messages/%d/reactions", firstID), `{"emoji":"+1","name":"alice","agent_id":"spoofed","author_type":"agent"}`)
	if humanRec.Code != http.StatusOK {
		t.Fatalf("human status = %d body %s", humanRec.Code, humanRec.Body.String())
	}
	agentReq := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/threads/%d/messages/%d/reactions", threadID, secondSeq), strings.NewReader(`{"emoji":"👀","name":"reviewer","agent_id":"ignored","author_type":"human"}`))
	agentReq.Header.Set("X-Fluffle-Agent", "reviewer")
	agentRec := httptest.NewRecorder()
	h.ServeHTTP(agentRec, agentReq)
	if agentRec.Code != http.StatusOK {
		t.Fatalf("agent status = %d body %s", agentRec.Code, agentRec.Body.String())
	}

	reactions, err := s.ListReactions(threadID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reactions) != 2 || reactions[0].Emoji != "+1" || reactions[0].AuthorType != "human" || reactions[1].Emoji != "👀" || reactions[1].AuthorType != "agent" {
		t.Fatalf("reactions = %+v, want live attribution by header", reactions)
	}
}

func TestBatchEventsReturnsMappedResultsAndPreservesHumanImportAttribution(t *testing.T) {
	s, h, threadID := newTestHandlerWithThread(t)
	body := `{"events":[{"type":"message","seq":10,"role":"user","name":"alice","author_type":"human","content":"root","timestamp":"2026-09-25T14:00:00Z"},{"type":"message","seq":20,"parent_seq":10,"role":"assistant","name":"reviewer","author_type":"agent","content":"reply","timestamp":"2026-09-25T14:01:00Z"},{"type":"reaction","seq":30,"message_seq":20,"name":"bob","author_type":"human","emoji":"👍","timestamp":"2026-09-25T14:02:00Z"}],"import":true}`
	rec := serveRequest(t, h, http.MethodPost, fmt.Sprintf("/v1/threads/%d/events", threadID), body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	assertJSONContentType(t, rec)
	var results []store.AppendResult
	decodeResponse(t, rec, &results)
	if len(results) != 3 {
		t.Fatalf("results = %+v", results)
	}
	if results[0].SourceSeq != 10 || results[0].Seq != 1 || results[0].MessageID == 0 || results[0].ReactionID != 0 {
		t.Fatalf("root result = %+v", results[0])
	}
	if results[1].SourceSeq != 20 || results[1].Seq != 2 || results[1].MessageID == 0 || results[1].ReactionID != 0 {
		t.Fatalf("reply result = %+v", results[1])
	}
	if results[2].SourceSeq != 30 || results[2].Seq != 0 || results[2].MessageID != results[1].MessageID || results[2].ReactionID == 0 {
		t.Fatalf("reaction result = %+v", results[2])
	}

	messages, err := s.ListMessagesAfter(threadID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].AuthorType != "human" || messages[1].AuthorType != "agent" || messages[1].ParentIDValue() != results[0].MessageID {
		t.Fatalf("messages = %+v", messages)
	}
	reactions, err := s.ListReactions(threadID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reactions) != 1 || reactions[0].MessageSeq != 2 || reactions[0].AuthorType != "human" {
		t.Fatalf("reactions = %+v", reactions)
	}
}

func TestLiveBatchAuthorTypeComesOnlyFromHeader(t *testing.T) {
	s, h, threadID := newTestHandlerWithThread(t)
	humanRec := serveRequest(t, h, http.MethodPost, fmt.Sprintf("/v1/threads/%d/events", threadID), `{"events":[{"type":"message","name":"alice","author_type":"agent","role":"user","content":"human live"}],"import":false}`)
	if humanRec.Code != http.StatusOK {
		t.Fatalf("human status = %d body %s", humanRec.Code, humanRec.Body.String())
	}
	agentBody := `{"events":[{"type":"message","name":"reviewer","author_type":"human","role":"assistant","content":"agent live"}],"import":true}`
	agentReq := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/threads/%d/events", threadID), strings.NewReader(agentBody))
	agentReq.Header.Set("X-Fluffle-Agent", "reviewer")
	agentRec := httptest.NewRecorder()
	h.ServeHTTP(agentRec, agentReq)
	if agentRec.Code != http.StatusOK {
		t.Fatalf("agent status = %d body %s", agentRec.Code, agentRec.Body.String())
	}

	messages, err := s.ListMessages(threadID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].AuthorType != "human" || messages[1].AuthorType != "agent" {
		t.Fatalf("author types = %+v, want [human agent]", messages)
	}
}

func TestBatchEventsRollbackLateInvalidReference(t *testing.T) {
	s, h, threadID := newTestHandlerWithThread(t)
	body := `{"events":[{"type":"message","seq":1,"name":"alice","author_type":"human","role":"user","content":"must roll back"},{"type":"reaction","seq":2,"message_seq":999,"name":"bob","author_type":"human","emoji":"+1"}],"import":true}`
	rec := serveRequest(t, h, http.MethodPost, fmt.Sprintf("/v1/threads/%d/events", threadID), body)
	assertErrorEnvelope(t, rec, http.StatusNotFound, "THREAD_NOT_FOUND")
	messages, err := s.ListMessages(threadID, 0)
	if err != nil {
		t.Fatal(err)
	}
	reactions, err := s.ListReactions(threadID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 || len(reactions) != 0 {
		t.Fatalf("failed batch left messages %+v and reactions %+v", messages, reactions)
	}
}

func TestBatchEventsRejectEmptyAndUnknownEvents(t *testing.T) {
	s, h, threadID := newTestHandlerWithThread(t)
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "missing events", body: `{"import":true}`},
		{name: "null events", body: `{"events":null,"import":true}`},
		{name: "empty events", body: `{"events":[],"import":true}`},
		{name: "unknown type", body: `{"events":[{"type":"thread","name":"alice","role":"user","content":"unknown"}],"import":true}`},
		{name: "bad imported author", body: `{"events":[{"type":"message","name":"alice","author_type":"system","role":"user","content":"bad"}],"import":true}`},
		{name: "bad timestamp", body: `{"events":[{"type":"message","name":"alice","author_type":"human","role":"user","content":"bad","timestamp":"not-a-timestamp"}],"import":true}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			rec := serveRequest(t, h, http.MethodPost, fmt.Sprintf("/v1/threads/%d/events", threadID), test.body)
			assertErrorEnvelope(t, rec, http.StatusBadRequest, "BAD_JSONL")
		})
	}
	messages, err := s.ListMessages(threadID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Fatalf("invalid batches created messages: %+v", messages)
	}
}

func TestBatchEventsRejectMoreThanOneThousandEvents(t *testing.T) {
	s, h, threadID := newTestHandlerWithThread(t)
	events := make([]map[string]string, 1001)
	for i := range events {
		events[i] = map[string]string{"type": "message", "name": "alice", "author_type": "human", "role": "user", "content": "x"}
	}
	encoded, err := json.Marshal(map[string]any{"events": events, "import": false})
	if err != nil {
		t.Fatal(err)
	}
	rec := serveRequest(t, h, http.MethodPost, fmt.Sprintf("/v1/threads/%d/events", threadID), string(encoded))
	assertErrorEnvelope(t, rec, http.StatusBadRequest, "BAD_JSONL")
	messages, err := s.ListMessages(threadID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Fatalf("oversized batch created %d messages", len(messages))
	}
}

func TestThreadRoutesReportStoreFailuresAsDaemonErrors(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	channelID, err := s.CreateChannel("test", "/repo", "", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	threadID, err := s.CreateThread(channelID, "thread")
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "reaction read", method: http.MethodGet, path: fmt.Sprintf("/v1/threads/%d/reactions", threadID)},
		{name: "event batch", method: http.MethodPost, path: fmt.Sprintf("/v1/threads/%d/events", threadID), body: `{"events":[{"type":"message","name":"alice","author_type":"human","role":"user","content":"hello"}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			rec := serveRequest(t, h, test.method, test.path, test.body)
			assertErrorEnvelope(t, rec, http.StatusInternalServerError, "DAEMON_ERROR")
		})
	}
}

func TestListMessagesRejectsMalformedQueryEncoding(t *testing.T) {
	_, h, threadID := newTestHandlerWithThread(t)
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/threads/%d/messages", threadID), nil)
	req.URL.RawQuery = "after_seq=%ZZ"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assertErrorEnvelope(t, rec, http.StatusBadRequest, "BAD_JSONL")
}

func TestThreadRoutesRejectMalformedThreadIDs(t *testing.T) {
	_, h, _ := newTestHandlerWithThread(t)
	for _, test := range []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "message read", method: http.MethodGet, path: "/v1/threads/not-a-number/messages"},
		{name: "message write", method: http.MethodPost, path: "/v1/threads/not-a-number/messages", body: `{"name":"alice","role":"user","content":"hello"}`},
		{name: "reaction read", method: http.MethodGet, path: "/v1/threads/not-a-number/reactions"},
		{name: "event write", method: http.MethodPost, path: "/v1/threads/not-a-number/events", body: `{"events":[{"type":"message","name":"alice","author_type":"human","role":"user","content":"hello"}]}`},
		{name: "sequence reaction", method: http.MethodPost, path: "/v1/threads/not-a-number/messages/1/reactions", body: `{"emoji":"+1","name":"alice"}`},
		{name: "zero thread", method: http.MethodPost, path: "/v1/threads/0/events", body: `{"events":[{"type":"message","name":"alice","author_type":"human","role":"user","content":"hello"}]}`},
		{name: "legacy reaction", method: http.MethodPost, path: "/v1/messages/not-a-number/reactions", body: `{"emoji":"+1","name":"alice"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			rec := serveRequest(t, h, test.method, test.path, test.body)
			assertErrorEnvelope(t, rec, http.StatusBadRequest, "BAD_JSONL")
		})
	}
}

func TestLiveBatchIgnoresSerializedSequencesAndUsesDestinationReferences(t *testing.T) {
	s, h, threadID := newTestHandlerWithThread(t)
	existingSeq, err := s.AppendMessage(threadID, "alice", "human", "user", "existing")
	if err != nil {
		t.Fatal(err)
	}
	existingID, err := s.MessageIDBySeq(threadID, existingSeq)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"events":[{"type":"message","seq":1,"name":"reviewer","author_type":"human","role":"assistant","content":"new live message"},{"type":"message","seq":2,"parent_seq":1,"name":"reviewer","author_type":"human","role":"assistant","content":"live reply"},{"type":"reaction","seq":30,"message_seq":1,"name":"alice","author_type":"human","emoji":"👍"}],"import":false}`
	rec := serveRequest(t, h, http.MethodPost, fmt.Sprintf("/v1/threads/%d/events", threadID), body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	var results []store.AppendResult
	decodeResponse(t, rec, &results)
	if len(results) != 3 {
		t.Fatalf("results = %+v", results)
	}
	for i, result := range results {
		if result.SourceSeq != 0 {
			t.Fatalf("result %d source sequence = %d, want 0", i, result.SourceSeq)
		}
	}
	if results[0].Seq != 2 || results[1].Seq != 3 || results[2].MessageID != existingID {
		t.Fatalf("results = %+v, want destination sequences 2 and 3 and existing message id %d", results, existingID)
	}

	messages, err := s.ListMessages(threadID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 || messages[1].ParentID.Valid || messages[2].ParentIDValue() != existingID || !messages[2].ParentSeq.Valid || messages[2].ParentSeq.Int64 != existingSeq {
		t.Fatalf("messages = %+v, want only the reply to reference existing sequence %d", messages, existingSeq)
	}
	reactions, err := s.ListReactions(threadID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reactions) != 1 || reactions[0].MessageID != existingID || reactions[0].MessageSeq != existingSeq {
		t.Fatalf("reactions = %+v, want existing sequence %d", reactions, existingSeq)
	}
}

func TestBatchEventsRejectBodyLargerThanOneMebibyte(t *testing.T) {
	s, h, threadID := newTestHandlerWithThread(t)
	body := `{"events":[{"type":"message","name":"alice","author_type":"human","role":"user","content":"` + strings.Repeat("x", (1<<20)+1) + `"}]}`
	rec := serveRequest(t, h, http.MethodPost, fmt.Sprintf("/v1/threads/%d/events", threadID), body)
	assertErrorEnvelope(t, rec, http.StatusBadRequest, "BAD_JSONL")
	messages, err := s.ListMessages(threadID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Fatalf("oversized body created %d messages", len(messages))
	}
}

func TestBatchEventsRejectTrailingJSON(t *testing.T) {
	s, h, threadID := newTestHandlerWithThread(t)
	body := `{"events":[{"type":"message","name":"alice","author_type":"human","role":"user","content":"hello"}]} {"extra":true}`
	rec := serveRequest(t, h, http.MethodPost, fmt.Sprintf("/v1/threads/%d/events", threadID), body)
	assertErrorEnvelope(t, rec, http.StatusBadRequest, "BAD_JSONL")
	messages, err := s.ListMessages(threadID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Fatalf("trailing JSON created %d messages", len(messages))
	}
}

func TestLegacyReactionStoreFailureUsesDaemonError(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	channelID, err := s.CreateChannel("test", "/repo", "", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	threadID, err := s.CreateThread(channelID, "thread")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(threadID, "alice", "human", "user", "target"); err != nil {
		t.Fatal(err)
	}
	messages, err := s.ListMessages(threadID, 0)
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	rec := serveRequest(t, h, http.MethodPost, fmt.Sprintf("/v1/messages/%d/reactions", messages[0].ID), `{"emoji":"+1","name":"alice"}`)
	assertErrorEnvelope(t, rec, http.StatusInternalServerError, "DAEMON_ERROR")
}

func TestLegacyReactionValidationAndConflictRemainClientErrors(t *testing.T) {
	s, h, threadID := newTestHandlerWithThread(t)
	seq, err := s.AppendMessage(threadID, "alice", "human", "user", "target")
	if err != nil {
		t.Fatal(err)
	}
	messageID, err := s.MessageIDBySeq(threadID, seq)
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/v1/messages/%d/reactions", messageID)

	invalid := serveRequest(t, h, http.MethodPost, path, `{"emoji":" ","name":"alice"}`)
	assertErrorEnvelope(t, invalid, http.StatusBadRequest, "BAD_JSONL")
	first := serveRequest(t, h, http.MethodPost, path, `{"emoji":"+1","name":"alice"}`)
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d body %s", first.Code, first.Body.String())
	}
	conflict := serveRequest(t, h, http.MethodPost, path, `{"emoji":"+1","name":"alice"}`)
	assertErrorEnvelope(t, conflict, http.StatusConflict, "BAD_JSONL")
}

func TestChannelAndThreadCreationStoreFailuresAreDaemonErrors(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	channelID, err := s.CreateChannel("test", "/repo", "", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name string
		path string
		body string
	}{
		{name: "channel create", path: "/v1/channels", body: `{"Name":"new","RepoAbsPath":"/repo"}`},
		{name: "thread create", path: fmt.Sprintf("/v1/channels/%d/threads", channelID), body: `{"title":"new thread"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			rec := serveRequest(t, h, http.MethodPost, test.path, test.body)
			assertErrorEnvelope(t, rec, http.StatusInternalServerError, "DAEMON_ERROR")
		})
	}
}

func TestChannelAndThreadCreationValidationAndConflictsStayClientErrors(t *testing.T) {
	_, h, _ := newTestHandlerWithThread(t)

	blankChannel := serveRequest(t, h, http.MethodPost, "/v1/channels", `{"Name":"  ","RepoAbsPath":"/repo"}`)
	assertErrorEnvelope(t, blankChannel, http.StatusBadRequest, "BAD_JSONL")
	existingChannel := serveRequest(t, h, http.MethodPost, "/v1/channels", `{"Name":"test","RepoAbsPath":"/repo"}`)
	assertErrorEnvelope(t, existingChannel, http.StatusConflict, "CHANNEL_EXISTS")

	blankThread := serveRequest(t, h, http.MethodPost, "/v1/channels/1/threads", `{"title":"  "}`)
	assertErrorEnvelope(t, blankThread, http.StatusBadRequest, "BAD_JSONL")
	missingChannel := serveRequest(t, h, http.MethodPost, "/v1/channels/9999/threads", `{"title":"orphan thread"}`)
	assertErrorEnvelope(t, missingChannel, http.StatusNotFound, "CHANNEL_NOT_FOUND")
}

func TestMessagePostRejectsParentFromAnotherThreadAsClientError(t *testing.T) {
	s, h, threadID := newTestHandlerWithThread(t)
	otherThreadID := newTestThread(t, s, "other")
	otherSeq, err := s.AppendMessage(otherThreadID, "alice", "human", "user", "elsewhere")
	if err != nil {
		t.Fatal(err)
	}
	otherID, err := s.MessageIDBySeq(otherThreadID, otherSeq)
	if err != nil {
		t.Fatal(err)
	}

	rec := serveRequest(t, h, http.MethodPost, fmt.Sprintf("/v1/threads/%d/messages", threadID), fmt.Sprintf(`{"name":"alice","role":"user","content":"reply","parent_id":%d}`, otherID))
	assertErrorEnvelope(t, rec, http.StatusBadRequest, "BAD_JSONL")
	messages, err := s.ListMessages(threadID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Fatalf("cross-thread parent created messages: %+v", messages)
	}
}

func TestBatchEventsUseJSONLCodecNormalization(t *testing.T) {
	s, h, threadID := newTestHandlerWithThread(t)

	body := `{"events":[{"role":"user","author":"legacy-agent","content":"no type, legacy author"},{"type":"message","name":"reviewer","role":"assistant","content":"typed"}]}`
	rec := serveRequest(t, h, http.MethodPost, fmt.Sprintf("/v1/threads/%d/events", threadID), body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	messages, err := s.ListMessages(threadID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages = %+v", messages)
	}
	if messages[0].Name != "legacy-agent" || messages[0].AuthorType != "human" {
		t.Fatalf("normalized legacy line = %+v", messages[0])
	}
	if messages[1].Name != "reviewer" || messages[1].AuthorType != "human" {
		t.Fatalf("normalized typed line = %+v", messages[1])
	}
}

func TestBatchEventsRejectLinesTheCodecRejects(t *testing.T) {
	s, h, threadID := newTestHandlerWithThread(t)
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "unknown type", body: `{"events":[{"type":"thread","name":"alice","role":"user","content":"unknown"}]}`},
		{name: "message without content", body: `{"events":[{"name":"alice","role":"user"}]}`},
		{name: "message without role", body: `{"events":[{"name":"alice","content":"no role"}]}`},
		{name: "message without name", body: `{"events":[{"role":"user","content":"no name"}]}`},
		{name: "reaction without emoji", body: `{"events":[{"type":"reaction","message_seq":1,"name":"bob"}]}`},
		{name: "reaction without target", body: `{"events":[{"type":"reaction","name":"bob","emoji":"+1"}]}`},
		{name: "null event", body: `{"events":[null]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			rec := serveRequest(t, h, http.MethodPost, fmt.Sprintf("/v1/threads/%d/events", threadID), test.body)
			assertErrorEnvelope(t, rec, http.StatusBadRequest, "BAD_JSONL")
		})
	}
	messages, err := s.ListMessages(threadID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Fatalf("rejected batches created messages: %+v", messages)
	}
}

func TestShutdownRejectsAgentRequests(t *testing.T) {
	_, h, _ := newTestHandlerWithThread(t)
	var shutdownCalls int
	SetShutdown(func() { shutdownCalls++ })
	t.Cleanup(func() { SetShutdown(nil) })

	req := httptest.NewRequest(http.MethodPost, "/api/shutdown", nil)
	req.Header.Set("X-Fluffle-Agent", "reviewer")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assertErrorEnvelope(t, rec, http.StatusForbidden, "AGENT_FORBIDDEN")
	if shutdownCalls != 0 {
		t.Fatal("agent request triggered shutdown")
	}

	humanRec := serveRequest(t, h, http.MethodPost, "/api/shutdown", "")
	if humanRec.Code != http.StatusOK {
		t.Fatalf("human status = %d body %s", humanRec.Code, humanRec.Body.String())
	}
}

func TestLegacyReactionRejectsMissingMessageTarget(t *testing.T) {
	s, h, threadID := newTestHandlerWithThread(t)
	if _, err := s.AppendMessage(threadID, "alice", "human", "user", "target"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/messages/9999/reactions"} {
		t.Run(path, func(t *testing.T) {
			rec := serveRequest(t, h, http.MethodPost, path, `{"emoji":"+1","name":"bob"}`)
			assertErrorEnvelope(t, rec, http.StatusNotFound, "MESSAGE_NOT_FOUND")
		})
	}
	reactions, err := s.ListReactions(threadID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reactions) != 0 {
		t.Fatalf("missing target created reactions: %+v", reactions)
	}
}

func newTestHandlerWithThread(t *testing.T) (*store.Store, http.Handler, int64) {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	if _, err := s.CreateChannel("test", "/repo", "", "", "", false); err != nil {
		t.Fatal(err)
	}
	threadID := newTestThread(t, s, "thread")
	return s, NewHandler(s), threadID
}

func newTestThread(t *testing.T, s *store.Store, title string) int64 {
	t.Helper()
	channels, err := s.ListChannels("", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 1 {
		t.Fatalf("channels = %+v", channels)
	}
	threadID, err := s.CreateThread(channels[0].ID, title)
	if err != nil {
		t.Fatal(err)
	}
	return threadID
}
