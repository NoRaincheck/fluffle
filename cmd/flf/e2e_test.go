//go:build e2e

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
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

func runCLIStdin(t *testing.T, env []string, bin, input string, args ...string) (string, string) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = strings.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("flf %v: %v\n%s", args, err, stderr.String())
	}
	return strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String())
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
	if proc, _ := os.FindProcess(pid); proc != nil {
		if err := proc.Signal(os.Signal(nil)); err == nil {
			t.Fatalf("process %d still exists after shutdown", pid)
		}
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
	startDaemon(t, bin, home)
	waitForDaemonReady(t, home, 10*time.Second)

	out, _ := runCLIStdin(t, env, bin, "", "channel", "create", "--name", "portable", "--repo", repoDir, "--json")
	var channel struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &channel); err != nil {
		t.Fatal(err)
	}
	out, _ = runCLIStdin(t, env, bin, "", "thread", "new", "--channel", "portable", "--repo", repoDir, "--title", "portable loop", "--json")
	var thread struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &thread); err != nil {
		t.Fatal(err)
	}
	threadID := strconv.FormatInt(thread.ID, 10)

	runCLIStdin(t, env, bin, "", "message", "send", "--thread", threadID, "--text", "portable root", "--as", "alice", "--json")
	type portableLine struct {
		Type       string `json:"type"`
		Seq        int64  `json:"seq"`
		ParentSeq  int64  `json:"parent_seq"`
		MessageSeq int64  `json:"message_seq"`
		Name       string `json:"name"`
		AuthorType string `json:"author_type"`
		Content    string `json:"content"`
		Emoji      string `json:"emoji"`
	}
	readEvents := func(raw string) []portableLine {
		t.Helper()
		var events []portableLine
		for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
			var event portableLine
			if err := json.Unmarshal([]byte(line), &event); err != nil {
				t.Fatalf("invalid JSONL: %v\n%s", err, raw)
			}
			events = append(events, event)
		}
		return events
	}

	out, _ = runCLIStdin(t, env, bin, "", "agent", "read", "--thread", threadID, "--json")
	events := readEvents(out)
	if len(events) != 1 || events[0].Type != "message" || events[0].Name != "alice" || events[0].Content != "portable root" {
		t.Fatalf("initial events = %+v", events)
	}
	rootSeq := events[0].Seq

	out, _ = runCLIStdin(t, env, bin, "portable reply", "message", "send", "--thread", threadID, "--reply-to-seq", strconv.FormatInt(rootSeq, 10), "--text", "-", "--agent-id", "portable-agent", "--as", "portable-agent", "--json")
	var reply struct {
		Seq       int64 `json:"seq"`
		ParentSeq int64 `json:"parent_seq"`
	}
	if err := json.Unmarshal([]byte(out), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.ParentSeq != rootSeq || reply.Seq <= rootSeq {
		t.Fatalf("reply = %+v, root seq %d", reply, rootSeq)
	}

	out, _ = runCLIStdin(t, env, bin, "", "react", "add", "--thread", threadID, "--message-seq", strconv.FormatInt(rootSeq, 10), "--emoji", "+1", "--agent-id", "portable-agent", "--as", "portable-agent")
	if out != "ok" {
		t.Fatalf("reaction output = %q", out)
	}

	out, _ = runCLIStdin(t, env, bin, "", "agent", "read", "--thread", threadID, "--after-seq", strconv.FormatInt(rootSeq, 10), "--json")
	events = readEvents(out)
	if len(events) != 1 || events[0].Type != "message" || events[0].Seq != reply.Seq || events[0].ParentSeq != rootSeq {
		t.Fatalf("cursor events = %+v", events)
	}

	out, _ = runCLIStdin(t, env, bin, "", "agent", "read", "--thread", threadID, "--json")
	events = readEvents(out)
	var foundReaction bool
	for _, event := range events {
		if event.Type == "reaction" && event.MessageSeq == rootSeq && event.Name == "portable-agent" && event.Emoji == "+1" {
			foundReaction = true
		}
	}
	if !foundReaction {
		t.Fatalf("reaction event missing: %+v", events)
	}

	out, _ = runCLIStdin(t, env, bin, "", "inbox", "--json")
	var inbox []struct {
		ThreadID    int64  `json:"ThreadID"`
		ChannelID   int64  `json:"channel_id"`
		ChannelName string `json:"channel_name"`
		Content     string `json:"Content"`
	}
	if err := json.Unmarshal([]byte(out), &inbox); err != nil {
		t.Fatal(err)
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

	appendInput := "{\"type\":\"message\",\"name\":\"portable-agent\",\"author_type\":\"agent\",\"role\":\"assistant\",\"content\":\"portable appended\"}\n"
	stdout, stderr := runCLIStdin(t, env, bin, appendInput, "agent", "append", "--thread", threadID, "--file", "-", "--agent-id", "portable-agent")
	if stdout != "" || stderr != "" {
		t.Fatalf("agent append output = %q, stderr = %q", stdout, stderr)
	}
	out, _ = runCLIStdin(t, env, bin, "", "agent", "read", "--thread", threadID, "--after-seq", strconv.FormatInt(reply.Seq, 10), "--json")
	events = readEvents(out)
	if len(events) != 1 || events[0].Type != "message" || events[0].Seq <= reply.Seq || events[0].Name != "portable-agent" || events[0].AuthorType != "agent" || events[0].Content != "portable appended" {
		t.Fatalf("appended events = %+v", events)
	}

	runCLIStdin(t, env, bin, "", "daemon", "stop")
}
