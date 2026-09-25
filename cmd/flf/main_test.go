package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/NoRaincheck/fluffle/internal/jsonl"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

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

func TestAgentReadAfterSeqBuildsStrictCursorAndFiltersReactions(t *testing.T) {
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
				{"ID":1,"MessageID":41,"MessageSeq":1,"Emoji":"old","Name":"alice","AuthorType":"human","CreatedAt":"2026-09-25T00:01:30Z"},
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
	if len(lines) != 4 {
		t.Fatalf("lines = %d, want 2 messages and 2 reactions: %s", len(lines), stdout)
	}
	if lines[0].Type != "message" || lines[0].Seq != 2 || lines[0].ParentSeq != 1 {
		t.Fatalf("first line = %+v", lines[0])
	}
	if lines[1].Type != "message" || lines[1].Seq != 3 || lines[1].ParentSeq != 2 {
		t.Fatalf("second line = %+v", lines[1])
	}
	if lines[2].Type != "reaction" || lines[2].MessageSeq != 2 || lines[2].Emoji != "new" {
		t.Fatalf("third line = %+v", lines[2])
	}
	if lines[3].Type != "reaction" || lines[3].MessageSeq != 3 || lines[3].Emoji != "latest" {
		t.Fatalf("fourth line = %+v", lines[3])
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
			_, _ = w.Write([]byte(`[]`))
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
			_, _ = w.Write([]byte(`[]`))
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
