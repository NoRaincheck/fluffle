# Agent Sessions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A leading `@agent-name` in a human message starts a daemon-run local agent subprocess; the agent's reply lands back in the thread and the whole run is recorded as a session the TUI shows as a preview.

**Architecture:** The daemon owns execution (roborev model). A mention on a human message append is a side effect that creates a session row and hands it to an in-memory scheduler. A one-shot runner spawns the agent as a subprocess with `setpgid`, streams stdout/stderr into typed session events, and resolves a reply per the agent's `reply` mode. Agent profiles come from layered TOML config. The TUI shows the session for the cursor's message in the existing `p` preview pane.

**Tech Stack:** Go 1.26, `modernc.org/sqlite` (already present), `github.com/BurntSushi/toml` v1.6.0 (new), Bubble Tea v1.3.10 and lipgloss v1.1.0 (already present).

**Spec:** `docs/superpowers/specs/2026-09-26-agent-sessions-design.md` — read it before starting. This plan argues from the spec, so the spec travels with it.

## Global Constraints

Every task below inherits these. They are not repeated per task.

- **No comments** in code unless explicitly requested. Code is self-documenting.
- `gofmt -l .` must be empty for every changed file. `go vet ./...` must be clean.
- **TDD, no exceptions:** failing test → run it and watch it fail → minimal implementation → run it and watch it pass → commit. Never commit with a failing test.
- **Exit codes:** 0 success, 1 client error, 2 daemon/transport error. Never deviate.
- **Error envelope:** exactly `{"code","message"}` at every layer boundary.
- **`X-Fluffle-Agent` header:** the daemon re-derives `author_type` from it. Never trust `author_type` from a client body.
- **Agents are append-only.** Agents may never create channels or threads, and (new) may never start or cancel sessions.
- **No hidden agent memory.** Session events are an audit transcript. Nothing in `agent_session_events` is ever fed back to an agent as context. The thread JSONL is the context.
- **SQLite-first.** `store.Open` already calls `SetMaxOpenConns(1)`. This is why output coalescing in Task 5 is mandatory, not an optimization: one event row per stream read would starve the agent's own HTTP POST to the same daemon.
- **Localhost-only.** No new network surface, no remote access, no auth work.
- **YAGNI.** No ACP, no MCP, no session reuse, no autocomplete, no webapp, no config-driven concurrency.
- **Exactly one new dependency:** `github.com/BurntSushi/toml`. Do not add yaml, a test framework, or an assertion library.
- **Forgiving parse, strict resolve.** The mention grammar accepts `[A-Za-z0-9][A-Za-z0-9_-]*`; agent names must match `^[a-z0-9][a-z0-9_-]*$`. So `@Reviewer` parses as a mention but never resolves, and is therefore inert. **This asymmetry is deliberate. Do not "fix" it by loosening the name rule or tightening the mention rule.**
- Tests use `store.Open(":memory:")`, which already applies `SetMaxOpenConns(1)`.
- Real subprocesses in tests, no mocks, for anything that spawns a process.

## File Structure

New packages, each with one responsibility:

| Package | Path | Responsibility |
|---|---|---|
| `mentions` | `internal/mentions/` | Pure function: leading `@name` tokens to names plus request text. No dependencies. |
| `agentcfg` | `internal/agentcfg/` | Agent profile type, TOML parse and validate, repo-over-global merge, mtime cache. Leaf package. |
| `runner` | `internal/runner/` | The `Runner` interface plus the one `exec` implementation. Knows nothing about fluffle. |
| `session` | `internal/session/` | `Manager`: scheduling, prompt building, output coalescing, reply resolution, lifecycle. |

New files inside existing packages:

| File | Responsibility |
|---|---|
| `internal/store/session.go` | `Session` / `SessionEvent` types and CRUD. Split out so `store.go` does not grow past its current 767 lines. |
| `internal/apiserver/sessions.go` | New routes plus the `SessionStarter` / `Deps` seam. |
| `internal/tui/session.go` | Session preview state, key, render, bounded tick. Keeps `model.go` from growing. |
| `cmd/flf/agents.go` | `flf agent list` and `flf agent session`. Keeps `main.go` from growing past 1281 lines. |

### Interface contracts

These names are referenced by later tasks. Do not rename them.

```go
// internal/mentions
func Parse(content string) (names []string, request string)

// internal/agentcfg
type Agent struct {
    Name, Description, Command, Reply, SystemPrompt string
    Args                                            []string
    TimeoutSecs                                      int
    Env                                              map[string]string
}
type Entry struct { Agent; Source string }
type Set struct{}
func (s *Set) Lookup(name string) (Entry, bool)
func (s *Set) Entries() []Entry
func Parse(data []byte, source string) ([]Entry, error)
func NewLoader(globalPath string) *Loader
func (l *Loader) Resolve(repoAbsPath string) (*Set, error)

// internal/runner
var ErrTimeout = errors.New("agent timed out")
type Request struct {
    Command string
    Args    []string
    Stdin   string
    Cwd     string
    Env     []string
    Timeout time.Duration
    OnChunk func(stream string, b []byte)
}
type Result struct{ ExitCode int }
type Runner interface{ Run(ctx context.Context, req Request) (Result, error) }
func NewExec() Runner

// internal/store (new file)
const SessionQueued, SessionRunning, SessionSucceeded = "queued", "running", "succeeded"
const SessionFailed, SessionCanceled                   = "failed", "canceled"
const SessionEventPrompt, SessionEventStdout           = "prompt", "stdout"
const SessionEventStderr, SessionEventExit             = "stderr", "exit"
const SessionEventError                                = "error"

// internal/session
const MaxConcurrentSessions = 4
const StreamFlushInterval   = 250 * time.Millisecond
const StreamFlushBytes      = 8 * 1024
const ReplyGracePeriod      = 2 * time.Second
const ReplyGracePoll        = 100 * time.Millisecond
var ErrTerminal = errors.New("session already finished")
type Manager struct{}
func NewManager(s *store.Store, agents *agentcfg.Loader, r runner.Runner) *Manager
func (m *Manager) Start(threadID, triggerMessageID int64, names []string)
func (m *Manager) Reconcile() error
func (m *Manager) Shutdown()
func (m *Manager) Cancel(sessionID int64) error

// internal/apiserver
type SessionStarter interface{ Start(threadID, triggerMessageID int64, names []string) }
type Deps struct {
    Starter SessionStarter
    Agents  *agentcfg.Loader
    Canceler interface{ Cancel(id int64) error }
}
func NewHandler(s *store.Store) http.Handler
func NewHandlerWithDeps(s *store.Store, d Deps) http.Handler
```

Routes added: `GET /v1/threads/:id/sessions`, `GET /v1/sessions/:id`, `GET /v1/agents`, `POST /v1/sessions/:id/cancel`. New error code: `SESSION_NOT_FOUND` (exit 1) and `SESSION_FINISHED` (exit 1).

---

### Task 1: Leading-mention parser

Pure function, no dependencies. Everything downstream depends on its exact output shape, so it goes first.

**Files:**
- Create: `internal/mentions/mentions.go`
- Test: `internal/mentions/mentions_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `func Parse(content string) (names []string, request string)`. `names` is deduplicated and order-preserving; an empty result is `nil`. `request` is the content after the last mention, trimmed, and is `""` when there is none.

- [ ] **Step 1: Write the failing test**

Create `internal/mentions/mentions_test.go`:

```go
package mentions

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseLeadingMentions(t *testing.T) {
	for _, tc := range []struct {
		name        string
		content     string
		wantNames   []string
		wantRequest string
	}{
		{"single leading mention", "@reviewer do xyz", []string{"reviewer"}, "do xyz"},
		{"two leading mentions", "@reviewer @fixer do xyz", []string{"reviewer", "fixer"}, "do xyz"},
		{"mention with no request", "@reviewer", []string{"reviewer"}, ""},
		{"mention then comma", "@reviewer, can you look", []string{"reviewer"}, ", can you look"},
		{"mention then newline", "@reviewer\nsecond line", []string{"reviewer"}, "second line"},
		{"tab separated", "@a\t@b hi", []string{"a", "b"}, "hi"},
		{"duplicate names collapse", "@a @a hi", []string{"a"}, "hi"},
		{"duplicate non adjacent", "@a hi @a", []string{"a"}, "hi @a"},
		{"non leading is inert", "don't @reviewer do that", nil, ""},
		{"mid sentence is inert", "hey @reviewer look", nil, ""},
		{"bare at sign", "@", nil, ""},
		{"at sign then space", "@ reviewer", nil, ""},
		{"name cannot start with dash", "@-x hi", nil, ""},
		{"name cannot start with underscore", "@_x hi", nil, ""},
		{"uppercase token parses", "@Reviewer hi", []string{"Reviewer"}, "hi"},
		{"digits allowed", "@a1 hi", []string{"a1"}, "hi"},
		{"underscore and dash inside", "@a_b-c hi", []string{"a_b-c"}, "hi"},
		{"empty content", "", nil, ""},
		{"whitespace only", "   \t ", nil, ""},
		{"only mentions no request", "@a @b", []string{"a", "b"}, ""},
		{"request keeps internal newlines", "@a line1\nline2", []string{"a"}, "line1\nline2"},
		{"punctuation ends name", "@a.b hi", []string{"a"}, ".b hi"},
		{"leading whitespace then mention", "  @a hi", []string{"a"}, "hi"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			names, request := Parse(tc.content)
			if !reflect.DeepEqual(names, tc.wantNames) {
				t.Fatalf("names = %#v, want %#v", names, tc.wantNames)
			}
			if request != tc.wantRequest {
				t.Fatalf("request = %q, want %q", request, tc.wantRequest)
			}
		})
	}
}

func TestParseHandlesTenKiB(t *testing.T) {
	names, request := Parse("@a " + strings.Repeat("x", 10*1024))
	if !reflect.DeepEqual(names, []string{"a"}) {
		t.Fatalf("names = %#v", names)
	}
	if len(request) != 10*1024 {
		t.Fatalf("request len = %d", len(request))
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/mentions/`
Expected: FAIL — `undefined: Parse`

- [ ] **Step 3: Write the minimal implementation**

Create `internal/mentions/mentions.go`:

```go
package mentions

import "strings"

func Parse(content string) ([]string, string) {
	rest := content
	var names []string
	seen := map[string]bool{}
	for {
		trimmed := strings.TrimLeft(rest, " \t\n\r")
		if !strings.HasPrefix(trimmed, "@") {
			break
		}
		body := trimmed[1:]
		end := 0
		for end < len(body) && isNameByte(body[end], end) {
			end++
		}
		if end == 0 {
			break
		}
		name := body[:end]
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
		rest = body[end:]
	}
	return names, strings.TrimSpace(rest)
}

func isNameByte(b byte, pos int) bool {
	switch {
	case b >= 'a' && b <= 'z':
		return true
	case b >= 'A' && b <= 'Z':
		return true
	case b >= '0' && b <= '9':
		return true
	case b == '_' || b == '-':
		return pos > 0
	}
	return false
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/mentions/`
Expected: PASS

- [ ] **Step 5: Verify format and vet**

Run: `gofmt -l internal/mentions/ && go vet ./internal/mentions/`
Expected: no output from either.

- [ ] **Step 6: Commit**

```bash
git add internal/mentions/
git commit -m "feat(mentions): leading @name parser for agent mentions"
```

---

### Task 2: Agent profile config

**Files:**
- Create: `internal/agentcfg/agentcfg.go`
- Test: `internal/agentcfg/agentcfg_test.go`
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Consumes: nothing.
- Produces: the `agentcfg` block from the Interface contracts above. `Parse` applies the defaults `Reply = "auto"` and `TimeoutSecs = 300`.

- [ ] **Step 1: Add the dependency**

Run: `go get github.com/BurntSushi/toml@v1.6.0`
Expected: `go.mod` gains `github.com/BurntSushi/toml v1.6.0`. This is the only new dependency in the whole plan.

- [ ] **Step 2: Write the failing test**

Create `internal/agentcfg/agentcfg_test.go`:

```go
package agentcfg

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseAppliesDefaults(t *testing.T) {
	entries, err := Parse([]byte("[[agents]]\nname = \"probe\"\ncommand = \"/bin/probe\"\n"), "/tmp/x.toml")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %+v", entries)
	}
	got := entries[0]
	if got.Reply != "auto" {
		t.Fatalf("Reply = %q, want auto", got.Reply)
	}
	if got.TimeoutSecs != 300 {
		t.Fatalf("TimeoutSecs = %d, want 300", got.TimeoutSecs)
	}
	if got.Source != "/tmp/x.toml" {
		t.Fatalf("Source = %q", got.Source)
	}
	if got.Args != nil {
		t.Fatalf("Args = %#v, want nil", got.Args)
	}
}

func TestParseFullEntry(t *testing.T) {
	src := `
[[agents]]
name = "reviewer"
description = "reviews the diff"
command = "claude"
args = ["-p", "{prompt}"]
reply = "cli"
timeout_secs = 42
env = { FOO = "bar" }
system_prompt = "be concrete"
`
	entries, err := Parse([]byte(src), "/tmp/x.toml")
	if err != nil {
		t.Fatal(err)
	}
	want := Entry{Agent: Agent{
		Name:         "reviewer",
		Description:  "reviews the diff",
		Command:      "claude",
		Args:         []string{"-p", "{prompt}"},
		Reply:        "cli",
		TimeoutSecs:  42,
		Env:          map[string]string{"FOO": "bar"},
		SystemPrompt: "be concrete",
	}, Source: "/tmp/x.toml"}
	if !reflect.DeepEqual(entries[0], want) {
		t.Fatalf("got  %+v\nwant %+v", entries[0], want)
	}
}

func TestParseRejectsInvalid(t *testing.T) {
	for _, tc := range []struct{ name, src, wantErr string }{
		{"missing name", "[[agents]]\ncommand = \"x\"\n", "name"},
		{"blank name", "[[agents]]\nname = \"   \"\ncommand = \"x\"\n", "name"},
		{"missing command", "[[agents]]\nname = \"a\"\n", "command"},
		{"uppercase name", "[[agents]]\nname = \"Reviewer\"\ncommand = \"x\"\n", "name"},
		{"name starting with dash", "[[agents]]\nname = \"-a\"\ncommand = \"x\"\n", "name"},
		{"name starting with underscore", "[[agents]]\nname = \"_a\"\ncommand = \"x\"\n", "name"},
		{"bad reply", "[[agents]]\nname=\"a\"\ncommand=\"x\"\nreply=\"nope\"\n", "reply"},
		{"zero timeout", "[[agents]]\nname=\"a\"\ncommand=\"x\"\ntimeout_secs=0\n", "timeout_secs"},
		{"negative timeout", "[[agents]]\nname=\"a\"\ncommand=\"x\"\ntimeout_secs=-1\n", "timeout_secs"},
		{"unknown key", "[[agents]]\nname=\"a\"\ncommand=\"x\"\ntimeout_second=5\n", "unknown"},
		{"agents not a table", "agents = 1\n", "agents"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.src), "/tmp/x.toml")
			if err == nil {
				t.Fatalf("expected an error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %q, want it to contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestParseEmptyIsNotAnError(t *testing.T) {
	entries, err := Parse(nil, "/tmp/x.toml")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestResolveRepoOverridesGlobal(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "config.toml")
	repoDir := filepath.Join(dir, "repo")
	repo := filepath.Join(repoDir, ".flf.toml")
	writeFile(t, global, "[[agents]]\nname=\"a\"\ncommand=\"global-a\"\n\n[[agents]]\nname=\"shared\"\ncommand=\"global-shared\"\n")
	writeFile(t, repo, "[[agents]]\nname=\"shared\"\ncommand=\"repo-shared\"\nreply=\"cli\"\n\n[[agents]]\nname=\"b\"\ncommand=\"repo-b\"\n")

	set, err := NewLoader(global).Resolve(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	shared, ok := set.Lookup("shared")
	if !ok {
		t.Fatal("shared missing")
	}
	if shared.Command != "repo-shared" || shared.Reply != "cli" {
		t.Fatalf("repo did not win: %+v", shared)
	}
	a, _ := set.Lookup("a")
	if a.Command != "global-a" || a.Source != global {
		t.Fatalf("global-only entry lost: %+v", a)
	}
	b, _ := set.Lookup("b")
	if b.Command != "repo-b" {
		t.Fatalf("repo-only entry lost: %+v", b)
	}
	if len(set.Entries()) != 3 {
		t.Fatalf("entries = %d, want 3", len(set.Entries()))
	}
	names := []string{}
	for _, e := range set.Entries() {
		names = append(names, e.Name)
	}
	if !reflect.DeepEqual(names, []string{"a", "b", "shared"}) {
		t.Fatalf("Entries not sorted by name: %v", names)
	}
}

func TestResolveMissingRepoFileFallsBackToGlobal(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "config.toml")
	writeFile(t, global, "[[agents]]\nname=\"a\"\ncommand=\"g\"\n")
	set, err := NewLoader(global).Resolve(filepath.Join(dir, "nope"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := set.Lookup("a"); !ok {
		t.Fatal("global entry missing")
	}
}

func TestResolveOrphanUsesGlobalOnly(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "config.toml")
	writeFile(t, global, "[[agents]]\nname=\"a\"\ncommand=\"g\"\n")
	set, err := NewLoader(global).Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Entries()) != 1 {
		t.Fatalf("entries = %+v", set.Entries())
	}
}

func TestResolveMissingGlobalIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	set, err := NewLoader(filepath.Join(dir, "absent.toml")).Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Entries()) != 0 {
		t.Fatalf("entries = %+v", set.Entries())
	}
}

func TestResolvePropagatesParseError(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "config.toml")
	writeFile(t, global, "[[agents]]\nname=\"BAD\"\ncommand=\"x\"\n")
	if _, err := NewLoader(global).Resolve(""); err == nil {
		t.Fatal("expected a validation error from Resolve")
	}
}

func TestResolvePicksUpRewrite(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "config.toml")
	writeFile(t, global, "[[agents]]\nname=\"a\"\ncommand=\"first\"\n")
	l := NewLoader(global)
	if _, err := l.Resolve(""); err != nil {
		t.Fatal(err)
	}
	writeFile(t, global, "[[agents]]\nname=\"a\"\ncommand=\"second\"\n")
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(global, later, later); err != nil {
		t.Fatal(err)
	}
	set, err := l.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	a, _ := set.Lookup("a")
	if a.Command != "second" {
		t.Fatalf("stale cache: %+v", a)
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/agentcfg/`
Expected: FAIL — `undefined: NewLoader`, `undefined: Parse`

- [ ] **Step 4: Write the minimal implementation**

Create `internal/agentcfg/agentcfg.go`:

```go
package agentcfg

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
)

const (
	DefaultReply       = "auto"
	DefaultTimeoutSecs = 300
)

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

type Agent struct {
	Name         string
	Description  string
	Command      string
	Args         []string
	Reply        string
	TimeoutSecs  int
	Env          map[string]string
	SystemPrompt string
}

type Entry struct {
	Agent
	Source string
}

type Set struct {
	byName map[string]Entry
}

func (s *Set) Lookup(name string) (Entry, bool) {
	e, ok := s.byName[name]
	return e, ok
}

func (s *Set) Entries() []Entry {
	out := make([]Entry, 0, len(s.byName))
	for _, e := range s.byName {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

type configFile struct {
	Agents []Agent `toml:"agents"`
}

func Parse(data []byte, source string) ([]Entry, error) {
	var f configFile
	md, err := toml.Decode(string(data), &f)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", source, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, k := range undecoded {
			keys = append(keys, k.String())
		}
		sort.Strings(keys)
		return nil, fmt.Errorf("parse %s: unknown keys: %s", source, strings.Join(keys, ", "))
	}
	out := make([]Entry, 0, len(f.Agents))
	for i, a := range f.Agents {
		if a.Reply == "" {
			a.Reply = DefaultReply
		}
		if a.TimeoutSecs == 0 {
			a.TimeoutSecs = DefaultTimeoutSecs
		}
		if err := validate(a, source, i); err != nil {
			return nil, err
		}
		out = append(out, Entry{Agent: a, Source: source})
	}
	return out, nil
}

func validate(a Agent, source string, i int) error {
	where := fmt.Sprintf("%s: agents[%d]", source, i)
	if !nameRe.MatchString(a.Name) {
		return fmt.Errorf("%s: invalid name %q: must match %s", where, a.Name, nameRe.String())
	}
	if strings.TrimSpace(a.Command) == "" {
		return fmt.Errorf("%s: command required", where)
	}
	switch a.Reply {
	case "stdout", "cli", "auto":
	default:
		return fmt.Errorf("%s: invalid reply %q: want stdout, cli, or auto", where, a.Reply)
	}
	if a.TimeoutSecs <= 0 {
		return fmt.Errorf("%s: invalid timeout_secs %d: must be positive", where, a.TimeoutSecs)
	}
	return nil
}

type cached struct {
	entries []Entry
	stamp   time.Time
}

type Loader struct {
	globalPath string
	mu         sync.Mutex
	cache      map[string]cached
}

func NewLoader(globalPath string) *Loader {
	return &Loader{globalPath: globalPath, cache: map[string]cached{}}
}

func (l *Loader) Resolve(repoAbsPath string) (*Set, error) {
	paths := []string{l.globalPath}
	if repoAbsPath != "" {
		paths = append(paths, filepath.Join(repoAbsPath, ".flf.toml"))
	}
	merged := map[string]Entry{}
	for _, p := range paths {
		entries, err := l.load(p)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			merged[e.Name] = e
		}
	}
	return &Set{byName: merged}, nil
}

func (l *Loader) load(path string) ([]Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	info, err := os.Stat(path)
	if err != nil {
		delete(l.cache, path)
		return nil, nil
	}
	stamp := info.ModTime()
	if c, ok := l.cache[path]; ok && c.stamp.Equal(stamp) {
		return c.entries, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	entries, err := Parse(data, path)
	if err != nil {
		return nil, err
	}
	l.cache[path] = cached{entries: entries, stamp: stamp}
	return entries, nil
}
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./internal/agentcfg/`
Expected: PASS

- [ ] **Step 6: Verify format and vet**

Run: `gofmt -l internal/agentcfg/ && go vet ./internal/agentcfg/`
Expected: no output.

- [ ] **Step 7: Commit**

```bash
git add internal/agentcfg/ go.mod go.sum
git commit -m "feat(agentcfg): layered TOML agent profile config"
```

---

### Task 3: Subprocess runner

Real subprocesses, no mocks. Process-group kill and bounded output are the point of this task, so each gets a real test.

**Files:**
- Create: `internal/runner/runner.go`
- Create: `internal/runner/exec.go`
- Test: `internal/runner/exec_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: the `runner` block from the Interface contracts. `OnChunk` is called with `"stdout"` or `"stderr"`, and may be called concurrently from two goroutines, one per stream.

- [ ] **Step 1: Write the failing tests**

Create `internal/runner/exec_test.go`:

```go
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

func TestOutputIsBoundedAndMiddleElided(t *testing.T) {
	c := &collector{}
	if _, err := run(t, Request{
		Command: "sh",
		Args:    []string{"-c", "yes 0123456789abcdef | head -c 300000"},
		Timeout: 30 * time.Second,
		OnChunk: c.fn,
	}); err != nil {
		t.Fatal(err)
	}
	got := c.out()
	if len(got) > MaxStreamBytes+len(ElisionMarker)+4096 {
		t.Fatalf("stdout len = %d, want <= %d", len(got), MaxStreamBytes+len(ElisionMarker)+4096)
	}
	if !strings.Contains(got, ElisionMarker) {
		t.Fatalf("oversize output was not middle-elided; tail = %q", got[len(got)-120:])
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/runner/`
Expected: FAIL — `undefined: NewExec`, `undefined: MaxStreamBytes`, `undefined: ElisionMarker`

- [ ] **Step 3: Write the runner types**

Create `internal/runner/runner.go`:

```go
package runner

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ErrTimeout = errors.New("agent timed out")

const (
	MaxStreamBytes = 1 << 20
	ElisionMarker  = "\n...[fluffle: output truncated]...\n"
)

type Request struct {
	Command string
	Args    []string
	Stdin   string
	Cwd     string
	Env     []string
	Timeout time.Duration
	OnChunk func(stream string, b []byte)
}

type Result struct {
	ExitCode int
}

type Runner interface {
	Run(ctx context.Context, req Request) (Result, error)
}

type limiter struct {
	mu      sync.Mutex
	written int
	sealed  bool
}

func (l *limiter) push(b []byte) []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sealed {
		return nil
	}
	remaining := MaxStreamBytes - l.written
	if len(b) <= remaining {
		l.written += len(b)
		return b
	}
	l.sealed = true
	l.written = MaxStreamBytes
	out := make([]byte, 0, remaining+len(ElisionMarker))
	out = append(out, b[:remaining]...)
	return append(out, ElisionMarker...)
}
```

- [ ] **Step 4: Write the exec implementation**

Create `internal/runner/exec.go`:

```go
package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

type execRunner struct{}

func NewExec() Runner { return execRunner{} }

func (execRunner) Run(ctx context.Context, req Request) (Result, error) {
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.Command(req.Command, req.Args...)
	cmd.Dir = req.Cwd
	cmd.Env = mergeEnv(os.Environ(), req.Env)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if req.Stdin != "" {
		cmd.Stdin = strings.NewReader(req.Stdin)
	} else {
		cmd.Stdin = strings.NewReader("")
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return Result{}, err
	}
	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("spawn %s: %w", req.Command, err)
	}

	pgid := cmd.Process.Pid
	var killOnce sync.Once
	killGroup := func() {
		killOnce.Do(func() {
			if pgid > 0 {
				_ = syscall.Kill(-pgid, syscall.SIGKILL)
			}
		})
	}

	limits := map[string]*limiter{"stdout": {}, "stderr": {}}
	var wg sync.WaitGroup
	drain := func(stream string, r io.Reader) {
		defer wg.Done()
		lim := limits[stream]
		buf := make([]byte, 32*1024)
		for {
			n, readErr := r.Read(buf)
			if n > 0 {
				if out := lim.push(buf[:n]); len(out) > 0 && req.OnChunk != nil {
					req.OnChunk(stream, out)
				}
			}
			if readErr != nil {
				return
			}
		}
	}
	wg.Add(2)
	go drain("stdout", stdout)
	go drain("stderr", stderr)

	waitErr := cmd.Wait()
	killGroup()
	wg.Wait()

	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return Result{}, ErrTimeout
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return Result{}, ctxErr
	}
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			return Result{ExitCode: exitErr.ExitCode()}, nil
		}
		return Result{}, waitErr
	}
	return Result{ExitCode: 0}, nil
}

func mergeEnv(base, extra []string) []string {
	if len(extra) == 0 {
		return base
	}
	overrides := map[string]bool{}
	for _, kv := range extra {
		k, _, _ := strings.Cut(kv, "=")
		overrides[k] = true
	}
	out := make([]string, 0, len(base)+len(extra))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if !overrides[k] {
			out = append(out, kv)
		}
	}
	return append(out, extra...)
}
```

The imports for this file are `context`, `errors`, `fmt`, `io`, `os`, `os/exec`, `strings`, `sync`, `syscall`, and `time`.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/runner/ -v`
Expected: PASS, including `TestTimeoutKillsGrandchildren`.

If `TestTimeoutKillsGrandchildren` fails, `killpg` is not being called with the child's own pid as the group id. Check that `Setpgid: true` is set and that `pgid` is captured from `cmd.Process.Pid` after `Start`.

- [ ] **Step 6: Verify format and vet**

Run: `gofmt -l internal/runner/ && go vet ./internal/runner/`
Expected: no output.

- [ ] **Step 7: Commit**

```bash
git add internal/runner/
git commit -m "feat(runner): process-group subprocess runner with bounded output"
```

---

### Task 4: Session schema and store CRUD

`store.go` is already 767 lines, so session code goes into its own file in the same package.

**Files:**
- Create: `internal/store/session.go`
- Test: `internal/store/session_test.go`
- Modify: `internal/store/store.go:21-68` (add two `CREATE TABLE` blocks to the `schema` const)

**Interfaces:**
- Consumes: nothing.
- Produces:

```go
type Session struct {
    ID, ThreadID, TriggerMessageID              int64
    AgentName, Status, ReplyMode, Command        string
    Cwd, Error, StartedAt, FinishedAt            *string
    ExitCode, ReplyMessageID                     *int64
    CreatedAt                                    string
}
type SessionEvent struct {
    ID, SessionID, Seq                 int64
    Type, Content, CreatedAt           string
}
type ThreadContext struct {
    ThreadID, ChannelID                int64
    ThreadTitle, ChannelName           string
    RepoAbsPath                        *string
}

func (s *Store) CreateSession(threadID, triggerMessageID int64, agentName, status, replyMode, command string, cwd *string) (int64, error)
func (s *Store) ListSessions(threadID int64) ([]Session, error)
func (s *Store) GetSession(id int64) (Session, error)
func (s *Store) MarkSessionRunning(id int64, startedAt string) error
func (s *Store) SetSessionReply(id int64, replyMessageID int64) error
func (s *Store) FinishSession(id int64, status string, exitCode *int64, errMsg *string, finishedAt string) error
func (s *Store) AppendSessionEvent(sessionID int64, eventType, content string) (seq int64, id int64, err error)
func (s *Store) ListSessionEvents(sessionID int64) ([]SessionEvent, error)
func (s *Store) CountAgentMessagesSince(threadID int64, name, since string) (int, error)
func (s *Store) ThreadContext(threadID int64) (ThreadContext, error)
func (s *Store) MessageByID(id int64) (Message, error)
func (s *Store) ReconcileSessions(finishedAt string) (int, error)
```

`CreateSession` returns `ErrConflict` when `(trigger_message_id, agent_name)` already exists and `ErrNotFound` when the trigger message does not exist. `GetSession` and `MessageByID` return `ErrNotFound` for a missing id.

- [ ] **Step 1: Add the tables to the schema const**

In `internal/store/store.go`, append to the `schema` const after the `reactions` table and before the closing backtick:

```sql
CREATE TABLE IF NOT EXISTS agent_sessions(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  thread_id INTEGER NOT NULL REFERENCES threads(id),
  trigger_message_id INTEGER NOT NULL REFERENCES messages(id),
  agent_name TEXT NOT NULL CHECK(length(trim(agent_name)) > 0),
  status TEXT NOT NULL CHECK(status IN ('queued','running','succeeded','failed','canceled')),
  reply_mode TEXT NOT NULL CHECK(reply_mode IN ('stdout','cli','auto')),
  command TEXT NOT NULL CHECK(length(trim(command)) > 0),
  cwd TEXT,
  exit_code INTEGER,
  error TEXT,
  reply_message_id INTEGER REFERENCES messages(id),
  started_at TEXT,
  finished_at TEXT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE UNIQUE INDEX IF NOT EXISTS uniq_session_trigger ON agent_sessions(trigger_message_id, agent_name);
CREATE INDEX IF NOT EXISTS idx_sessions_thread ON agent_sessions(thread_id, id);
CREATE INDEX IF NOT EXISTS idx_sessions_status ON agent_sessions(status);
CREATE TABLE IF NOT EXISTS agent_session_events(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id INTEGER NOT NULL REFERENCES agent_sessions(id),
  seq INTEGER NOT NULL,
  type TEXT NOT NULL CHECK(type IN ('prompt','stdout','stderr','exit','error')),
  content TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  UNIQUE(session_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_session_events ON agent_session_events(session_id, seq);
```

These are `IF NOT EXISTS`, so the existing `migrate()` adds them to a current-schema database with no version bump and no data loss. The destructive branch in `migrate()` only fires for the old pre-`parent_id` layout.

- [ ] **Step 2: Write the failing tests**

Create `internal/store/session_test.go`:

```go
package store

import (
	"errors"
	"testing"
)

func newSessionFixture(t *testing.T) (*Store, int64) {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	chID, err := s.CreateChannel("c", "/repo", "", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	thID, err := s.CreateThread(chID, "t")
	if err != nil {
		t.Fatal(err)
	}
	return s, thID
}

func triggerMessage(t *testing.T, s *Store, thID int64, content string) int64 {
	t.Helper()
	seq, err := s.AppendMessage(thID, "alice", "human", "user", content)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.MessageIDBySeq(thID, seq)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestCreateSessionAndReadBack(t *testing.T) {
	s, thID := newSessionFixture(t)
	msgID := triggerMessage(t, s, thID, "@probe hi")
	cwd := "/repo"
	id, err := s.CreateSession(thID, msgID, "probe", SessionQueued, "auto", "probe -p {prompt}", &cwd)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSession(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ThreadID != thID || got.TriggerMessageID != msgID || got.AgentName != "probe" {
		t.Fatalf("session = %+v", got)
	}
	if got.Status != SessionQueued || got.ReplyMode != "auto" {
		t.Fatalf("status/mode = %q/%q", got.Status, got.ReplyMode)
	}
	if got.Cwd == nil || *got.Cwd != "/repo" {
		t.Fatalf("cwd = %v", got.Cwd)
	}
	if got.StartedAt != nil || got.FinishedAt != nil || got.ReplyMessageID != nil {
		t.Fatalf("unexpected set fields: %+v", got)
	}
	if got.CreatedAt == "" {
		t.Fatal("CreatedAt empty")
	}
}

func TestCreateSessionIsIdempotentPerTriggerAndAgent(t *testing.T) {
	s, thID := newSessionFixture(t)
	msgID := triggerMessage(t, s, thID, "@probe hi")
	if _, err := s.CreateSession(thID, msgID, "probe", SessionQueued, "auto", "c", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSession(thID, msgID, "probe", SessionQueued, "auto", "c", nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if _, err := s.CreateSession(thID, msgID, "other", SessionQueued, "auto", "c", nil); err != nil {
		t.Fatalf("a second agent for the same message should succeed: %v", err)
	}
}

func TestCreateSessionValidates(t *testing.T) {
	s, thID := newSessionFixture(t)
	msgID := triggerMessage(t, s, thID, "@probe hi")
	if _, err := s.CreateSession(thID, 9999, "probe", SessionQueued, "auto", "c", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing trigger: err = %v, want ErrNotFound", err)
	}
	if _, err := s.CreateSession(thID, msgID, "  ", SessionQueued, "auto", "c", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank name: err = %v, want ErrInvalid", err)
	}
	if _, err := s.CreateSession(thID, msgID, "probe", "weird", "auto", "c", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad status: err = %v, want ErrInvalid", err)
	}
	if _, err := s.CreateSession(thID, msgID, "probe", SessionQueued, "nope", "c", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad reply mode: err = %v, want ErrInvalid", err)
	}
}

func TestCreateSessionRejectsCrossThreadTrigger(t *testing.T) {
	s, thID := newSessionFixture(t)
	chID, _ := s.ListChannels("", false)
	other, _ := s.CreateThread(chID[0].ID, "other")
	msgID := triggerMessage(t, s, other, "hi")
	if _, err := s.CreateSession(thID, msgID, "probe", SessionQueued, "auto", "c", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

func TestGetSessionMissingIsNotFound(t *testing.T) {
	s, _ := newSessionFixture(t)
	if _, err := s.GetSession(9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestSessionLifecycle(t *testing.T) {
	s, thID := newSessionFixture(t)
	msgID := triggerMessage(t, s, thID, "@probe hi")
	id, _ := s.CreateSession(thID, msgID, "probe", SessionQueued, "auto", "c", nil)
	if err := s.MarkSessionRunning(id, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetSession(id)
	if got.Status != SessionRunning || got.StartedAt == nil || *got.StartedAt != "2026-01-01T00:00:00Z" {
		t.Fatalf("after running: %+v", got)
	}
	replySeq, err := s.AppendMessage(thID, "probe", "agent", "assistant", "the reply")
	if err != nil {
		t.Fatal(err)
	}
	replyID, _ := s.MessageIDBySeq(thID, replySeq)
	if err := s.SetSessionReply(id, replyID); err != nil {
		t.Fatal(err)
	}
	code := int64(0)
	if err := s.FinishSession(id, SessionSucceeded, &code, nil, "2026-01-01T00:01:00Z"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetSession(id)
	if got.Status != SessionSucceeded || got.ExitCode == nil || *got.ExitCode != 0 {
		t.Fatalf("after finish: %+v", got)
	}
	if got.ReplyMessageID == nil || *got.ReplyMessageID != replyID {
		t.Fatalf("reply id = %v", got.ReplyMessageID)
	}
	if got.Error != nil {
		t.Fatalf("error = %v", got.Error)
	}
}

func TestSessionUpdatesRejectMissingRow(t *testing.T) {
	s, _ := newSessionFixture(t)
	if err := s.MarkSessionRunning(9999, "t"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("MarkSessionRunning: %v", err)
	}
	if err := s.SetSessionReply(9999, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetSessionReply: %v", err)
	}
}

func TestFinishSessionStoresError(t *testing.T) {
	s, thID := newSessionFixture(t)
	msgID := triggerMessage(t, s, thID, "@probe hi")
	id, _ := s.CreateSession(thID, msgID, "probe", SessionQueued, "auto", "c", nil)
	msg := "agent timed out"
	if err := s.FinishSession(id, SessionFailed, nil, &msg, "2026-01-01T00:01:00Z"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetSession(id)
	if got.Status != SessionFailed || got.Error == nil || *got.Error != msg {
		t.Fatalf("session = %+v", got)
	}
}

func TestFinishSessionRejectsNonTerminalStatus(t *testing.T) {
	s, thID := newSessionFixture(t)
	msgID := triggerMessage(t, s, thID, "@probe hi")
	id, _ := s.CreateSession(thID, msgID, "probe", SessionQueued, "auto", "c", nil)
	if err := s.FinishSession(id, SessionRunning, nil, nil, "t"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

func TestSessionEventsGetMonotonicSeq(t *testing.T) {
	s, thID := newSessionFixture(t)
	msgID := triggerMessage(t, s, thID, "@probe hi")
	id, _ := s.CreateSession(thID, msgID, "probe", SessionQueued, "auto", "c", nil)
	types := []string{SessionEventPrompt, SessionEventStdout, SessionEventStdout, SessionEventExit}
	for i, typ := range types {
		seq, _, err := s.AppendSessionEvent(id, typ, "chunk")
		if err != nil {
			t.Fatal(err)
		}
		if seq != int64(i+1) {
			t.Fatalf("event %d seq = %d, want %d", i, seq, i+1)
		}
	}
	events, err := s.ListSessionEvents(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != len(types) {
		t.Fatalf("events = %+v", events)
	}
	for i, e := range events {
		if e.Seq != int64(i+1) || e.Type != types[i] || e.SessionID != id {
			t.Fatalf("event %d = %+v", i, e)
		}
		if e.CreatedAt == "" {
			t.Fatalf("event %d has no CreatedAt", i)
		}
	}
}

func TestAppendSessionEventRejectsBadTypeAndSession(t *testing.T) {
	s, thID := newSessionFixture(t)
	msgID := triggerMessage(t, s, thID, "@probe hi")
	id, _ := s.CreateSession(thID, msgID, "probe", SessionQueued, "auto", "c", nil)
	if _, _, err := s.AppendSessionEvent(id, "note", "x"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad type: err = %v, want ErrInvalid", err)
	}
	if _, _, err := s.AppendSessionEvent(9999, SessionEventStdout, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing session: err = %v, want ErrNotFound", err)
	}
}

func TestListSessionsIsOldestFirstAndNeverNil(t *testing.T) {
	s, thID := newSessionFixture(t)
	empty, err := s.ListSessions(thID)
	if err != nil {
		t.Fatal(err)
	}
	if empty == nil {
		t.Fatal("empty result must be an empty slice, not nil")
	}
	msgID := triggerMessage(t, s, thID, "@a hi")
	for _, name := range []string{"a", "b", "c"} {
		if _, err := s.CreateSession(thID, msgID, name, SessionQueued, "auto", "c", nil); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.ListSessions(thID)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"a", "b", "c"} {
		if list[i].AgentName != want {
			t.Fatalf("session %d = %q, want %q", i, list[i].AgentName, want)
		}
	}
}

func TestCountAgentMessagesSince(t *testing.T) {
	s, thID := newSessionFixture(t)
	if _, err := s.AppendMessage(thID, "alice", "human", "user", "human words"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(thID, "probe", "agent", "assistant", "agent words"); err != nil {
		t.Fatal(err)
	}
	n, err := s.CountAgentMessagesSince(thID, "probe", "1970-01-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("count = %d, want 1", n)
	}
	n, err = s.CountAgentMessagesSince(thID, "probe", "2999-01-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("future window count = %d, want 0", n)
	}
}

func TestThreadContextAndMessageByID(t *testing.T) {
	s, thID := newSessionFixture(t)
	seq, _ := s.AppendMessage(thID, "alice", "human", "user", "hello")
	msgID, _ := s.MessageIDBySeq(thID, seq)
	tc, err := s.ThreadContext(thID)
	if err != nil {
		t.Fatal(err)
	}
	if tc.ThreadID != thID || tc.ThreadTitle != "t" || tc.ChannelName != "c" {
		t.Fatalf("ctx = %+v", tc)
	}
	if tc.RepoAbsPath == nil || *tc.RepoAbsPath != "/repo" {
		t.Fatalf("repo = %v", tc.RepoAbsPath)
	}
	m, err := s.MessageByID(msgID)
	if err != nil {
		t.Fatal(err)
	}
	if m.Content != "hello" || m.Name != "alice" {
		t.Fatalf("message = %+v", m)
	}
	if _, err := s.MessageByID(9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if _, err := s.ThreadContext(9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ThreadContext: err = %v, want ErrNotFound", err)
	}
}

func TestThreadContextOrphanHasNoRepo(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	chID, _ := s.CreateChannel("orphan", "", "", "", "", true)
	thID, _ := s.CreateThread(chID, "t")
	tc, err := s.ThreadContext(thID)
	if err != nil {
		t.Fatal(err)
	}
	if tc.RepoAbsPath != nil {
		t.Fatalf("repo = %v, want nil", *tc.RepoAbsPath)
	}
}

func TestReconcileSessionsTerminatesOnlyNonTerminal(t *testing.T) {
	s, thID := newSessionFixture(t)
	msgID := triggerMessage(t, s, thID, "@a hi")
	wasRunning, _ := s.CreateSession(thID, msgID, "a", SessionQueued, "auto", "c", nil)
	if err := s.MarkSessionRunning(wasRunning, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	msg2 := triggerMessage(t, s, thID, "@b hi")
	wasQueued, _ := s.CreateSession(thID, msg2, "b", SessionQueued, "auto", "c", nil)
	done, _ := s.CreateSession(thID, msgID, "done", SessionSucceeded, "auto", "c", nil)

	n, err := s.ReconcileSessions("2026-01-01T00:10:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("reconciled = %d, want 2", n)
	}
	for _, id := range []int64{wasRunning, wasQueued} {
		got, _ := s.GetSession(id)
		if got.Status != SessionCanceled {
			t.Fatalf("session %d status = %q", id, got.Status)
		}
		if got.FinishedAt == nil || *got.FinishedAt != "2026-01-01T00:10:00Z" {
			t.Fatalf("session %d finished_at = %v", id, got.FinishedAt)
		}
		if got.Error == nil {
			t.Fatalf("session %d has no error note", id)
		}
	}
	untouched, _ := s.GetSession(done)
	if untouched.Status != SessionSucceeded {
		t.Fatalf("terminal session was modified: %q", untouched.Status)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/store/ -run 'Session|ThreadContext|MessageByID|CountAgent|Reconcile'`
Expected: FAIL — `undefined: CreateSession` and friends.

- [ ] **Step 4: Write the implementation**

Create `internal/store/session.go`:

```go
package store

import (
	"database/sql"
	"errors"
	"strings"
)

const (
	SessionQueued    = "queued"
	SessionRunning   = "running"
	SessionSucceeded = "succeeded"
	SessionFailed    = "failed"
	SessionCanceled  = "canceled"
)

const (
	SessionEventPrompt = "prompt"
	SessionEventStdout = "stdout"
	SessionEventStderr = "stderr"
	SessionEventExit   = "exit"
	SessionEventError  = "error"
)

type Session struct {
	ID               int64
	ThreadID         int64
	TriggerMessageID int64
	AgentName        string
	Status           string
	ReplyMode        string
	Command          string
	Cwd              *string
	ExitCode         *int64
	Error            *string
	ReplyMessageID   *int64
	StartedAt        *string
	FinishedAt       *string
	CreatedAt        string
}

type SessionEvent struct {
	ID        int64
	SessionID int64
	Seq       int64
	Type      string
	Content   string
	CreatedAt string
}

type ThreadContext struct {
	ThreadID    int64
	ThreadTitle string
	ChannelID   int64
	ChannelName string
	RepoAbsPath *string
}

const sessionColumns = `id, thread_id, trigger_message_id, agent_name, status, reply_mode, command,
	cwd, exit_code, error, reply_message_id, started_at, finished_at, created_at`

type scanner interface {
	Scan(dest ...any) error
}

func scanSession(row scanner) (Session, error) {
	var s Session
	err := row.Scan(&s.ID, &s.ThreadID, &s.TriggerMessageID, &s.AgentName, &s.Status, &s.ReplyMode, &s.Command,
		&s.Cwd, &s.ExitCode, &s.Error, &s.ReplyMessageID, &s.StartedAt, &s.FinishedAt, &s.CreatedAt)
	return s, err
}

func (s *Store) CreateSession(threadID, triggerMessageID int64, agentName, status, replyMode, command string, cwd *string) (int64, error) {
	if strings.TrimSpace(agentName) == "" || strings.TrimSpace(command) == "" {
		return 0, invalid("agent_name and command required")
	}
	switch status {
	case SessionQueued, SessionRunning, SessionSucceeded, SessionFailed, SessionCanceled:
	default:
		return 0, invalid("bad session status %q", status)
	}
	switch replyMode {
	case "stdout", "cli", "auto":
	default:
		return 0, invalid("bad reply mode %q", replyMode)
	}
	var msgThread int64
	if err := s.db.QueryRow(`SELECT thread_id FROM messages WHERE id = ?`, triggerMessageID).Scan(&msgThread); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	if msgThread != threadID {
		return 0, invalid("trigger message not in this thread")
	}
	res, err := s.db.Exec(`INSERT INTO agent_sessions(thread_id, trigger_message_id, agent_name, status, reply_mode, command, cwd) VALUES(?,?,?,?,?,?,?)`,
		threadID, triggerMessageID, agentName, status, replyMode, command, nullIfEmptyPtr(cwd))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return 0, ErrConflict
		}
		return 0, err
	}
	return res.LastInsertId()
}

func nullIfEmptyPtr(v *string) any {
	if v == nil || *v == "" {
		return nil
	}
	return *v
}

func (s *Store) ListSessions(threadID int64) ([]Session, error) {
	rows, err := s.db.Query(`SELECT `+sessionColumns+` FROM agent_sessions WHERE thread_id = ? ORDER BY id ASC`, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

func (s *Store) GetSession(id int64) (Session, error) {
	sess, err := scanSession(s.db.QueryRow(`SELECT `+sessionColumns+` FROM agent_sessions WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	return sess, err
}

func (s *Store) MarkSessionRunning(id int64, startedAt string) error {
	return s.execSessionUpdate(`UPDATE agent_sessions SET status = ?, started_at = ? WHERE id = ?`, SessionRunning, startedAt, id)
}

func (s *Store) SetSessionReply(id int64, replyMessageID int64) error {
	return s.execSessionUpdate(`UPDATE agent_sessions SET reply_message_id = ? WHERE id = ?`, replyMessageID, id)
}

func (s *Store) FinishSession(id int64, status string, exitCode *int64, errMsg *string, finishedAt string) error {
	switch status {
	case SessionSucceeded, SessionFailed, SessionCanceled:
	default:
		return invalid("bad terminal session status %q", status)
	}
	return s.execSessionUpdate(`UPDATE agent_sessions SET status = ?, exit_code = ?, error = ?, finished_at = ? WHERE id = ?`,
		status, exitCode, nullIfEmptyPtr(errMsg), finishedAt, id)
}

func (s *Store) execSessionUpdate(query string, args ...any) error {
	res, err := s.db.Exec(query, args...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) AppendSessionEvent(sessionID int64, eventType, content string) (int64, int64, error) {
	switch eventType {
	case SessionEventPrompt, SessionEventStdout, SessionEventStderr, SessionEventExit, SessionEventError:
	default:
		return 0, 0, invalid("bad session event type %q", eventType)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM agent_sessions WHERE id = ?`, sessionID).Scan(&exists); err != nil {
		return 0, 0, err
	}
	if exists == 0 {
		return 0, 0, ErrNotFound
	}
	var maxSeq sql.NullInt64
	if err := tx.QueryRow(`SELECT MAX(seq) FROM agent_session_events WHERE session_id = ?`, sessionID).Scan(&maxSeq); err != nil {
		return 0, 0, err
	}
	seq := int64(1)
	if maxSeq.Valid {
		seq = maxSeq.Int64 + 1
	}
	res, err := tx.Exec(`INSERT INTO agent_session_events(session_id, seq, type, content) VALUES(?,?,?,?)`, sessionID, seq, eventType, content)
	if err != nil {
		return 0, 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return seq, id, nil
}

func (s *Store) ListSessionEvents(sessionID int64) ([]SessionEvent, error) {
	rows, err := s.db.Query(`SELECT id, session_id, seq, type, content, COALESCE(created_at,'') FROM agent_session_events WHERE session_id = ? ORDER BY seq ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SessionEvent{}
	for rows.Next() {
		var e SessionEvent
		if err := rows.Scan(&e.ID, &e.SessionID, &e.Seq, &e.Type, &e.Content, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) CountAgentMessagesSince(threadID int64, name, since string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE thread_id = ? AND name = ? AND author_type = 'agent' AND created_at >= ?`, threadID, name, since).Scan(&n)
	return n, err
}

func (s *Store) ThreadContext(threadID int64) (ThreadContext, error) {
	var tc ThreadContext
	err := s.db.QueryRow(`SELECT t.id, t.title, c.id, c.name, c.repo_abs_path
		FROM threads t JOIN channels c ON c.id = t.channel_id
		WHERE t.id = ? AND t.archived_at IS NULL AND c.archived_at IS NULL`, threadID).
		Scan(&tc.ThreadID, &tc.ThreadTitle, &tc.ChannelID, &tc.ChannelName, &tc.RepoAbsPath)
	if errors.Is(err, sql.ErrNoRows) {
		return ThreadContext{}, ErrNotFound
	}
	if err != nil {
		return ThreadContext{}, err
	}
	return tc, nil
}

func (s *Store) MessageByID(id int64) (Message, error) {
	row := s.db.QueryRow(`SELECT m.id, m.thread_id, m.seq, m.parent_id, p.seq, m.name, m.author_type, m.role, m.content, COALESCE(m.created_at,'')
		FROM messages m LEFT JOIN messages p ON p.id = m.parent_id WHERE m.id = ?`, id)
	var m Message
	err := row.Scan(&m.ID, &m.ThreadID, &m.Seq, &m.ParentID, &m.ParentSeq, &m.Name, &m.AuthorType, &m.Role, &m.Content, &m.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	return m, err
}

func (s *Store) ReconcileSessions(finishedAt string) (int, error) {
	res, err := s.db.Exec(`UPDATE agent_sessions
		SET status = ?, finished_at = ?, error = 'daemon restarted while ' || status
		WHERE status IN (?, ?)`, SessionCanceled, finishedAt, SessionQueued, SessionRunning)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/store/`
Expected: PASS, including all pre-existing store tests. `ReconcileSessions` must not disturb terminal sessions.

- [ ] **Step 6: Verify format and vet**

Run: `gofmt -l internal/store/ && go vet ./internal/store/`
Expected: no output.

- [ ] **Step 7: Commit**

```bash
git add internal/store/
git commit -m "feat(store): agent session and session event tables"
```

---

### Task 5: Session manager — lifecycle, prompt, coalescing

The largest task. It owns scheduling, prompt construction, running the subprocess, and turning its output into session events. Reply resolution is deliberately **not** here; Task 6 adds it, so this task ends with sessions that run and are recorded but post no reply.

**Files:**
- Create: `internal/session/manager.go`
- Create: `internal/session/prompt.go`
- Create: `internal/session/stream.go`
- Test: `internal/session/prompt_test.go`
- Test: `internal/session/manager_test.go`

**Interfaces:**
- Consumes: `agentcfg.Loader.Resolve` / `Set.Lookup`, `runner.Runner.Run`, and the Task 4 store methods.
- Produces: the `session` block from the Interface contracts. `Start` returns immediately and never blocks a request. `Shutdown` is idempotent.

- [ ] **Step 1: Write the failing prompt tests**

Create `internal/session/prompt_test.go`:

```go
package session

import (
	"strings"
	"testing"

	"github.com/NoRaincheck/fluffle/internal/agentcfg"
	"github.com/NoRaincheck/fluffle/internal/jsonl"
	"github.com/NoRaincheck/fluffle/internal/store"
)

func strptr(s string) *string { return &s }

func entryFor(name, reply string) agentcfg.Entry {
	return agentcfg.Entry{Agent: agentcfg.Agent{Name: name, Reply: reply, TimeoutSecs: 300}, Source: "test"}
}

func TestBuildPromptContainsContextAndRequest(t *testing.T) {
	lines := []jsonl.Line{
		{Type: "message", Seq: 1, Role: "user", Name: "alice", AuthorType: "human", Content: "first", Timestamp: "2026-01-01T00:00:00Z"},
		{Type: "message", Seq: 2, Role: "assistant", Name: "probe", AuthorType: "agent", Content: "prior answer", Timestamp: "2026-01-01T00:01:00Z"},
		{Type: "reaction", MessageSeq: 1, Name: "bob", AuthorType: "human", Emoji: "👀", Timestamp: "2026-01-01T00:02:00Z"},
	}
	tc := store.ThreadContext{ThreadID: 7, ThreadTitle: "hello", ChannelID: 2, ChannelName: "general", RepoAbsPath: strptr("/repo")}
	trigger := store.Message{ID: 9, ThreadID: 7, Seq: 3, Name: "alice", AuthorType: "human", Role: "user", Content: "@probe do xyz"}

	got, err := buildPrompt(promptInput{
		Agent:         entryFor("probe", "auto"),
		Thread:        tc,
		History:       lines,
		Trigger:       trigger,
		NeedsReplying: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"## Thread", "general", "hello", "/repo",
		"## History", "prior answer", "👀",
		"## Request", "#3", "@probe do xyz",
		"## Replying", "flf message send --thread 7", "--agent-id probe",
		"stdout is posted for you",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt missing %q\n---\n%s", want, got)
		}
	}
}

func TestBuildPromptOmitsReplyingBlockForStdout(t *testing.T) {
	got, err := buildPrompt(promptInput{
		Agent:         entryFor("probe", "stdout"),
		Thread:        store.ThreadContext{ThreadID: 7, ThreadTitle: "t", ChannelName: "c", RepoAbsPath: strptr("/repo")},
		Trigger:       store.Message{ID: 1, ThreadID: 7, Seq: 1, Content: "@probe hi"},
		NeedsReplying: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "## Replying") {
		t.Fatalf("stdout mode must not include the replying block:\n%s", got)
	}
}

func TestBuildPromptCliModeHasNoStdoutFallbackLine(t *testing.T) {
	got, err := buildPrompt(promptInput{
		Agent:         entryFor("probe", "cli"),
		Thread:        store.ThreadContext{ThreadID: 7, ThreadTitle: "t", ChannelName: "c"},
		Trigger:       store.Message{ID: 1, ThreadID: 7, Seq: 1, Content: "@probe hi"},
		NeedsReplying: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "## Replying") {
		t.Fatalf("cli mode must include the replying block:\n%s", got)
	}
	if strings.Contains(got, "stdout is posted for you") {
		t.Fatalf("cli mode must not promise a stdout fallback:\n%s", got)
	}
}

func TestBuildPromptSystemPromptComesFirst(t *testing.T) {
	entry := entryFor("probe", "stdout")
	entry.SystemPrompt = "be terse"
	got, err := buildPrompt(promptInput{
		Agent:   entry,
		Thread:  store.ThreadContext{ThreadID: 1, ThreadTitle: "t", ChannelName: "c"},
		Trigger: store.Message{ID: 1, ThreadID: 1, Seq: 1, Content: "@probe hi"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "be terse") {
		t.Fatalf("system prompt must come first:\n%s", got)
	}
}

func TestBuildPromptDescribesOrphanRepo(t *testing.T) {
	got, err := buildPrompt(promptInput{
		Agent:   entryFor("probe", "stdout"),
		Thread:  store.ThreadContext{ThreadID: 1, ThreadTitle: "t", ChannelName: "c"},
		Trigger: store.Message{ID: 1, ThreadID: 1, Seq: 1, Content: "@probe hi"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "repository: none") {
		t.Fatalf("orphan repo must be described as none:\n%s", got)
	}
}

func TestBuildPromptEmptyHistorySaysSo(t *testing.T) {
	got, err := buildPrompt(promptInput{
		Agent:   entryFor("probe", "stdout"),
		Thread:  store.ThreadContext{ThreadID: 1, ThreadTitle: "t", ChannelName: "c"},
		Trigger: store.Message{ID: 1, ThreadID: 1, Seq: 1, Content: "@probe hi"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "(empty)") {
		t.Fatalf("empty history must be explicit:\n%s", got)
	}
}

func TestDeliverPromptSubstitutesArgv(t *testing.T) {
	args, stdin := deliverPrompt([]string{"-p", "{prompt}", "--flag"}, "PROMPT")
	if len(args) != 3 || args[0] != "-p" || args[1] != "PROMPT" || args[2] != "--flag" {
		t.Fatalf("args = %#v", args)
	}
	if stdin != "" {
		t.Fatalf("stdin = %q, want empty when a placeholder is present", stdin)
	}
}

func TestDeliverPromptUsesStdinWithoutPlaceholder(t *testing.T) {
	args, stdin := deliverPrompt([]string{"--json"}, "PROMPT")
	if len(args) != 1 || args[0] != "--json" {
		t.Fatalf("args = %#v", args)
	}
	if stdin != "PROMPT" {
		t.Fatalf("stdin = %q, want PROMPT", stdin)
	}
}

func TestDeliverPromptReplacesEveryOccurrence(t *testing.T) {
	args, _ := deliverPrompt([]string{"{prompt}", "{prompt}"}, "P")
	if args[0] != "P" || args[1] != "P" {
		t.Fatalf("args = %#v", args)
	}
}

func TestEnvSliceIsNilWhenEmpty(t *testing.T) {
	if envSlice(nil) != nil {
		t.Fatal("envSlice(nil) must be nil")
	}
	got := envSlice(map[string]string{"A": "1"})
	if len(got) != 1 || got[0] != "A=1" {
		t.Fatalf("envSlice = %#v", got)
	}
}
```

- [ ] **Step 2: Run the prompt tests to verify they fail**

Run: `go test ./internal/session/ -run 'TestBuildPrompt|TestDeliverPrompt|TestEnvSlice'`
Expected: FAIL — `undefined: buildPrompt`, `undefined: deliverPrompt`, `undefined: envSlice`

- [ ] **Step 3: Write the prompt builder**

Create `internal/session/prompt.go`:

```go
package session

import (
	"fmt"
	"strings"

	"github.com/NoRaincheck/fluffle/internal/agentcfg"
	"github.com/NoRaincheck/fluffle/internal/jsonl"
	"github.com/NoRaincheck/fluffle/internal/store"
)

type promptInput struct {
	Agent         agentcfg.Entry
	Thread        store.ThreadContext
	History       []jsonl.Line
	Trigger       store.Message
	NeedsReplying bool
}

func buildPrompt(in promptInput) (string, error) {
	var b strings.Builder
	if sys := strings.TrimSpace(in.Agent.SystemPrompt); sys != "" {
		b.WriteString(sys)
		b.WriteString("\n\n")
	}
	repo := "none"
	if in.Thread.RepoAbsPath != nil && *in.Thread.RepoAbsPath != "" {
		repo = *in.Thread.RepoAbsPath
	}
	fmt.Fprintf(&b, "## Thread\n\n%s > %s\nrepository: %s\n\n", in.Thread.ChannelName, in.Thread.ThreadTitle, repo)

	b.WriteString("## History\n\n")
	if len(in.History) == 0 {
		b.WriteString("(empty)\n")
	}
	for _, l := range in.History {
		encoded, err := jsonl.MarshalLine(l)
		if err != nil {
			return "", err
		}
		b.WriteString(encoded)
		b.WriteByte('\n')
	}

	fmt.Fprintf(&b, "\n## Request\n\n#%d\n%s\n", in.Trigger.Seq, in.Trigger.Content)

	if in.NeedsReplying {
		fmt.Fprintf(&b, `
## Replying

You are agent %q in fluffle thread %d.
Post your reply with:

    flf message send --thread %d --text "<your reply>" --agent-id %s
`, in.Agent.Name, in.Thread.ThreadID, in.Thread.ThreadID, in.Agent.Name)
		if in.Agent.Reply == "auto" {
			b.WriteString("\nIf you do not post, whatever you write to stdout is posted for you.\n")
		}
	}
	return b.String(), nil
}

func deliverPrompt(args []string, prompt string) ([]string, string) {
	hasPlaceholder := false
	out := make([]string, len(args))
	for i, a := range args {
		if strings.Contains(a, "{prompt}") {
			hasPlaceholder = true
			out[i] = strings.ReplaceAll(a, "{prompt}", prompt)
			continue
		}
		out[i] = a
	}
	if hasPlaceholder {
		return out, ""
	}
	return out, prompt
}

func envSlice(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}
```

- [ ] **Step 4: Run the prompt tests to verify they pass**

Run: `go test ./internal/session/ -run 'TestBuildPrompt|TestDeliverPrompt|TestEnvSlice'`
Expected: PASS

- [ ] **Step 5: Write the failing manager tests**

Create `internal/session/manager_test.go`:

```go
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
	cfg := "[[agents]]\nname=\"probe\"\ncommand=\"claude\"\nargs=[\"-p\",\"{prompt}\"]\nenv={FOO=\"bar\"}\n"
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

func TestReconcileTerminatesStaleRows(t *testing.T) {
	fr := &fakeRunner{stdout: "x"}
	s, m, thID, msgID := harness(t, probeCfg, fr)
	m.Start(thID, msgID, []string{"probe"})
	waitSession(t, s, onlySession(t, s, thID).ID, 10*time.Second)
	stale, _ := s.CreateSession(thID, msgID, "probe", store.SessionQueued, "stdout", "c", nil)
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
	fr := &fakeRunner{delay: 30 * time.Second, onStart: func() { close(started) }}
	s, m, thID, msgID := harness(t, probeCfg, fr)
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
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		list, _ := s.ListSessions(thID)
		all := len(list) > 0
		for _, sess := range list {
			if sess.Status == store.SessionQueued || sess.Status == store.SessionRunning {
				all = false
			}
		}
		if all {
			for _, sess := range list {
				if sess.Status != store.SessionCanceled {
					t.Fatalf("session %d status = %q, want canceled", sess.ID, sess.Status)
				}
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("sessions did not all reach canceled after Shutdown")
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
```

- [ ] **Step 6: Run the manager tests to verify they fail**

Run: `go test ./internal/session/`
Expected: FAIL — `undefined: NewManager`, `undefined: ErrTerminal`

- [ ] **Step 7: Write the stream writer**

Create `internal/session/stream.go`:

```go
package session

import (
	"strings"
	"sync"
	"time"

	"github.com/NoRaincheck/fluffle/internal/store"
)

type streamWriter struct {
	store   *store.Store
	session int64

	mu   sync.Mutex
	bufs map[string]*strings.Builder
	done bool

	stop chan struct{}
	wg   sync.WaitGroup
}

func newStreamWriter(s *store.Store, sessionID int64) *streamWriter {
	w := &streamWriter{
		store:   s,
		session: sessionID,
		bufs:    map[string]*strings.Builder{"stdout": {}, "stderr": {}},
		stop:    make(chan struct{}),
	}
	w.wg.Add(1)
	go w.loop()
	return w
}

func (w *streamWriter) loop() {
	defer w.wg.Done()
	ticker := time.NewTicker(StreamFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
			w.flushAll()
		}
	}
}

func (w *streamWriter) write(stream string, b []byte) {
	w.mu.Lock()
	buf, ok := w.bufs[stream]
	if !ok || w.done {
		w.mu.Unlock()
		return
	}
	buf.Write(b)
	over := buf.Len() >= StreamFlushBytes
	w.mu.Unlock()
	if over {
		w.flush(stream)
	}
}

func (w *streamWriter) flush(stream string) {
	w.mu.Lock()
	buf, ok := w.bufs[stream]
	if !ok || w.done || buf.Len() == 0 {
		w.mu.Unlock()
		return
	}
	payload := buf.String()
	buf.Reset()
	w.mu.Unlock()
	w.store.AppendSessionEvent(w.session, stream, payload)
}

func (w *streamWriter) flushAll() {
	for stream := range w.bufs {
		w.flush(stream)
	}
}

func (w *streamWriter) closeAll() {
	w.mu.Lock()
	if w.done {
		w.mu.Unlock()
		return
	}
	w.mu.Unlock()
	close(w.stop)
	w.wg.Wait()
	w.flushAll()
	w.mu.Lock()
	w.done = true
	w.mu.Unlock()
}
```

`closeAll` is only called after the runner has returned, which means both pipes are at EOF and no `write` is in flight. The `done` flag is set after the final flush so the last buffered bytes are not dropped.

- [ ] **Step 8: Write the manager**

Create `internal/session/manager.go`:

```go
package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/NoRaincheck/fluffle/internal/agentcfg"
	"github.com/NoRaincheck/fluffle/internal/jsonl"
	"github.com/NoRaincheck/fluffle/internal/runner"
	"github.com/NoRaincheck/fluffle/internal/store"
)

const (
	MaxConcurrentSessions = 4
	StreamFlushInterval   = 250 * time.Millisecond
	StreamFlushBytes      = 8 * 1024
	ReplyGracePeriod      = 2 * time.Second
	ReplyGracePoll        = 100 * time.Millisecond
)

var ErrTerminal = errors.New("session already finished")

type Manager struct {
	store  *store.Store
	agents *agentcfg.Loader
	runner runner.Runner

	sem    chan struct{}
	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	running map[int64]context.CancelFunc
	queued  map[int64]bool
	closed  bool
}

func NewManager(s *store.Store, agents *agentcfg.Loader, r runner.Runner) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		store:   s,
		agents:  agents,
		runner:  r,
		sem:     make(chan struct{}, MaxConcurrentSessions),
		ctx:     ctx,
		cancel:  cancel,
		running: map[int64]context.CancelFunc{},
		queued:  map[int64]bool{},
	}
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }

func strptr(s string) *string { return &s }

func int64ptr(v int64) *int64 { return &v }

func auditCommand(entry agentcfg.Entry) string {
	return strings.TrimSpace(entry.Command + " " + strings.Join(entry.Args, " "))
}

func (m *Manager) Start(threadID, triggerMessageID int64, names []string) {
	tc, err := m.store.ThreadContext(threadID)
	if err != nil {
		return
	}
	repoPath := ""
	if tc.RepoAbsPath != nil {
		repoPath = *tc.RepoAbsPath
	}
	set, err := m.agents.Resolve(repoPath)
	if err != nil {
		return
	}
	trigger, err := m.store.MessageByID(triggerMessageID)
	if err != nil {
		return
	}
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		entry, ok := set.Lookup(name)
		if !ok {
			continue
		}
		id, err := m.store.CreateSession(threadID, triggerMessageID, name, store.SessionQueued, entry.Reply, auditCommand(entry), tc.RepoAbsPath)
		if err != nil {
			continue
		}
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			m.store.FinishSession(id, store.SessionCanceled, nil, strptr("daemon shut down before start"), nowRFC3339())
			continue
		}
		m.queued[id] = true
		m.mu.Unlock()
		go m.dispatch(id, entry, tc, trigger)
	}
}

func (m *Manager) dispatch(id int64, entry agentcfg.Entry, tc store.ThreadContext, trigger store.Message) {
	select {
	case m.sem <- struct{}{}:
	case <-m.ctx.Done():
		m.dropQueued(id)
		m.store.FinishSession(id, store.SessionCanceled, nil, strptr("daemon shut down before start"), nowRFC3339())
		return
	}
	defer func() { <-m.sem }()

	runCtx, cancel := context.WithCancel(m.ctx)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		cancel()
		m.dropQueued(id)
		m.store.FinishSession(id, store.SessionCanceled, nil, strptr("daemon shut down before start"), nowRFC3339())
		return
	}
	delete(m.queued, id)
	m.running[id] = cancel
	m.mu.Unlock()
	defer func() {
		cancel()
		m.mu.Lock()
		delete(m.running, id)
		m.mu.Unlock()
	}()

	m.run(runCtx, id, entry, tc, trigger)
}

func (m *Manager) dropQueued(id int64) {
	m.mu.Lock()
	delete(m.queued, id)
	m.mu.Unlock()
}

func (m *Manager) run(ctx context.Context, id int64, entry agentcfg.Entry, tc store.ThreadContext, trigger store.Message) {
	if err := m.store.MarkSessionRunning(id, nowRFC3339()); err != nil {
		return
	}
	prompt, err := buildPrompt(promptInput{
		Agent:         entry,
		Thread:        tc,
		History:       m.historyFor(tc.ThreadID),
		Trigger:       trigger,
		NeedsReplying: entry.Reply != "stdout",
	})
	if err != nil {
		m.failSession(id, err)
		return
	}
	m.store.AppendSessionEvent(id, store.SessionEventPrompt, prompt)

	writer := newStreamWriter(m.store, id)
	req := runner.Request{
		Command: entry.Command,
		Env:     envSlice(entry.Env),
		Timeout: time.Duration(entry.TimeoutSecs) * time.Second,
		OnChunk: writer.write,
	}
	req.Args, req.Stdin = deliverPrompt(entry.Args, prompt)
	if tc.RepoAbsPath != nil {
		req.Cwd = *tc.RepoAbsPath
	}

	result, runErr := m.runner.Run(ctx, req)
	writer.closeAll()

	if runErr != nil {
		m.store.AppendSessionEvent(id, store.SessionEventError, runErr.Error())
		if errors.Is(ctx.Err(), context.Canceled) {
			m.store.FinishSession(id, store.SessionCanceled, nil, strptr(runErr.Error()), nowRFC3339())
			return
		}
		m.store.FinishSession(id, store.SessionFailed, nil, strptr(runErr.Error()), nowRFC3339())
		return
	}

	m.store.AppendSessionEvent(id, store.SessionEventExit, fmt.Sprintf("%d", result.ExitCode))
	m.resolveReply(id, entry, tc, result)
}

func (m *Manager) failSession(id int64, err error) {
	m.store.AppendSessionEvent(id, store.SessionEventError, err.Error())
	m.store.FinishSession(id, store.SessionFailed, nil, strptr(err.Error()), nowRFC3339())
}

func (m *Manager) historyFor(threadID int64) []jsonl.Line {
	messages, err := m.store.ListMessages(threadID, 0)
	if err != nil {
		return nil
	}
	reactions, err := m.store.ListReactions(threadID)
	if err != nil {
		reactions = nil
	}
	lines := make([]jsonl.Line, 0, len(messages)+len(reactions))
	for _, msg := range messages {
		lines = append(lines, jsonl.Line{
			Type:       "message",
			Seq:        msg.Seq,
			ParentSeq:  msg.ParentSeq.Int64,
			Role:       msg.Role,
			Name:       msg.Name,
			AuthorType: msg.AuthorType,
			Content:    msg.Content,
			Timestamp:  msg.CreatedAt,
		})
	}
	for _, r := range reactions {
		lines = append(lines, jsonl.Line{
			Type:       "reaction",
			MessageSeq: r.MessageSeq,
			Name:       r.Name,
			AuthorType: r.AuthorType,
			Emoji:      r.Emoji,
			Timestamp:  r.CreatedAt,
		})
	}
	return lines
}

func (m *Manager) Reconcile() error {
	_, err := m.store.ReconcileSessions(nowRFC3339())
	return err
}

func (m *Manager) Shutdown() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	queued := make([]int64, 0, len(m.queued))
	for id := range m.queued {
		queued = append(queued, id)
	}
	m.queued = map[int64]bool{}
	m.mu.Unlock()

	m.cancel()
	for _, id := range queued {
		m.store.FinishSession(id, store.SessionCanceled, nil, strptr("daemon shut down before start"), nowRFC3339())
	}
}

func (m *Manager) Cancel(id int64) error {
	sess, err := m.store.GetSession(id)
	if err != nil {
		return err
	}
	switch sess.Status {
	case store.SessionSucceeded, store.SessionFailed, store.SessionCanceled:
		return ErrTerminal
	}
	m.mu.Lock()
	cancel, ok := m.running[id]
	m.mu.Unlock()
	if ok {
		cancel()
		return nil
	}
	m.store.FinishSession(id, store.SessionCanceled, nil, strptr("canceled"), nowRFC3339())
	return nil
}

func (m *Manager) resolveReply(id int64, entry agentcfg.Entry, tc store.ThreadContext, result runner.Result) {
	m.store.FinishSession(id, store.SessionSucceeded, int64ptr(result.ExitCode), nil, nowRFC3339())
}
```

Note on `historyFor`: `ListMessages` already returns ascending `seq` and `ListReactions` already returns reactions ordered by message `seq`, so concatenating them produces the correct portable order with no sorting. The JSONL projection here mirrors the one in `flf thread export` (`cmd/flf/main.go`); unifying the two is out of scope but they must stay behaviorally identical, so if you change one, change both.

- [ ] **Step 9: Run the tests to verify they pass**

Run: `go test ./internal/session/ -v`
Expected: PASS, including `TestOutputIsCoalesced` and `TestCoalescedOutputLosesNothing`.

If `TestOutputIsCoalesced` fails, the byte threshold is not being applied before the event write. If `TestCoalescedOutputLosesNothing` fails, `closeAll` is setting `done` before its final flush.

- [ ] **Step 10: Verify format and vet**

Run: `gofmt -l internal/session/ && go vet ./internal/session/`
Expected: no output.

- [ ] **Step 11: Commit**

```bash
git add internal/session/
git commit -m "feat(session): manager schedules agents and records transcripts"
```

---

### Task 6: Reply resolution

The subtlest logic in the feature, isolated so a reviewer can scrutinize it on its own. Replaces the Task 5 `resolveReply` stub.

**Files:**
- Modify: `internal/session/manager.go` (replace the `resolveReply` stub)
- Test: `internal/session/reply_test.go`

**Interfaces:**
- Consumes: Task 5's `Manager`, `agentcfg.Entry`, `store.GetSession`, `store.CountAgentMessagesSince`, `store.AppendMessageWithParent`, `store.MessageIDBySeq`, `store.SetSessionReply`, `store.FinishSession`.
- Produces: no new exported names. Only the body of `resolveReply` and its helpers change.

- [ ] **Step 1: Write the failing tests**

Create `internal/session/reply_test.go`:

```go
package session

import (
	"strings"
	"testing"
	"time"

	"github.com/NoRaincheck/fluffle/internal/store"
)

func cfgWith(reply string) string {
	return "[[agents]]\nname=\"probe\"\ncommand=\"/bin/probe\"\nreply=\"" + reply + "\"\ntimeout_secs=5\n"
}

func selfPost(t *testing.T, s *store.Store, thID int64, name, text string) {
	t.Helper()
	if _, err := s.AppendMessage(thID, name, "agent", "assistant", text); err != nil {
		t.Fatal(err)
	}
}

func TestStdoutModePostsTrimmedReplyParentedToTrigger(t *testing.T) {
	s, m, thID, msgID := harness(t, cfgWith("stdout"), &fakeRunner{stdout: "  the answer  \n"})
	m.Start(thID, msgID, []string{"probe"})
	got := waitSession(t, s, onlySession(t, s, thID).ID, 10*time.Second)

	if got.Status != store.SessionSucceeded {
		t.Fatalf("status = %q", got.Status)
	}
	if got.ReplyMessageID == nil {
		t.Fatal("no reply message recorded")
	}
	msg, err := s.MessageByID(*got.ReplyMessageID)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "the answer" {
		t.Fatalf("content = %q, want trimmed stdout", msg.Content)
	}
	if msg.Name != "probe" || msg.AuthorType != "agent" || msg.Role != "assistant" {
		t.Fatalf("attribution = %+v", msg)
	}
	if msg.ParentID.Int64 != msgID {
		t.Fatalf("reply parent = %d, want the trigger %d", msg.ParentID.Int64, msgID)
	}
}

func TestStdoutModeWithBlankOutputPostsNothingButSucceeds(t *testing.T) {
	s, m, thID, msgID := harness(t, cfgWith("stdout"), &fakeRunner{stdout: "   \n\t "})
	m.Start(thID, msgID, []string{"probe"})
	got := waitSession(t, s, onlySession(t, s, thID).ID, 10*time.Second)
	if got.Status != store.SessionSucceeded {
		t.Fatalf("status = %q", got.Status)
	}
	if got.ReplyMessageID != nil {
		t.Fatalf("unexpected reply %d", *got.ReplyMessageID)
	}
	msgs, _ := s.ListMessages(thID, 0)
	if len(msgs) != 1 {
		t.Fatalf("messages = %+v", msgs)
	}
}

func TestCliModeNeverPostsOnTheAgentsBehalf(t *testing.T) {
	s, m, thID, msgID := harness(t, cfgWith("cli"), &fakeRunner{stdout: "should be ignored"})
	m.Start(thID, msgID, []string{"probe"})
	got := waitSession(t, s, onlySession(t, s, thID).ID, 10*time.Second)
	if got.ReplyMessageID != nil {
		t.Fatalf("cli mode posted a reply: %d", *got.ReplyMessageID)
	}
	msgs, _ := s.ListMessages(thID, 0)
	if len(msgs) != 1 {
		t.Fatalf("cli mode appended a message: %+v", msgs)
	}
}

func TestCliModeWithSelfPostSucceeds(t *testing.T) {
	s, m, thID, msgID := harness(t, cfgWith("cli"), &fakeRunner{})
	fr := m.runner.(*fakeRunner)
	fr.onStart = func() { selfPost(t, s, thID, "probe", "my own reply") }
	m.Start(thID, msgID, []string{"probe"})
	got := waitSession(t, s, onlySession(t, s, thID).ID, 10*time.Second)
	if got.Status != store.SessionSucceeded {
		t.Fatalf("status = %q error %v", got.Status, got.Error)
	}
	if got.ReplyMessageID != nil {
		t.Fatalf("cli mode recorded its own post as a reply: %d", *got.ReplyMessageID)
	}
}

func TestCliModeWithSilentAgentFails(t *testing.T) {
	s, m, thID, msgID := harness(t, cfgWith("cli"), &fakeRunner{stdout: "words but no post"})
	m.Start(thID, msgID, []string{"probe"})
	got := waitSession(t, s, onlySession(t, s, thID).ID, 15*time.Second)
	if got.Status != store.SessionFailed {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	if got.Error == nil || !strings.Contains(*got.Error, "did not reply") {
		t.Fatalf("error = %v", got.Error)
	}
	if !eventsContain(s, got.ID, store.SessionEventError, "did not reply") {
		t.Fatalf("no error event: %v", eventTypes(s, got.ID))
	}
}

func TestAutoModeWithSelfPostDoesNotDuplicate(t *testing.T) {
	s, m, thID, msgID := harness(t, cfgWith("auto"), &fakeRunner{stdout: "duplicate risk"})
	fr := m.runner.(*fakeRunner)
	fr.onStart = func() { selfPost(t, s, thID, "probe", "the real reply") }
	m.Start(thID, msgID, []string{"probe"})
	got := waitSession(t, s, onlySession(t, s, thID).ID, 10*time.Second)
	if got.ReplyMessageID != nil {
		t.Fatalf("auto mode duplicated the reply: %d", *got.ReplyMessageID)
	}
	msgs, _ := s.ListMessages(thID, 0)
	if len(msgs) != 2 {
		t.Fatalf("messages = %+v, want trigger plus one agent post", msgs)
	}
	if got.Status != store.SessionSucceeded {
		t.Fatalf("status = %q", got.Status)
	}
}

func TestAutoModeFallsBackToStdoutWhenSilent(t *testing.T) {
	s, m, thID, msgID := harness(t, cfgWith("auto"), &fakeRunner{stdout: "fallback reply"})
	m.Start(thID, msgID, []string{"probe"})
	got := waitSession(t, s, onlySession(t, s, thID).ID, 15*time.Second)
	if got.ReplyMessageID == nil {
		t.Fatal("auto mode did not fall back to stdout")
	}
	msg, _ := s.MessageByID(*got.ReplyMessageID)
	if msg.Content != "fallback reply" {
		t.Fatalf("content = %q", msg.Content)
	}
}

func TestAutoModeWaitsForALateAgentPost(t *testing.T) {
	s, m, thID, msgID := harness(t, cfgWith("auto"), &fakeRunner{stdout: "this must not be posted"})
	fr := m.runner.(*fakeRunner)
	fr.onStart = func() {
		go func() {
			time.Sleep(400 * time.Millisecond)
			selfPost(t, s, thID, "probe", "the real reply")
		}()
	}
	m.Start(thID, msgID, []string{"probe"})
	got := waitSession(t, s, onlySession(t, s, thID).ID, 15*time.Second)

	if got.ReplyMessageID != nil {
		t.Fatalf("the exit race produced a duplicate reply: %d", *got.ReplyMessageID)
	}
	msgs, _ := s.ListMessages(thID, 0)
	if len(msgs) != 2 {
		t.Fatalf("messages = %+v, want trigger plus the late agent post", msgs)
	}
}

func TestAnotherAgentsPostDoesNotCountAsSelfPost(t *testing.T) {
	s, m, thID, msgID := harness(t, cfgWith("auto"), &fakeRunner{stdout: "should post"})
	fr := m.runner.(*fakeRunner)
	fr.onStart = func() { selfPost(t, s, thID, "someone-else", "not mine") }
	m.Start(thID, msgID, []string{"probe"})
	got := waitSession(t, s, onlySession(t, s, thID).ID, 15*time.Second)
	if got.ReplyMessageID == nil {
		t.Fatal("another agent's post must not suppress the stdout fallback")
	}
}

func TestHumanPostDoesNotCountAsSelfPost(t *testing.T) {
	s, m, thID, msgID := harness(t, cfgWith("auto"), &fakeRunner{stdout: "should post"})
	fr := m.runner.(*fakeRunner)
	fr.onStart = func() {
		if _, err := s.AppendMessage(thID, "alice", "human", "user", "human follow up"); err != nil {
			t.Error(err)
		}
	}
	m.Start(thID, msgID, []string{"probe"})
	got := waitSession(t, s, onlySession(t, s, thID).ID, 15*time.Second)
	if got.ReplyMessageID == nil {
		t.Fatal("a human post must not suppress the stdout fallback")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/session/ -run 'Stdout|Cli|Auto|SelfPost' -v`
Expected: FAIL — `TestStdoutModePostsTrimmedReplyParentedToTrigger` fails because the Task 5 stub never posts.

- [ ] **Step 3: Implement reply resolution**

In `internal/session/manager.go`, replace the `resolveReply` stub with:

```go
func (m *Manager) resolveReply(id int64, entry agentcfg.Entry, tc store.ThreadContext, result runner.Result) {
	exit := int64ptr(result.ExitCode)
	if entry.Reply == "stdout" {
		m.postReply(id, entry, tc, m.collectStdout(id), exit)
		return
	}
	if m.agentPosted(tc.ThreadID, entry.Name, id) {
		m.store.FinishSession(id, store.SessionSucceeded, exit, nil, nowRFC3339())
		return
	}
	if entry.Reply == "cli" {
		const msg = "agent did not reply (reply=cli)"
		m.store.AppendSessionEvent(id, store.SessionEventError, msg)
		m.store.FinishSession(id, store.SessionFailed, exit, strptr(msg), nowRFC3339())
		return
	}
	m.postReply(id, entry, tc, m.collectStdout(id), exit)
}

func (m *Manager) collectStdout(sessionID int64) string {
	events, err := m.store.ListSessionEvents(sessionID)
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, e := range events {
		if e.Type == store.SessionEventStdout {
			b.WriteString(e.Content)
		}
	}
	return strings.TrimSpace(b.String())
}

func (m *Manager) agentPosted(threadID int64, name string, sessionID int64) bool {
	sess, err := m.store.GetSession(sessionID)
	if err != nil {
		return false
	}
	since := nowRFC3339()
	if sess.StartedAt != nil {
		since = *sess.StartedAt
	}
	deadline := time.Now().Add(ReplyGracePeriod)
	for {
		n, err := m.store.CountAgentMessagesSince(threadID, name, since)
		if err == nil && n > 0 {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(ReplyGracePoll)
	}
}

func (m *Manager) postReply(id int64, entry agentcfg.Entry, tc store.ThreadContext, content string, exit *int64) {
	if strings.TrimSpace(content) == "" {
		m.store.FinishSession(id, store.SessionSucceeded, exit, nil, nowRFC3339())
		return
	}
	sess, err := m.store.GetSession(id)
	if err != nil {
		m.store.FinishSession(id, store.SessionFailed, nil, strptr(err.Error()), nowRFC3339())
		return
	}
	seq, err := m.store.AppendMessageWithParent(tc.ThreadID, entry.Name, "agent", "assistant", content, sess.TriggerMessageID)
	if err != nil {
		m.store.FinishSession(id, store.SessionFailed, exit, strptr(err.Error()), nowRFC3339())
		return
	}
	replyID, err := m.store.MessageIDBySeq(tc.ThreadID, seq)
	if err != nil {
		m.store.FinishSession(id, store.SessionFailed, exit, strptr(err.Error()), nowRFC3339())
		return
	}
	m.store.SetSessionReply(id, replyID)
	m.store.FinishSession(id, store.SessionSucceeded, exit, nil, nowRFC3339())
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/session/ -v`
Expected: PASS, including `TestAutoModeWaitsForALateAgentPost` and `TestCliModeWithSilentAgentFails`.

The two tests that wait out the grace period take about 2s each. Do not shorten `ReplyGracePeriod` below one second; the exit race returns.

- [ ] **Step 5: Verify format and vet**

Run: `gofmt -l internal/session/ && go vet ./internal/session/`
Expected: no output.

- [ ] **Step 6: Commit**

```bash
git add internal/session/
git commit -m "feat(session): resolve agent replies via stdout, cli, or auto"
```

---

### Task 7: API routes and mention trigger

**Files:**
- Create: `internal/apiserver/sessions.go`
- Test: `internal/apiserver/sessions_test.go`
- Modify: `internal/apiserver/server.go` — `NewHandler` (line 366), `appendThreadMessage` (line 170), `appendThreadEvents` (line 281), route registration (lines 483-525)

**Interfaces:**
- Consumes: `mentions.Parse`, `session.ErrTerminal`, `agentcfg.Loader`, and the Task 4 store methods.
- Produces: the `apiserver` block from the Interface contracts. `NewHandler` keeps its existing signature so all 15 existing test call sites keep compiling and see no session side effects.
- New routes: `GET /v1/threads/:id/sessions`, `GET /v1/sessions/:id`, `GET /v1/agents`, `POST /v1/sessions/:id/cancel`.

- [ ] **Step 1: Write the failing tests**

Create `internal/apiserver/sessions_test.go`:

```go
package apiserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/NoRaincheck/fluffle/internal/agentcfg"
	"github.com/NoRaincheck/fluffle/internal/store"
)

type recordingStarter struct {
	mu    sync.Mutex
	calls []string
}

func (r *recordingStarter) Start(threadID, triggerMessageID int64, names []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, fmt.Sprintf("%d:%d:%s", threadID, triggerMessageID, strings.Join(names, ",")))
}

func (r *recordingStarter) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.calls))
	copy(out, r.calls)
	return out
}

type stubCanceler struct {
	mu        sync.Mutex
	canceled  []int64
	returnErr error
}

func (c *stubCanceler) Cancel(id int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.returnErr != nil {
		return c.returnErr
	}
	c.canceled = append(c.canceled, id)
	return nil
}

func newTestLoader(t *testing.T, cfg string) *agentcfg.Loader {
	t.Helper()
	dir := t.TempDir()
	global := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(global, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	return agentcfg.NewLoader(global)
}

func newSessionHandler(t *testing.T, cfg string) (*store.Store, http.Handler, *recordingStarter, *stubCanceler, int64) {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	chID, _ := s.CreateChannel("c", "/repo", "", "", "", false)
	thID, _ := s.CreateThread(chID, "t")
	starter := &recordingStarter{}
	canceler := &stubCanceler{}
	h := NewHandlerWithDeps(s, Deps{Starter: starter, Agents: newTestLoader(t, cfg), Canceler: canceler})
	return s, h, starter, canceler, thID
}

func postThread(t *testing.T, h http.Handler, threadID int64, path, body, agentHeader string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/threads/%d/%s", threadID, path), strings.NewReader(body))
	if agentHeader != "" {
		req.Header.Set("X-Fluffle-Agent", agentHeader)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

const oneAgentCfg = "[[agents]]\nname=\"reviewer\"\ncommand=\"claude\"\n"

func TestLeadingMentionTriggersSession(t *testing.T) {
	_, h, starter, _, thID := newSessionHandler(t, oneAgentCfg)
	rec := postThread(t, h, thID, "messages", `{"name":"alice","role":"user","content":"@reviewer do xyz"}`, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	calls := starter.snapshot()
	if len(calls) != 1 {
		t.Fatalf("calls = %v", calls)
	}
	if want := fmt.Sprintf("%d:2:reviewer", thID); calls[0] != want {
		t.Fatalf("call = %q, want %q", calls[0], want)
	}
}

func TestNonLeadingMentionDoesNotTrigger(t *testing.T) {
	_, h, starter, _, thID := newSessionHandler(t, oneAgentCfg)
	postThread(t, h, thID, "messages", `{"name":"alice","role":"user","content":"don't @reviewer do that"}`, "")
	if len(starter.snapshot()) != 0 {
		t.Fatalf("calls = %v", starter.snapshot())
	}
}

func TestMultipleLeadingMentionsAreAllPassed(t *testing.T) {
	cfg := "[[agents]]\nname=\"a1\"\ncommand=\"x\"\n\n[[agents]]\nname=\"b2\"\ncommand=\"y\"\n"
	_, h, starter, _, thID := newSessionHandler(t, cfg)
	postThread(t, h, thID, "messages", `{"name":"alice","role":"user","content":"@a1 @b2 go"}`, "")
	calls := starter.snapshot()
	if len(calls) != 1 || calls[0] != fmt.Sprintf("%d:2:a1,b2", thID) {
		t.Fatalf("calls = %v", calls)
	}
}

func TestAgentAppendNeverTriggers(t *testing.T) {
	_, h, starter, _, thID := newSessionHandler(t, oneAgentCfg)
	rec := postThread(t, h, thID, "messages", `{"name":"probe","role":"assistant","content":"@reviewer ping"}`, "probe")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if len(starter.snapshot()) != 0 {
		t.Fatalf("agent append triggered a session: %v", starter.snapshot())
	}
}

func TestBatchEventsTriggerOncePerMentioningMessage(t *testing.T) {
	_, h, starter, _, thID := newSessionHandler(t, oneAgentCfg)
	rec := postThread(t, h, thID, "events", `{"events":[{"name":"alice","role":"user","content":"@reviewer go"}]}`, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if len(starter.snapshot()) != 1 {
		t.Fatalf("calls = %v", starter.snapshot())
	}
	rec2 := postThread(t, h, thID, "events", `{"events":[{"name":"a","role":"user","content":"@reviewer one"},{"name":"b","role":"user","content":"plain"},{"name":"c","role":"user","content":"@reviewer two"}]}`, "")
	if rec2.Code != http.StatusOK {
		t.Fatalf("batch status = %d body %s", rec2.Code, rec2.Body.String())
	}
	if len(starter.snapshot()) != 2 {
		t.Fatalf("calls = %v, want 2", starter.snapshot())
	}
}

func TestAgentBatchNeverTriggers(t *testing.T) {
	_, h, starter, _, thID := newSessionHandler(t, oneAgentCfg)
	postThread(t, h, thID, "events", `{"events":[{"name":"probe","role":"assistant","content":"@reviewer go"}]}`, "probe")
	if len(starter.snapshot()) != 0 {
		t.Fatalf("agent batch triggered a session: %v", starter.snapshot())
	}
}

func TestZeroDepsHandlerIsSafeOnMention(t *testing.T) {
	s, h, _ := newTestHandlerWithThread(t)
	chans, _ := s.ListChannels("", false)
	thID, _ := s.CreateThread(chans[0].ID, "t")
	rec := postThread(t, h, thID, "messages", `{"name":"alice","role":"user","content":"@reviewer go"}`, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
}

func TestListThreadSessionsReturnsArray(t *testing.T) {
	s, h, _, _, thID := newSessionHandler(t, "")
	rec := serveRequest(t, h, http.MethodGet, fmt.Sprintf("/v1/threads/%d/sessions", thID), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("empty sessions must serialize as [], got %s", rec.Body.String())
	}
}

func TestListThreadSessionsReturnsRows(t *testing.T) {
	s, h, _, _, thID := newSessionHandler(t, "")
	seq, _ := s.AppendMessage(thID, "alice", "human", "user", "go")
	msgID, _ := s.MessageIDBySeq(thID, seq)
	if _, err := s.CreateSession(thID, msgID, "probe", store.SessionSucceeded, "stdout", "c", nil); err != nil {
		t.Fatal(err)
	}
	rec := serveRequest(t, h, http.MethodGet, fmt.Sprintf("/v1/threads/%d/sessions", thID), "")
	var out []store.Session
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].AgentName != "probe" {
		t.Fatalf("sessions = %+v", out)
	}
}

func TestListThreadSessionsMissingThreadIsNotFound(t *testing.T) {
	_, h, _, _, _ := newSessionHandler(t, "")
	rec := serveRequest(t, h, http.MethodGet, "/v1/threads/9999/sessions", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
}

func TestGetSessionIncludesEvents(t *testing.T) {
	s, h, _, _, thID := newSessionHandler(t, "")
	seq, _ := s.AppendMessage(thID, "alice", "human", "user", "go")
	msgID, _ := s.MessageIDBySeq(thID, seq)
	id, _ := s.CreateSession(thID, msgID, "probe", store.SessionSucceeded, "stdout", "c", nil)
	if _, _, err := s.AppendSessionEvent(id, store.SessionEventStdout, "hello"); err != nil {
		t.Fatal(err)
	}
	rec := serveRequest(t, h, http.MethodGet, fmt.Sprintf("/v1/sessions/%d", id), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Session store.Session         `json:"session"`
		Events  []store.SessionEvent `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Session.ID != id || len(out.Events) != 1 || out.Events[0].Content != "hello" {
		t.Fatalf("out = %+v", out)
	}
}

func TestGetSessionEmptyEventsIsArray(t *testing.T) {
	s, h, _, _, thID := newSessionHandler(t, "")
	seq, _ := s.AppendMessage(thID, "alice", "human", "user", "go")
	msgID, _ := s.MessageIDBySeq(thID, seq)
	id, _ := s.CreateSession(thID, msgID, "probe", store.SessionSucceeded, "stdout", "c", nil)
	rec := serveRequest(t, h, http.MethodGet, fmt.Sprintf("/v1/sessions/%d", id), "")
	if !strings.Contains(rec.Body.String(), `"events":[]`) {
		t.Fatalf("empty events must serialize as [], got %s", rec.Body.String())
	}
}

func TestGetSessionMissingIsSessionNotFound(t *testing.T) {
	_, h, _, _, _ := newSessionHandler(t, "")
	rec := serveRequest(t, h, http.MethodGet, "/v1/sessions/9999", "")
	assertErrorEnvelope(t, rec, http.StatusNotFound, "SESSION_NOT_FOUND")
}

func TestGetSessionRejectsBadID(t *testing.T) {
	_, h, _, _, _ := newSessionHandler(t, "")
	rec := serveRequest(t, h, http.MethodGet, "/v1/sessions/abc", "")
	assertErrorEnvelope(t, rec, http.StatusBadRequest, "BAD_JSONL")
}

func TestListAgentsMergesRepoOverGlobal(t *testing.T) {
	dir := t.TempDir()
	repoPath := filepath.Join(dir, "repo")
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoPath, ".flf.toml"), []byte("[[agents]]\nname=\"local\"\ncommand=\"c\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, h, _, _, _ := newSessionHandler(t, "[[agents]]\nname=\"shared\"\ncommand=\"g\"\nreply=\"cli\"\n")
	rec := serveRequest(t, h, http.MethodGet, "/v1/agents?repo="+repoPath, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Agents []agentListItem `json:"agents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	byName := map[string]agentListItem{}
	for _, a := range out.Agents {
		byName[a.Name] = a
	}
	if _, ok := byName["local"]; !ok {
		t.Fatalf("repo agent missing: %+v", out.Agents)
	}
	if byName["shared"].Reply != "cli" {
		t.Fatalf("global agent = %+v", byName["shared"])
	}
}

func TestListAgentsRejectsPost(t *testing.T) {
	_, h, _, _, _ := newSessionHandler(t, "")
	rec := serveRequest(t, h, http.MethodPost, "/v1/agents", "")
	assertErrorEnvelope(t, rec, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
}

func TestCancelSucceedsForHumans(t *testing.T) {
	s, h, _, canceler, thID := newSessionHandler(t, "")
	seq, _ := s.AppendMessage(thID, "alice", "human", "user", "go")
	msgID, _ := s.MessageIDBySeq(thID, seq)
	id, _ := s.CreateSession(thID, msgID, "probe", store.SessionRunning, "stdout", "c", nil)
	rec := serveRequest(t, h, http.MethodPost, fmt.Sprintf("/v1/sessions/%d/cancel", id), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if len(canceler.canceled) != 1 || canceler.canceled[0] != id {
		t.Fatalf("canceler calls = %v", canceler.canceled)
	}
}

func TestCancelRejectsAgents(t *testing.T) {
	s, h, _, _, thID := newSessionHandler(t, "")
	seq, _ := s.AppendMessage(thID, "alice", "human", "user", "go")
	msgID, _ := s.MessageIDBySeq(thID, seq)
	id, _ := s.CreateSession(thID, msgID, "probe", store.SessionRunning, "stdout", "c", nil)
	rec := postSession(t, h, id, "probe")
	assertErrorEnvelope(t, rec, http.StatusForbidden, "AGENT_FORBIDDEN")
}

func TestCancelFinishedSessionIsConflict(t *testing.T) {
	s, h, _, canceler, thID := newSessionHandler(t, "")
	seq, _ := s.AppendMessage(thID, "alice", "human", "user", "go")
	msgID, _ := s.MessageIDBySeq(thID, seq)
	id, _ := s.CreateSession(thID, msgID, "probe", store.SessionSucceeded, "stdout", "c", nil)
	canceler.returnErr = session.ErrTerminal
	rec := serveRequest(t, h, http.MethodPost, fmt.Sprintf("/v1/sessions/%d/cancel", id), "")
	assertErrorEnvelope(t, rec, http.StatusConflict, "SESSION_FINISHED")
}

func TestCancelMissingSessionIsNotFound(t *testing.T) {
	_, h, _, _, _ := newSessionHandler(t, "")
	rec := serveRequest(t, h, http.MethodPost, "/v1/sessions/9999/cancel", "")
	assertErrorEnvelope(t, rec, http.StatusNotFound, "SESSION_NOT_FOUND")
}

func postSession(t *testing.T, h http.Handler, sessionID int64, agentHeader string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/sessions/%d/cancel", sessionID), nil)
	req.Header.Set("X-Fluffle-Agent", agentHeader)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}
```

Add `"github.com/NoRaincheck/fluffle/internal/session"` to the imports.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/apiserver/ -run 'Mention|Session|ListAgents|Cancel|Batch|ZeroDeps'`
Expected: FAIL — `undefined: NewHandlerWithDeps`, `undefined: Deps`, `undefined: agentListItem`

- [ ] **Step 3: Write the session routes**

Create `internal/apiserver/sessions.go`:

```go
package apiserver

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/NoRaincheck/fluffle/internal/agentcfg"
	"github.com/NoRaincheck/fluffle/internal/mentions"
	"github.com/NoRaincheck/fluffle/internal/session"
	"github.com/NoRaincheck/fluffle/internal/store"
)

type SessionStarter interface {
	Start(threadID, triggerMessageID int64, names []string)
}

type Deps struct {
	Starter SessionStarter
	Agents  *agentcfg.Loader
	Canceler interface {
		Cancel(id int64) error
	}
}

type agentListItem struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Command     string `json:"command"`
	Reply       string `json:"reply"`
	Source      string `json:"source"`
}

func (d Deps) startSessionForMessage(threadID, messageID int64, content string) {
	if d.Starter == nil {
		return
	}
	names, _ := mentions.Parse(content)
	if len(names) == 0 {
		return
	}
	d.Starter.Start(threadID, messageID, names)
}

func (d Deps) startSessionsForBatch(threadID int64, results []store.AppendResult) {
	if d.Starter == nil {
		return
	}
	for _, r := range results {
		if r.MessageID == 0 {
			continue
		}
		msg, err := d.store.MessageByID(r.MessageID)
		if err != nil {
			continue
		}
		d.startSessionForMessage(threadID, r.MessageID, msg.Content)
	}
}

func registerSessionRoutes(mux *http.ServeMux, s *store.Store, d Deps) {
	mux.HandleFunc("/v1/sessions/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/v1/sessions/")
		parts := strings.Split(rest, "/")
		id, valid := parsePositiveRouteID(parts[0])
		if !valid {
			writeErr(w, http.StatusBadRequest, "BAD_JSONL", "session id must be a positive integer")
			return
		}
		switch {
		case len(parts) == 1 && r.Method == http.MethodGet:
			serveSession(s, id, w)
		case len(parts) == 2 && parts[1] == "cancel" && r.Method == http.MethodPost:
			serveCancel(s, d, id, w, r)
		default:
			writeErr(w, http.StatusNotFound, "SESSION_NOT_FOUND", "unknown route")
		}
	})
	mux.HandleFunc("/v1/agents", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
			return
		}
		serveAgentList(d, w, r)
	})
}

func serveSession(s *store.Store, id int64, w http.ResponseWriter) {
	sess, err := s.GetSession(id)
	if err != nil {
		writeSessionError(w, err)
		return
	}
	events, err := s.ListSessionEvents(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "DAEMON_ERROR", err.Error())
		return
	}
	if events == nil {
		events = []store.SessionEvent{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": sess, "events": events})
}

func serveCancel(s *store.Store, d Deps, id int64, w http.ResponseWriter, r *http.Request) {
	if isAgent(r) {
		writeErr(w, http.StatusForbidden, "AGENT_FORBIDDEN", "agents cannot cancel sessions")
		return
	}
	if _, err := s.GetSession(id); err != nil {
		writeSessionError(w, err)
		return
	}
	if d.Canceler == nil {
		writeErr(w, http.StatusServiceUnavailable, "DAEMON_ERROR", "agent execution is not available")
		return
	}
	if err := d.Canceler.Cancel(id); err != nil {
		if errors.Is(err, session.ErrTerminal) {
			writeErr(w, http.StatusConflict, "SESSION_FINISHED", "session already finished")
			return
		}
		writeSessionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func serveAgentList(d Deps, w http.ResponseWriter, r *http.Request) {
	if d.Agents == nil {
		writeJSON(w, http.StatusOK, map[string]any{"agents": []agentListItem{}})
		return
	}
	set, err := d.Agents.Resolve(r.URL.Query().Get("repo"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "DAEMON_ERROR", err.Error())
		return
	}
	entries := set.Entries()
	out := make([]agentListItem, 0, len(entries))
	for _, e := range entries {
		out = append(out, agentListItem{
			Name:        e.Name,
			Description: e.Description,
			Command:     e.Command,
			Reply:       e.Reply,
			Source:      e.Source,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": out})
}

func writeSessionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "SESSION_NOT_FOUND", "no such session")
	case errors.Is(err, store.ErrInvalid):
		writeErr(w, http.StatusBadRequest, "BAD_JSONL", err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, "DAEMON_ERROR", err.Error())
	}
}
```

The imports for this file are `errors`, `net/http`, `strings`, plus the four fluffle packages. `startSessionsForBatch` takes the store as a parameter, so `Deps` carries no hidden state and needs no `store` field.

- [ ] **Step 4: Add the Deps seam and register the routes**

In `internal/apiserver/server.go`, change line 366 to:

```go
func NewHandler(s *store.Store) http.Handler {
	return NewHandlerWithDeps(s, Deps{})
}

func NewHandlerWithDeps(s *store.Store, d Deps) http.Handler {
	mux := http.NewServeMux()
	// existing registrations unchanged
```

Immediately before the final `return jsonMuxHandler(mux)`, add:

```go
	registerSessionRoutes(mux, s, d)
	return jsonMuxHandler(mux)
```

Go's `http.ServeMux` panics on duplicate pattern registration, and `/v1/threads/` is already registered at `server.go:483`. That is why the Step 3 code registers only `/v1/sessions/` and `/v1/agents`, which are new prefixes. Route the thread-sessions path into the existing handler instead: inside the `/v1/threads/` handler, extend the `knownRoute` check to include `len(parts) == 2 && parts[1] == "sessions"`, and add a case to the switch:

```go
		case len(parts) == 2 && parts[1] == "sessions":
			if r.Method != http.MethodGet {
				writeErr(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
				return
			}
			list, err := s.ListSessions(threadID)
			if err != nil {
				writeSessionError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, list)
```

- [ ] **Step 5: Wire the trigger into the append handlers**

In `internal/apiserver/server.go`, change the signature of `appendThreadMessage` to accept `deps Deps`:

```go
func appendThreadMessage(s *store.Store, deps Deps, threadID int64, w http.ResponseWriter, r *http.Request) {
```

and at the end of the function, after `seq` is assigned successfully and before `writeJSON`, add:

```go
	if triggerMessageID, idErr := s.MessageIDBySeq(threadID, seq); idErr == nil {
		deps.startSessionForMessage(threadID, triggerMessageID, body.Content)
	}
```

Update the call site in the `/v1/threads/` route to pass `deps`.

In `appendThreadEvents`, change the signature to accept `deps Deps` as well:

```go
func appendThreadEvents(s *store.Store, deps Deps, threadID int64, w http.ResponseWriter, r *http.Request) {
```

and immediately after the `results, err := s.AppendBatch(...)` error check, add:

```go
	if !agentRequest {
		deps.startSessionsForBatch(s, threadID, results)
	}
```

The `!agentRequest` guard is the anti-loop rule. `agentRequest` is already computed in that function as `isAgent(r)`. This duplicates the check `appendThreadMessage` gets for free from `liveAuthorType`, so make it explicit in both handlers rather than relying on it implicitly.

Update the call site in the route to pass `deps`.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/apiserver/ -v`
Expected: PASS, including all pre-existing apiserver tests. If `http.ServeMux` panics with "multiple registrations", you left the duplicate `/v1/threads/` handler in `sessions.go`.

- [ ] **Step 7: Verify format and vet**

Run: `gofmt -l internal/apiserver/ && go vet ./internal/apiserver/`
Expected: no output.

- [ ] **Step 8: Commit**

```bash
git add internal/apiserver/
git commit -m "feat(apiserver): session routes and mention trigger"
```

---

### Task 8: Daemon wiring, startup reconciliation, shutdown

**Files:**
- Modify: `cmd/flf/main.go` — `daemonStart` (line 1195) and the shutdown callback
- Test: `cmd/flf/main_test.go`

**Interfaces:**
- Consumes: `session.NewManager`, `agentcfg.NewLoader`, `runner.NewExec`, `client.FluffleHome`, `apiserver.NewHandlerWithDeps`.
- Produces: no new exported names. The daemon gains a `*session.Manager` and passes it into the handler.

- [ ] **Step 1: Write the failing test**

Add to `cmd/flf/main_test.go`:

```go
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
```

Add `"github.com/NoRaincheck/fluffle/internal/agentcfg"`, `"github.com/NoRaincheck/fluffle/internal/runner"`, `"github.com/NoRaincheck/fluffle/internal/session"` and `"strings"` to the imports if they are not already present.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/flf/ -run TestDaemonStartReconcilesStaleSessions`
Expected: FAIL — `undefined: session.NewManager` is available but `Reconcile` does not exist yet only if Task 5 was skipped. If Task 5 is done this test should already pass, which means the reconciliation wiring in `daemonStart` is the part still missing. Verify by continuing to Step 3.

- [ ] **Step 3: Wire the manager into daemonStart**

In `cmd/flf/main.go`, in `daemonStart`, after the store is opened and before `srv := &http.Server{...}`, add:

```go
	agents := agentcfg.NewLoader(filepath.Join(home, "config.toml"))
	sessions := session.NewManager(s, agents, runner.NewExec())
	if _, err := s.ReconcileSessions(time.Now().UTC().Format(time.RFC3339)); err != nil {
		return fail("DAEMON_ERROR", err.Error())
	}
```

then change the handler construction and shutdown callback to:

```go
	srv := &http.Server{Handler: apiserver.NewHandlerWithDeps(s, apiserver.Deps{
		Starter:  sessions,
		Agents:   agents,
		Canceler: sessions,
	})}
	apiserver.SetShutdown(func() {
		sessions.Shutdown()
		srv.Close()
	})
```

Add the three imports:

```go
	"github.com/NoRaincheck/fluffle/internal/agentcfg"
	"github.com/NoRaincheck/fluffle/internal/runner"
	"github.com/NoRaincheck/fluffle/internal/session"
```

`ReconcileSessions` is called directly on the store rather than through `Manager.Reconcile` so the daemon does not depend on a fresh manager for a pure database operation. Either is acceptable; if you prefer the manager, call `sessions.Reconcile()` instead and drop the store call.

Shutdown order matters: `sessions.Shutdown()` must run **before** `srv.Close()` so that any in-flight agent is told to stop and killed while the store is still usable for its final `FinishSession` write.

- [ ] **Step 4: Verify format, vet, and the whole build**

Run: `gofmt -l cmd/ internal/ && go vet ./... && go build ./...`
Expected: no output and a successful build.

- [ ] **Step 5: Run the full test suite**

Run: `go test ./...`
Expected: PASS. This is the first task where the whole module is exercised together.

- [ ] **Step 6: Commit**

```bash
git add cmd/flf/main.go cmd/flf/main_test.go
git commit -m "feat(daemon): own agent session execution and reconcile on start"
```

---

### Task 9: CLI subcommands

**Files:**
- Create: `cmd/flf/agents.go`
- Modify: `cmd/flf/main.go` — the `agentCmd` switch (add `list` and `session` cases)
- Test: `cmd/flf/e2e_test.go` (the existing CLI-output harness lives there, not in `main_test.go`)

**Interfaces:**
- Consumes: `apiserver.Deps` wire format for `GET /v1/agents` and `GET /v1/sessions/:id`, through the existing `apiGet` helper.
- Produces: `flf agent list [--repo PATH] [--json]` and `flf agent session --id N [--json]`.

Reuse these existing helpers rather than adding a second HTTP path. Their exact signatures are in `cmd/flf/main.go`:

```go
func newFlagSet(name string) *flag.FlagSet                                   // line 306
func apiGet(u, agentID string, out any, check apiResponseCheck) int          // line 85
type apiResponseCheck func() error
```

`apiGet` already handles the `DAEMON_DOWN` / `DAEMON_ERROR` classification, maps `>= 400` through `printAPIError`, and runs `decodeStrictJSON`. It returns a process exit code directly, so a subcommand is `if code := apiGet(...); code != 0 { return code }`.

- [ ] **Step 1: Write the failing tests**

Append to `cmd/flf/e2e_test.go`:

```go
func TestE2E_AgentListCmd(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	gitInit(t, repo)
	repoCfg := "[[agents]]\nname=\"local\"\ncommand=\"/bin/l\"\ndescription=\"repo agent\"\n"
	if err := os.WriteFile(filepath.Join(repo, ".flf.toml"), []byte(repoCfg), 0o644); err != nil {
		t.Fatal(err)
	}
	globalCfg := "[[agents]]\nname=\"shared\"\ncommand=\"/bin/s\"\nreply=\"cli\"\n"
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(globalCfg), 0o644); err != nil {
		t.Fatal(err)
	}

	bin := buildTestBinary(t, t.TempDir())
	env := []string{"FLUFFLE_HOME=" + home}
	runCLI(t, env, bin, "daemon", "start", "--background")
	t.Cleanup(func() { runCLI(t, env, bin, "daemon", "stop") })
	waitForDaemonReady(t, home, 10*time.Second)

	out := runCLI(t, env, bin, "agent", "list", "--repo", repo, "--json")
	var payload struct {
		Agents []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Command     string `json:"command"`
			Reply       string `json:"reply"`
			Source      string `json:"source"`
		} `json:"agents"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	byName := map[string]int{}
	for i, a := range payload.Agents {
		byName[a.Name] = i
	}
	localIdx, okLocal := byName["local"]
	sharedIdx, okShared := byName["shared"]
	if !okLocal || !okShared {
		t.Fatalf("agents = %+v, want both the repo and the global entry", payload.Agents)
	}
	if payload.Agents[localIdx].Description != "repo agent" {
		t.Fatalf("local = %+v", payload.Agents[localIdx])
	}
	if payload.Agents[sharedIdx].Reply != "cli" {
		t.Fatalf("shared = %+v", payload.Agents[sharedIdx])
	}
	if !strings.Contains(payload.Agents[localIdx].Source, ".flf.toml") {
		t.Fatalf("local source = %q, want the repo config", payload.Agents[localIdx].Source)
	}

	human := runCLI(t, env, bin, "agent", "list", "--repo", repo)
	if !strings.Contains(human, "local") || !strings.Contains(human, "/bin/l") {
		t.Fatalf("human output = %q", human)
	}
}

func TestE2E_AgentSessionCmd(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := buildTestBinary(t, t.TempDir())
	env := []string{"FLUFFLE_HOME=" + home}
	runCLI(t, env, bin, "daemon", "start", "--background")
	t.Cleanup(func() { runCLI(t, env, bin, "daemon", "stop") })
	waitForDaemonReady(t, home, 10*time.Second)

	repo := t.TempDir()
	gitInit(t, repo)
	channelID := jsonID(t, runCLI(t, env, bin, "channel", "create", "--name", "eng", "--repo", repo, "--json"))
	threadID := jsonID(t, runCLI(t, env, bin, "thread", "new", "--channel", "eng", "--repo", repo, "--title", "t", "--json"))
	runCLI(t, env, bin, "message", "send", "--thread", threadID, "--text", "hello", "--as", "alice", "--json")

	base := fmt.Sprintf("http://127.0.0.1:%d", daemonPort(t, home))
	seq := postMessageOverHTTP(t, base, threadID, "alice", "user", "@probe hi")
	msgID := messageIDOverHTTP(t, base, threadID, seq)
	sessionID := createSessionOverHTTP(t, base, threadID, msgID, "probe")
	appendSessionEventOverHTTP(t, base, sessionID, "stdout", "the answer")

	out := runCLI(t, env, bin, "agent", "session", "--id", strconv.FormatInt(sessionID, 10), "--json")
	var payload struct {
		Session store.Session         `json:"session"`
		Events  []store.SessionEvent `json:"events"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	if payload.Session.ID != sessionID || len(payload.Events) != 1 {
		t.Fatalf("payload = %+v", payload)
	}
	if payload.Events[0].Content != "the answer" {
		t.Fatalf("event = %+v", payload.Events[0])
	}

	human := runCLI(t, env, bin, "agent", "session", "--id", strconv.FormatInt(sessionID, 10))
	for _, want := range []string{"probe", "the answer", "reply=stdout"} {
		if !strings.Contains(human, want) {
			t.Fatalf("human output missing %q:\n%s", want, human)
		}
	}
}

func TestE2E_AgentSessionCmdMissingIsExitOne(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := buildTestBinary(t, t.TempDir())
	env := []string{"FLUFFLE_HOME=" + home}
	runCLI(t, env, bin, "daemon", "start", "--background")
	t.Cleanup(func() { runCLI(t, env, bin, "daemon", "stop") })
	waitForDaemonReady(t, home, 10*time.Second)

	res := runCLIResult(t, env, bin, "", "agent", "session", "--id", "9999")
	if res.code != 1 {
		t.Fatalf("exit = %d, want 1; stderr = %s", res.code, res.stderr)
	}
	if !strings.Contains(res.stderr, "SESSION_NOT_FOUND") {
		t.Fatalf("stderr = %q, want the SESSION_NOT_FOUND envelope", res.stderr)
	}
}

func TestE2E_AgentSessionCmdRequiresID(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := buildTestBinary(t, t.TempDir())
	env := []string{"FLUFFLE_HOME=" + home}
	res := runCLIResult(t, env, bin, "", "agent", "session")
	if res.code != 1 {
		t.Fatalf("exit = %d, want 1; stderr = %s", res.code, res.stderr)
	}
	if !strings.Contains(res.stderr, "BAD_ARGS") {
		t.Fatalf("stderr = %q, want BAD_ARGS", res.stderr)
	}
}
```

Add these helpers to `cmd/flf/e2e_test.go`. `runCLI`, `runCLIResult`, `buildTestBinary`, `gitInit`, and `waitForDaemonReady` already exist there. `waitForDaemonReady` already returns the port, so use its return value rather than a separate `daemonPort`:

```go
func jsonID(t *testing.T, out string) string {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	v, ok := payload["id"]
	if !ok {
		t.Fatalf("no id in %q", out)
	}
	return fmt.Sprintf("%v", v)
}

func daemonClient(t *testing.T, base string) *http.Client {
	t.Helper()
	return &http.Client{Timeout: 10 * time.Second}
}

func postMessageOverHTTP(t *testing.T, base string, threadID int64, name, role, content string) int64 {
	t.Helper()
	body := fmt.Sprintf(`{"name":%q,"role":%q,"content":%q}`, name, role, content)
	resp, err := daemonClient(t, base).Post(fmt.Sprintf("%s/v1/threads/%d/messages", base, threadID), "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("append status = %d", resp.StatusCode)
	}
	var ack struct {
		Seq int64 `json:"seq"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ack); err != nil {
		t.Fatal(err)
	}
	return ack.Seq
}

func messageIDOverHTTP(t *testing.T, base string, threadID, seq int64) int64 {
	t.Helper()
	resp, err := daemonClient(t, base).Get(fmt.Sprintf("%s/v1/threads/%d/messages", base, threadID))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var messages []store.Message
	if err := json.NewDecoder(resp.Body).Decode(&messages); err != nil {
		t.Fatal(err)
	}
	for _, m := range messages {
		if m.Seq == seq {
			return m.ID
		}
	}
	t.Fatalf("no message with seq %d", seq)
	return 0
}

func createSessionOverHTTP(t *testing.T, base string, threadID, msgID int64, agentName string) int64 {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_ = db
	t.Skip("session rows are created by the daemon; drive this case through a mention instead")
	return 0
}
```

`createSessionOverHTTP` cannot work: there is no HTTP endpoint that creates a session, because creation is a side effect of append. **Replace the `TestE2E_AgentSessionCmd` setup** with one that drives a real mention instead, using the fake agent script from Task 11:

```go
	binDir := filepath.Dir(bin)
	probe := filepath.Join(binDir, "probe-agent")
	if err := os.WriteFile(probe, []byte("#!/bin/sh\ncat > /dev/null\nprintf 'the answer'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "[[agents]]\nname=\"probe\"\ncommand=\"" + probe + "\"\nreply=\"stdout\"\ntimeout_secs=20\n"
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	// ... after thread creation:
	runCLI(t, env, bin, "message", "send", "--thread", threadID, "--text", "@probe hi", "--as", "alice", "--json")
	if !waitFor(t, 40*time.Second, func() bool {
		return strings.Contains(runCLI(t, env, bin, "agent", "session", "--id", "1", "--json"), `"status": "succeeded"`)
	}) {
		t.Fatalf("session never succeeded; got %s", runCLI(t, env, bin, "agent", "session", "--id", "1", "--json"))
	}
```

With that replacement, delete `postMessageOverHTTP`, `messageIDOverHTTP`, `createSessionOverHTTP`, `appendSessionEventOverHTTP`, `daemonClient`, and `jsonID` — none of them are needed, and `jsonID` is unused because `channelID` and `threadID` are only used as command arguments. This is the correct shape: a CLI test should exercise the CLI, and the session comes into existence the same way it does in production.

Note that `runCLI`'s existing signature is `runCLI(t *testing.T, env []string, bin string, args ...string) string` and `runCLIResult` returns a struct with `.code` and `.stderr`. Confirm those field names against `cmd/flf/e2e_test.go:166-200` before relying on them; adjust the assertions if they differ.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test -tags e2e ./cmd/flf/ -run 'TestE2E_Agent' -v`
Expected: FAIL — `flf agent list` and `flf agent session` hit `agentCmd`'s `default` case and return `BAD_ARGS`.

- [ ] **Step 3: Write the subcommands**

Create `cmd/flf/agents.go`:

```go
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/NoRaincheck/fluffle/internal/client"
	"github.com/NoRaincheck/fluffle/internal/store"
)

type agentListPayload struct {
	Agents []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Command     string `json:"command"`
		Reply       string `json:"reply"`
		Source      string `json:"source"`
	} `json:"agents"`
}

type sessionPayload struct {
	Session store.Session         `json:"session"`
	Events  []store.SessionEvent `json:"events"`
}

func encodeJSON(value any) int {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(value); err != nil {
		return fail("DAEMON_ERROR", err.Error())
	}
	return 0
}

func agentListCmd(args []string) int {
	fs := newFlagSet("agent list")
	repo := fs.String("repo", "", "repo path used to resolve .flf.toml")
	asJSON := fs.Bool("json", false, "JSON output")
	if err := fs.Parse(args); err != nil {
		return fail("BAD_ARGS", err.Error())
	}

	repoPath := ""
	if *repo != "" {
		abs, err := filepath.Abs(*repo)
		if err != nil {
			return fail("BAD_ARGS", err.Error())
		}
		repoPath = abs
	}

	base, err := client.EnsureDaemon()
	if err != nil {
		return fail("DAEMON_DOWN", err.Error())
	}
	var payload agentListPayload
	if code := apiGet(base+"/v1/agents?repo="+repoPath, "", &payload, nil); code != 0 {
		return code
	}
	if *asJSON {
		return encodeJSON(payload)
	}
	for _, a := range payload.Agents {
		description := a.Description
		if description == "" {
			description = "-"
		}
		fmt.Printf("%s\t%s\t%s\t%s\t%s\n", a.Name, a.Command, a.Reply, a.Source, description)
	}
	return 0
}

func agentSessionCmd(args []string) int {
	fs := newFlagSet("agent session")
	id := fs.Int64("id", 0, "session id")
	asJSON := fs.Bool("json", false, "JSON output")
	if err := fs.Parse(args); err != nil {
		return fail("BAD_ARGS", err.Error())
	}
	if *id <= 0 {
		return fail("BAD_ARGS", "--id must be a positive integer")
	}

	base, err := client.EnsureDaemon()
	if err != nil {
		return fail("DAEMON_DOWN", err.Error())
	}
	var payload sessionPayload
	u := base + "/v1/sessions/" + strconv.FormatInt(*id, 10)
	if code := apiGet(u, "", &payload, func() error {
		if payload.Session.ID <= 0 {
			return fmt.Errorf("session response missing id")
		}
		return nil
	}); code != 0 {
		return code
	}
	if *asJSON {
		return encodeJSON(payload)
	}
	s := payload.Session
	status := s.Status
	if s.StartedAt != nil && s.FinishedAt != nil {
		status = fmt.Sprintf("%s (%s -> %s)", s.Status, *s.StartedAt, *s.FinishedAt)
	}
	fmt.Printf("session %d  agent=%s  thread=%d  trigger=%d  status=%s  reply=%s\n",
		s.ID, s.AgentName, s.ThreadID, s.TriggerMessageID, status, s.ReplyMode)
	fmt.Printf("command: %s\n", s.Command)
	if s.Cwd != nil {
		fmt.Printf("cwd: %s\n", *s.Cwd)
	} else {
		fmt.Printf("cwd: none\n")
	}
	if s.ReplyMessageID != nil {
		fmt.Printf("reply: message %d\n", *s.ReplyMessageID)
	}
	if s.Error != nil {
		fmt.Printf("error: %s\n", *s.Error)
	}
	for _, e := range payload.Events {
		fmt.Printf("%d\t%s\t%s\n", e.Seq, e.Type, e.Content)
	}
	return 0
}
```

`apiGet` with a non-nil `check` runs the check after a successful decode and turns a failure into `DAEMON_ERROR`, which is the existing convention for "the response could not be verified". `printAPIError` already maps a `404 SESSION_NOT_FOUND` body to exit 1, so `agentSessionCmd` needs no error-code branching of its own.

- [ ] **Step 4: Register the subcommands**

In `cmd/flf/main.go`, in `agentCmd`'s switch, add:

```go
	case "list":
		return agentListCmd(args[1:])
	case "session":
		return agentSessionCmd(args[1:])
```

and update the `agentCmd` usage string to mention `list` and `session`.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test -tags e2e ./cmd/flf/ -run 'TestE2E_Agent' -v`
Expected: PASS

- [ ] **Step 6: Verify format, vet, and build**

Run: `gofmt -l cmd/ && go vet ./... && go build ./...`
Expected: no output and a successful build.

- [ ] **Step 7: Commit**

```bash
git add cmd/flf/agents.go cmd/flf/main.go cmd/flf/e2e_test.go
git commit -m "feat(cli): flf agent list and flf agent session"
```

---

### Task 10: TUI session preview and bounded tick

**Files:**
- Create: `internal/tui/session.go`
- Test: `internal/tui/session_test.go`
- Modify: `internal/tui/model.go` — the `model` struct (line 73), `handleKey` (line 393), `View` (line 1088), `helpView` (line 2641)
- Modify: `internal/tui/api.go` — add `ListSessions` and `GetSession`

**Interfaces:**
- Consumes: `store.Session`, `store.SessionEvent`, and the Task 7 wire format.
- Produces:

```go
type previewMode int
const (
    previewThread previewMode = iota
    previewSession
)

func (m *model) fetchSessions(threadID int64) tea.Cmd
func (m *model) fetchSessionEvents(sessionID int64) tea.Cmd
func (m model) renderSessionPreview(w, h int) string
func (m *model) handleSessionKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool)
func (m *model) syncSessionTick() tea.Cmd
func (m *model) applySessions(sessions []store.Session) tea.Cmd
```

- [ ] **Step 1: Add the API methods**

Append to `internal/tui/api.go`. Follow `ListMessages` (`api.go:94`) exactly: `do` for the request, `readAPIError` for `>= 400`, `decodeStrictJSON` for the body, and a `DAEMON_ERROR`-prefixed error for an undecodable read. **Do not use `doJSON`** — despite the name it is mutation-only: it returns an `int64` id, requires an `{"id":N}` body, and classifies failures as `DELIVERY_UNKNOWN`, which is wrong for a read.

```go
func (c *apiClient) ListSessions(ctx context.Context, threadID int64) ([]store.Session, error) {
	url := c.base + "/v1/threads/" + fmt.Sprintf("%d", threadID) + "/sessions"
	resp, err := c.do(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("DAEMON_DOWN: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, readAPIError(resp)
	}
	var sessions []store.Session
	if err := decodeStrictJSON(resp.Body, &sessions); err != nil {
		return nil, fmt.Errorf("DAEMON_ERROR: %w", err)
	}
	if sessions == nil {
		sessions = []store.Session{}
	}
	return sessions, nil
}

func (c *apiClient) GetSession(ctx context.Context, sessionID int64) (store.Session, []store.SessionEvent, error) {
	url := c.base + "/v1/sessions/" + fmt.Sprintf("%d", sessionID)
	resp, err := c.do(ctx, http.MethodGet, url, nil)
	if err != nil {
		return store.Session{}, nil, fmt.Errorf("DAEMON_DOWN: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return store.Session{}, nil, readAPIError(resp)
	}
	var payload struct {
		Session store.Session         `json:"session"`
		Events  []store.SessionEvent `json:"events"`
	}
	if err := decodeStrictJSON(resp.Body, &payload); err != nil {
		return store.Session{}, nil, fmt.Errorf("DAEMON_ERROR: %w", err)
	}
	if payload.Session.ID <= 0 {
		return store.Session{}, nil, fmt.Errorf("DAEMON_ERROR: session response missing id")
	}
	if payload.Events == nil {
		payload.Events = []store.SessionEvent{}
	}
	return payload.Session, payload.Events, nil
}
```

- [ ] **Step 2: Write the failing tests**

Create `internal/tui/session_test.go`:

```go
package tui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "charmbracelet/bubbletea"

	"github.com/NoRaincheck/fluffle/internal/store"
)

func sessionServer(t *testing.T, sessions []store.Session, events []store.SessionEvent) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/threads/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sessions)
	})
	mux.HandleFunc("/v1/sessions/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"session": store.Session{ID: 1, AgentName: "reviewer", Status: store.SessionSucceeded, ReplyMode: "auto"},
			"events":  events,
		})
	})
	mux.HandleFunc("/v1/inbox", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]store.InboxMessage{})
	})
	mux.HandleFunc("/v1/channels", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]store.Channel{})
	})
	return httptest.NewServer(mux)
}

func sessionModel(t *testing.T, sessions []store.Session, events []store.SessionEvent) model {
	t.Helper()
	srv := sessionServer(t, sessions, events)
	t.Cleanup(srv.Close)
	m := New(srv.URL)
	m.view = viewInbox
	m.width = 120
	m.height = 40
	m.preview = true
	m.inboxLayout = inboxLayoutCompact
	return *m
}

func TestSessionKeyRequiresASessionOnTheCursorRow(t *testing.T) {
	m := sessionModel(t, nil, nil)
	next, _, handled := m.handleSessionKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	got := toModel(t, next)
	if handled {
		t.Fatal("s must not be handled when the cursor row has no session")
	}
	if got.previewMode != previewThread {
		t.Fatalf("previewMode = %v", got.previewMode)
	}
}

func TestSessionKeyFlipsToSessionMode(t *testing.T) {
	one := store.Session{ID: 5, TriggerMessageID: 42, AgentName: "reviewer", Status: store.SessionSucceeded}
	m := sessionModel(t, []store.Session{one}, nil)
	m.inbox = []store.InboxMessage{{Message: store.Message{ID: 42}}}
	m.sessionsByMsg = map[int64]store.Session{42: one}
	next, _, handled := m.handleSessionKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	got := toModel(t, next)
	if !handled {
		t.Fatal("s should be handled when the cursor row has a session")
	}
	if got.previewMode != previewSession {
		t.Fatalf("previewMode = %v, want previewSession", got.previewMode)
	}
}

func TestSessionKeyFlipsBackToThread(t *testing.T) {
	one := store.Session{ID: 5, TriggerMessageID: 42}
	m := sessionModel(t, []store.Session{one}, nil)
	m.inbox = []store.InboxMessage{{Message: store.Message{ID: 42}}}
	m.sessionsByMsg = map[int64]store.Session{42: one}
	m.previewMode = previewSession
	next, _, handled := m.handleSessionKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	got := toModel(t, next)
	if !handled || got.previewMode != previewThread {
		t.Fatalf("handled = %v previewMode = %v", handled, got.previewMode)
	}
}

func TestRenderSessionPreviewShowsHeaderAndEvents(t *testing.T) {
	one := store.Session{ID: 5, AgentName: "reviewer", Status: store.SessionSucceeded, ReplyMode: "auto", StartedAt: strptr("t0"), FinishedAt: strptr("t1"), ReplyMessageID: int64ptr(9)}
	events := []store.SessionEvent{
		{Seq: 1, Type: store.SessionEventPrompt, Content: "the prompt"},
		{Seq: 2, Type: store.SessionEventStdout, Content: "the answer"},
		{Seq: 3, Type: store.SessionEventExit, Content: "0"},
	}
	m := sessionModel(t, []store.Session{one}, events)
	m.session = &one
	m.sessionEvents = events
	out := stripAnsi(m.renderSessionPreview(60, 20))
	for _, want := range []string{"SESSION", "reviewer", "succeeded", "stdout", "prompt", "the answer", "exit"} {
		if !strings.Contains(out, want) {
			t.Fatalf("preview missing %q\n---\n%s", want, out)
		}
	}
}

func TestRenderSessionPreviewShowsRunningStatus(t *testing.T) {
	one := store.Session{ID: 5, AgentName: "reviewer", Status: store.SessionRunning, ReplyMode: "auto"}
	m := sessionModel(t, []store.Session{one}, nil)
	m.session = &one
	out := stripAnsi(m.renderSessionPreview(60, 20))
	if !strings.Contains(out, "running") && !strings.Contains(out, "RUNNING") {
		t.Fatalf("running status not shown:\n%s", out)
	}
}

func TestRenderSessionPreviewShowsReplyLinkage(t *testing.T) {
	withReply := store.Session{ID: 5, AgentName: "reviewer", Status: store.SessionSucceeded, ReplyMode: "auto", ReplyMessageID: int64ptr(9)}
	m := sessionModel(t, []store.Session{withReply}, nil)
	m.session = &withReply
	if !strings.Contains(stripAnsi(m.renderSessionPreview(80, 20)), "9") {
		t.Fatal("reply message id not rendered")
	}
}

func TestRenderSessionPreviewHandlesNoSession(t *testing.T) {
	m := sessionModel(t, nil, nil)
	out := stripAnsi(m.renderSessionPreview(60, 20))
	if !strings.Contains(out, "no session") {
		t.Fatalf("empty state missing:\n%s", out)
	}
}

func TestTickOnlySchedulesWhileASessionIsNonTerminal(t *testing.T) {
	m := sessionModel(t, nil, nil)
	m.sessions = []store.Session{{ID: 1, Status: store.SessionSucceeded}}
	if cmd := m.syncSessionTick(); cmd != nil {
		t.Fatal("all sessions terminal must not schedule a tick")
	}
	m.sessions = []store.Session{{ID: 1, Status: store.SessionSucceeded}, {ID: 2, Status: store.SessionRunning}}
	if cmd := m.syncSessionTick(); cmd == nil {
		t.Fatal("a running session must schedule a tick")
	}
}

func TestTickStopsAfterReachingTerminal(t *testing.T) {
	m := sessionModel(t, nil, nil)
	m.sessions = []store.Session{{ID: 1, Status: store.SessionRunning}}
	if cmd := m.syncSessionTick(); cmd == nil {
		t.Fatal("expected a tick while running")
	}
	msg := sessionsFetchedMsg{sessions: []store.Session{{ID: 1, Status: store.SessionSucceeded}}}
	next, cmd := m.applySessions(msg.sessions)
	got := toModel(t, next)
	if got.sessions[0].Status != store.SessionSucceeded {
		t.Fatalf("status = %q", got.sessions[0].Status)
	}
	if cmd != nil {
		t.Fatal("terminal sessions must not reschedule the tick")
	}
}

func TestApplySessionsRebuildsTheMessageIndex(t *testing.T) {
	m := sessionModel(t, nil, nil)
	next, _ := m.applySessions([]store.Session{
		{ID: 1, TriggerMessageID: 10},
		{ID: 2, TriggerMessageID: 20},
	})
	got := toModel(t, next)
	if len(got.sessionsByMsg) != 2 {
		t.Fatalf("sessionsByMsg = %+v", got.sessionsByMsg)
	}
	if got.sessionsByMsg[20].ID != 2 {
		t.Fatalf("index = %+v", got.sessionsByMsg)
	}
}

func TestHelpMentionsTheSessionKey(t *testing.T) {
	m := sessionModel(t, nil, nil)
	if !strings.Contains(stripAnsi(m.helpView()), "s") {
		t.Fatal("helpView must document the s key")
	}
}
```

`strptr` and `int64ptr` helpers may already exist in the `tui` package under other names. If they do, reuse those. Otherwise add them to `session_test.go`:

```go
func strptr(s string) *string { return &s }
func int64ptr(v int64) *int64 { return &v }
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/tui/ -run 'Session|Tick|Help'`
Expected: FAIL — `undefined: previewMode`, `undefined: handleSessionKey`, `undefined: renderSessionPreview`, `undefined: syncSessionTick`, `undefined: applySessions`

- [ ] **Step 4: Write the TUI session code**

Create `internal/tui/session.go`:

```go
package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charmbracelet/bubbletea"

	"github.com/NoRaincheck/fluffle/internal/store"
)

const sessionTickInterval = 500 * time.Millisecond

type previewMode int

const (
	previewThread previewMode = iota
	previewSession
)

type sessionsFetchedMsg struct {
	sessions []store.Session
	err      error
}

type sessionEventsFetchedMsg struct {
	session store.Session
	events  []store.SessionEvent
	err     error
}

type sessionTickMsg time.Time

func sessionTickCmd(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg { return sessionTickMsg(t) })
}

func (m *model) applySessions(sessions []store.Session) tea.Cmd {
	m.sessions = sessions
	m.sessionsByMsg = make(map[int64]store.Session, len(sessions))
	for _, s := range sessions {
		m.sessionsByMsg[s.TriggerMessageID] = s
	}
	return m.syncSessionTick()
}

func (m *model) anySessionActive() bool {
	for _, s := range m.sessions {
		if s.Status == store.SessionQueued || s.Status == store.SessionRunning {
			return true
		}
	}
	return false
}

func (m *model) syncSessionTick() tea.Cmd {
	if !m.anySessionActive() {
		return nil
	}
	return sessionTickCmd(sessionTickInterval)
}

func (m *model) sessionForCursor() (store.Session, bool) {
	rows := m.inboxFilteredSorted()
	if m.cursor < 0 || m.cursor >= len(rows) {
		return store.Session{}, false
	}
	s, ok := m.sessionsByMsg[rows[m.cursor].ID]
	return s, ok
}

func (m *model) handleSessionKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if msg.Type != tea.KeyRunes || string(msg.Runes) != "s" {
		return m, nil, false
	}
	if !m.previewVisible() {
		return m, nil, false
	}
	if m.previewMode == previewSession {
		m.previewMode = previewThread
		m.session = nil
		m.sessionEvents = nil
		m.sessionScroll = 0
		return m, nil, true
	}
	sess, ok := m.sessionForCursor()
	if !ok {
		m.status = "no session on this message"
		return m, nil, false
	}
	m.previewMode = previewSession
	m.session = &sess
	m.sessionEvents = nil
	m.sessionScroll = 0
	return m, m.fetchSessionEvents(sess.ID), true
}

func (m model) renderSessionPreview(w, h int) string {
	if h < 3 {
		return ""
	}
	if m.session == nil {
		return "no session selected"
	}
	s := *m.session
	var b strings.Builder
	head := fmt.Sprintf("SESSION  %s · %s · reply=%s · #%d", s.AgentName, s.Status, s.ReplyMode, s.ID)
	if s.Status != store.SessionQueued && s.Status != store.SessionRunning && s.StartedAt != nil && s.FinishedAt != nil {
		head += fmt.Sprintf(" · %s", sessionDuration(*s.StartedAt, *s.FinishedAt))
	}
	if s.ReplyMessageID != nil {
		head += fmt.Sprintf(" · replied #%d", *s.ReplyMessageID)
	}
	if s.Error != nil {
		head += fmt.Sprintf(" · %s", truncate(*s.Error, 40))
	}
	b.WriteString(truncate(head, w))
	b.WriteByte('\n')

	if len(m.sessionEvents) == 0 {
		if s.Status == store.SessionQueued || s.Status == store.SessionRunning {
			b.WriteString("  (running…)")
		} else {
			b.WriteString("  (no events)")
		}
		return b.String()
	}

	labelWidth := 8
	rows := len(m.sessionEvents)
	visible := h - 2
	if visible < 1 {
		visible = 1
	}
	hidden := 0
	start := 0
	end := rows
	if rows > visible {
		hidden = rows - visible
		keepHead := visible / 2
		keepTail := visible - keepHead
		start = m.sessionScroll
		if start > hidden {
			start = hidden
		}
		end = start + keepHead
		if end > rows-keepTail {
			end = rows - keepTail
		}
		if end < start {
			end = start
		}
	}
	if start > 0 {
		b.WriteString(fmt.Sprintf("  … (%d hidden) …\n", start))
	}
	for i := start; i < end; i++ {
		e := m.sessionEvents[i]
		b.WriteString(fmt.Sprintf("  %-*s %s\n", labelWidth, e.Type, truncate(firstLine(e.Content), max(1, w-2-labelWidth-1))))
	}
	if end < rows {
		b.WriteString(fmt.Sprintf("  … (%d hidden) …", rows-end))
	}
	return b.String()
}

func sessionDuration(startedAt, finishedAt string) string {
	start, err := time.Parse(time.RFC3339, startedAt)
	if err != nil {
		return ""
	}
	end, err := time.Parse(time.RFC3339, finishedAt)
	if err != nil {
		return ""
	}
	d := end.Sub(start)
	if d < 0 {
		d = 0
	}
	return d.Round(time.Second).String()
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}
```

- [ ] **Step 5: Add the model fields and wire the key**

In `internal/tui/model.go`, add to the `model` struct:

```go
	previewMode    previewMode
	sessions       []store.Session
	sessionsByMsg  map[int64]store.Session
	session        *store.Session
	sessionEvents  []store.SessionEvent
	sessionScroll  int
```

In `handleKey`'s rune switch, add a case that delegates:

```go
	case "s":
		return m.handleSessionKey(msg)
```

In `Update`, add the three message handlers:

```go
	case sessionsFetchedMsg:
		if msg.err != nil {
			m.status = fmt.Sprintf("error: %v", msg.err)
			return m, nil
		}
		next, cmd := m.applySessions(msg.sessions)
		return next, cmd
	case sessionEventsFetchedMsg:
		if msg.err != nil {
			m.status = fmt.Sprintf("error: %v", msg.err)
			return m, nil
		}
		m.session = &msg.session
		m.sessionEvents = msg.events
		m.previewMode = previewSession
		return m, nil
	case sessionTickMsg:
		if !m.anySessionActive() {
			return m, nil
		}
		return m, tea.Batch(m.fetchSessions(m.previewThreadID), sessionTickCmd(sessionTickInterval))
```

In `renderPreview`, at the top of the `viewInbox` branch, return the session pane when in session mode:

```go
	if m.view == viewInbox && m.previewMode == previewSession {
		return m.renderSessionPreview(w, h)
	}
```

In `helpView`, add `"s session"` to the inbox hint list.

Add the two fetch methods to `session.go`. They close over `m` so they can reach `m.api`, matching how `fetchPreviewMessages` and `fetchPreviewThreads` are written in `model.go`:

```go
func (m *model) fetchSessions(threadID int64) tea.Cmd {
	return func() tea.Msg {
		sessions, err := m.api.ListSessions(nil, threadID)
		return sessionsFetchedMsg{sessions: sessions, err: err}
	}
}

func (m *model) fetchSessionEvents(sessionID int64) tea.Cmd {
	return func() tea.Msg {
		sess, events, err := m.api.GetSession(nil, sessionID)
		return sessionEventsFetchedMsg{session: sess, events: events, err: err}
	}
}
```

The Step 4 code deliberately omits these two methods; add them here. `sessionTickCmd` stays a free function because it does not need model state.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/tui/ -v -run 'Session|Tick|Help'`
Expected: PASS

- [ ] **Step 7: Verify format, vet, and the whole suite**

Run: `gofmt -l internal/ && go vet ./... && go test ./...`
Expected: no output and PASS across every package, including the pre-existing TUI layout tests.

- [ ] **Step 8: Commit**

```bash
git add internal/tui/
git commit -m "feat(tui): session preview pane with bounded polling"
```

---

### Task 11: End-to-end test of the full loop

**Files:**
- Test: `cmd/flf/e2e_test.go`

**Interfaces:**
- Consumes: everything. This is the test that proves the layers work together rather than in isolation.
- Produces: no code.

- [ ] **Step 1: Write the failing test**

Append to `cmd/flf/e2e_test.go`:

```go
func TestE2E_AgentMentionRunsAgentAndStoresSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("FLUFFLE_HOME", home)
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := buildTestBinary(t, t.TempDir())
	binDir := filepath.Dir(bin)
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}

	probe := filepath.Join(binDir, "probe-agent")
	script := "#!/bin/sh\ncat > /dev/null\nprintf 'the answer'\n"
	if err := os.WriteFile(probe, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	repo := t.TempDir()
	gitInit(t, repo)
	cfg := "[[agents]]\nname=\"probe\"\ncommand=\"" + probe + "\"\nreply=\"stdout\"\ntimeout_secs=20\n"
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	runFlf(t, home, bin, "daemon", "start", "--background")
	t.Cleanup(func() { runFlf(t, home, bin, "daemon", "stop") })

	channelID := mustJSONField(t, runFlf(t, home, bin, "channel", "create", "--name", "eng", "--repo", repo, "--json"), "id")
	threadID := mustJSONField(t, runFlf(t, home, bin, "thread", "new", "--channel", "eng", "--repo", repo, "--title", "review", "--json"), "id")

	runFlf(t, home, bin, "message", "send", "--thread", threadID, "--text", "@probe what is the status?", "--as", "alice", "--json")

	replySeq := int64(0)
	ok := waitFor(t, 40*time.Second, func() bool {
		out := runFlf(t, home, bin, "agent", "read", "--thread", threadID, "--json")
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
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
				if entry.Content != "the answer" {
					t.Fatalf("agent reply content = %q", entry.Content)
				}
				replySeq = entry.Seq
				return true
			}
		}
		return false
	})
	if !ok {
		t.Fatal("agent never replied within 40s")
	}
	if replySeq <= 1 {
		t.Fatalf("reply seq = %d, want greater than the trigger seq", replySeq)
	}

	sessionOut := runFlf(t, home, bin, "agent", "session", "--id", "1", "--json")
	var payload struct {
		Session store.Session         `json:"session"`
		Events  []store.SessionEvent `json:"events"`
	}
	if err := json.Unmarshal([]byte(sessionOut), &payload); err != nil {
		t.Fatalf("unmarshal %q: %v", sessionOut, err)
	}
	if payload.Session.Status != store.SessionSucceeded {
		t.Fatalf("session status = %q error %v", payload.Session.Status, payload.Session.Error)
	}
	if payload.Session.AgentName != "probe" || payload.Session.ReplyMode != "stdout" {
		t.Fatalf("session = %+v", payload.Session)
	}
	if payload.Session.ReplyMessageID == nil {
		t.Fatal("session has no reply message id")
	}
	if !strings.Contains(payload.Session.Command, probe) {
		t.Fatalf("session command = %q", payload.Session.Command)
	}
	if payload.Session.Cwd == nil || *payload.Session.Cwd != repo {
		t.Fatalf("session cwd = %v, want %q", payload.Session.Cwd, repo)
	}
	types := map[string]bool{}
	for _, e := range payload.Events {
		types[e.Type] = true
	}
	for _, want := range []string{"prompt", "stdout", "exit"} {
		if !types[want] {
			t.Fatalf("missing %q event; got %v", want, types)
		}
	}

	listOut := runFlf(t, home, bin, "agent", "session", "--id", "1")
	if !strings.Contains(listOut, "probe") || !strings.Contains(listOut, "the answer") {
		t.Fatalf("human session output = %q", listOut)
	}
}

func TestE2E_AgentMentionRequiresTheAgentBinaryToExist(t *testing.T) {
	home := t.TempDir()
	t.Setenv("FLUFFLE_HOME", home)
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := buildTestBinary(t, t.TempDir())
	repo := t.TempDir()
	gitInit(t, repo)
	cfg := "[[agents]]\nname=\"ghost\"\ncommand=\"/definitely/not/here\"\nreply=\"stdout\"\ntimeout_secs=5\n"
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	runFlf(t, home, bin, "daemon", "start", "--background")
	t.Cleanup(func() { runFlf(t, home, bin, "daemon", "stop") })

	channelID := mustJSONField(t, runFlf(t, home, bin, "channel", "create", "--name", "eng", "--repo", repo, "--json"), "id")
	threadID := mustJSONField(t, runFlf(t, home, bin, "thread", "new", "--channel", "eng", "--repo", repo, "--title", "t", "--json"), "id")
	runFlf(t, home, bin, "message", "send", "--thread", threadID, "--text", "@ghost hello", "--as", "alice", "--json")

	ok := waitFor(t, 30*time.Second, func() bool {
		var payload struct {
			Session store.Session `json:"session"`
		}
		out := runFlf(t, home, bin, "agent", "session", "--id", "1", "--json")
		if err := json.Unmarshal([]byte(out), &payload); err != nil {
			return false
		}
		return payload.Session.Status == store.SessionFailed && payload.Session.Error != nil
	})
	if !ok {
		t.Fatal("a missing agent binary must fail the session visibly, not silently")
	}
}
```

The helpers `runFlf`, `mustJSONField`, and `waitFor` may already exist in `e2e_test.go` under those or similar names. `waitFor` does exist at line 25. Reuse what exists; add only what is missing:

```go
func runFlf(t *testing.T, home, bin string, args ...string) string {
	t.Helper()
	full := append([]string{}, args...)
	full = append(full, "--json")
	cmd := exec.Command(bin, full...)
	cmd.Env = append(os.Environ(), "FLUFFLE_HOME="+home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("flf %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func mustJSONField(t *testing.T, out, field string) string {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	v, ok := payload[field]
	if !ok {
		t.Fatalf("field %q missing from %q", field, out)
	}
	return fmt.Sprintf("%v", v)
}
```

Note that `--json` is appended unconditionally, which will break commands that do not accept it. If `runFlf` in this file already handles per-command flags, use that instead and pass `--json` explicitly at each call site.

- [ ] **Step 2: Run the e2e tests to verify they fail, then pass**

Run: `go test -tags e2e ./cmd/flf/ -run 'TestE2E_AgentMention' -v`
Expected before the feature: FAIL with no session. Expected now: PASS. This test is expected to pass immediately on a correct implementation; its purpose is to catch integration breakage in later tasks, not to drive development of a missing feature.

If it hangs, the likely cause is the agent subprocess never exiting. The probe script reads stdin to EOF, which requires `closeAll` to have run and stdin to be closed. Check that `runner.Request.Stdin` is set and that `cmd.Stdin` is a reader the runner closes.

- [ ] **Step 3: Run the whole suite including e2e**

Run: `go test ./... && go test -tags e2e ./cmd/flf/`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add cmd/flf/e2e_test.go
git commit -m "test(e2e): agent mention runs an agent, replies, and stores a session"
```

---

### Task 12: Documentation

No code. Folded into its own task because the doc changes contradict existing statements, and a reviewer should check the contradictions are actually resolved rather than assumed.

**Files:**
- Modify: `docs/backend.md`
- Modify: `VISION.md`
- Modify: `README.md`
- Modify: `docs/tui-keybindings.md`
- Modify: `AGENTS.md`

**Interfaces:**
- Consumes: the finished feature.
- Produces: no code.

- [ ] **Step 1: Update `docs/backend.md`**

Add the two tables to the **Data model** section, after the `reactions` table. Add a **Agent sessions** subsection covering the config format, the repo-over-global resolution order, leading-mention detection, the reply modes, and the fact that a session transcript is never fed back as agent context.

Add to the **API reference**:

```
**`GET /v1/threads/:id/sessions`** — Sessions in a thread, oldest first, metadata only.
An empty thread returns `[]`.

**`GET /v1/sessions/:id`** — One session plus its events in `seq` order.
Returns `{"session":{...},"events":[...]}`. A missing session is `404 SESSION_NOT_FOUND`.

**`GET /v1/agents?repo=<abs path>`** — Resolved agent names for a repo scope, each with
`description`, `command`, `reply`, and the config `source` file. Returns `{"agents":[...]}`.

**`POST /v1/sessions/:id/cancel`** — Cancel a non-terminal session. Humans only: a request
carrying `X-Fluffle-Agent` is `403 AGENT_FORBIDDEN`. A terminal session is `409 SESSION_FINISHED`.
Returns `{"ok":true}`.
```

Add `SESSION_NOT_FOUND` and `SESSION_FINISHED` to the **Error codes** table with exit 1.

Add to the **CLI reference**:

```
flf agent list [--repo PATH] [--json]
flf agent session --id N [--json]
```

Update the **Agent contract / Permissions** table: agents additionally cannot start sessions and cannot cancel sessions.

**Remove these two entries from the "What was rejected" list**, because the feature implements them:

- `daemon-owned agent execution` (from the bullet beginning "YAML workflows, schedules, webhooks, inotify reloads, plugins, daemon-owned agent execution")
- `YAML workflows`

Leave the rest of that bullet intact, and leave `Agent memory, embeddings, summaries, or opaque per-agent state` in place: session events are a transcript, not memory.

Add a **Key design choices** bullet:

- **A session is a transcript, not memory.** `agent_session_events` records what an agent was asked and what it printed. It is never included in any prompt. The thread's JSONL remains the only agent context, which is what makes VISION tenet 5 hold while a run is still auditable.

- [ ] **Step 2: Update `VISION.md`**

Extend **tenet 5** with one sentence:

> A local agent run is recorded as an auditable transcript that a human can inspect, but that record is never fed back to an agent as context. The thread history remains the context; the transcript is a window onto a process, not a memory the agent draws on.

Extend **tenet 3** with: agents cannot start or cancel agent sessions.

- [ ] **Step 3: Update `README.md`**

Add to the command examples:

```bash
# agent sessions — configure in .flf.toml or ~/.fluffle/config.toml
printf '[[agents]]\nname="reviewer"\ncommand="claude"\nargs=["-p","{prompt}"]\n' > ~/.fluffle/config.toml
flf agent list --json
./flf message send --thread 1 --text "@reviewer what changed in the auth refactor?" --as alice
flf agent session --id 1 --json      # the full run: prompt, stdout, exit
```

And add one line to the glossary: **Session = one local agent run started by an `@mention`, with its prompt, output, and result.**

- [ ] **Step 4: Update `docs/tui-keybindings.md`**

Add `s` to the inbox key table: `s` — switch the preview pane between the thread and the agent session for the selected message.

Do **not** "fix" the documented-but-unbound `c` and `n` keys in this task. `internal/tui/simple_test.go:86-95` asserts they do nothing, so documenting them as working would be false. Note the drift in a comment-free way by leaving the existing rows and adding the new one; a separate task can reconcile all three.

- [ ] **Step 5: Update `AGENTS.md`**

Add to **Conventions**:

- **Agents cannot start or cancel agent sessions.** Only a human message append carrying a leading `@mention` creates a session.
- **Session transcripts are never agent context.** `agent_session_events` is an audit record for humans.

- [ ] **Step 6: Verify nothing claims removed behavior still exists**

Run: `rg -n "daemon-owned agent|YAML workflows" docs/ VISION.md AGENTS.md`
Expected: the only hits are inside the revised bullet in `backend.md` if any remain. If `daemon-owned agent execution` still appears in a rejection list, this task is not done.

Run: `rg -n "agent list|agent session" docs/backend.md`
Expected: hits in the CLI reference.

- [ ] **Step 7: Verify the build and full suite one more time**

Run: `gofmt -l . && go vet ./... && go test ./...`
Expected: no output and PASS.

- [ ] **Step 8: Commit**

```bash
git add docs/ VISION.md README.md AGENTS.md
git commit -m "docs: agent sessions, @mention trigger, and reply modes"
```

---

## Self-Review

Run against the spec before executing. Results recorded here so a reviewer can check the reasoning rather than redo it.

### 1. Spec coverage

| Spec section | Task |
|---|---|
| Config format, defaults, unknown keys, `{prompt}` rule | 2 |
| Resolution order, mtime cache, orphan uses global | 2 |
| Unresolvable names are inert | 1, 5, 7 |
| Two tables, `UNIQUE(trigger_message_id, agent_name)` | 4 |
| Migration without a version bump | 4, Step 1 |
| Mention grammar and the table of cases | 1 |
| Trigger point, commit-then-scan ordering | 7 |
| Agents cannot trigger agents | 7 |
| `Runner` interface, `Setpgid`, `killpg`, bounded output, finite timeout | 3 |
| Scheduler, cap of 4, `queued` overflow | 5 |
| Prompt construction, system prompt, replying block | 5 |
| Reply resolution table and the exit race | 6 |
| Shutdown, queued-session cancellation, restart reconciliation | 5, 8 |
| Four routes and the two error codes | 7, 9 |
| `Deps` seam preserving `NewHandler` | 7 |
| `flf agent list`, `flf agent session`, no `flf agent invoke` | 9 |
| TUI state, `s` key, `renderSessionPreview`, bounded tick, fallback | 10 |
| End-to-end loop | 11 |
| Doc updates including the rejection-list removal | 12 |
| Non-goals | honored: no ACP, MCP, reuse, autocomplete, TUI cancel, configurable concurrency, DB-stored profiles |

No gaps found.

### 2. Placeholder scan

Searched for `TBD`, `TODO`, `implement later`, `fill in`, `similar to Task`, `appropriate error handling`, `write tests for the above`. The only near-matches are deliberate: Task 9 and Task 11 tell the implementer to reuse existing test helpers if the file already has equivalents, and Task 10 tells them to delete a scaffolding function that does not exist in the final shape. Both name the concrete thing to check. No step defers work to an unspecified future task.

### 3. Type consistency

Cross-checked every identifier a task consumes against the task that produces it:

- `mentions.Parse` returns `([]string, string)`; Task 7 calls it as `names, _ := mentions.Parse(content)`. Consistent.
- `agentcfg.Entry` embeds `Agent`, so `entry.Reply`, `entry.Command`, `entry.Args`, `entry.Env`, `entry.TimeoutSecs`, `entry.SystemPrompt`, `entry.Name` all resolve through promotion. Task 2's `TestParseFullEntry` constructs `Entry{Agent: Agent{...}}`, which is the same shape Task 5 and 6 read. Consistent.
- `runner.Request` fields used in Task 5 (`Command`, `Env`, `Timeout`, `OnChunk`, `Args`, `Stdin`, `Cwd`) all exist in Task 3's struct. Consistent.
- `store.Session` pointer fields (`Cwd`, `Error`, `StartedAt`, `FinishedAt`, `ReplyMessageID`, `ExitCode`) are `*string` / `*int64` in Task 4 and dereferenced as such in Tasks 5, 6, 8, 10. Consistent.
- `session.ErrTerminal` is produced in Task 5 and matched with `errors.Is` in Task 7's `serveCancel`. Consistent.
- `apiserver.Deps.Canceler` is an anonymous interface `interface{ Cancel(id int64) error }`; `*session.Manager` satisfies it because Task 5 declares exactly `func (m *Manager) Cancel(id int64) error`. Task 8 assigns `Canceler: sessions`. Consistent.
- `store.SessionEventType` constants are used verbatim in Tasks 5, 6, 10 tests. Consistent.
- `previewMode` values `previewThread` / `previewSession` are declared in Task 10 and used only there. Consistent.

One naming fix applied during review: the TUI state field was drafted as `sessionsByMessage` in the spec and is `sessionsByMsg` in the plan. The plan is authoritative; the spec's prose uses the longer form only in a comment-like aside, so no code depends on it.

---

## Execution Order

```
1  mentions        pure, no deps
2  agentcfg        pure, +1 dependency
3  runner          pure, subprocesses
4  store           schema + CRUD
5  session         manager, prompt, stream   (reply stubbed)
6  session         reply resolution
7  apiserver       routes + trigger
8  daemon          wiring, reconcile, shutdown
9  cli             agent list, agent session
10 tui             preview pane + tick
11 e2e             full loop
12 docs            contract updates
```

Tasks 1 through 4 are independent of each other and can be dispatched in parallel. Task 5 depends on 2, 3, and 4. Task 6 depends on 5. Tasks 7 through 12 are strictly sequential after 6.
