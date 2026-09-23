package apiserver

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/NoRaincheck/fluffle/internal/store"
)

func TestInboxHandler(t *testing.T) {
	s, _ := store.Open(":memory:")
	defer s.Close()
	ch, _ := s.CreateChannel("general", "", "", "", "", true)
	th, _ := s.CreateThread(ch, "hello")
	_, _ = s.AppendMessage(th, "alice", "human", "user", "hi")
	h := NewHandler(s)
	req := httptest.NewRequest("GET", "/v1/inbox?limit=10", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("want 200 got %d body %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("want json ct got %q", ct)
	}
	var msgs []store.InboxMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &msgs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("want 1 got %d", len(msgs))
	}
	if msgs[0].ChannelName != "general" {
		t.Fatalf("enrichment got %q", msgs[0].ChannelName)
	}
}
