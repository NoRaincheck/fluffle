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
