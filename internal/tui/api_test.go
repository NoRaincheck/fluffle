package tui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
