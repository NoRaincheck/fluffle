package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NoRaincheck/fluffle/internal/agentcfg"
	"github.com/NoRaincheck/fluffle/internal/runner"
	"github.com/NoRaincheck/fluffle/internal/store"
)

type fakeRunner struct {
	mu          sync.Mutex
	calls       []runner.Request
	concurrent  int
	maxObserved int
	stdout      string
	stderr      string
	chunk       int
	chunkDelay  time.Duration
	exit        int
	err         error
	delay       time.Duration
	onStart     func()
}

func (f *fakeRunner) enter() {
	f.mu.Lock()
	f.concurrent++
	if f.concurrent > f.maxObserved {
		f.maxObserved = f.concurrent
	}
	f.mu.Unlock()
}

func (f *fakeRunner) leave() {
	f.mu.Lock()
	f.concurrent--
	f.mu.Unlock()
}

func (f *fakeRunner) peakConcurrency() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxObserved
}

func (f *fakeRunner) Run(ctx context.Context, req runner.Request) (runner.Result, error) {
	f.enter()
	defer f.leave()
	f.mu.Lock()
	f.calls = append(f.calls, req)
	hook := f.onStart
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return runner.Result{}, ctx.Err()
		}
	}
	if f.err != nil {
		return runner.Result{ExitCode: f.exit}, f.err
	}
	size := f.chunk
	if size <= 0 {
		size = len(f.stdout)
		if size == 0 {
			size = 1
		}
	}
	for i := 0; i < len(f.stdout); i += size {
		end := i + size
		if end > len(f.stdout) {
			end = len(f.stdout)
		}
		req.OnChunk("stdout", []byte(f.stdout[i:end]))
		if f.chunkDelay > 0 {
			time.Sleep(f.chunkDelay)
		}
	}
	if f.stderr != "" {
		req.OnChunk("stderr", []byte(f.stderr))
	}
	return runner.Result{ExitCode: f.exit}, nil
}

func (f *fakeRunner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeRunner) lastCall(t *testing.T) runner.Request {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		t.Fatal("runner was never called")
	}
	return f.calls[len(f.calls)-1]
}

const probeCfg = "[[agents]]\nname=\"probe\"\ncommand=\"/bin/probe\"\nreply=\"stdout\"\ntimeout_secs=5\n"

func harness(t *testing.T, cfg string, r runner.Runner) (*store.Store, *Manager, int64, int64) {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	dir := t.TempDir()
	global := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(global, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	chID, _ := s.CreateChannel("c", "/repo", "", "", "", false)
	thID, _ := s.CreateThread(chID, "t")
	seq, _ := s.AppendMessage(thID, "alice", "human", "user", "@probe do xyz")
	msgID, _ := s.MessageIDBySeq(thID, seq)
	m := NewManager(s, agentcfg.NewLoader(global), r)
	t.Cleanup(m.Shutdown)
	return s, m, thID, msgID
}

func waitSession(t *testing.T, s *store.Store, id int64, timeout time.Duration) store.Session {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		got, err := s.GetSession(id)
		if err == nil {
			switch got.Status {
			case store.SessionSucceeded, store.SessionFailed, store.SessionCanceled:
				return got
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("session %d did not reach a terminal status", id)
	return store.Session{}
}

func onlySession(t *testing.T, s *store.Store, thID int64) store.Session {
	t.Helper()
	list, err := s.ListSessions(thID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("sessions = %+v, want exactly 1", list)
	}
	return list[0]
}

func eventTypes(s *store.Store, id int64) []string {
	events, err := s.ListSessionEvents(id)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.Type)
	}
	return out
}

func hasEvent(s *store.Store, id int64, typ string) bool {
	for _, t := range eventTypes(s, id) {
		if t == typ {
			return true
		}
	}
	return false
}

func eventsContain(s *store.Store, id int64, typ, needle string) bool {
	events, err := s.ListSessionEvents(id)
	if err != nil {
		return false
	}
	for _, e := range events {
		if e.Type == typ && strings.Contains(e.Content, needle) {
			return true
		}
	}
	return false
}

func TestStartRunsAgentAndRecordsEvents(t *testing.T) {
	fr := &fakeRunner{stdout: "the answer", exit: 0}
	s, m, thID, msgID := harness(t, probeCfg, fr)
	m.Start(thID, msgID, []string{"probe"})

	got := waitSession(t, s, onlySession(t, s, thID).ID, 10*time.Second)
	if got.Status != store.SessionSucceeded {
		t.Fatalf("status = %q error %v", got.Status, got.Error)
	}
	if got.ReplyMode != "stdout" || got.AgentName != "probe" {
		t.Fatalf("session = %+v", got)
	}
	if fr.callCount() != 1 {
		t.Fatalf("runner calls = %d", fr.callCount())
	}
	if !hasEvent(s, got.ID, store.SessionEventPrompt) {
		t.Fatalf("no prompt event: %v", eventTypes(s, got.ID))
	}
	if !eventsContain(s, got.ID, store.SessionEventStdout, "the answer") {
		t.Fatalf("no stdout event: %v", eventTypes(s, got.ID))
	}
	if !hasEvent(s, got.ID, store.SessionEventExit) {
		t.Fatalf("no exit event: %v", eventTypes(s, got.ID))
	}
}

func TestStartDeduplicatesNames(t *testing.T) {
	fr := &fakeRunner{stdout: "x"}
	s, m, thID, msgID := harness(t, probeCfg, fr)
	m.Start(thID, msgID, []string{"probe", "probe", "probe"})
	waitSession(t, s, onlySession(t, s, thID).ID, 10*time.Second)
	if fr.callCount() != 1 {
		t.Fatalf("runner calls = %d, want 1", fr.callCount())
	}
}

func TestUnresolvableNameCreatesNoSession(t *testing.T) {
	fr := &fakeRunner{}
	s, m, thID, msgID := harness(t, probeCfg, fr)
	m.Start(thID, msgID, []string{"nosuchagent"})
	time.Sleep(200 * time.Millisecond)
	list, _ := s.ListSessions(thID)
	if len(list) != 0 {
		t.Fatalf("sessions = %+v", list)
	}
	if fr.callCount() != 0 {
		t.Fatal("runner was called for an unresolvable name")
	}
}

func TestBrokenConfigStartsNoSession(t *testing.T) {
	fr := &fakeRunner{}
	s, m, thID, msgID := harness(t, "[[agents]]\nname=\"BAD\"\ncommand=\"x\"\n", fr)
	m.Start(thID, msgID, []string{"BAD"})
	time.Sleep(200 * time.Millisecond)
	list, _ := s.ListSessions(thID)
	if len(list) != 0 {
		t.Fatalf("sessions = %+v", list)
	}
}

func TestCommandCwdAndStdinAreWired(t *testing.T) {
	cfg := "[[agents]]\nname=\"probe\"\ncommand=\"claude\"\nargs=[\"-p\",\"{prompt}\"]\nenv={FOO=\"bar\"}\ntimeout_secs=5\n"
	fr := &fakeRunner{stdout: "x"}
	s, m, thID, msgID := harness(t, cfg, fr)
	m.Start(thID, msgID, []string{"probe"})
	got := waitSession(t, s, onlySession(t, s, thID).ID, 10*time.Second)

	if !strings.Contains(got.Command, "claude") || !strings.Contains(got.Command, "-p") {
		t.Fatalf("command = %q", got.Command)
	}
	if got.Cwd == nil || *got.Cwd != "/repo" {
		t.Fatalf("cwd = %v", got.Cwd)
	}
	call := fr.lastCall(t)
	if call.Command != "claude" {
		t.Fatalf("runner command = %q", call.Command)
	}
	if call.Cwd != "/repo" {
		t.Fatalf("runner cwd = %q", call.Cwd)
	}
	if len(call.Args) != 2 || call.Args[1] == "" {
		t.Fatalf("runner args = %#v", call.Args)
	}
	if call.Stdin != "" {
		t.Fatalf("stdin = %q, want empty because a placeholder is present", call.Stdin)
	}
	if len(call.Env) != 1 || call.Env[0] != "FOO=bar" {
		t.Fatalf("runner env = %#v", call.Env)
	}
	if call.Timeout != 5*time.Second {
		t.Fatalf("runner timeout = %v", call.Timeout)
	}
}

func TestPromptIsDeliveredOnStdinWithoutPlaceholder(t *testing.T) {
	fr := &fakeRunner{stdout: "x"}
	s, m, thID, msgID := harness(t, probeCfg, fr)
	m.Start(thID, msgID, []string{"probe"})
	waitSession(t, s, onlySession(t, s, thID).ID, 10*time.Second)
	call := fr.lastCall(t)
	if !strings.Contains(call.Stdin, "## Request") {
		t.Fatalf("stdin missing the prompt:\n%s", call.Stdin)
	}
}

func TestPromptEventMatchesDeliveredPrompt(t *testing.T) {
	fr := &fakeRunner{stdout: "x"}
	s, m, thID, msgID := harness(t, probeCfg, fr)
	m.Start(thID, msgID, []string{"probe"})
	got := waitSession(t, s, onlySession(t, s, thID).ID, 10*time.Second)
	if !eventsContain(s, got.ID, store.SessionEventPrompt, "@probe do xyz") {
		t.Fatalf("prompt event does not contain the trigger:\n%s", eventTypes(s, got.ID))
	}
}

func TestOrphanChannelSessionHasNullCwd(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	dir := t.TempDir()
	global := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(global, []byte(probeCfg), 0o644); err != nil {
		t.Fatal(err)
	}
	chID, _ := s.CreateChannel("orphan", "", "", "", "", true)
	thID, _ := s.CreateThread(chID, "t")
	seq, _ := s.AppendMessage(thID, "alice", "human", "user", "@probe hi")
	msgID, _ := s.MessageIDBySeq(thID, seq)
	m := NewManager(s, agentcfg.NewLoader(global), &fakeRunner{stdout: "x"})
	t.Cleanup(m.Shutdown)
	m.Start(thID, msgID, []string{"probe"})
	got := waitSession(t, s, onlySession(t, s, thID).ID, 10*time.Second)
	if got.Cwd != nil {
		t.Fatalf("cwd = %q, want nil", *got.Cwd)
	}
}

func TestRunnerErrorMarksSessionFailedWithErrorEvent(t *testing.T) {
	fr := &fakeRunner{err: errors.New("spawn boom")}
	s, m, thID, msgID := harness(t, probeCfg, fr)
	m.Start(thID, msgID, []string{"probe"})
	got := waitSession(t, s, onlySession(t, s, thID).ID, 10*time.Second)
	if got.Status != store.SessionFailed {
		t.Fatalf("status = %q", got.Status)
	}
	if got.Error == nil || !strings.Contains(*got.Error, "spawn boom") {
		t.Fatalf("error = %v", got.Error)
	}
	if !hasEvent(s, got.ID, store.SessionEventError) {
		t.Fatalf("no error event: %v", eventTypes(s, got.ID))
	}
}

func TestNonZeroExitIsSucceededWithCode(t *testing.T) {
	fr := &fakeRunner{stdout: "partial", exit: 2}
	s, m, thID, msgID := harness(t, probeCfg, fr)
	m.Start(thID, msgID, []string{"probe"})
	got := waitSession(t, s, onlySession(t, s, thID).ID, 10*time.Second)
	if got.Status != store.SessionSucceeded {
		t.Fatalf("status = %q", got.Status)
	}
	if got.ExitCode == nil || *got.ExitCode != 2 {
		t.Fatalf("exit code = %v", got.ExitCode)
	}
}

func TestStderrIsRecorded(t *testing.T) {
	fr := &fakeRunner{stdout: "out", stderr: "warning text"}
	s, m, thID, msgID := harness(t, probeCfg, fr)
	m.Start(thID, msgID, []string{"probe"})
	got := waitSession(t, s, onlySession(t, s, thID).ID, 10*time.Second)
	if !eventsContain(s, got.ID, store.SessionEventStderr, "warning text") {
		t.Fatalf("no stderr event: %v", eventTypes(s, got.ID))
	}
}

func TestOutputIsCoalesced(t *testing.T) {
	fr := &fakeRunner{stdout: strings.Repeat("0123456789", 400), chunk: 8}
	s, m, thID, msgID := harness(t, probeCfg, fr)
	m.Start(thID, msgID, []string{"probe"})
	got := waitSession(t, s, onlySession(t, s, thID).ID, 10*time.Second)
	stdoutEvents := 0
	events, _ := s.ListSessionEvents(got.ID)
	for _, e := range events {
		if e.Type == store.SessionEventStdout {
			stdoutEvents++
		}
	}
	if stdoutEvents == 0 {
		t.Fatal("no stdout events")
	}
	if stdoutEvents >= 50 {
		t.Fatalf("stdout events = %d, want well below the 50-chunk input", stdoutEvents)
	}
}

func TestCoalescedOutputLosesNothing(t *testing.T) {
	payload := strings.Repeat("abcdefghij", 300)
	fr := &fakeRunner{stdout: payload, chunk: 7}
	s, m, thID, msgID := harness(t, probeCfg, fr)
	m.Start(thID, msgID, []string{"probe"})
	got := waitSession(t, s, onlySession(t, s, thID).ID, 10*time.Second)
	events, _ := s.ListSessionEvents(got.ID)
	var b strings.Builder
	for _, e := range events {
		if e.Type == store.SessionEventStdout {
			b.WriteString(e.Content)
		}
	}
	if b.String() != payload {
		t.Fatalf("coalescing lost data: got %d bytes, want %d", b.Len(), len(payload))
	}
}

func stdoutEventContents(t *testing.T, s *store.Store, id int64) []string {
	t.Helper()
	events, err := s.ListSessionEvents(id)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range events {
		if e.Type == store.SessionEventStdout {
			out = append(out, e.Content)
		}
	}
	return out
}

func TestStreamWriterFlushesOnByteThreshold(t *testing.T) {
	const chunk = 1024
	fr := &fakeRunner{stdout: strings.Repeat("0123456789", 32*chunk/10), chunk: chunk}
	s, m, thID, msgID := harness(t, probeCfg, fr)
	m.Start(thID, msgID, []string{"probe"})
	got := waitSession(t, s, onlySession(t, s, thID).ID, 10*time.Second)

	events := stdoutEventContents(t, s, got.ID)
	if len(events) < 2 {
		t.Fatalf("stdout events = %d, want at least 2: the byte threshold must flush before the stream closes", len(events))
	}
	for i, e := range events {
		if len(e) > StreamFlushBytes+chunk {
			t.Fatalf("stdout event %d is %d bytes, want at most %d", i, len(e), StreamFlushBytes+chunk)
		}
	}
}

func TestStreamWriterFlushesOnTimer(t *testing.T) {
	fr := &fakeRunner{stdout: strings.Repeat("x", 2048), chunk: 512, chunkDelay: 100 * time.Millisecond}
	s, m, thID, msgID := harness(t, probeCfg, fr)
	m.Start(thID, msgID, []string{"probe"})
	got := waitSession(t, s, onlySession(t, s, thID).ID, 10*time.Second)

	events := stdoutEventContents(t, s, got.ID)
	if len(events) < 2 {
		t.Fatalf("stdout events = %d, want at least 2: output below the byte threshold must still be flushed every %v", len(events), StreamFlushInterval)
	}
	var joined strings.Builder
	for _, e := range events {
		joined.WriteString(e)
	}
	if joined.String() != fr.stdout {
		t.Fatalf("timer flush lost data: got %d bytes, want %d", joined.Len(), len(fr.stdout))
	}
}

func TestHistoryForOrdersMessagesThenReactions(t *testing.T) {
	fr := &fakeRunner{stdout: "x"}
	s, m, thID, firstMsgID := harness(t, probeCfg, fr)
	second, _ := s.AppendMessage(thID, "bob", "human", "user", "beta")
	third, _ := s.AppendMessage(thID, "probe", "agent", "assistant", "gamma")
	if err := s.AddReaction(firstMsgID, "👀", "carol", "human"); err != nil {
		t.Fatal(err)
	}

	lines := m.historyFor(thID)
	if len(lines) != 4 {
		t.Fatalf("history lines = %d, want 4: %+v", len(lines), lines)
	}
	for i, wantSeq := range []int64{1, second, third} {
		if lines[i].Type != "message" || lines[i].Seq != wantSeq {
			t.Fatalf("line %d = %+v, want a message with seq %d", i, lines[i], wantSeq)
		}
	}
	if lines[3].Type != "reaction" {
		t.Fatalf("line 3 = %+v, want the reaction after every message", lines[3])
	}
	if lines[3].MessageSeq != 1 {
		t.Fatalf("reaction message_seq = %d, want 1", lines[3].MessageSeq)
	}
	if lines[3].Emoji != "👀" || lines[3].Name != "carol" || lines[3].AuthorType != "human" {
		t.Fatalf("reaction = %+v", lines[3])
	}
}

func TestPromptCarriesHistoryInOrder(t *testing.T) {
	fr := &fakeRunner{stdout: "x"}
	s, m, thID, firstMsgID := harness(t, probeCfg, fr)
	s.AppendMessage(thID, "bob", "human", "user", "beta")
	s.AppendMessage(thID, "probe", "agent", "assistant", "gamma")
	if err := s.AddReaction(firstMsgID, "👀", "carol", "human"); err != nil {
		t.Fatal(err)
	}
	m.Start(thID, firstMsgID, []string{"probe"})
	got := waitSession(t, s, onlySession(t, s, thID).ID, 10*time.Second)

	var prompt string
	events, err := s.ListSessionEvents(got.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Type == store.SessionEventPrompt {
			prompt = e.Content
		}
	}
	start := strings.Index(prompt, "## History")
	end := strings.Index(prompt, "## Request")
	if start < 0 || end < start {
		t.Fatalf("prompt is missing its history section:\n%s", prompt)
	}
	section := prompt[start:end]
	idx := func(needle string) int { return strings.Index(section, needle) }
	if idx("do xyz") < 0 || idx("beta") < 0 || idx("gamma") < 0 || idx("👀") < 0 {
		t.Fatalf("history section is missing content:\n%s", section)
	}
	if !(idx("do xyz") < idx("beta") && idx("beta") < idx("gamma") && idx("gamma") < idx("👀")) {
		t.Fatalf("history section is out of order:\n%s", section)
	}
}

func TestReconcileTerminatesStaleRows(t *testing.T) {
	fr := &fakeRunner{stdout: "x"}
	s, m, thID, msgID := harness(t, probeCfg, fr)
	m.Start(thID, msgID, []string{"probe"})
	waitSession(t, s, onlySession(t, s, thID).ID, 10*time.Second)
	staleSeq, _ := s.AppendMessage(thID, "alice", "human", "user", "@probe stale")
	staleMsgID, _ := s.MessageIDBySeq(thID, staleSeq)
	stale, err := s.CreateSession(thID, staleMsgID, "probe", store.SessionQueued, "stdout", "c", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetSession(stale)
	if got.Status != store.SessionCanceled {
		t.Fatalf("status = %q", got.Status)
	}
}

func TestShutdownCancelsRunningAndQueuedSessions(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	fr := &fakeRunner{delay: 30 * time.Second, onStart: func() { once.Do(func() { close(started) }) }}
	s, m, thID, _ := harness(t, probeCfg, fr)
	for i := 0; i < MaxConcurrentSessions+2; i++ {
		seq, _ := s.AppendMessage(thID, "alice", "human", "user", "@probe hi")
		id, _ := s.MessageIDBySeq(thID, seq)
		m.Start(thID, id, []string{"probe"})
	}
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("agent never started")
	}
	m.Shutdown()
	m.Shutdown()
	list, _ := s.ListSessions(thID)
	if len(list) != MaxConcurrentSessions+2 {
		t.Fatalf("sessions = %d, want %d", len(list), MaxConcurrentSessions+2)
	}
	for _, sess := range list {
		if sess.Status != store.SessionCanceled {
			t.Fatalf("session %d status = %q, want canceled the moment Shutdown returns", sess.ID, sess.Status)
		}
		if sess.StartedAt != nil && *sess.StartedAt == "" {
			t.Fatalf("session %d has an empty started_at", sess.ID)
		}
	}
}

func waitForStatus(t *testing.T, s *store.Store, id int64, want string, timeout time.Duration) store.Session {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last store.Session
	for time.Now().Before(deadline) {
		got, err := s.GetSession(id)
		if err == nil {
			last = got
			if got.Status == want {
				return got
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("session %d status = %q, want %q", id, last.Status, want)
	return store.Session{}
}

func splitRunningAndQueued(t *testing.T, s *store.Store, thID int64) (running, queued []int64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		list, err := s.ListSessions(thID)
		if err != nil {
			t.Fatal(err)
		}
		running, queued = nil, nil
		for _, sess := range list {
			switch sess.Status {
			case store.SessionRunning:
				running = append(running, sess.ID)
			case store.SessionQueued:
				queued = append(queued, sess.ID)
			}
		}
		if len(running) == MaxConcurrentSessions && len(queued) == 1 {
			return running, queued
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("running = %d queued = %d, want %d and 1", len(running), len(queued), MaxConcurrentSessions)
	return nil, nil
}

func TestCancelQueuedSessionNeverRunsIt(t *testing.T) {
	fr := &fakeRunner{delay: 30 * time.Second}
	s, m, thID, _ := harness(t, probeCfg, fr)
	for i := 0; i < MaxConcurrentSessions+1; i++ {
		seq, _ := s.AppendMessage(thID, "alice", "human", "user", "@probe hi")
		id, _ := s.MessageIDBySeq(thID, seq)
		m.Start(thID, id, []string{"probe"})
	}
	running, queued := splitRunningAndQueued(t, s, thID)
	callsBefore := fr.callCount()

	if err := m.Cancel(queued[0]); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetSession(queued[0]); got.Status != store.SessionCanceled {
		t.Fatalf("status = %q, want canceled", got.Status)
	}
	if err := m.Cancel(running[0]); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, s, running[0], store.SessionCanceled, 10*time.Second)
	time.Sleep(200 * time.Millisecond)

	if got := fr.callCount(); got != callsBefore {
		t.Fatalf("runner calls = %d, want %d: a canceled queued session must not spawn an agent", got, callsBefore)
	}
	got, _ := s.GetSession(queued[0])
	if got.Status != store.SessionCanceled {
		t.Fatalf("session %d status = %q, want canceled", got.ID, got.Status)
	}
	if got.StartedAt != nil {
		t.Fatalf("started_at = %q, want NULL for a session that never ran", *got.StartedAt)
	}
}

func TestCancelUnknownIsNotFound(t *testing.T) {
	_, m, _, _ := harness(t, probeCfg, &fakeRunner{})
	if err := m.Cancel(9999); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestCancelRunningSession(t *testing.T) {
	started := make(chan struct{})
	fr := &fakeRunner{delay: 30 * time.Second, onStart: func() { close(started) }}
	s, m, thID, msgID := harness(t, probeCfg, fr)
	m.Start(thID, msgID, []string{"probe"})
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("agent never started")
	}
	id := onlySession(t, s, thID).ID
	if err := m.Cancel(id); err != nil {
		t.Fatal(err)
	}
	got := waitSession(t, s, id, 10*time.Second)
	if got.Status != store.SessionCanceled {
		t.Fatalf("status = %q", got.Status)
	}
}

func TestCancelFinishedSessionIsTerminal(t *testing.T) {
	fr := &fakeRunner{stdout: "x"}
	s, m, thID, msgID := harness(t, probeCfg, fr)
	m.Start(thID, msgID, []string{"probe"})
	got := waitSession(t, s, onlySession(t, s, thID).ID, 10*time.Second)
	if err := m.Cancel(got.ID); !errors.Is(err, ErrTerminal) {
		t.Fatalf("err = %v, want ErrTerminal", err)
	}
}

func TestStartAfterShutdownDoesNotRun(t *testing.T) {
	fr := &fakeRunner{stdout: "x"}
	s, m, thID, msgID := harness(t, probeCfg, fr)
	m.Shutdown()
	m.Start(thID, msgID, []string{"probe"})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		list, _ := s.ListSessions(thID)
		if len(list) > 0 {
			for _, sess := range list {
				if sess.Status != store.SessionCanceled {
					t.Fatalf("session %d status = %q, want canceled", sess.ID, sess.Status)
				}
			}
			if fr.callCount() != 0 {
				t.Fatalf("runner ran %d times after Shutdown", fr.callCount())
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("session row was never created")
}

func TestConcurrencyIsCapped(t *testing.T) {
	fr := &fakeRunner{stdout: "x", delay: 300 * time.Millisecond}
	s, m, thID, _ := harness(t, probeCfg, fr)
	total := MaxConcurrentSessions + 3
	for i := 0; i < total; i++ {
		seq, _ := s.AppendMessage(thID, "alice", "human", "user", "@probe hi")
		id, _ := s.MessageIDBySeq(thID, seq)
		m.Start(thID, id, []string{"probe"})
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if fr.callCount() >= total {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	list, _ := s.ListSessions(thID)
	if len(list) != total {
		t.Fatalf("sessions = %d, want %d", len(list), total)
	}
	if peak := fr.peakConcurrency(); peak > MaxConcurrentSessions {
		t.Fatalf("peak concurrency = %d, want at most %d", peak, MaxConcurrentSessions)
	}
}
