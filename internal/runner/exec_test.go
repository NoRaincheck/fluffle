package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type collector struct {
	mu     sync.Mutex
	stdout strings.Builder
	stderr strings.Builder
}

func (c *collector) fn(stream string, b []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if stream == "stdout" {
		c.stdout.Write(b)
	} else {
		c.stderr.Write(b)
	}
}

func (c *collector) out() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stdout.String()
}

func (c *collector) errOut() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stderr.String()
}

func run(t *testing.T, req Request) (Result, error) {
	t.Helper()
	return NewExec().Run(context.Background(), req)
}

func TestCapturesStdout(t *testing.T) {
	c := &collector{}
	res, err := run(t, Request{Command: "echo", Args: []string{"hello"}, Timeout: 10 * time.Second, OnChunk: c.fn})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d", res.ExitCode)
	}
	if strings.TrimSpace(c.out()) != "hello" {
		t.Fatalf("stdout = %q", c.out())
	}
}

func TestDeliversStdinWhenNoPromptArg(t *testing.T) {
	c := &collector{}
	if _, err := run(t, Request{Command: "cat", Stdin: "piped", Timeout: 10 * time.Second, OnChunk: c.fn}); err != nil {
		t.Fatal(err)
	}
	if c.out() != "piped" {
		t.Fatalf("stdout = %q", c.out())
	}
}

func TestNonZeroExitIsNotAnError(t *testing.T) {
	res, err := run(t, Request{Command: "sh", Args: []string{"-c", "exit 3"}, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 3 {
		t.Fatalf("exit = %d, want 3", res.ExitCode)
	}
}

func TestCapturesStderr(t *testing.T) {
	c := &collector{}
	if _, err := run(t, Request{Command: "sh", Args: []string{"-c", "echo oops 1>&2"}, Timeout: 10 * time.Second, OnChunk: c.fn}); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(c.errOut()) != "oops" {
		t.Fatalf("stderr = %q", c.errOut())
	}
}

func TestTimeoutReturnsErrTimeout(t *testing.T) {
	start := time.Now()
	_, err := run(t, Request{Command: "sleep", Args: []string{"30"}, Timeout: 200 * time.Millisecond})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("timeout did not fire promptly")
	}
}

func TestTimeoutKillsGrandchildren(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "grandchild.pid")
	script := "sh -c 'echo $$ > " + marker + "; sleep 30' & wait"
	_, err := run(t, Request{Command: "sh", Args: []string{"-c", script}, Timeout: 500 * time.Millisecond})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if !waitForFile(t, marker, 5*time.Second) {
		t.Fatal("grandchild never recorded its pid")
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	if !waitForGone(t, pid, 5*time.Second) {
		t.Fatalf("grandchild %d survived killpg", pid)
	}
}

func TestOversizeOutputKeepsHeadAndTailWithMarker(t *testing.T) {
	c := &collector{}
	script := `printf 'FLUFFLE_HEAD_SENTINEL\n'; yes 0123456789abcdef | head -c 2000000; printf '\nFLUFFLE_TAIL_SENTINEL\n'`
	if _, err := run(t, Request{
		Command: "sh",
		Args:    []string{"-c", script},
		Timeout: 30 * time.Second,
		OnChunk: c.fn,
	}); err != nil {
		t.Fatal(err)
	}
	got := c.out()
	if len(got) != MaxStreamBytes {
		t.Fatalf("stdout len = %d, want exactly %d", len(got), MaxStreamBytes)
	}
	if !strings.HasPrefix(got, "FLUFFLE_HEAD_SENTINEL\n") {
		t.Fatalf("head was not kept; head = %q", got[:min(120, len(got))])
	}
	if !strings.Contains(got, ElisionMarker) {
		t.Fatalf("oversize output was not middle-elided; tail = %q", got[max(0, len(got)-120):])
	}
	if !strings.HasSuffix(got, "\nFLUFFLE_TAIL_SENTINEL\n") {
		t.Fatalf("tail was dropped; tail = %q", got[max(0, len(got)-120):])
	}
}

func TestSmallOutputIsStreamedBeforeExit(t *testing.T) {
	dir := t.TempDir()
	release := filepath.Join(dir, "release")
	body := strings.Repeat("small-chunk;", 40)
	script := "printf '" + body + "'; while [ ! -f \"" + release + "\" ]; do sleep 0.05; done"
	c := &collector{}
	var runErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, runErr = NewExec().Run(context.Background(), Request{
			Command: "sh",
			Args:    []string{"-c", script},
			Timeout: 30 * time.Second,
			OnChunk: c.fn,
		})
	}()
	arrived := waitForString(c.out, body, 10*time.Second)
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	<-done
	if runErr != nil {
		t.Fatal(runErr)
	}
	if !arrived {
		t.Fatalf("small output was not delivered while the process was still running; got %d bytes", len(c.out()))
	}
	if c.out() != body {
		t.Fatalf("stdout = %q, want %q", c.out(), body)
	}
	if strings.Contains(c.out(), ElisionMarker) {
		t.Fatal("marker appended to output that never overflowed")
	}
}

func TestUnderBudgetOutputArrivesOnceInOrderWithNoMarker(t *testing.T) {
	c := &collector{}
	if _, err := run(t, Request{
		Command: "sh",
		Args:    []string{"-c", "yes 0123456789 | head -n 10000"},
		Timeout: 30 * time.Second,
		OnChunk: c.fn,
	}); err != nil {
		t.Fatal(err)
	}
	want := strings.Repeat("0123456789\n", 10000)
	if c.out() != want {
		t.Fatalf("stdout len = %d, want %d (exact bytes, once, in order)", len(c.out()), len(want))
	}
	if strings.Contains(c.out(), ElisionMarker) {
		t.Fatal("marker appended to under-budget output")
	}
}

func TestEnvIsMergedOverInherited(t *testing.T) {
	c := &collector{}
	t.Setenv("FLF_RUNNER_INHERITED", "yes")
	_, err := run(t, Request{
		Command: "sh",
		Args:    []string{"-c", "echo $FLF_RUNNER_EXTRA-$FLF_RUNNER_INHERITED"},
		Env:     []string{"FLF_RUNNER_EXTRA=set"},
		Timeout: 10 * time.Second,
		OnChunk: c.fn,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(c.out()) != "set-yes" {
		t.Fatalf("stdout = %q, want set-yes", strings.TrimSpace(c.out()))
	}
}

func TestEnvOverrideReplacesInheritedValue(t *testing.T) {
	c := &collector{}
	t.Setenv("FLF_RUNNER_DUP", "inherited")
	_, err := run(t, Request{
		Command: "sh",
		Args:    []string{"-c", "echo $FLF_RUNNER_DUP"},
		Env:     []string{"FLF_RUNNER_DUP=override"},
		Timeout: 10 * time.Second,
		OnChunk: c.fn,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(c.out()) != "override" {
		t.Fatalf("stdout = %q, want override", strings.TrimSpace(c.out()))
	}
}

func TestCwdIsHonored(t *testing.T) {
	dir := t.TempDir()
	c := &collector{}
	if _, err := run(t, Request{Command: "pwd", Cwd: dir, Timeout: 10 * time.Second, OnChunk: c.fn}); err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(c.out())
	resolved, _ := filepath.EvalSymlinks(dir)
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != resolved {
		t.Fatalf("cwd = %q, want %q", gotResolved, resolved)
	}
}

func TestMissingCommandIsAnError(t *testing.T) {
	if _, err := run(t, Request{Command: "definitely-not-a-real-binary-xyz", Timeout: 5 * time.Second}); err == nil {
		t.Fatal("expected a spawn error")
	}
}

func TestContextCancelStopsTheProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	_, err := NewExec().Run(ctx, Request{Command: "sleep", Args: []string{"30"}, Timeout: 30 * time.Second})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestStdoutAndStderrBothDrain(t *testing.T) {
	c := &collector{}
	if _, err := run(t, Request{
		Command: "sh",
		Args:    []string{"-c", "echo out; echo err 1>&2"},
		Timeout: 10 * time.Second,
		OnChunk: c.fn,
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.out(), "out") || !strings.Contains(c.errOut(), "err") {
		t.Fatalf("stdout = %q stderr = %q", c.out(), c.errOut())
	}
}

func waitForFile(t *testing.T, path string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func waitForGone(t *testing.T, pid int, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func waitForString(get func() string, want string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(get(), want) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}
