package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/NoRaincheck/fluffle/internal/agentcfg"
	"github.com/NoRaincheck/fluffle/internal/jsonl"
	"github.com/NoRaincheck/fluffle/internal/runner"
	"github.com/NoRaincheck/fluffle/internal/session"
	"github.com/NoRaincheck/fluffle/internal/store"
)

type recordedCLIRequest struct {
	Method   string
	Path     string
	RawQuery string
	Header   http.Header
	Body     []byte
}

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

func setTestStdin(t *testing.T, file *os.File) {
	t.Helper()
	original := os.Stdin
	os.Stdin = file
	t.Cleanup(func() {
		os.Stdin = original
		_ = file.Close()
	})
}

func stdinFile(t *testing.T, content string) *os.File {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(file, content); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	return file
}

func recordCLIRequest(requests *[]recordedCLIRequest, mu *sync.Mutex, r *http.Request) {
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
	}
	mu.Lock()
	*requests = append(*requests, recordedCLIRequest{
		Method:   r.Method,
		Path:     r.URL.Path,
		RawQuery: r.URL.RawQuery,
		Header:   r.Header.Clone(),
		Body:     body,
	})
	mu.Unlock()
}

func snapshotCLIRequests(requests *[]recordedCLIRequest, mu *sync.Mutex) []recordedCLIRequest {
	mu.Lock()
	defer mu.Unlock()
	return append([]recordedCLIRequest(nil), (*requests)...)
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
		return apiGet(server.URL, "", &out, nil)
	})
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	assertCLIErrorEnvelope(t, output, "DAEMON_ERROR")
}

func TestCLIAPIPostConnectionFailureIsDaemonDown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close()

	code, output := captureStderr(t, func() int {
		return apiPost(url, "", map[string]any{"content": "hello"}, nil, nil)
	})
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	assertCLIErrorEnvelope(t, output, "DAEMON_DOWN")
}

func TestCLIAPIPostTimeoutIsDeliveryUnknownWithoutRetry(t *testing.T) {
	var attempts atomic.Int32
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseRequest := func() {
		releaseOnce.Do(func() {
			close(release)
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer func() {
		releaseRequest()
		server.Close()
	}()

	type result struct {
		code   int
		output string
	}
	done := make(chan result, 1)
	go func() {
		code, output := captureStderr(t, func() int {
			return apiPost(server.URL, "", map[string]any{"content": "hello"}, nil, nil)
		})
		done <- result{code: code, output: output}
	}()

	var got result
	select {
	case got = <-done:
	case <-time.After(7 * time.Second):
		releaseRequest()
		<-done
		t.Fatal("stalled post did not hit the finite client timeout")
	}
	if got.code != 2 {
		t.Fatalf("exit = %d, want 2", got.code)
	}
	assertCLIErrorEnvelope(t, got.output, "DELIVERY_UNKNOWN")
	if got := attempts.Load(); got != 1 {
		t.Fatalf("post attempts = %d, want 1", got)
	}
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
		return apiPost(server.URL, "", map[string]any{"name": "test"}, &out, nil)
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
		return apiPost("http://127.0.0.1/messages", "", map[string]any{"bad": make(chan struct{})}, nil, nil)
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
		switch r.URL.Path {
		case "/v1/threads/7/messages":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[
				{"ID":41,"ThreadID":7,"Seq":1,"ParentID":null,"Name":"alice","AuthorType":"human","Role":"user","Content":"parent","CreatedAt":"2026-09-25T00:00:00Z"},
				{"ID":42,"ThreadID":7,"Seq":2,"ParentID":{"Int64":41,"Valid":true},"Name":"bob","AuthorType":"human","Role":"user","Content":"reply","CreatedAt":"2026-09-25T00:01:00Z"}
			]`))
		case "/v1/threads/7/reactions":
			_, _ = w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	code, stdout, stderr := captureOutput(t, func() int {
		return dumpThreadMessages(server.URL, 7, 0, -1, "")
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

func TestInboxJSONUsesLimitAndDecodesEntries(t *testing.T) {
	var requests []recordedCLIRequest
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recordCLIRequest(&requests, &mu, r)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/health":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/v1/inbox":
			_, _ = w.Write([]byte(`[{"ID":9,"ThreadID":3,"Seq":2,"ParentID":null,"Name":"alice","AuthorType":"human","Role":"user","Content":"portable","CreatedAt":"2026-09-25T01:00:00Z","channel_name":"dev","channel_id":4,"thread_title":"agent-loop"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	useTestDaemon(t, server)

	code, stdout, stderr := captureOutput(t, func() int {
		return run([]string{"inbox", "--limit", "7", "--json"})
	})
	if code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, stderr)
	}
	recorded := snapshotCLIRequests(&requests, &mu)
	if len(recorded) != 2 {
		t.Fatalf("requests = %d, want health and inbox", len(recorded))
	}
	if got := recorded[1]; got.Method != http.MethodGet || got.Path != "/v1/inbox" || got.RawQuery != "limit=7" {
		t.Fatalf("inbox request = %s %s?%s", got.Method, got.Path, got.RawQuery)
	}
	var got []struct {
		ThreadID    int64  `json:"ThreadID"`
		Seq         int64  `json:"Seq"`
		Name        string `json:"Name"`
		Content     string `json:"Content"`
		ChannelName string `json:"channel_name"`
		ChannelID   int64  `json:"channel_id"`
		ThreadTitle string `json:"thread_title"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ThreadID != 3 || got[0].Seq != 2 || got[0].Name != "alice" || got[0].Content != "portable" || got[0].ChannelName != "dev" || got[0].ChannelID != 4 || got[0].ThreadTitle != "agent-loop" {
		t.Fatalf("inbox = %+v", got)
	}
}

func TestInboxEmptyJSONOutputsArray(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/health" {
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		if r.URL.Path == "/v1/inbox" {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	useTestDaemon(t, server)

	code, stdout, stderr := captureOutput(t, func() int {
		return run([]string{"inbox", "--json"})
	})
	if code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, stderr)
	}
	if stdout != "[]\n" {
		t.Fatalf("stdout = %q, want empty JSON array", stdout)
	}
}

func TestInboxTextIncludesPortableMessageFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/health" {
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		if r.URL.Path == "/v1/inbox" {
			_, _ = w.Write([]byte(`[{"ID":9,"ThreadID":3,"Seq":2,"ParentID":null,"Name":"alice","AuthorType":"human","Role":"user","Content":"portable","CreatedAt":"2026-09-25T01:00:00Z","channel_name":"dev","channel_id":4,"thread_title":"agent-loop"}]`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	useTestDaemon(t, server)

	code, stdout, stderr := captureOutput(t, func() int {
		return run([]string{"inbox"})
	})
	if code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, stderr)
	}
	for _, want := range []string{"dev", "agent-loop", "2", "alice", "portable"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout %q does not contain %q", stdout, want)
		}
	}
}

func TestAgentReadAfterSeqReturnsCompleteReactionSnapshot(t *testing.T) {
	var requests []recordedCLIRequest
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recordCLIRequest(&requests, &mu, r)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/health":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/v1/threads/7/messages":
			if r.URL.RawQuery == "after_seq=1" {
				_, _ = w.Write([]byte(`[
					{"ID":42,"ThreadID":7,"Seq":2,"ParentID":{"Int64":41,"Valid":true},"Name":"bob","AuthorType":"human","Role":"user","Content":"second","CreatedAt":"2026-09-25T00:02:00Z"},
					{"ID":43,"ThreadID":7,"Seq":3,"ParentID":{"Int64":42,"Valid":true},"Name":"carol","AuthorType":"human","Role":"user","Content":"third","CreatedAt":"2026-09-25T00:03:00Z"}
				]`))
				return
			}
			_, _ = w.Write([]byte(`[
				{"ID":41,"ThreadID":7,"Seq":1,"ParentID":null,"Name":"alice","AuthorType":"human","Role":"user","Content":"first","CreatedAt":"2026-09-25T00:01:00Z"},
				{"ID":42,"ThreadID":7,"Seq":2,"ParentID":{"Int64":41,"Valid":true},"Name":"bob","AuthorType":"human","Role":"user","Content":"second","CreatedAt":"2026-09-25T00:02:00Z"},
				{"ID":43,"ThreadID":7,"Seq":3,"ParentID":{"Int64":42,"Valid":true},"Name":"carol","AuthorType":"human","Role":"user","Content":"third","CreatedAt":"2026-09-25T00:03:00Z"}
			]`))
		case "/v1/threads/7/reactions":
			_, _ = w.Write([]byte(`[
				{"ID":1,"MessageID":41,"MessageSeq":1,"Emoji":"late","Name":"alice","AuthorType":"human","CreatedAt":"2026-09-25T00:05:00Z"},
				{"ID":2,"MessageID":42,"MessageSeq":2,"Emoji":"new","Name":"bob","AuthorType":"human","CreatedAt":"2026-09-25T00:02:30Z"},
				{"ID":3,"MessageID":43,"MessageSeq":3,"Emoji":"latest","Name":"carol","AuthorType":"human","CreatedAt":"2026-09-25T00:03:30Z"}
			]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	useTestDaemon(t, server)

	code, stdout, stderr := captureOutput(t, func() int {
		return run([]string{"agent", "read", "--thread", "7", "--after-seq", "1", "--json"})
	})
	if code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, stderr)
	}
	recorded := snapshotCLIRequests(&requests, &mu)
	if len(recorded) != 4 {
		t.Fatalf("requests = %d, want health, cursor messages, mapping messages, and reactions", len(recorded))
	}
	if got := recorded[1]; got.Method != http.MethodGet || got.Path != "/v1/threads/7/messages" || got.RawQuery != "after_seq=1" {
		t.Fatalf("cursor request = %s %s?%s", got.Method, got.Path, got.RawQuery)
	}
	if got := recorded[2]; got.Method != http.MethodGet || got.Path != "/v1/threads/7/messages" || got.RawQuery != "" {
		t.Fatalf("mapping request = %s %s?%s", got.Method, got.Path, got.RawQuery)
	}
	lines, err := jsonl.ParseLines([]byte(stdout))
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 5 {
		t.Fatalf("lines = %d, want 2 cursor messages and the full reaction snapshot: %s", len(lines), stdout)
	}
	if lines[0].Type != "message" || lines[0].Seq != 2 || lines[0].ParentSeq != 1 {
		t.Fatalf("first line = %+v", lines[0])
	}
	if lines[1].Type != "message" || lines[1].Seq != 3 || lines[1].ParentSeq != 2 {
		t.Fatalf("second line = %+v", lines[1])
	}
	if lines[2].Type != "reaction" || lines[2].MessageSeq != 1 || lines[2].Emoji != "late" {
		t.Fatalf("reaction on an older message is missing: %+v", lines[2])
	}
	if lines[3].Type != "reaction" || lines[3].MessageSeq != 2 || lines[3].Emoji != "new" {
		t.Fatalf("fourth line = %+v", lines[3])
	}
	if lines[4].Type != "reaction" || lines[4].MessageSeq != 3 || lines[4].Emoji != "latest" {
		t.Fatalf("fifth line = %+v", lines[4])
	}
}

func TestAgentReadJSONProjectsExactParentAndReactionEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/health":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/v1/threads/7/messages":
			_, _ = w.Write([]byte(`[
				{"ID":41,"ThreadID":7,"Seq":1,"ParentID":null,"Name":"alice","AuthorType":"human","Role":"user","Content":"parent","CreatedAt":"2026-09-25T00:00:00Z"},
				{"ID":42,"ThreadID":7,"Seq":2,"ParentID":{"Int64":41,"Valid":true},"Name":"bob","AuthorType":"human","Role":"user","Content":"reply","CreatedAt":"2026-09-25T00:01:00Z"}
			]`))
		case "/v1/threads/7/reactions":
			_, _ = w.Write([]byte(`[{"ID":1,"MessageID":41,"MessageSeq":1,"Emoji":"+1","Name":"carol","AuthorType":"human","CreatedAt":"2026-09-25T00:02:00Z"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	useTestDaemon(t, server)

	code, stdout, stderr := captureOutput(t, func() int {
		return run([]string{"agent", "read", "--thread", "7", "--json"})
	})
	if code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, stderr)
	}
	const want = "{\"type\":\"message\",\"seq\":1,\"role\":\"user\",\"name\":\"alice\",\"author_type\":\"human\",\"content\":\"parent\",\"timestamp\":\"2026-09-25T00:00:00Z\"}\n" +
		"{\"type\":\"message\",\"seq\":2,\"parent_seq\":1,\"role\":\"user\",\"name\":\"bob\",\"author_type\":\"human\",\"content\":\"reply\",\"timestamp\":\"2026-09-25T00:01:00Z\"}\n" +
		"{\"type\":\"reaction\",\"message_seq\":1,\"name\":\"carol\",\"author_type\":\"human\",\"emoji\":\"+1\",\"timestamp\":\"2026-09-25T00:02:00Z\"}\n"
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
}

func TestSequenceMessageSendReadsStdinAndUsesParentSequence(t *testing.T) {
	var requests []recordedCLIRequest
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recordCLIRequest(&requests, &mu, r)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/health":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/v1/threads/7/messages":
			_, _ = w.Write([]byte(`{"seq":4}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	useTestDaemon(t, server)
	setTestStdin(t, stdinFile(t, "reply from stdin"))

	code, stdout, stderr := captureOutput(t, func() int {
		return run([]string{"message", "send", "--thread", "7", "--reply-to-seq", "3", "--text", "-", "--agent-id", "agent-7", "--as", "pi-agent", "--json"})
	})
	if code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, stderr)
	}
	recorded := snapshotCLIRequests(&requests, &mu)
	if len(recorded) != 2 {
		t.Fatalf("requests = %d, want health and message", len(recorded))
	}
	messageRequest := recorded[1]
	if messageRequest.Method != http.MethodPost || messageRequest.Path != "/v1/threads/7/messages" {
		t.Fatalf("message request = %s %s", messageRequest.Method, messageRequest.Path)
	}
	if got := messageRequest.Header.Get("X-Fluffle-Agent"); got != "agent-7" {
		t.Fatalf("agent header = %q", got)
	}
	var body map[string]any
	if err := json.Unmarshal(messageRequest.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["parent_seq"] != float64(3) || body["Content"] != "reply from stdin" {
		t.Fatalf("message body = %#v", body)
	}
	if _, exists := body["ParentID"]; exists {
		t.Fatalf("sequence request contains database ID: %#v", body)
	}
	var output map[string]any
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatal(err)
	}
	if output["seq"] != float64(4) || output["parent_seq"] != float64(3) {
		t.Fatalf("output = %#v", output)
	}
}

func TestSequenceMessageSendPreservesDatabaseIDTarget(t *testing.T) {
	var requests []recordedCLIRequest
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recordCLIRequest(&requests, &mu, r)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/health" {
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		if r.URL.Path == "/v1/threads/7/messages" {
			_, _ = w.Write([]byte(`{"seq":8}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	useTestDaemon(t, server)

	code, stdout, stderr := captureOutput(t, func() int {
		return run([]string{"message", "send", "--thread", "7", "--reply-to", "9", "--text", "legacy", "--json"})
	})
	if code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, stderr)
	}
	var output map[string]any
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatal(err)
	}
	if output["parent_id"] != float64(9) {
		t.Fatalf("output = %#v", output)
	}
	if _, exists := output["parent_seq"]; exists {
		t.Fatalf("database ID output contains unknown parent sequence: %#v", output)
	}
	recorded := snapshotCLIRequests(&requests, &mu)
	if len(recorded) != 2 {
		t.Fatalf("requests = %d, want health and message", len(recorded))
	}
	var body map[string]any
	if err := json.Unmarshal(recorded[1].Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["ParentID"] != float64(9) {
		t.Fatalf("message body = %#v", body)
	}
	if _, exists := body["parent_seq"]; exists {
		t.Fatalf("database ID request contains sequence: %#v", body)
	}
}

func TestSequenceMessageTargetsAreMutuallyExclusive(t *testing.T) {
	code, output := captureStderr(t, func() int {
		return run([]string{"message", "send", "--thread", "7", "--reply-to", "9", "--reply-to-seq", "3", "--text", "reply"})
	})
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	message := assertCLIErrorEnvelope(t, output, "BAD_ARGS")
	if !strings.Contains(strings.ToLower(message), "mutually exclusive") {
		t.Fatalf("message = %q, want mutually exclusive", message)
	}
}

func TestStdinMessageReadFailurePrecedesEnsureDaemon(t *testing.T) {
	var requests []recordedCLIRequest
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recordCLIRequest(&requests, &mu, r)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/health" {
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"seq":1}`))
	}))
	defer server.Close()
	useTestDaemon(t, server)
	file, err := os.CreateTemp(t.TempDir(), "closed-stdin")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	setTestStdin(t, file)

	code, output := captureStderr(t, func() int {
		return run([]string{"message", "send", "--thread", "7", "--text", "-"})
	})
	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if code == 1 {
		assertCLIErrorEnvelope(t, output, "FILE_READ")
	}
	if recorded := snapshotCLIRequests(&requests, &mu); len(recorded) != 0 {
		t.Fatalf("requests = %d, want none before stdin is read", len(recorded))
	}
}

func TestAgentAppendStdinPostsSingleBatch(t *testing.T) {
	var requests []recordedCLIRequest
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recordCLIRequest(&requests, &mu, r)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/health":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/v1/threads/7/events":
			_, _ = w.Write([]byte(`[{"SourceSeq":0,"Seq":1,"MessageID":11,"ReactionID":0},{"SourceSeq":0,"Seq":0,"MessageID":11,"ReactionID":12}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	useTestDaemon(t, server)
	input := "{\"type\":\"message\",\"seq\":1,\"name\":\"pi-agent\",\"author_type\":\"agent\",\"role\":\"assistant\",\"content\":\"one\",\"timestamp\":\"2026-09-25T00:00:00Z\"}\n" +
		"{\"type\":\"reaction\",\"seq\":2,\"message_seq\":1,\"name\":\"pi-agent\",\"author_type\":\"agent\",\"emoji\":\"+1\",\"timestamp\":\"2026-09-25T00:01:00Z\"}\n"
	setTestStdin(t, stdinFile(t, input))

	code, stdout, stderr := captureOutput(t, func() int {
		return run([]string{"agent", "append", "--thread", "7", "--file", "-", "--agent-id", "pi-agent"})
	})
	if code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	recorded := snapshotCLIRequests(&requests, &mu)
	if len(recorded) != 2 {
		t.Fatalf("requests = %d, want health and one batch", len(recorded))
	}
	batch := recorded[1]
	if batch.Method != http.MethodPost || batch.Path != "/v1/threads/7/events" {
		t.Fatalf("batch request = %s %s", batch.Method, batch.Path)
	}
	if got := batch.Header.Get("X-Fluffle-Agent"); got != "pi-agent" {
		t.Fatalf("agent header = %q", got)
	}
	var body struct {
		Events []jsonl.Line `json:"events"`
		Import bool         `json:"import"`
	}
	if err := json.Unmarshal(batch.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Import || len(body.Events) != 2 || body.Events[0].Content != "one" || body.Events[1].Emoji != "+1" {
		t.Fatalf("batch body = %+v", body)
	}
}

func TestStdinAgentAppendParseFailurePrecedesEnsureDaemon(t *testing.T) {
	var requests []recordedCLIRequest
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recordCLIRequest(&requests, &mu, r)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	useTestDaemon(t, server)
	setTestStdin(t, stdinFile(t, "not-json\n"))

	code, output := captureStderr(t, func() int {
		return run([]string{"agent", "append", "--thread", "7", "--file", "-"})
	})
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	assertCLIErrorEnvelope(t, output, "BAD_JSONL")
	if recorded := snapshotCLIRequests(&requests, &mu); len(recorded) != 0 {
		t.Fatalf("requests = %d, want none before stdin is parsed", len(recorded))
	}
}

func TestAgentThreadImportPostsSingleImportBatch(t *testing.T) {
	var requests []recordedCLIRequest
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recordCLIRequest(&requests, &mu, r)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/health":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/channels":
			_, _ = w.Write([]byte(`[{"ID":5,"Name":"imports","IsOrphaned":true}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/channels/5/threads":
			_, _ = w.Write([]byte(`{"id":9}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/threads/9/events":
			_, _ = w.Write([]byte(`[{"SourceSeq":10,"Seq":1,"MessageID":21,"ReactionID":0},{"SourceSeq":20,"Seq":0,"MessageID":21,"ReactionID":22}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/threads/9/messages":
			_, _ = w.Write([]byte(`{"seq":1}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	useTestDaemon(t, server)
	path := filepath.Join(t.TempDir(), "thread.jsonl")
	input := "{\"type\":\"message\",\"seq\":10,\"name\":\"alice\",\"author_type\":\"human\",\"role\":\"user\",\"content\":\"one\",\"timestamp\":\"2026-09-25T00:00:00Z\"}\n" +
		"{\"type\":\"reaction\",\"seq\":20,\"message_seq\":10,\"name\":\"bob\",\"author_type\":\"human\",\"emoji\":\"+1\",\"timestamp\":\"2026-09-25T00:01:00Z\"}\n"
	if err := os.WriteFile(path, []byte(input), 0o644); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := captureOutput(t, func() int {
		return run([]string{"thread", "import", "--file", path, "--channel", "imports", "--orphaned"})
	})
	if code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, stderr)
	}
	recorded := snapshotCLIRequests(&requests, &mu)
	var batches []recordedCLIRequest
	for _, request := range recorded {
		if request.Method == http.MethodPost && request.Path == "/v1/threads/9/events" {
			batches = append(batches, request)
		}
	}
	if len(batches) != 1 {
		t.Fatalf("event batch requests = %d, want 1; requests = %+v", len(batches), recorded)
	}
	var body struct {
		Events []jsonl.Line `json:"events"`
		Import bool         `json:"import"`
	}
	if err := json.Unmarshal(batches[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if !body.Import || len(body.Events) != 2 || body.Events[0].Seq != 10 || body.Events[1].MessageSeq != 10 {
		t.Fatalf("batch body = %+v", body)
	}
}

func TestAgentThreadImportChannelDefaultsToCwd(t *testing.T) {
	var requests []recordedCLIRequest
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recordCLIRequest(&requests, &mu, r)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/health":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/channels":
			_, _ = w.Write([]byte(`[{"ID":5,"Name":"imports","IsOrphaned":false}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/channels/5/threads":
			_, _ = w.Write([]byte(`{"id":9}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/threads/9/events":
			_, _ = w.Write([]byte(`[{"SourceSeq":10,"Seq":1,"MessageID":21,"ReactionID":0}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	useTestDaemon(t, server)

	path := filepath.Join(t.TempDir(), "thread.jsonl")
	if err := os.WriteFile(path, []byte("{\"type\":\"message\",\"seq\":10,\"name\":\"alice\",\"role\":\"user\",\"content\":\"one\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := captureOutput(t, func() int {
		return run([]string{"thread", "import", "--file", path, "--channel", "imports"})
	})
	if code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, stderr)
	}
	if !strings.Contains(stdout, "thread 9") {
		t.Fatalf("stdout = %q", stdout)
	}
	recorded := snapshotCLIRequestPaths(requests, &mu)
	if !containsRequest(recorded, "/v1/threads/9/events") {
		t.Fatalf("requests = %v, want batch import", recorded)
	}
}

func snapshotCLIRequestPaths(requests []recordedCLIRequest, mu *sync.Mutex) []string {
	mu.Lock()
	defer mu.Unlock()
	paths := make([]string, 0, len(requests))
	for _, request := range requests {
		paths = append(paths, request.Method+" "+request.Path)
	}
	return paths
}

func containsRequest(requests []string, path string) bool {
	for _, request := range requests {
		if strings.HasSuffix(request, " "+path) {
			return true
		}
	}
	return false
}

func TestChannelCreateReportsMutuallyExclusiveRepoFlags(t *testing.T) {
	code, output := captureStderr(t, func() int {
		return channelCreateCmd([]string{"--name", "x", "--repo", t.TempDir(), "--orphaned"})
	})
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	message := assertCLIErrorEnvelope(t, output, "BAD_ARGS")
	if !strings.Contains(strings.ToLower(message), "not both") {
		t.Fatalf("message = %q, want mutual-exclusivity guidance", message)
	}
}

func TestSequenceReactionUsesThreadScopedRoute(t *testing.T) {
	var requests []recordedCLIRequest
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recordCLIRequest(&requests, &mu, r)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/health":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/v1/threads/7/messages/3/reactions":
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	useTestDaemon(t, server)

	code, stdout, stderr := captureOutput(t, func() int {
		return run([]string{"react", "add", "--thread", "7", "--message-seq", "3", "--emoji", "+1", "--agent-id", "agent-7", "--as", "pi-agent"})
	})
	if code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, stderr)
	}
	if stdout != "ok\n" {
		t.Fatalf("stdout = %q, want ok", stdout)
	}
	recorded := snapshotCLIRequests(&requests, &mu)
	if len(recorded) != 2 {
		t.Fatalf("requests = %d, want health and reaction", len(recorded))
	}
	reaction := recorded[1]
	if reaction.Method != http.MethodPost || reaction.Path != "/v1/threads/7/messages/3/reactions" {
		t.Fatalf("reaction request = %s %s", reaction.Method, reaction.Path)
	}
	if got := reaction.Header.Get("X-Fluffle-Agent"); got != "agent-7" {
		t.Fatalf("agent header = %q", got)
	}
	var body map[string]any
	if err := json.Unmarshal(reaction.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["Emoji"] != "+1" || body["Name"] != "pi-agent" {
		t.Fatalf("reaction body = %#v", body)
	}
}

func TestSequenceReactionRequiresThread(t *testing.T) {
	code, output := captureStderr(t, func() int {
		return run([]string{"react", "add", "--message-seq", "3", "--emoji", "+1"})
	})
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	message := assertCLIErrorEnvelope(t, output, "BAD_ARGS")
	if !strings.Contains(message, "--thread") {
		t.Fatalf("message = %q, want --thread requirement", message)
	}
}

func TestSequenceReactionTargetsAreMutuallyExclusive(t *testing.T) {
	code, output := captureStderr(t, func() int {
		return run([]string{"react", "add", "--thread", "7", "--message", "9", "--message-seq", "3", "--emoji", "+1"})
	})
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	message := assertCLIErrorEnvelope(t, output, "BAD_ARGS")
	if !strings.Contains(strings.ToLower(message), "mutually exclusive") {
		t.Fatalf("message = %q, want mutually exclusive", message)
	}
}

func TestSequenceReactionPreservesDatabaseIDRoute(t *testing.T) {
	var requests []recordedCLIRequest
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recordCLIRequest(&requests, &mu, r)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/health" {
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		if r.URL.Path == "/v1/messages/9/reactions" {
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	useTestDaemon(t, server)

	code, _, stderr := captureOutput(t, func() int {
		return run([]string{"react", "add", "--message", "9", "--emoji", "+1"})
	})
	if code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, stderr)
	}
	recorded := snapshotCLIRequests(&requests, &mu)
	if len(recorded) != 2 || recorded[1].Path != "/v1/messages/9/reactions" {
		t.Fatalf("requests = %+v", recorded)
	}
}

func TestAgentReadRejectsExplicitNegativeAfterSeq(t *testing.T) {
	var requests []recordedCLIRequest
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recordCLIRequest(&requests, &mu, r)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/health" {
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()
	useTestDaemon(t, server)

	code, output := captureStderr(t, func() int {
		return run([]string{"agent", "read", "--thread", "7", "--after-seq", "-1"})
	})
	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if code == 1 {
		message := assertCLIErrorEnvelope(t, output, "BAD_ARGS")
		if !strings.Contains(message, "after-seq") {
			t.Errorf("message = %q, want after-seq validation", message)
		}
	}
	if recorded := snapshotCLIRequests(&requests, &mu); len(recorded) != 0 {
		t.Fatalf("requests = %d, want none", len(recorded))
	}
}

func TestSequenceMessageSendRejectsExplicitNegativeReplySeq(t *testing.T) {
	var requests []recordedCLIRequest
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recordCLIRequest(&requests, &mu, r)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/health" {
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"seq":1}`))
	}))
	defer server.Close()
	useTestDaemon(t, server)

	code, output := captureStderr(t, func() int {
		return run([]string{"message", "send", "--thread", "7", "--reply-to-seq", "-1", "--text", "legacy"})
	})
	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if code == 1 {
		message := assertCLIErrorEnvelope(t, output, "BAD_ARGS")
		if !strings.Contains(message, "positive integer") {
			t.Errorf("message = %q, want positive integer validation", message)
		}
	}
	if recorded := snapshotCLIRequests(&requests, &mu); len(recorded) != 0 {
		t.Fatalf("requests = %d, want none", len(recorded))
	}
}

func TestSequenceReactionRejectsExplicitNegativeMessageSeq(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{
			name: "sequence only",
			args: []string{"react", "add", "--thread", "7", "--message-seq", "-1", "--emoji", "+1"},
		},
		{
			name: "mixed with database ID",
			args: []string{"react", "add", "--thread", "7", "--message", "9", "--message-seq", "-1", "--emoji", "+1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requests []recordedCLIRequest
			var mu sync.Mutex
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				recordCLIRequest(&requests, &mu, r)
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/v1/health" {
					_, _ = w.Write([]byte(`{"ok":true}`))
					return
				}
				_, _ = w.Write([]byte(`{"ok":true}`))
			}))
			defer server.Close()
			useTestDaemon(t, server)

			code, output := captureStderr(t, func() int {
				return run(tt.args)
			})
			if code != 1 {
				t.Errorf("exit = %d, want 1", code)
			}
			if code == 1 {
				message := assertCLIErrorEnvelope(t, output, "BAD_ARGS")
				if !strings.Contains(message, "positive integer") {
					t.Errorf("message = %q, want positive integer validation", message)
				}
			}
			if recorded := snapshotCLIRequests(&requests, &mu); len(recorded) != 0 {
				t.Fatalf("requests = %d, want none", len(recorded))
			}
		})
	}
}

func TestAgentAppendRejectsInvalidBatchSuccessResponses(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "null", body: `null`},
		{name: "object", body: `{}`},
		{name: "null element", body: `[null]`},
		{name: "empty object element", body: `[{}]`},
		{name: "scalar element", body: `[1]`},
		{name: "missing result field", body: `[{"SourceSeq":0,"Seq":1,"MessageID":9}]`},
		{name: "wrong result field type", body: `[{"SourceSeq":"zero","Seq":1,"MessageID":9,"ReactionID":0}]`},
		{name: "trailing object", body: `[] {}`},
		{name: "trailing null", body: `[] null`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/threads/7/events" {
					t.Errorf("request = %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			code, output := captureStderr(t, func() int {
				return postJSONLLines(server.URL, 7, []jsonl.Line{{Type: "message", Name: "alice", Role: "user", Content: "hello"}}, "")
			})
			if code != 2 {
				t.Errorf("exit = %d, want 2", code)
			}
			if code == 2 {
				assertCLIErrorEnvelope(t, output, "DELIVERY_UNKNOWN")
			}
		})
	}
}

func TestAgentAppendAcceptsCompleteBatchResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"SourceSeq":0,"Seq":1,"MessageID":9,"ReactionID":0}]`))
	}))
	defer server.Close()

	code, output := captureStderr(t, func() int {
		return postJSONLLines(server.URL, 7, []jsonl.Line{{Type: "message", Name: "alice", Role: "user", Content: "hello"}}, "")
	})
	if code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, output)
	}
}

func TestAgentAppendRejectsBatchResultCountMismatch(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		lineCount int
	}{
		{
			name:      "empty result array",
			body:      `[]`,
			lineCount: 1,
		},
		{
			name:      "short result array",
			body:      `[{"SourceSeq":0,"Seq":1,"MessageID":9,"ReactionID":0}]`,
			lineCount: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			lines := make([]jsonl.Line, tt.lineCount)
			for i := range lines {
				lines[i] = jsonl.Line{Type: "message", Name: "alice", Role: "user", Content: "hello"}
			}
			code, output := captureStderr(t, func() int {
				return postJSONLLines(server.URL, 7, lines, "")
			})
			if code != 2 {
				t.Fatalf("exit = %d, want 2", code)
			}
			assertCLIErrorEnvelope(t, output, "DELIVERY_UNKNOWN")
		})
	}
}

func TestSequenceExplicitZeroLegacyIDsAreMutuallyExclusive(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{
			name: "message reply",
			args: []string{"message", "send", "--thread", "7", "--reply-to", "0", "--reply-to-seq", "2", "--text", "reply"},
		},
		{
			name: "reaction target",
			args: []string{"react", "add", "--thread", "7", "--message", "0", "--message-seq", "2", "--emoji", "+1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requests []recordedCLIRequest
			var mu sync.Mutex
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				recordCLIRequest(&requests, &mu, r)
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/v1/health" {
					_, _ = w.Write([]byte(`{"ok":true}`))
					return
				}
				_, _ = w.Write([]byte(`{"seq":3,"ok":true}`))
			}))
			defer server.Close()
			useTestDaemon(t, server)

			code, output := captureStderr(t, func() int {
				return run(tt.args)
			})
			if code != 1 {
				t.Fatalf("exit = %d, want 1", code)
			}
			message := assertCLIErrorEnvelope(t, output, "BAD_ARGS")
			if !strings.Contains(strings.ToLower(message), "mutually exclusive") {
				t.Fatalf("message = %q, want mutually exclusive", message)
			}
			if recorded := snapshotCLIRequests(&requests, &mu); len(recorded) != 0 {
				t.Fatalf("requests = %d, want none", len(recorded))
			}
		})
	}
}

func TestCLIReadResponsesAreSemanticallyValidated(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		responses map[string]string
	}{
		{
			name: "inbox null",
			args: []string{"inbox", "--json"},
			responses: map[string]string{
				"/v1/inbox": `null`,
			},
		},
		{
			name: "inbox object instead of array",
			args: []string{"inbox", "--json"},
			responses: map[string]string{
				"/v1/inbox": `{"items":[]}`,
			},
		},
		{
			name: "inbox trailing json",
			args: []string{"inbox", "--json"},
			responses: map[string]string{
				"/v1/inbox": `[] {}`,
			},
		},
		{
			name: "inbox zero id",
			args: []string{"inbox", "--json"},
			responses: map[string]string{
				"/v1/inbox": `[{"ID":0,"ThreadID":3,"Seq":1,"Name":"","AuthorType":"human","Role":"user","Content":"x","channel_name":"dev","channel_id":4,"thread_title":"t"}]`,
			},
		},
		{
			name: "inbox missing content",
			args: []string{"inbox", "--json"},
			responses: map[string]string{
				"/v1/inbox": `[{"ID":9,"ThreadID":3,"Seq":1,"Name":"alice","AuthorType":"human","Role":"user","channel_name":"dev","channel_id":4,"thread_title":"t"}]`,
			},
		},
		{
			name: "channel list null",
			args: []string{"channel", "list", "--json"},
			responses: map[string]string{
				"/v1/channels": `null`,
			},
		},
		{
			name: "channel list zero id",
			args: []string{"channel", "list", "--json"},
			responses: map[string]string{
				"/v1/channels": `[{"ID":0,"Name":"broken"}]`,
			},
		},
		{
			name: "channel list blank name",
			args: []string{"channel", "list", "--json"},
			responses: map[string]string{
				"/v1/channels": `[{"ID":4,"Name":"  "}]`,
			},
		},
		{
			name: "thread list zero channel",
			args: []string{"thread", "list", "--channel", "dev", "--orphaned", "--json"},
			responses: map[string]string{
				"/v1/channels":           `[{"ID":5,"Name":"dev","IsOrphaned":true}]`,
				"/v1/channels/5/threads": `[{"ID":0,"ChannelID":5,"Title":"broken"}]`,
			},
		},
		{
			name: "message read zero sequence",
			args: []string{"agent", "read", "--thread", "7", "--json"},
			responses: map[string]string{
				"/v1/threads/7/messages":  `[{"ID":0,"ThreadID":7,"Seq":1,"Name":"alice","AuthorType":"human","Role":"user","Content":"x"}]`,
				"/v1/threads/7/reactions": `[]`,
			},
		},
		{
			name: "message read null",
			args: []string{"agent", "read", "--thread", "7", "--json"},
			responses: map[string]string{
				"/v1/threads/7/messages":  `null`,
				"/v1/threads/7/reactions": `[]`,
			},
		},
		{
			name: "reaction read null",
			args: []string{"agent", "read", "--thread", "7", "--json"},
			responses: map[string]string{
				"/v1/threads/7/messages":  `[]`,
				"/v1/threads/7/reactions": `null`,
			},
		},
		{
			name: "reaction read missing target",
			args: []string{"agent", "read", "--thread", "7", "--json"},
			responses: map[string]string{
				"/v1/threads/7/messages":  `[]`,
				"/v1/threads/7/reactions": `[{"ID":1,"MessageID":0,"MessageSeq":0,"Emoji":"+1","Name":"bob","AuthorType":"human"}]`,
			},
		},
		{
			name: "message read trailing json",
			args: []string{"agent", "read", "--thread", "7"},
			responses: map[string]string{
				"/v1/threads/7/messages": `[] []`,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newResponseServer(t, tt.responses)
			defer server.Close()
			useTestDaemon(t, server)

			code, stdout, stderr := captureOutput(t, func() int {
				return run(tt.args)
			})
			if code != 2 {
				t.Fatalf("exit = %d, want 2; stdout=%q stderr=%q", code, stdout, stderr)
			}
			assertCLIErrorEnvelope(t, stderr, "DAEMON_ERROR")
			if stdout != "" {
				t.Fatalf("stdout = %q, want empty", stdout)
			}
		})
	}
}

func TestCLIMutationResponsesAreSemanticallyValidated(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		responses map[string]string
	}{
		{
			name: "channel create missing id",
			args: []string{"channel", "create", "--name", "test", "--orphaned"},
			responses: map[string]string{
				"/v1/channels": `{}`,
			},
		},
		{
			name: "channel create zero id",
			args: []string{"channel", "create", "--name", "test", "--orphaned"},
			responses: map[string]string{
				"/v1/channels": `{"id":0}`,
			},
		},
		{
			name: "thread create zero id",
			args: []string{"thread", "new", "--channel", "dev", "--orphaned", "--title", "t"},
			responses: map[string]string{
				"/v1/channels":           `[{"ID":5,"Name":"dev","IsOrphaned":true}]`,
				"/v1/channels/5/threads": `{"id":0}`,
			},
		},
		{
			name: "message send zero sequence",
			args: []string{"message", "send", "--thread", "7", "--text", "hello"},
			responses: map[string]string{
				"/v1/threads/7/messages": `{"seq":0}`,
			},
		},
		{
			name: "message send null",
			args: []string{"message", "send", "--thread", "7", "--text", "hello"},
			responses: map[string]string{
				"/v1/threads/7/messages": `null`,
			},
		},
		{
			name: "legacy reaction not acknowledged",
			args: []string{"react", "add", "--message", "9", "--emoji", "+1"},
			responses: map[string]string{
				"/v1/messages/9/reactions": `{}`,
			},
		},
		{
			name: "sequence reaction rejected ack",
			args: []string{"react", "add", "--thread", "7", "--message-seq", "3", "--emoji", "+1"},
			responses: map[string]string{
				"/v1/threads/7/messages/3/reactions": `{"ok":false}`,
			},
		},
		{
			name: "batch message without assigned sequence",
			args: []string{"agent", "append", "--thread", "7", "--file", "-"},
			responses: map[string]string{
				"/v1/threads/7/events": `[{"SourceSeq":0,"Seq":0,"MessageID":9,"ReactionID":0},{"SourceSeq":0,"Seq":0,"MessageID":9,"ReactionID":12}]`,
			},
		},
		{
			name: "batch reaction without reaction id",
			args: []string{"agent", "append", "--thread", "7", "--file", "-"},
			responses: map[string]string{
				"/v1/threads/7/events": `[{"SourceSeq":0,"Seq":3,"MessageID":11,"ReactionID":0},{"SourceSeq":0,"Seq":0,"MessageID":11,"ReactionID":0}]`,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newResponseServer(t, tt.responses)
			defer server.Close()
			useTestDaemon(t, server)
			input := "{\"type\":\"message\",\"name\":\"alice\",\"role\":\"user\",\"content\":\"one\"}\n{\"type\":\"reaction\",\"message_seq\":1,\"name\":\"bob\",\"emoji\":\"+1\"}\n"
			setTestStdin(t, stdinFile(t, input))

			code, stdout, stderr := captureOutput(t, func() int {
				return run(tt.args)
			})
			if code != 2 {
				t.Fatalf("exit = %d, want 2; stdout=%q stderr=%q", code, stdout, stderr)
			}
			assertCLIErrorEnvelope(t, stderr, "DELIVERY_UNKNOWN")
			if stdout != "" {
				t.Fatalf("stdout = %q, want no false success", stdout)
			}
		})
	}
}

func TestCLIAcceptsWellFormedMutationResponses(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		stdin     string
		responses map[string]string
	}{
		{
			name: "channel create",
			args: []string{"channel", "create", "--name", "ok", "--orphaned"},
			responses: map[string]string{
				"/v1/channels": `{"id":5}`,
			},
		},
		{
			name: "thread new",
			args: []string{"thread", "new", "--channel", "ok", "--orphaned", "--title", "t"},
			responses: map[string]string{
				"/v1/channels":           `[{"ID":5,"Name":"ok","IsOrphaned":true}]`,
				"/v1/channels/5/threads": `{"id":9}`,
			},
		},
		{
			name: "message send",
			args: []string{"message", "send", "--thread", "9", "--text", "hello"},
			responses: map[string]string{
				"/v1/threads/9/messages": `{"seq":2}`,
			},
		},
		{
			name: "react add",
			args: []string{"react", "add", "--thread", "9", "--message-seq", "2", "--emoji", "+1"},
			responses: map[string]string{
				"/v1/threads/9/messages/2/reactions": `{"ok":true}`,
			},
		},
		{
			name:  "agent append",
			args:  []string{"agent", "append", "--thread", "9", "--file", "-"},
			stdin: "{\"type\":\"message\",\"name\":\"alice\",\"role\":\"user\",\"content\":\"one\"}\n{\"type\":\"reaction\",\"message_seq\":1,\"name\":\"bob\",\"emoji\":\"+1\"}\n",
			responses: map[string]string{
				"/v1/threads/9/events": `[{"SourceSeq":0,"Seq":3,"MessageID":11,"ReactionID":0},{"SourceSeq":0,"Seq":0,"MessageID":11,"ReactionID":12}]`,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newResponseServer(t, tt.responses)
			defer server.Close()
			useTestDaemon(t, server)
			if tt.stdin != "" {
				setTestStdin(t, stdinFile(t, tt.stdin))
			}

			code, stdout, stderr := captureOutput(t, func() int {
				return run(tt.args)
			})
			if code != 0 {
				t.Fatalf("exit = %d, want 0: %s", code, stderr)
			}
			if tt.name == "agent append" && stdout != "" {
				t.Fatalf("stdout = %q, want empty", stdout)
			}
		})
	}
}

func newResponseServer(t *testing.T, responses map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/health" {
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		body, ok := responses[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"CHANNEL_NOT_FOUND","message":"unexpected path"}`))
			return
		}
		_, _ = w.Write([]byte(body))
	}))
}

func TestAgentReadTreatsExplicitZeroLastAsPresent(t *testing.T) {
	for _, args := range [][]string{
		{"agent", "read", "--thread", "7", "--after-seq", "1", "--last", "0"},
		{"agent", "read", "--thread", "7", "--after-seq", "0", "--last", "0"},
	} {
		t.Run(strings.Join(args[2:], " "), func(t *testing.T) {
			var requests []recordedCLIRequest
			var mu sync.Mutex
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				recordCLIRequest(&requests, &mu, r)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"ok":true}`))
			}))
			defer server.Close()
			useTestDaemon(t, server)

			code, output := captureStderr(t, func() int {
				return run(args)
			})
			if code != 1 {
				t.Fatalf("exit = %d, want 1", code)
			}
			message := assertCLIErrorEnvelope(t, output, "BAD_ARGS")
			if !strings.Contains(strings.ToLower(message), "mutually exclusive") {
				t.Fatalf("message = %q, want mutually exclusive", message)
			}
			if recorded := snapshotCLIRequests(&requests, &mu); len(recorded) != 0 {
				t.Fatalf("requests = %d, want none", len(recorded))
			}
		})
	}
}

func TestAgentReadAllowsExplicitZeroLastWithoutCursor(t *testing.T) {
	var requests []recordedCLIRequest
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recordCLIRequest(&requests, &mu, r)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/health":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/v1/threads/7/messages":
			_, _ = w.Write([]byte(`[]`))
		case "/v1/threads/7/reactions":
			_, _ = w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	useTestDaemon(t, server)

	code, stdout, stderr := captureOutput(t, func() int {
		return run([]string{"agent", "read", "--thread", "7", "--last", "0", "--json"})
	})
	if code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
}

// awaitedChildPID stands in for the pid of the child daemon spawned by
// daemonStartBackground, so the awaitDaemonPort tests can pin that only the
// runtime file written by that child is accepted.
const awaitedChildPID = 4242

func TestAwaitDaemonPortReportsChildExitAndTimeout(t *testing.T) {
	t.Run("child exit", func(t *testing.T) {
		exited := make(chan error, 1)
		exited <- errors.New("exit status 2")
		home := t.TempDir()
		code, output := captureStderr(t, func() int {
			_, err := awaitDaemonPort(home, 20, time.Millisecond, exited, awaitedChildPID)
			if err == nil {
				t.Fatal("expected readiness error")
			}
			return fail("DAEMON_ERROR", err.Error())
		})
		if code != 2 {
			t.Fatalf("exit = %d, want 2", code)
		}
		message := assertCLIErrorEnvelope(t, output, "DAEMON_ERROR")
		if !strings.Contains(message, "exit status 2") {
			t.Fatalf("message = %q, want the child failure reason", message)
		}
	})

	t.Run("readiness timeout", func(t *testing.T) {
		home := t.TempDir()
		code, output := captureStderr(t, func() int {
			_, err := awaitDaemonPort(home, 3, time.Millisecond, make(chan error), awaitedChildPID)
			if err == nil {
				t.Fatal("expected readiness timeout")
			}
			return fail("DAEMON_ERROR", err.Error())
		})
		if code != 2 {
			t.Fatalf("exit = %d, want 2", code)
		}
		message := assertCLIErrorEnvelope(t, output, "DAEMON_ERROR")
		if !strings.Contains(message, "ready") {
			t.Fatalf("message = %q, want readiness wording", message)
		}
	})

	t.Run("invalid runtime file", func(t *testing.T) {
		home := t.TempDir()
		if err := os.WriteFile(filepath.Join(home, "daemon.json"), []byte(`{"port":0,"pid":4242}`), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := awaitDaemonPort(home, 3, time.Millisecond, make(chan error), awaitedChildPID)
		if err == nil {
			t.Fatal("expected error for a runtime file without a usable port")
		}
	})

	t.Run("runtime file from another process", func(t *testing.T) {
		home := t.TempDir()
		if err := os.WriteFile(filepath.Join(home, "daemon.json"), []byte(`{"port":1234,"pid":999999}`), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := awaitDaemonPort(home, 3, time.Millisecond, make(chan error), awaitedChildPID)
		if err == nil {
			t.Fatal("expected error for a runtime file this child did not write")
		}
	})

	t.Run("ready port", func(t *testing.T) {
		home := t.TempDir()
		if err := os.WriteFile(filepath.Join(home, "daemon.json"), []byte(`{"port":1234,"pid":4242}`), 0o644); err != nil {
			t.Fatal(err)
		}
		port, err := awaitDaemonPort(home, 3, time.Millisecond, make(chan error), awaitedChildPID)
		if err != nil {
			t.Fatal(err)
		}
		if port != 1234 {
			t.Fatalf("port = %d, want 1234", port)
		}
	})
}

func TestDaemonStartReconcilesStaleSessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("FLUFFLE_HOME", home)
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(home, "fluffle.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	chID, _ := s.CreateChannel("c", "/repo", "", "", "", false)
	thID, _ := s.CreateThread(chID, "t")
	seq, _ := s.AppendMessage(thID, "alice", "human", "user", "@probe hi")
	msgID, _ := s.MessageIDBySeq(thID, seq)
	running, _ := s.CreateSession(thID, msgID, "probe", store.SessionRunning, "stdout", "c", nil)
	queued, _ := s.CreateSession(thID, msgID, "other", store.SessionQueued, "stdout", "c", nil)
	done, _ := s.CreateSession(thID, msgID, "done", store.SessionSucceeded, "stdout", "c", nil)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	agents := agentcfg.NewLoader(filepath.Join(home, "config.toml"))
	m := session.NewManager(reopened, agents, runner.NewExec())
	defer m.Shutdown()
	if err := m.Reconcile(); err != nil {
		t.Fatal(err)
	}

	for _, id := range []int64{running, queued} {
		got, err := reopened.GetSession(id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != store.SessionCanceled {
			t.Fatalf("session %d status = %q, want canceled", id, got.Status)
		}
		if got.FinishedAt == nil {
			t.Fatalf("session %d has no finished_at", id)
		}
		if got.Error == nil || !strings.Contains(*got.Error, "daemon restarted") {
			t.Fatalf("session %d error = %v", id, got.Error)
		}
	}
	untouched, _ := reopened.GetSession(done)
	if untouched.Status != store.SessionSucceeded {
		t.Fatalf("terminal session was modified: %q", untouched.Status)
	}
}

// TestDaemonStopSleepsUntilKilled is the child process used by the daemonStop
// tests. It only does anything when re-executed with FLUFFLE_STOP_HELPER=1.
func TestDaemonStopSleepsUntilKilled(t *testing.T) {
	if os.Getenv("FLUFFLE_STOP_HELPER") != "1" {
		t.Skip("helper process; only runs when re-executed by the daemonStop tests")
	}
	time.Sleep(2 * time.Minute)
}

// startFakeDaemon starts a real child process that stands in for a running
// daemon, so the stop tests can assert on genuine process death rather than on
// a mock. waitDead blocks until the child is killed and reaped, which is what
// makes the assertions race-free: a killed-but-unreaped child still answers
// signal 0 on Unix, so the tests must wait for the reap, not sample it.
func startFakeDaemon(t *testing.T) (pid int, waitDead func() bool) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestDaemonStopSleepsUntilKilled", "-test.timeout=3m")
	cmd.Env = append(os.Environ(), "FLUFFLE_STOP_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	reaped := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(reaped)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		select {
		case <-reaped:
		case <-time.After(10 * time.Second):
			t.Error("fake daemon was not reaped")
		}
	})
	return cmd.Process.Pid, func() bool {
		select {
		case <-reaped:
			return true
		case <-time.After(10 * time.Second):
			return false
		}
	}
}

func writeDaemonRuntimeFile(t *testing.T, home string, port, pid int) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"port": port, "pid": pid})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "daemon.json"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
}

func serverPort(t *testing.T, server *httptest.Server) int {
	t.Helper()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func TestDaemonStopRequestsGracefulShutdownBeforeKilling(t *testing.T) {
	home := t.TempDir()
	t.Setenv("FLUFFLE_HOME", home)
	pid, waitDead := startFakeDaemon(t)

	var mu sync.Mutex
	var seen []recordedCLIRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, recordedCLIRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Header: r.Header.Clone(),
		})
		mu.Unlock()
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
			t.Errorf("killing the fake daemon: %v", err)
		}
		waitDead()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	writeDaemonRuntimeFile(t, home, serverPort(t, server), pid)

	code, stdout, stderr := captureOutput(t, daemonStop)

	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, stderr)
	}
	if !strings.Contains(stdout, "daemon stopped") {
		t.Fatalf("stdout = %q, want the success message", stdout)
	}

	mu.Lock()
	got := append([]recordedCLIRequest(nil), seen...)
	mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("daemon requests = %d, want exactly the graceful shutdown POST", len(got))
	}
	if got[0].Method != http.MethodPost || got[0].Path != "/api/shutdown" {
		t.Fatalf("request = %s %s, want POST /api/shutdown", got[0].Method, got[0].Path)
	}
	if agent := got[0].Header.Get("X-Fluffle-Agent"); agent != "" {
		t.Fatalf("X-Fluffle-Agent = %q, want the header absent: a human stop must not be spoofed as an agent", agent)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want a clean graceful stop with no forced-kill notice", stderr)
	}
	if !waitDead() {
		t.Fatal("fake daemon survived the graceful stop")
	}
	if _, err := os.Stat(filepath.Join(home, "daemon.json")); !os.IsNotExist(err) {
		t.Fatalf("daemon.json still present after a clean stop: %v", err)
	}
}

func TestDaemonStopFallsBackToKillWhenGracefulShutdownFails(t *testing.T) {
	t.Run("nothing listening on the recorded port", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("FLUFFLE_HOME", home)
		pid, waitDead := startFakeDaemon(t)

		dead, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		closedPort := dead.Addr().(*net.TCPAddr).Port
		if err := dead.Close(); err != nil {
			t.Fatal(err)
		}
		writeDaemonRuntimeFile(t, home, closedPort, pid)

		code, stdout, stderr := captureOutput(t, daemonStop)
		if code != 0 {
			t.Fatalf("exit = %d, want 0: an unreachable graceful path must still stop the daemon", code)
		}
		if !strings.Contains(stdout, "daemon stopped") {
			t.Fatalf("stdout = %q, want the success message", stdout)
		}
		if !strings.Contains(stderr, "did not shut down gracefully") {
			t.Fatalf("stderr = %q, want the forced-kill notice so the stop is not reported as clean", stderr)
		}
		if !waitDead() {
			t.Fatal("fake daemon survived the kill fallback")
		}
	})

	t.Run("listener accepts but never answers", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("FLUFFLE_HOME", home)
		pid, waitDead := startFakeDaemon(t)

		release := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			select {
			case <-release:
			case <-time.After(30 * time.Second):
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer func() {
			close(release)
			server.Close()
		}()
		writeDaemonRuntimeFile(t, home, serverPort(t, server), pid)

		done := make(chan int, 1)
		go func() { done <- daemonStop() }()
		var code int
		select {
		case code = <-done:
		case <-time.After(30 * time.Second):
			t.Fatal("daemonStop hung on a non-responsive graceful endpoint")
		}
		if code != 0 {
			t.Fatalf("exit = %d, want 0", code)
		}
		if !waitDead() {
			t.Fatal("fake daemon survived the kill fallback")
		}
	})
}

func TestDaemonStopReportsDaemonDownForUnusableRuntimeFile(t *testing.T) {
	t.Run("missing daemon.json", func(t *testing.T) {
		t.Setenv("FLUFFLE_HOME", t.TempDir())
		code, stderr := captureStderr(t, daemonStop)
		if code != 2 {
			t.Fatalf("exit = %d, want 2", code)
		}
		assertCLIErrorEnvelope(t, stderr, "DAEMON_DOWN")
	})

	t.Run("malformed daemon.json", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("FLUFFLE_HOME", home)
		if err := os.WriteFile(filepath.Join(home, "daemon.json"), []byte("not json"), 0o644); err != nil {
			t.Fatal(err)
		}
		code, stderr := captureStderr(t, daemonStop)
		if code != 2 {
			t.Fatalf("exit = %d, want 2", code)
		}
		assertCLIErrorEnvelope(t, stderr, "DAEMON_DOWN")
	})

	t.Run("daemon.json without a usable pid", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("FLUFFLE_HOME", home)
		writeDaemonRuntimeFile(t, home, 65000, 0)
		code, stderr := captureStderr(t, daemonStop)
		if code != 2 {
			t.Fatalf("exit = %d, want 2: pid 0 would signal the caller's own process group", code)
		}
		assertCLIErrorEnvelope(t, stderr, "DAEMON_DOWN")
	})

	t.Run("stale daemon.json whose process is already gone", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("FLUFFLE_HOME", home)
		pid, waitDead := startFakeDaemon(t)
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
		if !waitDead() {
			t.Fatal("could not stage a dead pid")
		}
		dead, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		closedPort := dead.Addr().(*net.TCPAddr).Port
		if err := dead.Close(); err != nil {
			t.Fatal(err)
		}
		writeDaemonRuntimeFile(t, home, closedPort, pid)

		code, stdout, stderr := captureOutput(t, daemonStop)
		if code != 2 {
			t.Fatalf("exit = %d, want 2", code)
		}
		if stdout != "" {
			t.Fatalf("stdout = %q, want nothing on a failed stop", stdout)
		}
		// assertCLIErrorEnvelope decodes the whole stream, so it also proves
		// stderr carries exactly one JSON document and no bare notice above it.
		message := assertCLIErrorEnvelope(t, stderr, "DAEMON_DOWN")
		if !strings.Contains(message, "did not shut down gracefully") {
			t.Fatalf("message = %q, want the forced-kill reason inside the envelope", message)
		}
		if !strings.Contains(message, "kill failed") {
			t.Fatalf("message = %q, want the kill failure folded into the envelope", message)
		}
	})
}

// TestDaemonStopWaitsForTheProcessBeforeForcingAKill covers the wait itself. The
// other stop tests either reap the child inside the handler, so the wait
// succeeds on its first probe, or never reach it because the graceful POST
// failed. Only a daemon that answers 200 and then refuses to die exercises the
// loop, and only a non-200 response distinguishes a real graceful stop from a
// rejected one.
func TestDaemonStopWaitsForTheProcessBeforeForcingAKill(t *testing.T) {
	t.Run("graceful 200 but the process never exits", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("FLUFFLE_HOME", home)
		pid, waitDead := startFakeDaemon(t)

		var mu sync.Mutex
		requests := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			mu.Lock()
			requests++
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()
		writeDaemonRuntimeFile(t, home, serverPort(t, server), pid)

		const exitWait = 400 * time.Millisecond
		start := time.Now()
		code, stdout, stderr := captureOutput(t, func() int {
			return daemonStopWithin(exitWait)
		})
		elapsed := time.Since(start)

		if code != 0 {
			t.Fatalf("exit = %d, want 0", code)
		}
		if !strings.Contains(stdout, "daemon stopped") {
			t.Fatalf("stdout = %q, want the success message", stdout)
		}
		if !strings.Contains(stderr, "did not shut down gracefully") {
			t.Fatalf("stderr = %q, want the forced-kill notice after the wait ran out", stderr)
		}
		if elapsed < exitWait/2 {
			t.Fatalf("elapsed = %v, want at least %v: the stop must wait for the daemon to exit", elapsed, exitWait/2)
		}
		if elapsed > 10*time.Second {
			t.Fatalf("elapsed = %v, want the wait to stay bounded", elapsed)
		}
		if !waitDead() {
			t.Fatal("fake daemon survived the forced kill")
		}
		mu.Lock()
		got := requests
		mu.Unlock()
		if got != 1 {
			t.Fatalf("daemon requests = %d, want exactly one graceful POST", got)
		}
	})

	t.Run("non-200 response is not treated as a graceful stop", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("FLUFFLE_HOME", home)
		pid, waitDead := startFakeDaemon(t)

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"code":"AGENT_FORBIDDEN","message":"agents cannot stop the daemon"}`))
		}))
		defer server.Close()
		writeDaemonRuntimeFile(t, home, serverPort(t, server), pid)

		// A long exit wait: a rejected request must not be waited out, because
		// only a 200 means the daemon actually started shutting down.
		const exitWait = 8 * time.Second
		start := time.Now()
		code, stdout, stderr := captureOutput(t, func() int {
			return daemonStopWithin(exitWait)
		})
		elapsed := time.Since(start)

		if code != 0 {
			t.Fatalf("exit = %d, want 0", code)
		}
		if !strings.Contains(stdout, "daemon stopped") {
			t.Fatalf("stdout = %q, want the success message", stdout)
		}
		if !strings.Contains(stderr, "did not shut down gracefully") {
			t.Fatalf("stderr = %q, want the forced-kill notice for a rejected request", stderr)
		}
		if elapsed >= exitWait/2 {
			t.Fatalf("elapsed = %v, want no wait after a 403: the daemon never began shutting down", elapsed)
		}
		if !waitDead() {
			t.Fatal("fake daemon survived the forced kill")
		}
	})
}

func TestAgentListEncodesRepoAndNeverDefaultsToCwd(t *testing.T) {
	var requests []recordedCLIRequest
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recordCLIRequest(&requests, &mu, r)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/health":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/v1/agents":
			if r.URL.Query().Get("repo") == "" {
				_, _ = w.Write([]byte(`{"agents":[{"name":"shared","description":"","command":"/bin/shared","reply":"cli","source":"/home/test/config.toml"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"agents":[{"name":"local","description":"repo agent","command":"/bin/local","reply":"auto","source":"/repo/.flf.toml"},{"name":"shared","description":"","command":"/bin/shared","reply":"cli","source":"/home/test/config.toml"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	useTestDaemon(t, server)

	code, stdout, stderr := captureOutput(t, func() int {
		return run([]string{"agent", "list", "--json"})
	})
	if code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, stderr)
	}
	var payload struct {
		Agents []struct {
			Name string `json:"name"`
		} `json:"agents"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("agent list JSON: %v\n%s", err, stdout)
	}
	if len(payload.Agents) != 1 || payload.Agents[0].Name != "shared" {
		t.Fatalf("agents = %+v, want the global config only", payload.Agents)
	}

	relative := filepath.Join("nested repo", "..", "repo with space")
	abs, err := filepath.Abs(relative)
	if err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = captureOutput(t, func() int {
		return run([]string{"agent", "list", "--repo", relative, "--json"})
	})
	if code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, stderr)
	}
	if !strings.Contains(stdout, `"name": "local"`) || !strings.Contains(stdout, `"name": "shared"`) {
		t.Fatalf("stdout = %q, want the repo and global entries", stdout)
	}

	recorded := snapshotCLIRequests(&requests, &mu)
	if len(recorded) != 4 {
		t.Fatalf("requests = %d, want two health probes and two agent reads", len(recorded))
	}
	if got := recorded[1]; got.Method != http.MethodGet || got.Path != "/v1/agents" || got.RawQuery != "repo=" {
		t.Fatalf("agent read without --repo = %s %s?%s, want an empty repo parameter", got.Method, got.Path, got.RawQuery)
	}
	if got := recorded[3]; got.Method != http.MethodGet || got.Path != "/v1/agents" || got.RawQuery != "repo="+url.QueryEscape(abs) {
		t.Fatalf("agent read = %s %s?%s, want repo=%s", got.Method, got.Path, got.RawQuery, url.QueryEscape(abs))
	}
}

func TestAgentSessionRejectsUnverifiableResponse(t *testing.T) {
	t.Run("session without an id is a daemon error", func(t *testing.T) {
		var requests []recordedCLIRequest
		var mu sync.Mutex
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			recordCLIRequest(&requests, &mu, r)
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/v1/health":
				_, _ = w.Write([]byte(`{"ok":true}`))
			case "/v1/sessions/1":
				_, _ = w.Write([]byte(`{"session":{"ID":0,"ThreadID":2,"TriggerMessageID":3,"AgentName":"probe","Status":"succeeded","ReplyMode":"stdout","Command":"/bin/probe"},"events":[]}`))
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()
		useTestDaemon(t, server)

		code, stdout, stderr := captureOutput(t, func() int {
			return run([]string{"agent", "session", "--id", "1", "--json"})
		})
		if code != 2 {
			t.Fatalf("exit = %d, want 2: %s", code, stderr)
		}
		if stdout != "" {
			t.Fatalf("stdout = %q, want empty", stdout)
		}
		assertCLIErrorEnvelope(t, stderr, "DAEMON_ERROR")
		recorded := snapshotCLIRequests(&requests, &mu)
		if len(recorded) != 2 || recorded[1].Path != "/v1/sessions/1" {
			t.Fatalf("requests = %+v, want a read of the requested session", recorded)
		}
	})

	t.Run("event from another session is a daemon error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/v1/health":
				_, _ = w.Write([]byte(`{"ok":true}`))
			case "/v1/sessions/1":
				_, _ = w.Write([]byte(`{"session":{"ID":1,"ThreadID":2,"TriggerMessageID":3,"AgentName":"probe","Status":"running","ReplyMode":"stdout","Command":"/bin/probe"},"events":[{"ID":9,"SessionID":2,"Seq":1,"Type":"stdout","Content":"the answer","CreatedAt":""}]}`))
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()
		useTestDaemon(t, server)

		code, stdout, stderr := captureOutput(t, func() int {
			return run([]string{"agent", "session", "--id", "1", "--json"})
		})
		if code != 2 {
			t.Fatalf("exit = %d, want 2: %s", code, stderr)
		}
		if stdout != "" {
			t.Fatalf("stdout = %q, want empty", stdout)
		}
		assertCLIErrorEnvelope(t, stderr, "DAEMON_ERROR")
	})
}
