package tui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NoRaincheck/fluffle/internal/store"
)

func TestAPIListInbox(t *testing.T) {
	var inbox []store.InboxMessage = []store.InboxMessage{{Message: store.Message{ID: 1, Content: "hi"}, ChannelName: "general", ThreadTitle: "hello"}}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/inbox" {
			t.Fatalf("path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(inbox)
	}))
	defer ts.Close()
	c := NewAPIClient(ts.URL)
	got, err := c.ListInbox(nil, 10)
	if err != nil {
		t.Fatalf("err %v", err)
	}
	if len(got) != 1 || got[0].Content != "hi" {
		t.Fatalf("wrong")
	}
}

func TestAPIContextCancellationReturnsPromptly(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseRequest := func() {
		releaseOnce.Do(func() {
			close(release)
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer func() {
		releaseRequest()
		server.Close()
	}()

	c := NewAPIClient(server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := c.ListInbox(ctx, 10)
		result <- err
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request did not start")
	}
	cancel()

	select {
	case err := <-result:
		if err == nil {
			t.Fatal("expected canceled request to fail")
		}
	case <-time.After(time.Second):
		releaseRequest()
		<-result
		t.Fatal("canceled request did not return promptly")
	}
}

func assertErrorCode(t *testing.T, err error, wantCode string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s error", wantCode)
	}
	if !strings.HasPrefix(err.Error(), wantCode+":") {
		t.Fatalf("error = %q, want %s classification", err.Error(), wantCode)
	}
}

func TestAPIMutationConnectionFailuresAreDaemonDown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := server.URL
	server.Close()
	c := NewAPIClient(base)

	assertErrorCode(t, c.SendMessage(nil, 1, 0, "hello"), "DAEMON_DOWN")
	assertErrorCode(t, c.AddReaction(nil, 1, "👀"), "DAEMON_DOWN")
	if _, err := c.CreateChannel(nil, "name", "", "", false); err == nil || !strings.HasPrefix(err.Error(), "DAEMON_DOWN:") {
		t.Fatalf("CreateChannel error = %v", err)
	}
	if _, err := c.CreateThread(nil, 1, "title"); err == nil || !strings.HasPrefix(err.Error(), "DAEMON_DOWN:") {
		t.Fatalf("CreateThread error = %v", err)
	}
}

func TestAPIMutationTimeoutIsDeliveryUnknown(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseServer := func() { releaseOnce.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer func() {
		releaseServer()
		server.Close()
	}()
	c := NewAPIClient(server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	assertErrorCode(t, c.SendMessage(ctx, 1, 0, "hello"), "DELIVERY_UNKNOWN")
	assertErrorCode(t, c.AddReaction(ctx, 1, "👀"), "DELIVERY_UNKNOWN")
	if _, err := c.CreateThread(ctx, 1, "title"); err == nil || !strings.HasPrefix(err.Error(), "DELIVERY_UNKNOWN:") {
		t.Fatalf("CreateThread error = %v", err)
	}
}

func TestAPIReadDecodeFailuresAreDaemonErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/channels":
			_, _ = w.Write([]byte(`{"not":"a list"}`))
		case "/v1/channels/1/threads":
			_, _ = w.Write([]byte("not-json"))
		case "/v1/threads/1/messages":
			_, _ = w.Write([]byte(`null`))
		case "/v1/inbox":
			_, _ = w.Write([]byte("not-json"))
		case "/v1/broken-error":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("not-json"))
		default:
			_, _ = w.Write([]byte("not-json"))
		}
	}))
	defer server.Close()
	c := NewAPIClient(server.URL)

	if _, err := c.ListChannels(nil, "", ""); err == nil || !strings.HasPrefix(err.Error(), "DAEMON_ERROR:") {
		t.Fatalf("ListChannels error = %v", err)
	}
	if _, err := c.ListThreads(nil, 1); err == nil || !strings.HasPrefix(err.Error(), "DAEMON_ERROR:") {
		t.Fatalf("ListThreads error = %v", err)
	}
	if _, err := c.ListMessages(nil, 1); err == nil || !strings.HasPrefix(err.Error(), "DAEMON_ERROR:") {
		t.Fatalf("ListMessages error = %v", err)
	}
	if _, err := c.ListInbox(nil, 10); err == nil || !strings.HasPrefix(err.Error(), "DAEMON_ERROR:") {
		t.Fatalf("ListInbox error = %v", err)
	}
}

func TestAPIReadTransportFailuresRemainDaemonDown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := server.URL
	server.Close()
	c := NewAPIClient(base)

	if _, err := c.ListChannels(nil, "", ""); err == nil || !strings.HasPrefix(err.Error(), "DAEMON_DOWN:") {
		t.Fatalf("ListChannels error = %v", err)
	}
}

func TestAPIMalformedMutationAcksAreDeliveryUnknown(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "empty object", body: `{}`},
		{name: "null", body: `null`},
		{name: "trailing json", body: `{"ok":true} {}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			c := NewAPIClient(server.URL)

			assertErrorCode(t, c.SendMessage(nil, 1, 0, "hello"), "DELIVERY_UNKNOWN")
			assertErrorCode(t, c.AddReaction(nil, 1, "👀"), "DELIVERY_UNKNOWN")
			if _, err := c.CreateChannel(nil, "name", "", "", false); err == nil || !strings.HasPrefix(err.Error(), "DELIVERY_UNKNOWN:") {
				t.Fatalf("CreateChannel error = %v", err)
			}
		})
	}
}

func TestAPIMutationZeroAcknowledgementIsDeliveryUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/threads/1/messages":
			_, _ = w.Write([]byte(`{"seq":0}`))
		case "/v1/messages/1/reactions":
			_, _ = w.Write([]byte(`{"ok":false}`))
		default:
			_, _ = w.Write([]byte(`{"id":0}`))
		}
	}))
	defer server.Close()
	c := NewAPIClient(server.URL)

	assertErrorCode(t, c.SendMessage(nil, 1, 0, "hello"), "DELIVERY_UNKNOWN")
	assertErrorCode(t, c.AddReaction(nil, 1, "👀"), "DELIVERY_UNKNOWN")
	if _, err := c.CreateThread(nil, 1, "title"); err == nil || !strings.HasPrefix(err.Error(), "DELIVERY_UNKNOWN:") {
		t.Fatalf("CreateThread error = %v", err)
	}
}

func TestAPIErrorResponsesWithoutEnvelopeAreDaemonErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("not-json"))
	}))
	defer server.Close()
	c := NewAPIClient(server.URL)

	if _, err := c.ListMessages(nil, 1); err == nil || !strings.HasPrefix(err.Error(), "DAEMON_ERROR:") {
		t.Fatalf("ListMessages error = %v", err)
	}
	assertErrorCode(t, c.SendMessage(nil, 1, 0, "hello"), "DAEMON_ERROR")
}
