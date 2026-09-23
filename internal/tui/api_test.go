package tui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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
