//go:build e2e

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/NoRaincheck/fluffle/internal/store"
)

// waitFor polls fn every 50ms until it returns true or timeout expires.
func waitFor(t *testing.T, timeout time.Duration, fn func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// syncBuffer is a goroutine-safe bytes.Buffer for capturing subprocess output.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// buildTestBinary builds the flf binary into the given directory.
func buildTestBinary(t *testing.T, dir string) string {
	t.Helper()
	bin := filepath.Join(dir, "flf")
	// Build from the project root (where go.mod lives).
	// The test file lives in cmd/flf/; go up two levels to the module root.
	cmd := exec.Command("go", "build", "-tags", "e2e", "-o", bin, "./cmd/flf")
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}
	return bin
}

// gitInit creates a minimal git repo at dir.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	runCmd(t, dir, "git", "init")
	runCmd(t, dir, "git", "config", "user.email", "test@test.com")
	runCmd(t, dir, "git", "config", "user.name", "Test")
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("test"), 0o644)
	runCmd(t, dir, "git", "add", ".")
	runCmd(t, dir, "git", "commit", "-m", "init")
}

func runCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

// startDaemon starts the daemon as a subprocess and waits for it to become
// responsive, following Roborev's lifecycle test pattern.
func startDaemon(t *testing.T, bin, home string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(bin, "daemon", "start")
	cmd.Env = append(os.Environ(), "FLUFFLE_HOME="+home)
	output := new(syncBuffer)
	cmd.Stdout = output
	cmd.Stderr = output

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start daemon: %v", err)
	}

	t.Cleanup(func() {
		if cmd.ProcessState == nil || !cmd.ProcessState.Exited() {
			// Try HTTP shutdown first (the production stop path)
			daemonJSON := filepath.Join(home, "daemon.json")
			if data, err := os.ReadFile(daemonJSON); err == nil {
				var df struct {
					Port int `json:"port"`
				}
				if json.Unmarshal(data, &df) == nil && df.Port > 0 {
					resp, err := http.Post(
						"http://127.0.0.1:"+strconv.Itoa(df.Port)+"/api/shutdown",
						"application/json", nil,
					)
					if err == nil && resp != nil {
						resp.Body.Close()
					}
				}
			}
			// Fallback: kill
			_ = cmd.Process.Kill()
		}
	})

	// Wait for daemon.json to appear (Roborev pattern: poll for runtime file)
	if !waitFor(t, 5*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(home, "daemon.json"))
		return err == nil
	}) {
		t.Fatalf("daemon did not start: %s", output.String())
	}

	return cmd
}

// waitForDaemonReady waits for the daemon to be responsive via its port.
func waitForDaemonReady(t *testing.T, home string, timeout time.Duration) int {
	t.Helper()
	var port int
	data, err := os.ReadFile(filepath.Join(home, "daemon.json"))
	if err != nil {
		t.Fatalf("no daemon.json: %v", err)
	}
	var df struct {
		Port int `json:"port"`
	}
	if err := json.Unmarshal(data, &df); err != nil {
		t.Fatalf("bad daemon.json: %v", err)
	}
	port = df.Port

	if !waitFor(t, timeout, func() bool {
		resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/v1/health")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return true
	}) {
		t.Fatalf("daemon not responsive on port %d", port)
	}
	return port
}

// runCLI runs the flf binary and returns its stdout.
func runCLI(t *testing.T, env []string, bin string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("flf %v: %s", args, string(out))
	}
	return strings.TrimSpace(string(out))
}

type cliResult struct {
	stdout   string
	stderr   string
	exitCode int
}

func runCLIResult(t *testing.T, env []string, bin, input string, args ...string) cliResult {
	t.Helper()
	return runCLIResultInDir(t, env, bin, "", input, args...)
}

func runCLIResultInDir(t *testing.T, env []string, bin, dir, input string, args ...string) cliResult {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), env...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Stdin = strings.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	exitCode := 0
	if err := cmd.Run(); err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("flf %v: %v", args, err)
		}
		exitCode = exitErr.ExitCode()
	}
	return cliResult{
		stdout:   strings.TrimSpace(stdout.String()),
		stderr:   strings.TrimSpace(stderr.String()),
		exitCode: exitCode,
	}
}

func assertE2ECLIErrorResult(t *testing.T, result cliResult, wantCode string) {
	t.Helper()
	if result.exitCode != 1 {
		t.Fatalf("exit = %d, want 1; stdout=%q stderr=%q", result.exitCode, result.stdout, result.stderr)
	}
	if result.stdout != "" {
		t.Fatalf("stdout = %q, want empty", result.stdout)
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(result.stderr), &fields); err != nil {
		t.Fatalf("invalid error envelope: %v\n%s", err, result.stderr)
	}
	if len(fields) != 2 || fields["code"] != wantCode {
		t.Fatalf("error envelope = %#v, want code %q", fields, wantCode)
	}
	if message, ok := fields["message"].(string); !ok || message == "" {
		t.Fatalf("error message = %#v", fields["message"])
	}
}

func startAgentDaemon(t *testing.T, bin, home string) func() {
	t.Helper()
	env := []string{"FLUFFLE_HOME=" + home}
	result := runCLIResult(t, env, bin, "", "daemon", "start", "--background")
	if result.exitCode != 0 {
		t.Fatalf("daemon start failed: exit=%d stdout=%q stderr=%q", result.exitCode, result.stdout, result.stderr)
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		stopResult := runCLIResult(t, env, bin, "", "daemon", "stop")
		if stopResult.exitCode != 0 {
			if _, err := os.Stat(filepath.Join(home, "daemon.json")); os.IsNotExist(err) {
				return
			}
			t.Logf("daemon stop cleanup: exit=%d stdout=%q stderr=%q", stopResult.exitCode, stopResult.stdout, stopResult.stderr)
			return
		}
		if !waitFor(t, 5*time.Second, func() bool {
			_, err := os.Stat(filepath.Join(home, "daemon.json"))
			return os.IsNotExist(err)
		}) {
			t.Logf("daemon runtime file remained after cleanup")
		}
	}
	t.Cleanup(stop)
	var last cliResult
	if !waitFor(t, 10*time.Second, func() bool {
		last = runCLIResult(t, env, bin, "", "daemon", "status")
		return last.exitCode == 0 && strings.Contains(last.stdout, "daemon up")
	}) {
		t.Fatalf("daemon did not become ready: exit=%d stdout=%q stderr=%q", last.exitCode, last.stdout, last.stderr)
	}
	return stop
}

// readDaemonJSON reads and parses daemon.json.
func readDaemonJSON(t *testing.T, home string) (port int, pid int) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, "daemon.json"))
	if err != nil {
		t.Fatalf("read daemon.json: %v", err)
	}
	var df struct {
		Port int `json:"port"`
		PID  int `json:"pid"`
	}
	if err := json.Unmarshal(data, &df); err != nil {
		t.Fatalf("parse daemon.json: %v", err)
	}
	return df.Port, df.PID
}

// TestE2E_DaemonLifecycleEndToEnd follows Roborev's TestDaemonLifecycleEndToEnd:
// spawn, runtime publication, liveness probe, DB-backed API endpoint, and HTTP shutdown.
func TestE2E_DaemonLifecycleEndToEnd(t *testing.T) {
	tmpDir := t.TempDir()
	home := filepath.Join(tmpDir, "fluffle")
	bin := buildTestBinary(t, tmpDir)

	cmd := exec.Command(bin, "daemon", "start")
	cmd.Env = append(os.Environ(), "FLUFFLE_HOME="+home)
	output := new(syncBuffer)
	cmd.Stdout = output
	cmd.Stderr = output

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start daemon: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	t.Cleanup(func() {
		if cmd.ProcessState == nil || !cmd.ProcessState.Exited() {
			_ = cmd.Process.Kill()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
			}
		}
	})

	// Wait for daemon.json to appear
	if !waitFor(t, 10*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(home, "daemon.json"))
		return err == nil
	}) {
		t.Fatalf("daemon did not publish runtime record: %s", output.String())
	}

	port, pid := readDaemonJSON(t, home)

	// Liveness probe: /v1/health
	resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/v1/health")
	if err != nil {
		t.Fatalf("daemon must answer /v1/health: %v\n%s", err, output.String())
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// DB-backed endpoint: /v1/channels
	resp, err = http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/v1/channels")
	if err != nil {
		t.Fatalf("daemon must serve /v1/channels: %v\n%s", err, output.String())
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on /v1/channels, got %d", resp.StatusCode)
	}

	// HTTP shutdown (the production stop path)
	resp, err = http.Post("http://127.0.0.1:"+strconv.Itoa(port)+"/api/shutdown", "application/json", nil)
	if err != nil {
		t.Fatalf("shutdown request failed: %v", err)
	}
	resp.Body.Close()

	select {
	case <-done:
		// Daemon exited cleanly
	case <-time.After(10 * time.Second):
		t.Fatalf("daemon did not exit after /api/shutdown\n%s", output.String())
	}

	// Verify process is gone
	if processAlive(pid) {
		t.Fatalf("process %d still exists after shutdown", pid)
	}
}

// TestE2E_DaemonBackgroundStability verifies the background daemon stays alive
// after the CLI process exits, then responds to API calls.
func TestE2E_DaemonBackgroundStability(t *testing.T) {
	tmpDir := t.TempDir()
	home := filepath.Join(tmpDir, "fluffle")
	bin := buildTestBinary(t, tmpDir)

	env := []string{"FLUFFLE_HOME=" + home}

	// Start daemon in background.
	// daemonStartBackground now forks into a detached subprocess, so the parent
	// exits immediately.
	cmd := exec.Command(bin, "daemon", "start", "--background")
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("daemon start --background failed: %v\n%s", err, out)
	}

	// Wait for daemon.json to appear (Roborev pattern: poll for runtime file)
	if !waitFor(t, 10*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(home, "daemon.json"))
		return err == nil
	}) {
		t.Fatalf("daemon did not start: %s", string(out))
	}

	// Wait for daemon to be responsive
	port := waitForDaemonReady(t, home, 10*time.Second)

	// Status should show daemon up
	statusOut := runCLI(t, env, bin, "daemon", "status")
	if !strings.Contains(statusOut, "daemon up") {
		t.Fatalf("expected 'daemon up', got: %s", statusOut)
	}

	// Verify daemon is responsive via HTTP
	resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/v1/health")
	if err != nil {
		t.Fatalf("daemon not responsive: %v", err)
	}
	resp.Body.Close()

	// HTTP shutdown
	_, _ = http.Post("http://127.0.0.1:"+strconv.Itoa(port)+"/api/shutdown", "application/json", nil)
}

// TestE2E_ChannelCreateList exercises channel CRUD with both text and JSON output.
func TestE2E_ChannelCreateList(t *testing.T) {
	tmpDir := t.TempDir()
	home := filepath.Join(tmpDir, "fluffle")
	bin := buildTestBinary(t, tmpDir)

	repoDir := t.TempDir()
	gitInit(t, repoDir)

	env := []string{"FLUFFLE_HOME=" + home}
	cmd := startDaemon(t, bin, home)
	_ = cmd

	port := waitForDaemonReady(t, home, 10*time.Second)
	_ = port

	// Create channel with --json
	out := runCLI(t, env, bin, "channel", "create", "--name", "dev", "--repo", repoDir, "--json")
	var ch struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &ch); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if ch.ID == 0 {
		t.Fatalf("expected channel ID, got: %s", out)
	}

	// List channels without --repo (should list all)
	out = runCLI(t, env, bin, "channel", "list")
	if !strings.Contains(out, "dev") {
		t.Fatalf("expected 'dev' in channel list, got: %s", out)
	}

	// List channels with --json
	out = runCLI(t, env, bin, "channel", "list", "--json")
	type Channel struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	var list []Channel
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if len(list) != 1 || list[0].Name != "dev" {
		t.Fatalf("expected 1 channel named 'dev', got: %s", out)
	}

	// HTTP shutdown
	_, _ = http.Post("http://127.0.0.1:"+strconv.Itoa(port)+"/api/shutdown", "application/json", nil)
}

// TestE2E_ThreadCreateList exercises thread creation and listing.
func TestE2E_ThreadCreateList(t *testing.T) {
	tmpDir := t.TempDir()
	home := filepath.Join(tmpDir, "fluffle")
	bin := buildTestBinary(t, tmpDir)

	repoDir := t.TempDir()
	gitInit(t, repoDir)

	env := []string{"FLUFFLE_HOME=" + home}
	cmd := startDaemon(t, bin, home)
	_ = cmd

	port := waitForDaemonReady(t, home, 10*time.Second)
	_ = port

	// Create channel
	runCLI(t, env, bin, "channel", "create", "--name", "bugs", "--repo", repoDir)

	// Create thread with --json
	out := runCLI(t, env, bin, "thread", "new", "--channel", "bugs", "--repo", repoDir, "--title", "something broke", "--json")
	var th struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &th); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if th.ID == 0 {
		t.Fatalf("expected thread ID, got: %s", out)
	}

	// List threads with --json
	out = runCLI(t, env, bin, "thread", "list", "--channel", "bugs", "--repo", repoDir, "--json")
	type Thread struct {
		ID    int64  `json:"id"`
		Title string `json:"title"`
	}
	var threads []Thread
	if err := json.Unmarshal([]byte(out), &threads); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if len(threads) != 1 || threads[0].Title != "something broke" {
		t.Fatalf("expected 1 thread, got: %s", out)
	}

	// HTTP shutdown
	_, _ = http.Post("http://127.0.0.1:"+strconv.Itoa(port)+"/api/shutdown", "application/json", nil)
}

// TestE2E_MessageSend exercises message sending with JSON output.
func TestE2E_MessageSend(t *testing.T) {
	tmpDir := t.TempDir()
	home := filepath.Join(tmpDir, "fluffle")
	bin := buildTestBinary(t, tmpDir)

	repoDir := t.TempDir()
	gitInit(t, repoDir)

	env := []string{"FLUFFLE_HOME=" + home}
	cmd := startDaemon(t, bin, home)
	_ = cmd

	port := waitForDaemonReady(t, home, 10*time.Second)
	_ = port

	// Create channel and thread
	runCLI(t, env, bin, "channel", "create", "--name", "dev", "--repo", repoDir)
	out := runCLI(t, env, bin, "thread", "new", "--channel", "dev", "--repo", repoDir, "--title", "test", "--json")
	var th struct {
		ID int64 `json:"id"`
	}
	json.Unmarshal([]byte(out), &th)

	// Send message with --json
	out = runCLI(t, env, bin, "message", "send", "--thread", strconv.FormatInt(th.ID, 10), "--text", "hello world", "--json")
	var msg struct {
		Seq int64 `json:"seq"`
	}
	if err := json.Unmarshal([]byte(out), &msg); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if msg.Seq == 0 {
		t.Fatalf("expected seq, got: %s", out)
	}

	// HTTP shutdown
	_, _ = http.Post("http://127.0.0.1:"+strconv.Itoa(port)+"/api/shutdown", "application/json", nil)
}

// TestE2E_AgentRead exercises message retrieval with JSON output.
func TestE2E_AgentRead(t *testing.T) {
	tmpDir := t.TempDir()
	home := filepath.Join(tmpDir, "fluffle")
	bin := buildTestBinary(t, tmpDir)

	repoDir := t.TempDir()
	gitInit(t, repoDir)

	env := []string{"FLUFFLE_HOME=" + home}
	cmd := startDaemon(t, bin, home)
	_ = cmd

	port := waitForDaemonReady(t, home, 10*time.Second)
	_ = port

	// Create channel and thread
	runCLI(t, env, bin, "channel", "create", "--name", "dev", "--repo", repoDir)
	out := runCLI(t, env, bin, "thread", "new", "--channel", "dev", "--repo", repoDir, "--title", "test", "--json")
	var th struct {
		ID int64 `json:"id"`
	}
	json.Unmarshal([]byte(out), &th)

	// Send a message
	runCLI(t, env, bin, "message", "send", "--thread", strconv.FormatInt(th.ID, 10), "--text", "agent message", "--as", "alice")

	// Read messages with --json (agent read returns JSONL: one JSON object per line)
	out = runCLI(t, env, bin, "agent", "read", "--thread", strconv.FormatInt(th.ID, 10), "--json")
	type Message struct {
		Seq     int64  `json:"seq"`
		Name    string `json:"name"`
		Content string `json:"content"`
	}
	var msgs []Message
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var m Message
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("invalid JSONL line: %v\n%s", err, out)
		}
		msgs = append(msgs, m)
	}
	if len(msgs) != 1 || msgs[0].Content != "agent message" || msgs[0].Name != "alice" {
		t.Fatalf("expected 1 message from alice, got: %s", out)
	}

	// HTTP shutdown
	_, _ = http.Post("http://127.0.0.1:"+strconv.Itoa(port)+"/api/shutdown", "application/json", nil)
}

// TestE2E_OrphanChannel exercises orphan channel creation and listing.
func TestE2E_OrphanChannel(t *testing.T) {
	tmpDir := t.TempDir()
	home := filepath.Join(tmpDir, "fluffle")
	bin := buildTestBinary(t, tmpDir)

	env := []string{"FLUFFLE_HOME=" + home}
	cmd := startDaemon(t, bin, home)
	_ = cmd

	port := waitForDaemonReady(t, home, 10*time.Second)
	_ = port

	// Create orphan channel
	out := runCLI(t, env, bin, "channel", "create", "--name", "general", "--orphaned", "--json")
	var ch struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &ch); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}

	// List all channels (includes orphaned)
	out = runCLI(t, env, bin, "channel", "list", "--include-orphaned", "--json")
	type Channel struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	var list []Channel
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if len(list) != 1 || list[0].Name != "general" {
		t.Fatalf("expected 'general' orphaned channel, got: %s", out)
	}

	// List without --include-orphaned should not show orphaned
	out = runCLI(t, env, bin, "channel", "list", "--json")
	var list2 []Channel
	if err := json.Unmarshal([]byte(out), &list2); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if len(list2) != 0 {
		t.Fatalf("expected 0 channels without --include-orphaned, got: %s", out)
	}

	// HTTP shutdown
	_, _ = http.Post("http://127.0.0.1:"+strconv.Itoa(port)+"/api/shutdown", "application/json", nil)
}

// TestE2E_FullWorkflow exercises the complete channel/thread/message workflow.
func TestE2E_FullWorkflow(t *testing.T) {
	tmpDir := t.TempDir()
	home := filepath.Join(tmpDir, "fluffle")
	bin := buildTestBinary(t, tmpDir)

	repoDir := t.TempDir()
	gitInit(t, repoDir)

	env := []string{"FLUFFLE_HOME=" + home}
	cmd := startDaemon(t, bin, home)
	_ = cmd

	port := waitForDaemonReady(t, home, 10*time.Second)
	_ = port

	// 1. Create channel
	out := runCLI(t, env, bin, "channel", "create", "--name", "code-review", "--repo", repoDir, "--json")
	var ch struct {
		ID int64 `json:"id"`
	}
	json.Unmarshal([]byte(out), &ch)

	// 2. Create thread
	out = runCLI(t, env, bin, "thread", "new", "--channel", "code-review", "--repo", repoDir, "--title", "PR #42", "--json")
	var th struct {
		ID int64 `json:"id"`
	}
	json.Unmarshal([]byte(out), &th)

	// 3. Send messages
	runCLI(t, env, bin, "message", "send", "--thread", strconv.FormatInt(th.ID, 10), "--text", "LGTM", "--as", "alice")
	runCLI(t, env, bin, "message", "send", "--thread", strconv.FormatInt(th.ID, 10), "--text", "Can you fix the typo?", "--as", "bob")

	// 4. List channels (no --repo needed)
	out = runCLI(t, env, bin, "channel", "list")
	if !strings.Contains(out, "code-review") {
		t.Fatalf("expected 'code-review' in channel list, got: %s", out)
	}

	// 5. Read messages (JSONL: one JSON object per line)
	out = runCLI(t, env, bin, "agent", "read", "--thread", strconv.FormatInt(th.ID, 10), "--json")
	type Message struct {
		Seq     int64  `json:"seq"`
		Name    string `json:"name"`
		Content string `json:"content"`
	}
	var msgs []Message
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var m Message
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("invalid JSONL line: %v\n%s", err, out)
		}
		msgs = append(msgs, m)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Name != "alice" || msgs[1].Name != "bob" {
		t.Fatalf("expected alice then bob, got: %+v", msgs)
	}

	// HTTP shutdown
	_, _ = http.Post("http://127.0.0.1:"+strconv.Itoa(port)+"/api/shutdown", "application/json", nil)
}

// TestE2E_CwdInference verifies that --repo defaults to cwd when omitted.
func TestE2E_CwdInference(t *testing.T) {
	tmpDir := t.TempDir()
	home := filepath.Join(tmpDir, "fluffle")
	bin := buildTestBinary(t, tmpDir)

	repoDir := t.TempDir()
	gitInit(t, repoDir)

	env := []string{"FLUFFLE_HOME=" + home}
	cmd := startDaemon(t, bin, home)
	_ = cmd

	port := waitForDaemonReady(t, home, 10*time.Second)
	_ = port

	// Create channel with explicit --repo
	runCLI(t, env, bin, "channel", "create", "--name", "infra", "--repo", repoDir)

	// Without --repo, thread list defaults to cwd which is the module root.
	// The channel is anchored to repoDir, so it won't be found.
	out := runCLI(t, env, bin, "thread", "list", "--channel", "infra")
	if !strings.Contains(out, "CHANNEL_NOT_FOUND") && !strings.Contains(out, "NOT_A_GIT_REPO") {
		t.Fatalf("expected error when --repo not specified and cwd != repo, got: %s", out)
	}

	// Create thread WITH --repo (should work)
	out = runCLI(t, env, bin, "thread", "new", "--channel", "infra", "--repo", repoDir, "--title", "infra change", "--json")
	var th struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &th); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}

	// HTTP shutdown
	_, _ = http.Post("http://127.0.0.1:"+strconv.Itoa(port)+"/api/shutdown", "application/json", nil)
}

// TestE2E_FullWorkflowWithCwdInference exercises the full workflow with cwd-based --repo inference.
func TestE2E_FullWorkflowWithCwdInference(t *testing.T) {
	tmpDir := t.TempDir()
	home := filepath.Join(tmpDir, "fluffle")
	bin := buildTestBinary(t, tmpDir)

	repoDir := t.TempDir()
	gitInit(t, repoDir)

	env := []string{"FLUFFLE_HOME=" + home}
	cmd := startDaemon(t, bin, home)
	_ = cmd

	port := waitForDaemonReady(t, home, 10*time.Second)
	_ = port

	// Create channel with explicit --repo
	runCLI(t, env, bin, "channel", "create", "--name", "infra", "--repo", repoDir)

	// Create thread from within the repo dir (no --repo needed)
	threadCmd := exec.Command(bin, "thread", "new", "--channel", "infra", "--title", "cwd test", "--json")
	threadCmd.Env = append(os.Environ(), env...)
	threadCmd.Dir = repoDir
	out, err := threadCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("thread new from cwd failed: %v\n%s", err, out)
	}
	var th struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(out, &th); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if th.ID == 0 {
		t.Fatalf("expected thread ID, got: %s", string(out))
	}

	// Send message from within the repo dir (no --repo needed for thread)
	msgCmd := exec.Command(bin, "message", "send", "--thread", strconv.FormatInt(th.ID, 10), "--text", "from cwd", "--as", "test")
	msgCmd.Env = append(os.Environ(), env...)
	msgCmd.Dir = repoDir
	out, err = msgCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("message send from cwd failed: %v\n%s", err, out)
	}

	// HTTP shutdown
	_, _ = http.Post("http://127.0.0.1:"+strconv.Itoa(port)+"/api/shutdown", "application/json", nil)
}

// TestE2E_DaemonShutdownViaAPI verifies the daemon shuts down cleanly via HTTP.
func TestE2E_DaemonShutdownViaAPI(t *testing.T) {
	tmpDir := t.TempDir()
	home := filepath.Join(tmpDir, "fluffle")
	bin := buildTestBinary(t, tmpDir)

	cmd := exec.Command(bin, "daemon", "start")
	cmd.Env = append(os.Environ(), "FLUFFLE_HOME="+home)
	output := new(syncBuffer)
	cmd.Stdout = output
	cmd.Stderr = output

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start daemon: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	// Wait for daemon to be ready
	if !waitFor(t, 10*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(home, "daemon.json"))
		return err == nil
	}) {
		t.Fatalf("daemon did not start: %s", output.String())
	}

	port, _ := readDaemonJSON(t, home)

	// Verify daemon is up
	resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/v1/health")
	if err != nil {
		t.Fatalf("daemon not responsive: %v", err)
	}
	resp.Body.Close()

	// Send shutdown
	resp, err = http.Post("http://127.0.0.1:"+strconv.Itoa(port)+"/api/shutdown", "application/json", nil)
	if err != nil {
		t.Fatalf("shutdown request failed: %v", err)
	}
	resp.Body.Close()

	// Verify daemon exits
	select {
	case <-done:
		// Clean exit
	case <-time.After(10 * time.Second):
		t.Fatalf("daemon did not exit after /api/shutdown\n%s", output.String())
	}
}

// TestE2E_DaemonProcessExists verifies the daemon process persists after CLI exits.
func TestE2E_DaemonProcessExists(t *testing.T) {
	tmpDir := t.TempDir()
	home := filepath.Join(tmpDir, "fluffle")
	bin := buildTestBinary(t, tmpDir)

	env := []string{"FLUFFLE_HOME=" + home}

	// Start daemon in background.
	cmd := exec.Command(bin, "daemon", "start", "--background")
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("daemon start --background failed: %v\n%s", err, out)
	}

	// Wait for daemon.json to appear
	if !waitFor(t, 10*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(home, "daemon.json"))
		return err == nil
	}) {
		t.Fatalf("daemon did not start: %s", string(out))
	}

	// Wait for daemon to be responsive
	port := waitForDaemonReady(t, home, 10*time.Second)

	// Read daemon.json to get the PID
	_, pid := readDaemonJSON(t, home)

	// Verify process exists - signal 0 checks existence without sending a signal
	proc, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("find process: %v", err)
	}
	if proc.Pid != pid {
		t.Fatalf("FindProcess returned wrong PID: got %d, want %d", proc.Pid, pid)
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		// Process might have exited; check if it's still responsive
		resp, err2 := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/v1/health")
		if err2 != nil {
			t.Fatalf("process %d does not exist and not responsive: %v", pid, err)
		}
		resp.Body.Close()
	}

	// Verify daemon is still responsive via HTTP
	resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/v1/health")
	if err != nil {
		t.Fatalf("daemon not responsive: %v", err)
	}
	resp.Body.Close()

	// HTTP shutdown
	_, _ = http.Post("http://127.0.0.1:"+strconv.Itoa(port)+"/api/shutdown", "application/json", nil)
}

// TestE2E_DaemonStopViaCLI verifies flf daemon stop works correctly.
func TestE2E_DaemonStopViaCLI(t *testing.T) {
	tmpDir := t.TempDir()
	home := filepath.Join(tmpDir, "fluffle")
	bin := buildTestBinary(t, tmpDir)

	env := []string{"FLUFFLE_HOME=" + home}

	// Start daemon in background (use Start(), not CombinedOutput()).
	cmd := exec.Command(bin, "daemon", "start", "--background")
	cmd.Env = append(os.Environ(), env...)
	if err := cmd.Start(); err != nil {
		t.Fatalf("daemon start --background failed: %v", err)
	}

	// Wait for daemon.json to appear
	if !waitFor(t, 10*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(home, "daemon.json"))
		return err == nil
	}) {
		t.Fatalf("daemon did not start")
	}

	// Wait for daemon to be responsive
	port := waitForDaemonReady(t, home, 10*time.Second)
	_ = port

	// Stop daemon via CLI
	stopCmd := exec.Command(bin, "daemon", "stop")
	stopCmd.Env = append(os.Environ(), env...)
	stopOut, err := stopCmd.CombinedOutput()
	if err != nil {
		t.Logf("daemon stop: %v\n%s", err, string(stopOut))
	}

	// Verify daemon.json is removed
	if _, err := os.Stat(filepath.Join(home, "daemon.json")); !os.IsNotExist(err) {
		t.Fatalf("daemon.json should be removed after stop")
	}

	// Verify daemon is no longer responsive
	time.Sleep(500 * time.Millisecond)
	resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/v1/health")
	if err == nil {
		resp.Body.Close()
		t.Fatalf("daemon should not be responsive after stop")
	}
}

// TestE2E_MutuallyExclusiveFlags verifies --orphaned and --repo are mutually exclusive.
func TestE2E_MutuallyExclusiveFlags(t *testing.T) {
	tmpDir := t.TempDir()
	home := filepath.Join(tmpDir, "fluffle")
	bin := buildTestBinary(t, tmpDir)

	repoDir := t.TempDir()
	gitInit(t, repoDir)

	env := []string{"FLUFFLE_HOME=" + home}

	// Try to create channel with both --orphaned and --repo
	cmd := exec.Command(bin, "channel", "create", "--name", "bad", "--orphaned", "--repo", repoDir)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected error when using both --orphaned and --repo, got: %s", out)
	}
}

// TestE2E_ChannelCreateValidation verifies channel create rejects invalid input.
func TestE2E_ChannelCreateValidation(t *testing.T) {
	tmpDir := t.TempDir()
	home := filepath.Join(tmpDir, "fluffle")
	bin := buildTestBinary(t, tmpDir)

	env := []string{"FLUFFLE_HOME=" + home}
	cmd := startDaemon(t, bin, home)
	_ = cmd

	port := waitForDaemonReady(t, home, 10*time.Second)
	_ = port

	// Create a channel
	runCLI(t, env, bin, "channel", "create", "--name", "test", "--json")

	// Try to create a duplicate channel
	out := runCLI(t, env, bin, "channel", "create", "--name", "test", "--json")
	// Should return an error (409 conflict)
	if strings.Contains(out, `"id"`) {
		t.Fatalf("duplicate channel should fail, got: %s", out)
	}

	// HTTP shutdown
	_, _ = http.Post("http://127.0.0.1:"+strconv.Itoa(port)+"/api/shutdown", "application/json", nil)
}

// TestE2E_ThreadReply exercises orphan channel thread + message + reply via --json and --reply-to.
func TestE2E_ThreadReply(t *testing.T) {
	tmpDir := t.TempDir()
	home := filepath.Join(tmpDir, "fluffle")
	bin := buildTestBinary(t, tmpDir)

	env := []string{"FLUFFLE_HOME=" + home}
	cmd := startDaemon(t, bin, home)
	_ = cmd

	port := waitForDaemonReady(t, home, 10*time.Second)
	_ = port

	// Orphan channel + thread
	out := runCLI(t, env, bin, "channel", "create", "--name", "reply-demo", "--orphaned", "--json")
	var ch struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &ch); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	out = runCLI(t, env, bin, "thread", "new", "--channel", "reply-demo", "--orphaned", "--title", "topic", "--json")
	var th struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &th); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	tid := strconv.FormatInt(th.ID, 10)

	// Parent message
	out = runCLI(t, env, bin, "message", "send", "--thread", tid, "--text", "parent", "--as", "alice", "--json")
	var m1 struct {
		Seq int64 `json:"seq"`
	}
	if err := json.Unmarshal([]byte(out), &m1); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if m1.Seq != 1 {
		t.Fatalf("want seq 1 got %d", m1.Seq)
	}

	// Need parent message ID (not seq) for reply — fetch via HTTP
	resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/v1/threads/" + tid + "/messages")
	if err != nil {
		t.Fatalf("fetch messages: %v", err)
	}
	var msgs []struct {
		ID int64 `json:"ID"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&msgs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if len(msgs) != 1 {
		t.Fatalf("want 1 msg got %d", len(msgs))
	}
	parentID := strconv.FormatInt(msgs[0].ID, 10)

	// Reply
	out = runCLI(t, env, bin, "message", "send", "--thread", tid, "--text", "child reply", "--as", "bob", "--reply-to", parentID, "--json")
	var m2 struct {
		Seq int64 `json:"seq"`
	}
	if err := json.Unmarshal([]byte(out), &m2); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if m2.Seq != 2 {
		t.Fatalf("want seq 2 got %d", m2.Seq)
	}

	// Verify via HTTP that second message has parent
	resp, err = http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/v1/threads/" + tid + "/messages")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	defer resp.Body.Close()
	var full []struct {
		ParentID struct {
			Int64 int64 `json:"Int64"`
			Valid bool  `json:"Valid"`
		} `json:"ParentID"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&full); err != nil {
		t.Fatalf("decode full: %v", err)
	}
	if len(full) != 2 || !full[1].ParentID.Valid || full[1].ParentID.Int64 == 0 {
		t.Fatalf("want reply with parent, got %+v", full)
	}

	_, _ = http.Post("http://127.0.0.1:"+strconv.Itoa(port)+"/api/shutdown", "application/json", nil)
}

func TestE2E_AgentPortableLoop(t *testing.T) {
	tmpDir := t.TempDir()
	home := filepath.Join(tmpDir, "fluffle")
	bin := buildTestBinary(t, tmpDir)
	repoDir := t.TempDir()
	gitInit(t, repoDir)
	env := []string{"FLUFFLE_HOME=" + home}
	stop := startAgentDaemon(t, bin, home)

	runOK := func(input string, args ...string) string {
		t.Helper()
		result := runCLIResult(t, env, bin, input, args...)
		if result.exitCode != 0 {
			t.Fatalf("flf %v: exit=%d stdout=%q stderr=%q", args, result.exitCode, result.stdout, result.stderr)
		}
		if result.stderr != "" {
			t.Fatalf("flf %v wrote stderr: %q", args, result.stderr)
		}
		return result.stdout
	}

	out := runOK("", "channel", "create", "--name", "portable", "--repo", repoDir, "--json")
	var channel struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &channel); err != nil {
		t.Fatalf("channel JSON: %v\n%s", err, out)
	}
	if channel.ID == 0 {
		t.Fatalf("channel = %s", out)
	}

	out = runOK("", "thread", "new", "--channel", "portable", "--repo", repoDir, "--title", "portable loop", "--json")
	var thread struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &thread); err != nil {
		t.Fatalf("thread JSON: %v\n%s", err, out)
	}
	if thread.ID == 0 {
		t.Fatalf("thread = %s", out)
	}
	threadID := strconv.FormatInt(thread.ID, 10)

	runOK("", "message", "send", "--thread", threadID, "--text", "portable seed", "--as", "seed", "--json")
	runOK("", "message", "send", "--thread", threadID, "--text", "portable seed 2", "--as", "seed-2", "--json")
	runOK("", "message", "send", "--thread", threadID, "--text", "portable root", "--as", "alice", "--created-at", "2026-09-25T10:00:00Z", "--json")

	type portableLine struct {
		Type       string `json:"type"`
		Seq        int64  `json:"seq"`
		ParentSeq  int64  `json:"parent_seq"`
		MessageSeq int64  `json:"message_seq"`
		Role       string `json:"role"`
		Name       string `json:"name"`
		AuthorType string `json:"author_type"`
		Content    string `json:"content"`
		Emoji      string `json:"emoji"`
		Timestamp  string `json:"timestamp"`
	}
	readEvents := func(raw string) []portableLine {
		t.Helper()
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return nil
		}
		var events []portableLine
		for _, line := range strings.Split(raw, "\n") {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal([]byte(line), &fields); err != nil {
				t.Fatalf("invalid JSONL: %v\n%s", err, raw)
			}
			for _, key := range []string{"id", "ID", "thread_id", "ThreadID", "message_id", "MessageID", "parent_id", "ParentID"} {
				if _, ok := fields[key]; ok {
					t.Fatalf("portable line contains internal field %q: %s", key, line)
				}
			}
			var event portableLine
			if err := json.Unmarshal([]byte(line), &event); err != nil {
				t.Fatalf("invalid JSONL: %v\n%s", err, raw)
			}
			events = append(events, event)
		}
		return events
	}
	assertStream := func(events []portableLine, label string) {
		t.Helper()
		if len(events) != 5 {
			t.Fatalf("%s events = %d, want 5: %+v", label, len(events), events)
		}
		for i, event := range events {
			if event.Timestamp == "" {
				t.Fatalf("%s event %d has no timestamp: %+v", label, i, event)
			}
			if _, err := time.Parse(time.RFC3339, event.Timestamp); err != nil {
				t.Fatalf("%s event %d timestamp = %q: %v", label, i, event.Timestamp, err)
			}
		}
		if events[0].Type != "message" || events[0].Seq <= 0 || events[0].Name != "seed" || events[0].Role != "user" || events[0].AuthorType != "human" || events[0].Content != "portable seed" || events[0].ParentSeq != 0 {
			t.Fatalf("%s first seed = %+v", label, events[0])
		}
		if events[1].Type != "message" || events[1].Seq != events[0].Seq+1 || events[1].ParentSeq != 0 || events[1].Name != "seed-2" || events[1].Role != "user" || events[1].AuthorType != "human" || events[1].Content != "portable seed 2" {
			t.Fatalf("%s second seed = %+v", label, events[1])
		}
		if events[2].Type != "message" || events[2].Seq != events[1].Seq+1 || events[2].ParentSeq != 0 || events[2].Name != "alice" || events[2].Role != "user" || events[2].AuthorType != "human" || events[2].Content != "portable root" || events[2].Timestamp != "2026-09-25T10:00:00Z" {
			t.Fatalf("%s root = %+v", label, events[2])
		}
		if events[3].Type != "message" || events[3].Seq != events[2].Seq+1 || events[3].ParentSeq != events[2].Seq || events[3].Name != "portable-agent" || events[3].Role != "user" || events[3].AuthorType != "agent" || events[3].Content != "portable reply" {
			t.Fatalf("%s reply = %+v, root seq %d", label, events[3], events[2].Seq)
		}
		if events[4].Type != "reaction" || events[4].MessageSeq != events[2].Seq || events[4].Name != "portable-agent" || events[4].AuthorType != "agent" || events[4].Emoji != "+1" || events[4].Role != "" {
			t.Fatalf("%s reaction = %+v, root seq %d", label, events[4], events[2].Seq)
		}
	}

	events := readEvents(runOK("", "agent", "read", "--thread", threadID, "--json"))
	if len(events) != 3 || events[0].Type != "message" || events[0].Seq != 1 || events[0].Content != "portable seed" || events[0].Role != "user" || events[0].Timestamp == "" || events[1].Type != "message" || events[1].Seq != 2 || events[1].Content != "portable seed 2" || events[1].Role != "user" || events[1].Timestamp == "" || events[2].Type != "message" || events[2].Seq != 3 || events[2].Name != "alice" || events[2].Role != "user" || events[2].Content != "portable root" || events[2].Timestamp != "2026-09-25T10:00:00Z" {
		t.Fatalf("initial events = %+v", events)
	}
	rootSeq := events[2].Seq

	out = runOK("portable reply", "message", "send", "--thread", threadID, "--reply-to-seq", strconv.FormatInt(rootSeq, 10), "--text", "-", "--agent-id", "portable-agent", "--as", "portable-agent", "--json")
	var reply struct {
		Seq       int64 `json:"seq"`
		ParentSeq int64 `json:"parent_seq"`
	}
	if err := json.Unmarshal([]byte(out), &reply); err != nil {
		t.Fatalf("reply JSON: %v\n%s", err, out)
	}
	if reply.ParentSeq != rootSeq || reply.Seq <= rootSeq {
		t.Fatalf("reply = %+v, root seq %d", reply, rootSeq)
	}

	reactionOut := runOK("", "react", "add", "--thread", threadID, "--message-seq", strconv.FormatInt(rootSeq, 10), "--emoji", "+1", "--agent-id", "portable-agent", "--as", "portable-agent")
	if reactionOut != "ok" {
		t.Fatalf("reaction output = %q", reactionOut)
	}

	assertCursor := func(events []portableLine, label string, wantMessageSeqs []int64, wantReactionTargets []int64) {
		t.Helper()
		var messageSeqs, reactionTargets []int64
		for _, event := range events {
			switch event.Type {
			case "message":
				messageSeqs = append(messageSeqs, event.Seq)
			case "reaction":
				reactionTargets = append(reactionTargets, event.MessageSeq)
			default:
				t.Fatalf("%s unexpected event type %q", label, event.Type)
			}
		}
		if fmt.Sprint(messageSeqs) != fmt.Sprint(wantMessageSeqs) {
			t.Fatalf("%s message sequences = %v, want %v", label, messageSeqs, wantMessageSeqs)
		}
		if fmt.Sprint(reactionTargets) != fmt.Sprint(wantReactionTargets) {
			t.Fatalf("%s reaction targets = %v, want the full thread snapshot %v", label, reactionTargets, wantReactionTargets)
		}
	}

	events = readEvents(runOK("", "agent", "read", "--thread", threadID, "--after-seq", strconv.FormatInt(rootSeq, 10), "--json"))
	assertCursor(events, "cursor", []int64{reply.Seq}, []int64{rootSeq})
	if events[0].ParentSeq != rootSeq {
		t.Fatalf("cursor message parent = %+v, want %d", events[0], rootSeq)
	}
	events = readEvents(runOK("", "agent", "read", "--thread", threadID, "--after-seq", strconv.FormatInt(reply.Seq, 10), "--json"))
	assertCursor(events, "exhausted cursor", nil, []int64{rootSeq})

	events = readEvents(runOK("", "agent", "read", "--thread", threadID, "--json"))
	assertStream(events, "source")

	var inbox []struct {
		ThreadID    int64  `json:"ThreadID"`
		ChannelID   int64  `json:"channel_id"`
		ChannelName string `json:"channel_name"`
		Content     string `json:"Content"`
	}
	if err := json.Unmarshal([]byte(runOK("", "inbox", "--json")), &inbox); err != nil {
		t.Fatalf("inbox JSON: %v", err)
	}
	var foundRoot, foundReply bool
	for _, message := range inbox {
		if message.ThreadID == thread.ID && message.ChannelID == channel.ID && message.ChannelName == "portable" && message.Content == "portable root" {
			foundRoot = true
		}
		if message.ThreadID == thread.ID && message.ChannelID == channel.ID && message.ChannelName == "portable" && message.Content == "portable reply" {
			foundReply = true
		}
	}
	if !foundRoot || !foundReply {
		t.Fatalf("inbox = %+v", inbox)
	}

	exportRaw := runOK("", "thread", "export", "--thread", threadID, "--format", "jsonl")
	exported := readEvents(exportRaw)
	assertStream(exported, "export")
	exportPath := filepath.Join(tmpDir, "portable.jsonl")
	if err := os.WriteFile(exportPath, []byte(exportRaw), 0o644); err != nil {
		t.Fatalf("write export: %v", err)
	}

	destinationChannelOut := runOK("", "channel", "create", "--name", "portable-import", "--repo", repoDir, "--json")
	var destinationChannel struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(destinationChannelOut), &destinationChannel); err != nil || destinationChannel.ID == 0 || destinationChannel.ID == channel.ID {
		t.Fatalf("destination channel JSON: %v\n%s", err, destinationChannelOut)
	}
	destinationThreadOut := runOK("", "thread", "new", "--channel", "portable-import", "--repo", repoDir, "--title", "import destination", "--json")
	var destinationThread struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(destinationThreadOut), &destinationThread); err != nil || destinationThread.ID == 0 || destinationThread.ID == thread.ID {
		t.Fatalf("destination thread JSON: %v\n%s", err, destinationThreadOut)
	}
	destinationThreadID := strconv.FormatInt(destinationThread.ID, 10)
	runOK("", "message", "send", "--thread", destinationThreadID, "--text", "destination seed", "--as", "destination", "--json")

	importOut := runOK("", "thread", "import", "--file", exportPath, "--thread", destinationThreadID)
	parts := strings.Fields(importOut)
	if len(parts) != 2 || parts[0] != "thread" {
		t.Fatalf("import output = %q", importOut)
	}
	importedID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || importedID != destinationThread.ID {
		t.Fatalf("imported thread ID = %q, want %d: %v", importOut, destinationThread.ID, err)
	}

	var channels []struct {
		ID   int64  `json:"ID"`
		Name string `json:"Name"`
	}
	if err := json.Unmarshal([]byte(runOK("", "channel", "list", "--repo", repoDir, "--json")), &channels); err != nil {
		t.Fatalf("channel list JSON: %v", err)
	}
	var destinationChannelListed bool
	for _, item := range channels {
		if item.Name == "portable-import" && item.ID == destinationChannel.ID {
			destinationChannelListed = true
		}
	}
	if !destinationChannelListed {
		t.Fatalf("destination channel identity missing: %+v", channels)
	}

	var importedThreads []struct {
		ID        int64  `json:"ID"`
		ChannelID int64  `json:"ChannelID"`
		Title     string `json:"Title"`
	}
	if err := json.Unmarshal([]byte(runOK("", "thread", "list", "--channel", "portable-import", "--repo", repoDir, "--json")), &importedThreads); err != nil {
		t.Fatalf("imported thread list JSON: %v", err)
	}
	var importedThreadFound bool
	for _, item := range importedThreads {
		if item.ID == importedID {
			importedThreadFound = true
			if item.ChannelID != destinationChannel.ID || item.Title != "import destination" {
				t.Fatalf("imported thread identity = %+v, destination channel %d", item, destinationChannel.ID)
			}
		}
	}
	if !importedThreadFound {
		t.Fatalf("imported thread %d missing from %+v", importedID, importedThreads)
	}

	imported := readEvents(runOK("", "agent", "read", "--thread", destinationThreadID, "--json"))
	if len(imported) != 6 || imported[0].Type != "message" || imported[0].Seq != 1 || imported[0].Content != "destination seed" || imported[0].Role != "user" || imported[0].AuthorType != "human" {
		t.Fatalf("destination seed = %+v", imported)
	}
	assertStream(imported[1:], "imported")
	if imported[3].Seq == rootSeq || imported[4].Seq == reply.Seq || imported[4].ParentSeq == rootSeq || imported[5].MessageSeq == rootSeq {
		t.Fatalf("imported references retained source identity: source root=%d reply=%d imported=%+v", rootSeq, reply.Seq, imported)
	}

	appendInput := "{\"type\":\"message\",\"parent_seq\":" + strconv.FormatInt(reply.Seq, 10) + ",\"name\":\"portable-agent\",\"author_type\":\"agent\",\"role\":\"assistant\",\"content\":\"portable appended\"}\n" +
		"{\"type\":\"reaction\",\"message_seq\":" + strconv.FormatInt(reply.Seq+1, 10) + ",\"name\":\"portable-agent\",\"author_type\":\"agent\",\"emoji\":\"👀\"}\n"
	appendResult := runCLIResult(t, env, bin, appendInput, "agent", "append", "--thread", threadID, "--file", "-", "--agent-id", "portable-agent")
	if appendResult.exitCode != 0 || appendResult.stdout != "" || appendResult.stderr != "" {
		t.Fatalf("agent append: exit=%d stdout=%q stderr=%q", appendResult.exitCode, appendResult.stdout, appendResult.stderr)
	}
	appended := readEvents(runOK("", "agent", "read", "--thread", threadID, "--after-seq", strconv.FormatInt(reply.Seq, 10), "--json"))
	assertCursor(appended, "appended", []int64{reply.Seq + 1}, []int64{rootSeq, reply.Seq + 1})
	if appended[0].ParentSeq != reply.Seq || appended[0].AuthorType != "agent" || appended[0].Content != "portable appended" {
		t.Fatalf("appended message = %+v", appended[0])
	}
	if appended[1].MessageSeq != rootSeq || appended[1].Emoji != "+1" {
		t.Fatalf("earlier reaction = %+v", appended[1])
	}
	if appended[2].MessageSeq != appended[0].Seq || appended[2].AuthorType != "agent" || appended[2].Emoji != "👀" {
		t.Fatalf("appended reaction = %+v", appended[2])
	}

	failure := runCLIResult(t, env, bin, "", "message", "send", "--thread", threadID, "--reply-to-seq", "0", "--text", "-", "--agent-id", "portable-agent")
	assertE2ECLIErrorResult(t, failure, "BAD_ARGS")

	stop()
}

func TestE2E_AgentAppendAtomicFailure(t *testing.T) {
	tmpDir := t.TempDir()
	home := filepath.Join(tmpDir, "fluffle")
	bin := buildTestBinary(t, tmpDir)
	repoDir := t.TempDir()
	gitInit(t, repoDir)
	env := []string{"FLUFFLE_HOME=" + home}
	stop := startAgentDaemon(t, bin, home)

	runOK := func(args ...string) string {
		t.Helper()
		result := runCLIResult(t, env, bin, "", args...)
		if result.exitCode != 0 {
			t.Fatalf("flf %v: exit=%d stdout=%q stderr=%q", args, result.exitCode, result.stdout, result.stderr)
		}
		if result.stderr != "" {
			t.Fatalf("flf %v wrote stderr: %q", args, result.stderr)
		}
		return result.stdout
	}

	channelOut := runOK("channel", "create", "--name", "atomic", "--repo", repoDir, "--json")
	var channel struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(channelOut), &channel); err != nil || channel.ID == 0 {
		t.Fatalf("channel JSON: %v\n%s", err, channelOut)
	}
	threadOut := runOK("thread", "new", "--channel", "atomic", "--repo", repoDir, "--title", "atomic batch", "--json")
	var thread struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(threadOut), &thread); err != nil || thread.ID == 0 {
		t.Fatalf("thread JSON: %v\n%s", err, threadOut)
	}
	threadID := strconv.FormatInt(thread.ID, 10)

	validAndInvalid := "{\"type\":\"message\",\"name\":\"alice\",\"role\":\"user\",\"content\":\"must roll back\"}\n" +
		"{\"type\":\"reaction\",\"message_seq\":999,\"name\":\"alice\",\"emoji\":\"+1\"}\n"
	lateFailure := runCLIResult(t, env, bin, validAndInvalid, "agent", "append", "--thread", threadID, "--file", "-", "--agent-id", "atomic-agent")
	assertE2ECLIErrorResult(t, lateFailure, "THREAD_NOT_FOUND")
	empty := runCLIResult(t, env, bin, "", "agent", "read", "--thread", threadID, "--json")
	if empty.exitCode != 0 || empty.stderr != "" || empty.stdout != "" {
		t.Fatalf("post-rollback read: exit=%d stdout=%q stderr=%q", empty.exitCode, empty.stdout, empty.stderr)
	}

	malformed := "{\"type\":\"message\",\"name\":\"alice\",\"role\":\"user\",\"content\":\"must not parse\"}\nnot-json\n"
	parseFailure := runCLIResult(t, env, bin, malformed, "agent", "append", "--thread", threadID, "--file", "-", "--agent-id", "atomic-agent")
	assertE2ECLIErrorResult(t, parseFailure, "BAD_JSONL")
	empty = runCLIResult(t, env, bin, "", "agent", "read", "--thread", threadID, "--json")
	if empty.exitCode != 0 || empty.stderr != "" || empty.stdout != "" {
		t.Fatalf("post-parse-failure read: exit=%d stdout=%q stderr=%q", empty.exitCode, empty.stdout, empty.stderr)
	}

	stop()
}

func TestE2E_ReactionAfterCursorSurvivesOnOlderMessage(t *testing.T) {
	tmpDir := t.TempDir()
	home := filepath.Join(tmpDir, "fluffle")
	bin := buildTestBinary(t, tmpDir)
	repoDir := t.TempDir()
	gitInit(t, repoDir)
	env := []string{"FLUFFLE_HOME=" + home}
	stop := startAgentDaemon(t, bin, home)

	runOK := func(args ...string) string {
		t.Helper()
		result := runCLIResult(t, env, bin, "", args...)
		if result.exitCode != 0 {
			t.Fatalf("flf %v: exit=%d stdout=%q stderr=%q", args, result.exitCode, result.stdout, result.stderr)
		}
		if result.stderr != "" {
			t.Fatalf("flf %v wrote stderr: %q", args, result.stderr)
		}
		return result.stdout
	}

	channelOut := runOK("channel", "create", "--name", "cursor-reactions", "--repo", repoDir, "--json")
	var channel struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(channelOut), &channel); err != nil || channel.ID == 0 {
		t.Fatalf("channel JSON: %v\n%s", err, channelOut)
	}
	threadOut := runOK("thread", "new", "--channel", "cursor-reactions", "--repo", repoDir, "--title", "late reactions", "--json")
	var thread struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(threadOut), &thread); err != nil || thread.ID == 0 {
		t.Fatalf("thread JSON: %v\n%s", err, threadOut)
	}
	threadID := strconv.FormatInt(thread.ID, 10)
	for i := 0; i < 3; i++ {
		runOK("message", "send", "--thread", threadID, "--text", "seed "+strconv.Itoa(i+1), "--as", "seed", "--json")
	}
	cursor := "3"

	readCursor := func() (messages, reactions []map[string]any) {
		t.Helper()
		raw := runOK("agent", "read", "--thread", threadID, "--after-seq", cursor, "--json")
		for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var event map[string]any
			if err := json.Unmarshal([]byte(line), &event); err != nil {
				t.Fatalf("invalid JSONL: %v\n%s", err, raw)
			}
			if event["type"] == "reaction" {
				reactions = append(reactions, event)
			} else {
				messages = append(messages, event)
			}
		}
		return messages, reactions
	}

	messages, reactions := readCursor()
	if len(messages) != 0 || len(reactions) != 0 {
		t.Fatalf("initial cursor read = %d messages %d reactions, want none", len(messages), len(reactions))
	}

	runOK("react", "add", "--thread", threadID, "--message-seq", "1", "--emoji", "👀", "--agent-id", "late-agent", "--as", "late-agent")

	messages, reactions = readCursor()
	if len(messages) != 0 {
		t.Fatalf("cursor read returned older messages: %+v", messages)
	}
	if len(reactions) != 1 {
		t.Fatalf("cursor read reactions = %+v, want the late reaction on sequence 1", reactions)
	}
	if reactions[0]["message_seq"] != float64(1) || reactions[0]["emoji"] != "👀" || reactions[0]["name"] != "late-agent" || reactions[0]["author_type"] != "agent" {
		t.Fatalf("late reaction = %+v", reactions[0])
	}

	stop()
}

type e2eAgentListItem struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Command     string `json:"command"`
	Reply       string `json:"reply"`
	Source      string `json:"source"`
}

var stdoutEventLine = regexp.MustCompile(`(?m)^\d+\tstdout\t.*the answer`)

func e2eErrorMessage(t *testing.T, envelope string) string {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal([]byte(envelope), &fields); err != nil {
		t.Fatalf("invalid error envelope: %v\n%s", err, envelope)
	}
	message, ok := fields["message"].(string)
	if !ok || message == "" {
		t.Fatalf("error envelope = %#v, want a message", fields)
	}
	return message
}

func e2eLines(t *testing.T, output string) []string {
	t.Helper()
	trimmed := strings.TrimSuffix(output, "\n")
	if trimmed == "" {
		t.Fatalf("output = %q, want at least one line", output)
	}
	return strings.Split(trimmed, "\n")
}

func decodeE2EAgentList(t *testing.T, raw string) map[string]e2eAgentListItem {
	t.Helper()
	var payload struct {
		Agents []e2eAgentListItem `json:"agents"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("agent list JSON: %v\n%s", err, raw)
	}
	byName := map[string]e2eAgentListItem{}
	for _, item := range payload.Agents {
		byName[item.Name] = item
	}
	return byName
}

func TestE2E_AgentListCmd(t *testing.T) {
	tmpDir := t.TempDir()
	home := filepath.Join(tmpDir, "fluffle")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := buildTestBinary(t, tmpDir)
	env := []string{"FLUFFLE_HOME=" + home}

	repoDir := t.TempDir()
	gitInit(t, repoDir)
	spacedDir := filepath.Join(t.TempDir(), "repo with space")
	if err := os.MkdirAll(spacedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInit(t, spacedDir)

	repoConfig := "[[agents]]\nname=\"local\"\ncommand=\"/bin/local\"\ndescription=\"repo agent\"\n"
	if err := os.WriteFile(filepath.Join(repoDir, ".flf.toml"), []byte(repoConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	spacedConfig := "[[agents]]\nname=\"spaced\"\ncommand=\"/bin/spaced\"\n"
	if err := os.WriteFile(filepath.Join(spacedDir, ".flf.toml"), []byte(spacedConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	globalConfig := "[[agents]]\nname=\"shared\"\ncommand=\"/bin/shared\"\nreply=\"cli\"\n"
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(globalConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	stop := startAgentDaemon(t, bin, home)
	runOK := func(args ...string) string {
		t.Helper()
		result := runCLIResult(t, env, bin, "", args...)
		if result.exitCode != 0 {
			t.Fatalf("flf %v: exit=%d stdout=%q stderr=%q", args, result.exitCode, result.stdout, result.stderr)
		}
		if result.stderr != "" {
			t.Fatalf("flf %v wrote stderr: %q", args, result.stderr)
		}
		return result.stdout
	}

	agents := decodeE2EAgentList(t, runOK("agent", "list", "--repo", repoDir, "--json"))
	local, ok := agents["local"]
	if !ok {
		t.Fatalf("agents = %+v, want the repo entry", agents)
	}
	if local.Description != "repo agent" || local.Command != "/bin/local" || local.Reply != "auto" {
		t.Fatalf("repo entry = %+v", local)
	}
	if local.Source != filepath.Join(repoDir, ".flf.toml") {
		t.Fatalf("repo entry source = %q, want %q", local.Source, filepath.Join(repoDir, ".flf.toml"))
	}
	shared, ok := agents["shared"]
	if !ok {
		t.Fatalf("agents = %+v, want the global entry alongside the repo entry", agents)
	}
	if shared.Reply != "cli" || shared.Command != "/bin/shared" || shared.Description != "" {
		t.Fatalf("global entry = %+v", shared)
	}
	if shared.Source != filepath.Join(home, "config.toml") {
		t.Fatalf("global entry source = %q, want %q", shared.Source, filepath.Join(home, "config.toml"))
	}

	fromRepo := runCLIResultInDir(t, env, bin, repoDir, "", "agent", "list", "--json")
	if fromRepo.exitCode != 0 || fromRepo.stderr != "" {
		t.Fatalf("agent list from the repo dir: exit=%d stdout=%q stderr=%q", fromRepo.exitCode, fromRepo.stdout, fromRepo.stderr)
	}
	agents = decodeE2EAgentList(t, fromRepo.stdout)
	if len(agents) != 1 || agents["shared"].Command != "/bin/shared" {
		t.Fatalf("agents without --repo = %+v, want only the global entry even when the cwd is a repo", agents)
	}

	assertE2ECLIErrorResult(t, runCLIResult(t, env, bin, "", "agent", "list", "--repo", filepath.Join(tmpDir, "no-such-repo")), "NOT_A_GIT_REPO")

	agents = decodeE2EAgentList(t, runOK("agent", "list", "--repo", spacedDir, "--json"))
	if spaced, ok := agents["spaced"]; !ok || spaced.Source != filepath.Join(spacedDir, ".flf.toml") {
		t.Fatalf("agents for a spaced repo path = %+v, want the spaced entry", agents)
	}

	lines := e2eLines(t, runOK("agent", "list", "--repo", repoDir))
	want := []string{
		"local\t/bin/local\tauto\t" + filepath.Join(repoDir, ".flf.toml") + "\trepo agent",
		"shared\t/bin/shared\tcli\t" + filepath.Join(home, "config.toml") + "\t-",
	}
	if len(lines) != len(want) {
		t.Fatalf("human lines = %d, want %d: %q", len(lines), len(want), lines)
	}
	for i, line := range want {
		if lines[i] != line {
			t.Fatalf("human line %d = %q, want %q", i, lines[i], line)
		}
	}

	stop()
}

func TestE2E_AgentSessionCmd(t *testing.T) {
	tmpDir := t.TempDir()
	home := filepath.Join(tmpDir, "fluffle")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := buildTestBinary(t, tmpDir)
	env := []string{"FLUFFLE_HOME=" + home}
	repoDir := t.TempDir()
	gitInit(t, repoDir)

	probe := filepath.Join(tmpDir, "probe-agent")
	if err := os.WriteFile(probe, []byte("#!/bin/sh\ncat > /dev/null\nprintf 'the answer'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := "[[agents]]\nname=\"probe\"\ncommand=\"" + probe + "\"\nreply=\"stdout\"\ntimeout_secs=20\n"
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	stop := startAgentDaemon(t, bin, home)
	runOK := func(args ...string) string {
		t.Helper()
		result := runCLIResult(t, env, bin, "", args...)
		if result.exitCode != 0 {
			t.Fatalf("flf %v: exit=%d stdout=%q stderr=%q", args, result.exitCode, result.stdout, result.stderr)
		}
		if result.stderr != "" {
			t.Fatalf("flf %v wrote stderr: %q", args, result.stderr)
		}
		return result.stdout
	}

	channelOut := runOK("channel", "create", "--name", "eng", "--repo", repoDir, "--json")
	var channel struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(channelOut), &channel); err != nil || channel.ID <= 0 {
		t.Fatalf("channel create: err=%v out=%s", err, channelOut)
	}
	threadOut := runOK("thread", "new", "--channel", "eng", "--repo", repoDir, "--title", "t", "--json")
	var thread struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(threadOut), &thread); err != nil || thread.ID <= 0 {
		t.Fatalf("thread new: err=%v out=%s", err, threadOut)
	}
	threadID := strconv.FormatInt(thread.ID, 10)
	runOK("message", "send", "--thread", threadID, "--text", "hello", "--as", "alice", "--json")
	runOK("message", "send", "--thread", threadID, "--text", "@probe hi", "--as", "alice", "--json")

	// A fresh home holds one session, the one this mention creates, so it is id 1.
	// The poll covers the real race: the run finishing after the trigger returns.
	var (
		last    cliResult
		payload sessionPayload
	)
	if !waitFor(t, 40*time.Second, func() bool {
		last = runCLIResult(t, env, bin, "", "agent", "session", "--id", "1", "--json")
		if last.exitCode != 0 || last.stderr != "" {
			return false
		}
		payload = sessionPayload{}
		if err := json.Unmarshal([]byte(last.stdout), &payload); err != nil {
			return false
		}
		return payload.Session.Status == store.SessionSucceeded
	}) {
		t.Fatalf("session never succeeded: exit=%d stdout=%q stderr=%q", last.exitCode, last.stdout, last.stderr)
	}
	sess := payload.Session
	if sess.AgentName != "probe" || sess.Status != store.SessionSucceeded || sess.ReplyMode != "stdout" {
		t.Fatalf("session = %+v", sess)
	}
	if sess.ThreadID != thread.ID || sess.TriggerMessageID <= 0 {
		t.Fatalf("session references = %+v, want thread %d", sess, thread.ID)
	}
	canonicalRepo, err := filepath.EvalSymlinks(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Cwd == nil || *sess.Cwd != canonicalRepo {
		t.Fatalf("session cwd = %v, want %q", sess.Cwd, canonicalRepo)
	}
	if sess.Command != probe {
		t.Fatalf("session command = %q, want %q", sess.Command, probe)
	}
	if sess.ExitCode == nil || *sess.ExitCode != 0 {
		t.Fatalf("session exit code = %v, want 0", sess.ExitCode)
	}
	if sess.Error != nil {
		t.Fatalf("session error = %q, want none", *sess.Error)
	}
	if sess.ReplyMessageID == nil {
		t.Fatalf("session has no reply message: %+v", sess)
	}

	byType := map[string]store.SessionEvent{}
	for i, event := range payload.Events {
		if event.Seq != int64(i+1) || event.SessionID != sess.ID {
			t.Fatalf("event %d = %+v", i, event)
		}
		byType[event.Type] = event
	}
	if byType[store.SessionEventPrompt].Content == "" {
		t.Fatalf("events = %+v, want a prompt", payload.Events)
	}
	if !strings.Contains(byType[store.SessionEventStdout].Content, "the answer") {
		t.Fatalf("events = %+v, want the agent stdout", payload.Events)
	}
	if byType[store.SessionEventExit].Content != "0" {
		t.Fatalf("events = %+v, want the exit code", payload.Events)
	}

	lines := e2eLines(t, runOK("agent", "session", "--id", "1"))
	if len(lines) < 5 {
		t.Fatalf("human lines = %d, want the header, command, exit, cwd, and reply: %q", len(lines), lines)
	}
	if !strings.HasPrefix(lines[0], "session 1  agent=probe  thread=") || !strings.Contains(lines[0], "  status=succeeded ") || !strings.HasSuffix(lines[0], "  reply=stdout") {
		t.Fatalf("session header = %q", lines[0])
	}
	if lines[1] != "command: "+probe {
		t.Fatalf("command line = %q, want %q", lines[1], "command: "+probe)
	}
	if lines[2] != "exit: 0" {
		t.Fatalf("exit line = %q, want %q", lines[2], "exit: 0")
	}
	if lines[3] != "cwd: "+canonicalRepo {
		t.Fatalf("cwd line = %q, want %q", lines[3], "cwd: "+canonicalRepo)
	}
	if lines[4] != "reply: message "+strconv.FormatInt(*sess.ReplyMessageID, 10) {
		t.Fatalf("reply line = %q", lines[4])
	}
	wantEvents := make([]string, 0, len(payload.Events))
	for _, event := range payload.Events {
		wantEvents = append(wantEvents, fmt.Sprintf("%d\t%s\t%s", event.Seq, event.Type, event.Content))
	}
	events := strings.Join(lines[5:], "\n")
	if events != strings.Join(wantEvents, "\n") {
		t.Fatalf("event lines = %q, want %q", events, strings.Join(wantEvents, "\n"))
	}
	if !stdoutEventLine.MatchString(events) {
		t.Fatalf("event lines lack a stdout event carrying the answer: %q", events)
	}

	stop()
}

func TestE2E_AgentSessionCmdMissingIsExitOne(t *testing.T) {
	tmpDir := t.TempDir()
	home := filepath.Join(tmpDir, "fluffle")
	bin := buildTestBinary(t, tmpDir)
	env := []string{"FLUFFLE_HOME=" + home}
	stop := startAgentDaemon(t, bin, home)

	assertE2ECLIErrorResult(t, runCLIResult(t, env, bin, "", "agent", "session", "--id", "9999"), "SESSION_NOT_FOUND")

	stop()
}

func TestE2E_AgentSessionCmdRequiresID(t *testing.T) {
	tmpDir := t.TempDir()
	home := filepath.Join(tmpDir, "fluffle")
	bin := buildTestBinary(t, tmpDir)
	env := []string{"FLUFFLE_HOME=" + home}

	assertE2ECLIErrorResult(t, runCLIResult(t, env, bin, "", "agent", "session"), "BAD_ARGS")
	assertE2ECLIErrorResult(t, runCLIResult(t, env, bin, "", "agent", "session", "--id", "0"), "BAD_ARGS")
	assertE2ECLIErrorResult(t, runCLIResult(t, env, bin, "", "agent", "session", "--id", "-1"), "BAD_ARGS")
	assertE2ECLIErrorResult(t, runCLIResult(t, env, bin, "", "agent", "session", "--id", "abc"), "BAD_ARGS")

	missing := runCLIResult(t, env, bin, "", "agent", "session")
	if message := e2eErrorMessage(t, missing.stderr); !strings.Contains(message, "--id") {
		t.Fatalf("error message = %q, want it to name the missing flag", message)
	}

	usage := e2eErrorMessage(t, runCLIResult(t, env, bin, "", "agent", "bogus").stderr)
	for _, want := range []string{"read", "append", "list", "session"} {
		if !strings.Contains(usage, want) {
			t.Fatalf("usage %q does not name %q", usage, want)
		}
	}
}

const e2eHTTPTimeout = 5 * time.Second

// jsonIDField reads one id out of a --json response. flf channel create --json
// emits "id" from the anonymous struct it falls back to and "ID" from a
// store.Channel with no json tag, so either key is a real answer. runCLI only
// logs a non-zero exit, so parsing the output is what catches a failed create.
func jsonIDField(t *testing.T, out, field string) string {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	if v, ok := payload[field]; ok {
		return fmt.Sprintf("%v", v)
	}
	if field == "id" {
		if v, ok := payload["ID"]; ok {
			return fmt.Sprintf("%v", v)
		}
	}
	t.Fatalf("field %q missing from %q", field, out)
	return ""
}

// e2eProcessGroupAlive reports whether any process remains in pgid, and only for
// a pgid that is a real group leader: kill(-pgid, 0) answers ESRCH for a live pid
// that leads no group, so it says nothing about liveness on its own. A pid entry
// that still exists counts as alive, the rule processAlive (main.go:1250) follows
// for unreaped zombies; darwin answers EPERM rather than ESRCH for a group left
// holding only zombies, and linux answers 0, so both count as alive here.
func e2eProcessGroupAlive(pgid int) bool {
	err := syscall.Kill(-pgid, syscall.Signal(0))
	return err == nil || err == syscall.EPERM
}

func e2eGetJSON(t *testing.T, url string, into any) {
	t.Helper()
	client := &http.Client{Timeout: e2eHTTPTimeout}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
		t.Fatalf("GET %s: decode: %v", url, err)
	}
}

func TestE2E_AgentMentionRunsAgentAndStoresSession(t *testing.T) {
	tmpDir := t.TempDir()
	home := filepath.Join(tmpDir, "fluffle")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := buildTestBinary(t, tmpDir)
	env := []string{"FLUFFLE_HOME=" + home}

	probe := filepath.Join(tmpDir, "probe-agent")
	// pwd goes to stdout because sess.Cwd is copied from the thread's repo path
	// when the row is created, so it would stay correct with the run-time cwd
	// left unwired; the agent's own stdout is the only evidence of where it ran.
	script := "#!/bin/sh\ncat > /dev/null\npwd\nprintf 'the answer'\n"
	if err := os.WriteFile(probe, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	config := "[[agents]]\nname=\"probe\"\ncommand=\"" + probe + "\"\nreply=\"stdout\"\ntimeout_secs=20\n"
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	repo := t.TempDir()
	gitInit(t, repo)
	// The daemon canonicalizes the repo path and t.TempDir hands back a symlinked
	// one on macOS, so both sides of the cwd comparison use the canonical form.
	canonicalRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}

	stop := startAgentDaemon(t, bin, home)
	port := waitForDaemonReady(t, home, 10*time.Second)

	channelID := jsonIDField(t, runCLI(t, env, bin, "channel", "create", "--name", "eng", "--repo", repo, "--json"), "id")
	threadArg := jsonIDField(t, runCLI(t, env, bin, "thread", "new", "--channel", "eng", "--repo", repo, "--title", "review", "--json"), "id")
	threadID, err := strconv.ParseInt(threadArg, 10, 64)
	if err != nil || channelID == "0" || threadID <= 0 {
		t.Fatalf("channel = %q thread = %q: %v", channelID, threadArg, err)
	}

	triggerOut := runCLI(t, env, bin, "message", "send", "--thread", threadArg, "--text", "@probe what is the status?", "--as", "alice", "--json")
	var trigger struct {
		Seq int64 `json:"seq"`
	}
	if err := json.Unmarshal([]byte(triggerOut), &trigger); err != nil || trigger.Seq <= 0 {
		t.Fatalf("trigger message: err=%v out=%s", err, triggerOut)
	}

	var replySeq int64
	wantReply := canonicalRepo + "\nthe answer"
	if !waitFor(t, 40*time.Second, func() bool {
		out := runCLI(t, env, bin, "agent", "read", "--thread", threadArg, "--json")
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var entry struct {
				Type       string `json:"type"`
				Name       string `json:"name"`
				AuthorType string `json:"author_type"`
				Content    string `json:"content"`
				Seq        int64  `json:"seq"`
			}
			if err := json.Unmarshal([]byte(line), &entry); err != nil {
				t.Fatalf("bad agent read line %q: %v", line, err)
			}
			if entry.Type == "message" && entry.AuthorType == "agent" && entry.Name == "probe" {
				if entry.Content != wantReply {
					t.Fatalf("agent reply content = %q, want %q", entry.Content, wantReply)
				}
				replySeq = entry.Seq
				return true
			}
		}
		return false
	}) {
		t.Fatalf("agent never replied within 40s: %s", runCLI(t, env, bin, "agent", "read", "--thread", threadArg, "--json"))
	}
	if replySeq <= trigger.Seq {
		t.Fatalf("reply seq = %d, want greater than the trigger seq %d", replySeq, trigger.Seq)
	}

	// The reply message is appended before the session goes terminal, so a
	// single read here can still see a running session.
	var (
		last    cliResult
		payload sessionPayload
	)
	if !waitFor(t, 40*time.Second, func() bool {
		last = runCLIResult(t, env, bin, "", "agent", "session", "--id", "1", "--json")
		if last.exitCode != 0 {
			return false
		}
		payload = sessionPayload{}
		if err := json.Unmarshal([]byte(last.stdout), &payload); err != nil {
			return false
		}
		return payload.Session.Status == store.SessionSucceeded
	}) {
		t.Fatalf("session never succeeded: exit=%d stdout=%q stderr=%q", last.exitCode, last.stdout, last.stderr)
	}
	sess := payload.Session
	if sess.Status != store.SessionSucceeded {
		t.Fatalf("session status = %q error %v", sess.Status, sess.Error)
	}
	if sess.AgentName != "probe" || sess.ReplyMode != "stdout" {
		t.Fatalf("session = %+v", sess)
	}
	if sess.ReplyMessageID == nil {
		t.Fatalf("session has no reply message id: %+v", sess)
	}
	if !strings.Contains(sess.Command, probe) {
		t.Fatalf("session command = %q, want it to contain %q", sess.Command, probe)
	}
	if sess.Cwd == nil || *sess.Cwd != canonicalRepo {
		t.Fatalf("session cwd = %v, want %q", sess.Cwd, canonicalRepo)
	}
	types := map[string]bool{}
	stdoutEvent := ""
	for _, event := range payload.Events {
		types[event.Type] = true
		if event.Type == store.SessionEventStdout {
			stdoutEvent += event.Content
		}
	}
	for _, want := range []string{store.SessionEventPrompt, store.SessionEventStdout, store.SessionEventExit} {
		if !types[want] {
			t.Fatalf("missing %q event; got %v", want, types)
		}
	}
	// The agent reports the directory it was run in, which is what repo-anchoring
	// promises; sess.Cwd above only proves the column was filled from the thread.
	if strings.TrimSpace(stdoutEvent) != canonicalRepo+"\nthe answer" {
		t.Fatalf("agent stdout = %q, want the agent to have run in %q", stdoutEvent, canonicalRepo)
	}

	human := runCLI(t, env, bin, "agent", "session", "--id", "1")
	if !strings.Contains(human, "probe") || !strings.Contains(human, "the answer") {
		t.Fatalf("human session output = %q", human)
	}

	// The pane the TUI renders is driven by the thread-scoped list, which
	// carries no events: the TUI fetches the detail for a session it keys on.
	// A second thread with its own session is what makes the scoping falsifiable;
	// selecting only by id would pass against an unfiltered list.
	otherArg := jsonIDField(t, runCLI(t, env, bin, "thread", "new", "--channel", "eng", "--repo", repo, "--title", "other", "--json"), "id")
	otherThread, err := strconv.ParseInt(otherArg, 10, 64)
	if err != nil || otherThread <= 0 || otherThread == threadID {
		t.Fatalf("second thread = %q, want a distinct thread: %v", otherArg, err)
	}
	runCLI(t, env, bin, "message", "send", "--thread", otherArg, "--text", "@probe over here", "--as", "alice", "--json")
	var otherSession store.Session
	if !waitFor(t, 40*time.Second, func() bool {
		result := runCLIResult(t, env, bin, "", "agent", "session", "--id", "2", "--json")
		if result.exitCode != 0 {
			return false
		}
		var other sessionPayload
		if err := json.Unmarshal([]byte(result.stdout), &other); err != nil {
			return false
		}
		otherSession = other.Session
		return otherSession.Status == store.SessionSucceeded
	}) {
		t.Fatalf("second thread session never succeeded")
	}

	base := "http://127.0.0.1:" + strconv.Itoa(port)
	var listed []store.Session
	e2eGetJSON(t, base+"/v1/threads/"+strconv.FormatInt(threadID, 10)+"/sessions", &listed)
	if len(listed) != 1 {
		t.Fatalf("thread %d sessions = %+v, want only its own session %d", threadID, listed, sess.ID)
	}
	found := listed[0]
	if found.ID != sess.ID {
		t.Fatalf("thread %d session = %d, want %d", threadID, found.ID, sess.ID)
	}
	if found.AgentName != "probe" || found.Status != store.SessionSucceeded ||
		found.TriggerMessageID != sess.TriggerMessageID || found.ThreadID != threadID {
		t.Fatalf("listed session = %+v", found)
	}
	if otherSession.ThreadID != otherThread || otherSession.ID == sess.ID {
		t.Fatalf("second session = %+v, want a distinct session on thread %d", otherSession, otherThread)
	}
	var otherListed []store.Session
	e2eGetJSON(t, base+"/v1/threads/"+strconv.FormatInt(otherThread, 10)+"/sessions", &otherListed)
	if len(otherListed) != 1 || otherListed[0].ID != otherSession.ID {
		t.Fatalf("thread %d sessions = %+v, want only session %d", otherThread, otherListed, otherSession.ID)
	}
	var detail sessionPayload
	e2eGetJSON(t, base+"/v1/sessions/"+strconv.FormatInt(sess.ID, 10), &detail)
	if detail.Session.ID != sess.ID || len(detail.Events) == 0 {
		t.Fatalf("session detail = %+v, want session %d with events", detail, sess.ID)
	}

	stop()
}

func TestE2E_AgentMentionWithMissingBinaryFailsVisibly(t *testing.T) {
	tmpDir := t.TempDir()
	home := filepath.Join(tmpDir, "fluffle")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := buildTestBinary(t, tmpDir)
	env := []string{"FLUFFLE_HOME=" + home}

	repo := t.TempDir()
	gitInit(t, repo)
	config := "[[agents]]\nname=\"ghost\"\ncommand=\"/definitely/not/here\"\nreply=\"stdout\"\ntimeout_secs=5\n"
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	stop := startAgentDaemon(t, bin, home)

	channelID := jsonIDField(t, runCLI(t, env, bin, "channel", "create", "--name", "eng", "--repo", repo, "--json"), "id")
	threadArg := jsonIDField(t, runCLI(t, env, bin, "thread", "new", "--channel", "eng", "--repo", repo, "--title", "t", "--json"), "id")
	if channelID == "0" || threadArg == "0" {
		t.Fatalf("channel = %q thread = %q, want real ids", channelID, threadArg)
	}
	runCLI(t, env, bin, "message", "send", "--thread", threadArg, "--text", "@ghost hello", "--as", "alice", "--json")

	var (
		last   cliResult
		failed store.Session
	)
	if !waitFor(t, 30*time.Second, func() bool {
		last = runCLIResult(t, env, bin, "", "agent", "session", "--id", "1", "--json")
		if last.exitCode != 0 {
			return false
		}
		var payload sessionPayload
		if err := json.Unmarshal([]byte(last.stdout), &payload); err != nil {
			return false
		}
		failed = payload.Session
		return failed.Status == store.SessionFailed && failed.Error != nil
	}) {
		t.Fatalf("a missing agent binary must fail the session visibly; got exit=%d stdout=%q stderr=%q", last.exitCode, last.stdout, last.stderr)
	}
	if !strings.Contains(*failed.Error, "/definitely/not/here") {
		t.Fatalf("session error = %q, want it to name the missing command", *failed.Error)
	}

	stop()
}

func TestE2E_AgentDaemonStopReapsRunningAgent(t *testing.T) {
	tmpDir := t.TempDir()
	home := filepath.Join(tmpDir, "fluffle")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := buildTestBinary(t, tmpDir)
	env := []string{"FLUFFLE_HOME=" + home}
	repo := t.TempDir()
	gitInit(t, repo)

	pidFile := filepath.Join(tmpDir, "probe.pid")
	probe := filepath.Join(tmpDir, "blocking-agent")
	if err := os.WriteFile(probe, []byte("#!/bin/sh\necho $$ > \""+pidFile+"\"\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := "[[agents]]\nname=\"blocker\"\ncommand=\"" + probe + "\"\nreply=\"stdout\"\ntimeout_secs=60\n"
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	stop := startAgentDaemon(t, bin, home)
	_, daemonPID := readDaemonJSON(t, home)

	channelID := jsonIDField(t, runCLI(t, env, bin, "channel", "create", "--name", "eng", "--repo", repo, "--json"), "id")
	threadArg := jsonIDField(t, runCLI(t, env, bin, "thread", "new", "--channel", "eng", "--repo", repo, "--title", "t", "--json"), "id")
	if channelID == "0" || threadArg == "0" {
		t.Fatalf("channel = %q thread = %q, want real ids", channelID, threadArg)
	}
	runCLI(t, env, bin, "message", "send", "--thread", threadArg, "--text", "@blocker go", "--as", "alice", "--json")

	var (
		last cliResult
		sess store.Session
	)
	if !waitFor(t, 20*time.Second, func() bool {
		last = runCLIResult(t, env, bin, "", "agent", "session", "--id", "1", "--json")
		if last.exitCode != 0 {
			return false
		}
		var payload sessionPayload
		if err := json.Unmarshal([]byte(last.stdout), &payload); err != nil {
			return false
		}
		sess = payload.Session
		return sess.Status == store.SessionRunning
	}) {
		t.Fatalf("session never reached running: exit=%d stdout=%q stderr=%q", last.exitCode, last.stdout, last.stderr)
	}

	probePID := 0
	if !waitFor(t, 20*time.Second, func() bool {
		data, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		probePID, err = strconv.Atoi(strings.TrimSpace(string(data)))
		return err == nil && probePID > 0
	}) {
		t.Fatalf("probe never published its pid to %s", pidFile)
	}
	if !e2eProcessGroupAlive(probePID) {
		t.Fatalf("probe process group %d is already gone: the test would prove nothing", probePID)
	}
	t.Cleanup(func() {
		if e2eProcessGroupAlive(probePID) {
			_ = syscall.Kill(-probePID, syscall.SIGKILL)
		}
	})

	stopResult := runCLIResult(t, env, bin, "", "daemon", "stop")
	if stopResult.exitCode != 0 {
		t.Fatalf("daemon stop: exit=%d stdout=%q stderr=%q", stopResult.exitCode, stopResult.stdout, stopResult.stderr)
	}
	if !waitForProcessExit(daemonPID, 10*time.Second) {
		t.Fatalf("daemon %d survived flf daemon stop", daemonPID)
	}
	if !waitFor(t, 10*time.Second, func() bool { return !e2eProcessGroupAlive(probePID) }) {
		t.Fatalf("agent process group %d survived flf daemon stop: the Setpgid/Kill(-pgid) path leaks subprocesses", probePID)
	}
	stop()

	startAgentDaemon(t, bin, home)
	restarted := runCLIResult(t, env, bin, "", "agent", "session", "--id", "1", "--json")
	if restarted.exitCode != 0 {
		t.Fatalf("session after restart: exit=%d stdout=%q stderr=%q", restarted.exitCode, restarted.stdout, restarted.stderr)
	}
	var payload sessionPayload
	if err := json.Unmarshal([]byte(restarted.stdout), &payload); err != nil {
		t.Fatalf("session after restart: err=%v out=%s", err, restarted.stdout)
	}
	if payload.Session.ID != sess.ID {
		t.Fatalf("session after restart = %+v, want session %d", payload.Session, sess.ID)
	}
	if payload.Session.Status != store.SessionCanceled {
		t.Fatalf("session status after restart = %q, want %q", payload.Session.Status, store.SessionCanceled)
	}
	// Status alone cannot tell the two apart, because every daemon start runs
	// Reconcile, which rewrites running to canceled. Only the error text can:
	// "daemon restarted while ..." is written by that reconcile, so its presence
	// means the barrier never finished the row and startup covered for it.
	if payload.Session.Error == nil {
		t.Fatalf("session error = nil, want the reason the barrier recorded: %+v", payload.Session)
	}
	if strings.Contains(*payload.Session.Error, "daemon restarted while") {
		t.Fatalf("session error = %q: the row was left unfinished by daemon stop and startup reconcile masked it, so the shutdown barrier did not run", *payload.Session.Error)
	}
}
