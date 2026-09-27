package apiserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/NoRaincheck/fluffle/internal/store"
)

func seedRows(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	chID, err := s.CreateChannel(context.Background(), "eng", "", "", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	thID, err := s.CreateThread(context.Background(), chID, "pr-review")
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"looks great", "ship it"} {
		if _, err := s.AppendMessage(context.Background(), thID, "alice", "human", "user", body); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestRowsEndpoint(t *testing.T) {
	h := NewHandler(seedRows(t))
	for _, g := range []string{"message", "thread", "channel"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/rows?g="+g, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("g=%s status = %d, want 200", g, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("g=%s Content-Type = %q", g, ct)
		}
		var rows []store.Row
		if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
			t.Fatal(err)
		}
		if len(rows) == 0 {
			t.Errorf("g=%s returned no rows", g)
		}
	}
}

func TestRowsRejectsBadGranularity(t *testing.T) {
	h := NewHandler(seedRows(t))
	for _, q := range []string{"", "?g=folder", "?g="} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/rows"+q, nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("/v1/rows%s status = %d, want 400", q, rec.Code)
		}
		var eb struct{ Code, Message string }
		if err := json.Unmarshal(rec.Body.Bytes(), &eb); err != nil {
			t.Fatal(err)
		}
		if eb.Code == "" || eb.Message == "" {
			t.Fatalf("/v1/rows%s error envelope = %+v", q, eb)
		}
	}
}

func TestRowsEndpointCoexistsWithInbox(t *testing.T) {
	h := NewHandler(seedRows(t))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/inbox", nil))
	if rec.Code != http.StatusOK {
		t.Fatal("/v1/inbox must keep answering until the CLI migrates")
	}
}
