package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/NoRaincheck/fluffle/internal/jsonl"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func captureOutput(t *testing.T, fn func() int) (int, string, string) {
	t.Helper()
	stdoutFile, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	stderrFile, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	originalStdout, originalStderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = stdoutFile, stderrFile
	code := fn()
	os.Stdout, os.Stderr = originalStdout, originalStderr
	if err := stdoutFile.Close(); err != nil {
		t.Fatal(err)
	}
	if err := stderrFile.Close(); err != nil {
		t.Fatal(err)
	}
	stdout, err := os.ReadFile(stdoutFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := os.ReadFile(stderrFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	return code, string(stdout), string(stderr)
}

func captureStderr(t *testing.T, fn func() int) (int, string) {
	t.Helper()
	code, _, stderr := captureOutput(t, fn)
	return code, stderr
}

func assertCLIErrorEnvelope(t *testing.T, output, wantCode string) string {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal([]byte(output), &fields); err != nil {
		t.Fatalf("invalid JSON error %q: %v", output, err)
	}
	if len(fields) != 2 {
		t.Fatalf("error fields = %d, want 2: %q", len(fields), output)
	}
	code, ok := fields["code"].(string)
	if !ok || code != wantCode {
		t.Fatalf("code = %#v, want %q: %q", fields["code"], wantCode, output)
	}
	message, ok := fields["message"].(string)
	if !ok || message == "" {
		t.Fatalf("message = %#v, want nonempty string: %q", fields["message"], output)
	}
	return message
}

func TestCLIErrorEnvelope(t *testing.T) {
	var buf bytes.Buffer
	if got := writeCLIError(&buf, "DAEMON_ERROR", "storage failed"); got != 2 {
		t.Fatalf("exit = %d, want 2", got)
	}
	const want = "{\"code\":\"DAEMON_ERROR\",\"message\":\"storage failed\"}\n"
	if got := buf.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if strings.Contains(buf.String(), "retryable") {
		t.Fatal("unexpected retryable field")
	}
}

func TestExitCodeForClassification(t *testing.T) {
	tests := []struct {
		code string
		want int
	}{
		{"DAEMON_DOWN", 2},
		{"DAEMON_ERROR", 2},
		{"DELIVERY_UNKNOWN", 2},
		{"BAD_ARGS", 1},
		{"BAD_JSONL", 1},
		{"FILE_READ", 1},
		{"CHANNEL_NOT_FOUND", 1},
		{"PERMISSION_DENIED", 1},
		{"VALIDATION_ERROR", 1},
		{"METHOD_NOT_ALLOWED", 1},
	}
	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			if got := exitCodeFor(tt.code); got != tt.want {
				t.Fatalf("exitCodeFor(%q) = %d, want %d", tt.code, got, tt.want)
			}
		})
	}
}

func TestCLIFlagParseErrorEnvelope(t *testing.T) {
	code, output := captureStderr(t, func() int {
		return run([]string{"init", "--unknown"})
	})
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	message := assertCLIErrorEnvelope(t, output, "BAD_ARGS")
	if !strings.Contains(message, "unknown") {
		t.Fatalf("message = %q, want unknown flag", message)
	}
}

func TestCLIAPIGetMalformedResponseIsDaemonError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not-json"))
	}))
	defer server.Close()
	code, output := captureStderr(t, func() int {
		var out any
		return apiGet(server.URL, "", &out)
	})
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	assertCLIErrorEnvelope(t, output, "DAEMON_ERROR")
}

func TestCLIAPIPostTransportFailureIsDeliveryUnknown(t *testing.T) {
	originalClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection reset")
	})}
	t.Cleanup(func() { http.DefaultClient = originalClient })
	code, output := captureStderr(t, func() int {
		return apiPost("http://127.0.0.1/messages", "", map[string]any{"content": "hello"}, nil)
	})
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	assertCLIErrorEnvelope(t, output, "DELIVERY_UNKNOWN")
}

func TestCLIAPIPostMalformedSuccessBodyIsDeliveryUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":`))
	}))
	defer server.Close()
	var out struct {
		ID int64 `json:"id"`
	}
	code, output := captureStderr(t, func() int {
		return apiPost(server.URL, "", map[string]any{"name": "test"}, &out)
	})
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	assertCLIErrorEnvelope(t, output, "DELIVERY_UNKNOWN")
}

func TestCLIChannelCreateJSONReturnsEnrichmentFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/health":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/channels":
			_, _ = w.Write([]byte(`{"id":42}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/channels":
			_, _ = w.Write([]byte("not-json"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
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
	code, stdout, stderr := captureOutput(t, func() int {
		return channelCreateCmd([]string{"--name", "test", "--orphaned", "--json"})
	})
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	assertCLIErrorEnvelope(t, stderr, "DAEMON_ERROR")
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
}

func TestCLIAPIPostLocalFailureIsClientError(t *testing.T) {
	code, output := captureStderr(t, func() int {
		return apiPost("http://127.0.0.1/messages", "", map[string]any{"bad": make(chan struct{})}, nil)
	})
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	assertCLIErrorEnvelope(t, output, "BAD_REQUEST")
}

func TestCLITuiDaemonErrorEnvelope(t *testing.T) {
	home := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(home, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLUFFLE_HOME", home)
	code, output := captureStderr(t, func() int {
		return run([]string{"tui"})
	})
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	assertCLIErrorEnvelope(t, output, "DAEMON_DOWN")
}

func TestInitOutsideGitFails(t *testing.T) {
	dir := t.TempDir()
	if got := run([]string{"init", "--repo", dir}); got != 1 {
		t.Fatalf("want exit 1 got %d", got)
	}
}

func TestDumpThreadMessagesProjectsParentSequence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/threads/7/messages" {
			t.Errorf("path = %q, want /v1/threads/7/messages", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"ID":41,"ThreadID":7,"Seq":1,"ParentID":null,"Name":"alice","AuthorType":"human","Role":"user","Content":"parent","CreatedAt":"2026-09-25T00:00:00Z"},
			{"ID":42,"ThreadID":7,"Seq":2,"ParentID":{"Int64":41,"Valid":true},"Name":"bob","AuthorType":"human","Role":"user","Content":"reply","CreatedAt":"2026-09-25T00:01:00Z"}
		]`))
	}))
	defer server.Close()

	code, stdout, stderr := captureOutput(t, func() int {
		return dumpThreadMessages(server.URL, 7, 0, "")
	})
	if code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, stderr)
	}
	lines, err := jsonl.ParseLines([]byte(stdout))
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2", len(lines))
	}
	if lines[1].ParentSeq != 1 {
		t.Fatalf("parent_seq = %d, want 1", lines[1].ParentSeq)
	}
}
