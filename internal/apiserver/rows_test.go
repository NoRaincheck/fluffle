package apiserver

import (
	"context"
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
	for _, seed := range []struct {
		channel string
		threads map[string][]string
	}{
		{channel: "eng", threads: map[string][]string{
			"pr-review": {"looks great", "ship it"},
			"ci-flake":  {"flaky again"},
		}},
		{channel: "ops", threads: map[string][]string{
			"deploy": {"shipped"},
		}},
	} {
		chID, err := s.CreateChannel(context.Background(), seed.channel, "", "", "", "", true)
		if err != nil {
			t.Fatal(err)
		}
		for title, bodies := range seed.threads {
			thID, err := s.CreateThread(context.Background(), chID, title)
			if err != nil {
				t.Fatal(err)
			}
			for _, body := range bodies {
				if _, err := s.AppendMessage(context.Background(), thID, "alice", "human", "user", body); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	return s
}

func TestRowsEndpoint(t *testing.T) {
	want := map[string]int{"message": 4, "thread": 3, "channel": 2}
	seen := make(map[string]int, len(want))
	h := NewHandler(seedRows(t))
	for _, g := range []string{"message", "thread", "channel"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/rows?g="+g, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("g=%s status = %d, want 200", g, rec.Code)
		}
		assertJSONContentType(t, rec)
		var rows []store.Row
		decodeResponse(t, rec, &rows)
		if len(rows) != want[g] {
			t.Errorf("g=%s rows = %d, want %d", g, len(rows), want[g])
		}
		seen[g] = len(rows)
	}
	for a, countA := range seen {
		for b, countB := range seen {
			if a < b && countA == countB {
				t.Fatalf("fixture gives g=%s and g=%s both %d rows, so it cannot tell them apart", a, b, countA)
			}
		}
	}
}

func TestRowsRejectsBadGranularity(t *testing.T) {
	h := NewHandler(seedRows(t))
	for _, q := range []string{"", "?g=folder", "?g="} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/rows"+q, nil))
		assertErrorEnvelope(t, rec, http.StatusBadRequest, "BAD_ARGS")
	}
}

func TestRowsMapsStoreFailureToDaemonError(t *testing.T) {
	s := seedRows(t)
	h := NewHandler(s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/rows?g=message", nil))
	assertErrorEnvelope(t, rec, http.StatusInternalServerError, "DAEMON_ERROR")
}
