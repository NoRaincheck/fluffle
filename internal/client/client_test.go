package client

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func useTestDaemon(t *testing.T, server *httptest.Server) {
	t.Helper()
	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(serverURL.Port())
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	daemon, err := json.Marshal(map[string]any{"port": port})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "daemon.json"), daemon, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLUFFLE_HOME", home)
}

func TestIsPreDispatchErrorRecognizesDialFailure(t *testing.T) {
	err := &url.Error{Op: "Post", URL: "http://127.0.0.1:1", Err: &net.OpError{Op: "dial", Err: errors.New("connection refused")}}
	if !IsPreDispatchError(err) {
		t.Fatalf("IsPreDispatchError(%v) = false", err)
	}
	readErr := &url.Error{Op: "Post", URL: "http://127.0.0.1:1", Err: &net.OpError{Op: "read", Err: errors.New("connection reset")}}
	if IsPreDispatchError(readErr) {
		t.Fatalf("IsPreDispatchError(%v) = true", readErr)
	}
}

func TestDaemonBaseURLMissingFileErrors(t *testing.T) {
	t.Setenv("FLUFFLE_HOME", t.TempDir())
	if _, err := DaemonBaseURL(); err == nil {
		t.Fatal("expected error with no daemon.json")
	}
}

func TestDaemonBaseURLContextTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * daemonHealthTimeout)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	useTestDaemon(t, server)

	if _, err := DaemonBaseURLContext(context.Background()); err == nil {
		t.Fatal("expected stalled health probe to fail")
	}
}

func TestEnsureDaemonContextUsesRunningDaemon(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/health" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	useTestDaemon(t, server)

	got, err := EnsureDaemonContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != server.URL {
		t.Fatalf("base URL = %q, want %q", got, server.URL)
	}
}
