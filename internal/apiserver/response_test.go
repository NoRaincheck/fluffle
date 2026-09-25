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

func TestSuccessfulPostsUseJSONContentType(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := NewHandler(s)

	channelRec := serveRequest(t, h, http.MethodPost, "/v1/channels", `{"Name":"contract","RepoAbsPath":"/repo"}`)
	assertJSONContentType(t, channelRec)
	if channelRec.Code != http.StatusOK {
		t.Fatalf("channel status = %d body %s", channelRec.Code, channelRec.Body.String())
	}
	var channel struct {
		ID int64 `json:"id"`
	}
	decodeResponse(t, channelRec, &channel)
	if channel.ID == 0 {
		t.Fatal("channel id = 0")
	}

	threadRec := serveRequest(t, h, http.MethodPost, fmt.Sprintf("/v1/channels/%d/threads", channel.ID), `{"Title":"contract-thread"}`)
	assertJSONContentType(t, threadRec)
	if threadRec.Code != http.StatusOK {
		t.Fatalf("thread status = %d body %s", threadRec.Code, threadRec.Body.String())
	}
	var thread struct {
		ID int64 `json:"id"`
	}
	decodeResponse(t, threadRec, &thread)
	if thread.ID == 0 {
		t.Fatal("thread id = 0")
	}

	messageRec := serveRequest(t, h, http.MethodPost, fmt.Sprintf("/v1/threads/%d/messages", thread.ID), `{"Name":"alice","Role":"user","Content":"hello"}`)
	assertJSONContentType(t, messageRec)
	if messageRec.Code != http.StatusOK {
		t.Fatalf("message status = %d body %s", messageRec.Code, messageRec.Body.String())
	}
	var message struct {
		Seq int64 `json:"seq"`
	}
	decodeResponse(t, messageRec, &message)
	if message.Seq != 1 {
		t.Fatalf("message seq = %d", message.Seq)
	}

	messages, err := s.ListMessages(thread.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages = %d", len(messages))
	}
	reactionRec := serveRequest(t, h, http.MethodPost, fmt.Sprintf("/v1/messages/%d/reactions", messages[0].ID), `{"Emoji":"+1","Name":"alice"}`)
	assertJSONContentType(t, reactionRec)
	if reactionRec.Code != http.StatusOK {
		t.Fatalf("reaction status = %d body %s", reactionRec.Code, reactionRec.Body.String())
	}
	var reaction struct {
		OK bool `json:"ok"`
	}
	decodeResponse(t, reactionRec, &reaction)
	if !reaction.OK {
		t.Fatal("reaction ok = false")
	}
}

func TestPutChannelsReturnsMethodNotAllowedEnvelope(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := NewHandler(s)
	rec := serveRequest(t, h, http.MethodPut, "/v1/channels", "")
	assertErrorEnvelope(t, rec, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
}

func TestStoreFailuresUseDaemonErrorEnvelope(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	channelID, err := s.CreateChannel("c", "/repo", "", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	threadID, err := s.CreateThread(channelID, "t")
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
	}{
		{name: "channels", path: "/v1/channels"},
		{name: "threads", path: fmt.Sprintf("/v1/channels/%d/threads", channelID)},
		{name: "messages", path: fmt.Sprintf("/v1/threads/%d/messages", threadID)},
	} {
		t.Run(test.name, func(t *testing.T) {
			rec := serveRequest(t, h, http.MethodGet, test.path, "")
			assertErrorEnvelope(t, rec, http.StatusInternalServerError, "DAEMON_ERROR")
		})
	}
}

func serveRequest(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func assertJSONContentType(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type = %q", got)
	}
}

func decodeResponse(t *testing.T, rec *httptest.ResponseRecorder, value any) {
	t.Helper()
	if err := json.NewDecoder(rec.Body).Decode(value); err != nil {
		t.Fatalf("decode response: %v body %s", err, rec.Body.String())
	}
}

func assertErrorEnvelope(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	assertJSONContentType(t, rec)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &fields); err != nil {
		t.Fatalf("decode error: %v body %s", err, rec.Body.String())
	}
	if len(fields) != 2 {
		t.Fatalf("error fields = %d body %s", len(fields), rec.Body.String())
	}
	if _, ok := fields["code"]; !ok {
		t.Fatalf("error code missing: %s", rec.Body.String())
	}
	if _, ok := fields["message"]; !ok {
		t.Fatalf("error message missing: %s", rec.Body.String())
	}
	var got struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode error fields: %v", err)
	}
	if got.Code != code {
		t.Fatalf("error code = %q want %q", got.Code, code)
	}
	if got.Message == "" {
		t.Fatal("error message is empty")
	}
}
