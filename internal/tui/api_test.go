package tui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/NoRaincheck/fluffle/internal/store"
)

func assertErrorCode(t *testing.T, err error, wantCode string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s error", wantCode)
	}
	if !strings.HasPrefix(err.Error(), wantCode+":") && !strings.HasPrefix(err.Error(), wantCode) {
		t.Fatalf("error = %q, want %s classification", err.Error(), wantCode)
	}
}

// jsonServer answers with a fixed body for any path.
func jsonServer(t *testing.T, status int, body string) string {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	return ts.URL
}

// closedServer returns the base URL of a server that is no longer listening.
func closedServer(t *testing.T) string {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := ts.URL
	ts.Close()
	return url
}

func TestAPIListRowsDecodesTheFeed(t *testing.T) {
	var gotPath, gotQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"ID":7,"ThreadID":3,"Time":"2026-09-23T15:04:12Z",` +
			`"Channel":"eng","Thread":"pr-review","Name":"ci.bot","Content":"build passed","Count":3}]`))
	}))
	defer ts.Close()

	got, err := NewAPIClient(ts.URL).ListRows(nil, store.GranularityThread, 50)
	if err != nil {
		t.Fatalf("ListRows: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1", len(got))
	}
	if got[0].ID != 7 || got[0].ThreadID != 3 || got[0].Channel != "eng" ||
		got[0].Thread != "pr-review" || got[0].Name != "ci.bot" ||
		got[0].Content != "build passed" || got[0].Count != 3 {
		t.Fatalf("row decoded wrong: %+v", got[0])
	}
	if gotPath != "/v1/rows" {
		t.Errorf("path = %q, want /v1/rows", gotPath)
	}
	if gotQuery != "g=thread&limit=50" {
		t.Errorf("query = %q, want g=thread&limit=50", gotQuery)
	}
}

func TestAPIListRowsDefaultsTheLimit(t *testing.T) {
	var gotQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer ts.Close()

	if _, err := NewAPIClient(ts.URL).ListRows(nil, store.GranularityMessage, 0); err != nil {
		t.Fatalf("ListRows: %v", err)
	}
	if gotQuery != "g=message&limit=200" {
		t.Errorf("query = %q, want the default limit 200", gotQuery)
	}
}

func TestAPIListRowsEmptyArrayIsNonNil(t *testing.T) {
	ts := jsonServer(t, http.StatusOK, `[]`)
	got, err := NewAPIClient(ts).ListRows(nil, store.GranularityMessage, 10)
	if err != nil {
		t.Fatalf("ListRows: %v", err)
	}
	if got == nil {
		t.Fatal("an empty feed must be a non-nil empty slice, never nil")
	}
	if len(got) != 0 {
		t.Fatalf("got %d rows, want 0", len(got))
	}
}

// A null body is rejected on purpose. The daemon must answer [] rather than
// null, so a null here is a protocol violation, not an empty feed, and the
// model must see an error instead of silently rendering an empty list.
func TestAPIListRowsRejectsANullBody(t *testing.T) {
	ts := jsonServer(t, http.StatusOK, `null`)
	_, err := NewAPIClient(ts).ListRows(nil, store.GranularityMessage, 10)
	assertErrorCode(t, err, "DAEMON_ERROR")
}

func TestAPIListMessagesDecodesTheThread(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"ID":1,"ThreadID":5,"Seq":1,"Name":"alice","AuthorType":"human","Content":"hi"}]`))
	}))
	defer ts.Close()

	got, err := NewAPIClient(ts.URL).ListMessages(nil, 5)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(got) != 1 || got[0].Content != "hi" || got[0].AuthorType != "human" {
		t.Fatalf("messages decoded wrong: %+v", got)
	}
	if gotPath != "/v1/threads/5/messages" {
		t.Errorf("path = %q, want /v1/threads/5/messages", gotPath)
	}
}

// 400 is the boundary: readAPIError must run for exactly 400 and not for 399.
func TestAPIErrorResponseSurfacesTheEnvelope(t *testing.T) {
	for _, status := range []int{
		http.StatusBadRequest, http.StatusConflict, http.StatusInternalServerError,
	} {
		ts := jsonServer(t, status, `{"code":"THREAD_CLOSED","message":"the thread is closed"}`)
		_, err := NewAPIClient(ts).ListRows(nil, store.GranularityMessage, 10)
		if err == nil {
			t.Fatalf("status %d: expected an error", status)
		}
		if !strings.Contains(err.Error(), "THREAD_CLOSED") || !strings.Contains(err.Error(), "the thread is closed") {
			t.Fatalf("status %d: error = %q, want the envelope's code and message", status, err.Error())
		}
	}
}

func TestAPIErrorResponseWithoutEnvelopeIsDaemonError(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusInternalServerError} {
		ts := jsonServer(t, status, `not-json`)
		_, err := NewAPIClient(ts).ListRows(nil, store.GranularityMessage, 10)
		assertErrorCode(t, err, "DAEMON_ERROR")
		_, err = NewAPIClient(ts).ListMessages(nil, 1)
		assertErrorCode(t, err, "DAEMON_ERROR")
		assertErrorCode(t, NewAPIClient(ts).SendReply(nil, 1, "hi"), "DAEMON_ERROR")
	}
}

// A 2xx response that is not JSON is a decode failure, not a delivery
// failure, so reads and writes classify it differently.
func TestAPISuccessStatusWithBadBodyIsNotSwallowed(t *testing.T) {
	ts := jsonServer(t, http.StatusOK, `[]`)
	_, err := NewAPIClient(ts).ListRows(nil, store.GranularityMessage, 10)
	if err != nil {
		t.Fatalf("a 200 with a valid body must not error: %v", err)
	}
}

func TestAPIReadDecodeFailuresAreDaemonErrors(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
	}{
		{name: "object instead of array", body: `{"not":"a list"}`},
		{name: "not json", body: `not-json`},
		{name: "trailing json", body: `[] []`},
		{name: "truncated array", body: `[`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ts := jsonServer(t, http.StatusOK, tt.body)
			c := NewAPIClient(ts)
			_, err := c.ListRows(nil, store.GranularityMessage, 10)
			assertErrorCode(t, err, "DAEMON_ERROR")
			_, err = c.ListMessages(nil, 1)
			assertErrorCode(t, err, "DAEMON_ERROR")
		})
	}
}

func TestAPIReadTransportFailureIsDaemonDown(t *testing.T) {
	c := NewAPIClient(closedServer(t))
	_, err := c.ListRows(nil, store.GranularityMessage, 10)
	assertErrorCode(t, err, "DAEMON_DOWN")
	_, err = c.ListMessages(nil, 1)
	assertErrorCode(t, err, "DAEMON_DOWN")
}

func TestAPISendReplyPostsTheTextWithoutAParent(t *testing.T) {
	var gotPath, gotBody string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotPath, gotBody = r.URL.Path, string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"seq":7}`))
	}))
	defer ts.Close()

	if err := NewAPIClient(ts.URL).SendReply(nil, 5, "thanks"); err != nil {
		t.Fatalf("SendReply: %v", err)
	}
	if gotPath != "/v1/threads/5/messages" {
		t.Errorf("path = %q, want /v1/threads/5/messages", gotPath)
	}
	if strings.Contains(gotBody, "ParentID") {
		t.Errorf("body = %q, want no parent: a reply is a message in the thread", gotBody)
	}
	if !strings.Contains(gotBody, "thanks") {
		t.Errorf("body = %q, want the reply text", gotBody)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(gotBody), &decoded); err != nil {
		t.Fatalf("body is not a JSON object: %q", gotBody)
	}
	if decoded["Role"] != "user" {
		t.Errorf("Role = %v, want user", decoded["Role"])
	}
}

func TestAPISendReplyWithoutSeqIsDeliveryUnknown(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
	}{
		{name: "empty object", body: `{}`},
		{name: "zero seq", body: `{"seq":0}`},
		{name: "negative seq", body: `{"seq":-1}`},
		{name: "null", body: `null`},
		{name: "not json", body: `not-json`},
		{name: "trailing json", body: `{"seq":7} {}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ts := jsonServer(t, http.StatusOK, tt.body)
			assertErrorCode(t, NewAPIClient(ts).SendReply(nil, 1, "hi"), "DELIVERY_UNKNOWN")
		})
	}
}

func TestAPISendReplyErrorResponseSurfacesTheEnvelope(t *testing.T) {
	ts := jsonServer(t, http.StatusConflict, `{"code":"THREAD_CLOSED","message":"closed"}`)
	err := NewAPIClient(ts).SendReply(nil, 1, "hi")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "THREAD_CLOSED") {
		t.Fatalf("error = %q, want the envelope's code", err.Error())
	}
}

func TestAPIMutationConnectionFailureIsDaemonDown(t *testing.T) {
	assertErrorCode(t, NewAPIClient(closedServer(t)).SendReply(nil, 1, "hello"), "DAEMON_DOWN")
}

// A reply that leaves the wire is a delivery the TUI cannot confirm, which is
// exit code 2's territory, not a daemon that was never up.
func TestAPIMutationTimeoutIsDeliveryUnknown(t *testing.T) {
	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer func() {
		close(release)
		ts.Close()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	assertErrorCode(t, NewAPIClient(ts.URL).SendReply(ctx, 1, "hello"), "DELIVERY_UNKNOWN")
}

func TestAPIContextCancellationReturnsPromptly(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer func() {
		close(release)
		ts.Close()
	}()

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := NewAPIClient(ts.URL).ListRows(ctx, store.GranularityMessage, 10)
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("expected a canceled request to fail")
		}
	case <-time.After(time.Second):
		close(release)
		<-result
		t.Fatal("canceled request did not return promptly")
	}
}

func TestDecodeStrictJSONRejectsBadBodies(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
	}{
		{name: "null", body: `null`},
		{name: "padded null", body: "  null\n"},
		{name: "two values", body: `{} {}`},
		{name: "empty", body: ``},
		{name: "not json", body: `nope`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out struct {
				Seq int64 `json:"seq"`
			}
			if err := decodeStrictJSON(strings.NewReader(tt.body), &out); err == nil {
				t.Fatalf("decodeStrictJSON accepted %q", tt.body)
			}
		})
	}
	var out struct {
		Seq int64 `json:"seq"`
	}
	if err := decodeStrictJSON(strings.NewReader(`{"seq":7} `), &out); err != nil {
		t.Fatalf("decodeStrictJSON rejected a valid body with trailing space: %v", err)
	}
	if out.Seq != 7 {
		t.Fatalf("seq = %d, want 7", out.Seq)
	}
}
