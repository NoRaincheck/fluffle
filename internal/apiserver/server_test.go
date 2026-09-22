package apiserver

import (
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
