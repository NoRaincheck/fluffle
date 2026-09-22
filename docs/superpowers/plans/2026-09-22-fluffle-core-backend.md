# Fluffle Core Backend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the Fluffle core backend slice: SQLite store, localhost HTTP daemon with auto-spawn, CLI subset, agent guards, and JSONL export/import round-trip.

**Architecture:** Single Go module. Daemon owns `~/.fluffle/fluffle.db` exclusively. CLI (human + agent) is an HTTP client to `127.0.0.1:<ephemeral>` discovered via `~/.fluffle/daemon.json`, auto-spawning on probe failure with 3 retries. Unified `messages` table; agent append-only enforced in HTTP handlers.

**Tech Stack:** Go 1.26, `modernc.org/sqlite` (pure-Go, no cgo), stdlib `net/http`, `flag`, `encoding/json`, `os/exec`. No cobra, no ORM, no OpenAPI codegen.

**Spec:** `docs/superpowers/specs/2026-09-22-fluffle-core-backend-design.md`

## Global Constraints

- Single SQLite file at `$FLUFFLE_HOME/fluffle.db` or `~/.fluffle/fluffle.db`; only the daemon opens it.
- Daemon binds `127.0.0.1:0` (ephemeral) and persists `{port, pid, started_at}` to `$FLUFFLE_HOME/daemon.json` or `~/.fluffle/daemon.json`.
- CLI exit codes: `0` ok, `1` user error, `2` daemon error — exact values, no other codes.
- Daemon error envelope is exactly `{"code":"...","message":"..."}` with codes `DAEMON_DOWN`, `NOT_A_GIT_REPO`, `AGENT_FORBIDDEN`, `THREAD_NOT_FOUND`, `CHANNEL_NOT_FOUND`, `BAD_JSONL`.
- Integer IDs (`--thread 42`); no ULIDs in this slice.
- `author_type` is `human` unless `--agent-id` flag or `X-Fluffle-Agent` header is present, then `agent`.
- `flf tui` prints `backend-only milestone: tui deferred` and exits `1`.
- Non-goals: TUI, webapp/WebSocket, FTS, embeddings, federation, PostgreSQL sync, identity tokens.
- YAGNI: stdlib CLI dispatch (no cobra); hand-rolled routes (no router dep).

## File Map

- `go.mod` — module `github.com/NoRaincheck/fluffle`, go `1.26`, require `modernc.org/sqlite`.
- `internal/store/store.go` — `Store` type; schema exactly per spec section 3; CRUD + `seq` assignment in tx.
- `internal/store/store_test.go` — `:memory:` tests.
- `internal/jsonl/jsonl.go` — `Line` struct + marshal/parse helpers.
- `internal/jsonl/jsonl_test.go` — round-trip test.
- `internal/repo/repo.go` — `Canonicalize`, `InspectGitDir`.
- `internal/repo/repo_test.go` — tempdir `.git` tests.
- `internal/apiserver/server.go` — `NewHandler(store *Store) http.Handler`, agent-guard middleware, all `/v1/…` routes.
- `internal/apiserver/server_test.go` — `httptest` permission matrix.
- `internal/client/client.go` — `DaemonBaseURL`, `EnsureDaemon`, HTTP helpers.
- `cmd/flf/main.go` — CLI dispatch for all MVP commands.

---

### Task 1: Go module + store schema + channels (anchored + arbitrary orphans)

**Files:**
- Create: `go.mod`
- Create: `internal/store/store.go`
- Create: `internal/store/store_test.go`

**Interfaces:**
- Consumes: nothing (greenfield).
- Produces: `func Open(path string) (*Store, error)`; `type Channel struct { ID int64; Name, RepoAbsPath, RepoRemote, RepoHeadSHA, CreatedAt string; IsOrphaned bool; ArchivedAt *string }`; `func (s *Store) CreateChannel(name, repoAbsPath, repoRemote, repoHeadSHA string, orphaned bool) (int64, error)`; `func (s *Store) ListChannels(repoAbsPath string, includeOrphaned bool) ([]Channel, error)`; `var ErrConflict, ErrNotFound error`.

- [ ] **Step 1: Write the failing store test**

```go
package store

import "testing"

func TestCreateAndListAnchoredChannel(t *testing.T) {
    s, err := Open(":memory:")
    if err != nil { t.Fatal(err) }
    defer s.Close()
    id, err := s.CreateChannel("auth-refactor", "/tmp/proj", "git@x:y.git", "abc123", false)
    if err != nil { t.Fatal(err) }
    if id == 0 { t.Fatal("expected nonzero id") }
    got, err := s.ListChannels("/tmp/proj", false)
    if err != nil { t.Fatal(err) }
    if len(got) != 1 || got[0].Name != "auth-refactor" { t.Fatalf("got %+v", got) }
}

func TestArbitraryOrphanChannel(t *testing.T) {
    s, _ := Open(":memory:")
    defer s.Close()
    if _, err := s.CreateChannel("scratch", "", "", "", true); err != nil { t.Fatal(err) }
    got, _ := s.ListChannels("", false)
    if len(got) != 0 { t.Fatalf("orphans must hide without flag: %+v", got) }
    got, _ = s.ListChannels("", true)
    if len(got) != 1 { t.Fatalf("orphans must show with flag: %+v", got) }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run 'TestCreateAndListAnchoredChannel|TestArbitraryOrphanChannel' -v`
Expected: FAIL with `no such file or directory` or `undefined: Open`.

- [ ] **Step 3: Write minimal implementation**

`go.mod`:
```
module github.com/NoRaincheck/fluffle

go 1.26
```

`internal/store/store.go` (schema verbatim from spec, plus `Open`, `CreateChannel` with validation `name` non-empty, `orphaned == (repoAbsPath == "")`, `ListChannels` filtering orphans):
```go
package store

import (
    "database/sql"
    "errors"
    "strings"

    _ "modernc.org/sqlite"
)

var ErrConflict = errors.New("conflict")
var ErrNotFound = errors.New("not found")

const schema = `
CREATE TABLE IF NOT EXISTS channels(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL CHECK(length(trim(name)) > 0),
  repo_abs_path TEXT,
  repo_remote TEXT,
  repo_head_sha TEXT,
  is_orphaned INTEGER NOT NULL DEFAULT 0 CHECK(is_orphaned IN (0,1)),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  archived_at TEXT,
  CHECK ((is_orphaned = 1 AND repo_abs_path IS NULL)
      OR (is_orphaned = 0 AND repo_abs_path IS NOT NULL)),
  UNIQUE(name, repo_abs_path)
);
CREATE UNIQUE INDEX IF NOT EXISTS uniq_orphan_channel_name ON channels(name) WHERE is_orphaned = 1;
CREATE TABLE IF NOT EXISTS threads(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  channel_id INTEGER NOT NULL REFERENCES channels(id),
  title TEXT NOT NULL CHECK(length(trim(title)) > 0),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  archived_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_threads_channel ON threads(channel_id, id);
CREATE TABLE IF NOT EXISTS messages(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  thread_id INTEGER NOT NULL REFERENCES threads(id),
  seq INTEGER NOT NULL,
  author TEXT NOT NULL CHECK(length(trim(author)) > 0),
  author_type TEXT NOT NULL CHECK(author_type IN ('human','agent')),
  role TEXT NOT NULL CHECK(role IN ('user','assistant','system')),
  content TEXT NOT NULL CHECK(length(trim(content)) > 0),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  UNIQUE(thread_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_messages_thread_seq ON messages(thread_id, seq);
CREATE TABLE IF NOT EXISTS reactions(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  message_id INTEGER NOT NULL REFERENCES messages(id),
  emoji TEXT NOT NULL CHECK(length(trim(emoji)) > 0),
  author TEXT NOT NULL CHECK(length(trim(author)) > 0),
  author_type TEXT NOT NULL CHECK(author_type IN ('human','agent')),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  UNIQUE(message_id, emoji, author)
);
`

type Store struct{ db *sql.DB }

type Channel struct {
    ID int64
    Name, RepoAbsPath, RepoRemote, RepoHeadSHA, CreatedAt string
    IsOrphaned bool
    ArchivedAt *string
}

func Open(path string) (*Store, error) {
    db, err := sql.Open("sqlite", path)
    if err != nil { return nil, err }
    if _, err := db.Exec(schema); err != nil { db.Close(); return nil, err }
    return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) CreateChannel(name, repoAbsPath, repoRemote, repoHeadSHA string, orphaned bool) (int64, error) {
    if strings.TrimSpace(name) == "" { return 0, errors.New("channel name required") }
    var abs any
    var isOrphan int
    if orphaned {
        if repoAbsPath != "" { return 0, errors.New("orphaned channel must not have repo path") }
        abs = nil
        isOrphan = 1
    } else {
        if strings.TrimSpace(repoAbsPath) == "" { return 0, errors.New("repo path required") }
        abs = repoAbsPath
    }
    res, err := s.db.Exec(`INSERT INTO channels(name, repo_abs_path, repo_remote, repo_head_sha, is_orphaned) VALUES(?,?,?,?,?)`, name, abs, nullIfEmpty(repoRemote), nullIfEmpty(repoHeadSHA), isOrphan)
    if err != nil {
        if strings.Contains(err.Error(), "UNIQUE") { return 0, ErrConflict }
        return 0, err
    }
    return res.LastInsertId()
}

func (s *Store) ListChannels(repoAbsPath string, includeOrphaned bool) ([]Channel, error) {
    q := `SELECT id, name, COALESCE(repo_abs_path,''), COALESCE(repo_remote,''), COALESCE(repo_head_sha,''), is_orphaned, COALESCE(created_at,''), archived_at FROM channels WHERE archived_at IS NULL`
    args := []any{}
    if repoAbsPath != "" {
        q += ` AND repo_abs_path = ?`
        args = append(args, repoAbsPath)
    }
    if !includeOrphaned {
        q += ` AND is_orphaned = 0`
    }
    q += ` ORDER BY id`
    rows, err := s.db.Query(q, args...)
    if err != nil { return nil, err }
    defer rows.Close()
    var out []Channel
    for rows.Next() {
        var c Channel
        var isOrphan int
        if err := rows.Scan(&c.ID, &c.Name, &c.RepoAbsPath, &c.RepoRemote, &c.RepoHeadSHA, &isOrphan, &c.CreatedAt, &c.ArchivedAt); err != nil { return nil, err }
        c.IsOrphaned = isOrphan == 1
        out = append(out, c)
    }
    return out, rows.Err()
}

func nullIfEmpty(v string) any {
    if v == "" { return nil }
    return v
}
```

Run: `go mod tidy` to resolve `modernc.org/sqlite`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/store/ -v`
Expected: PASS both tests.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum internal/store/store.go internal/store/store_test.go
git commit -m "feat(store): schema and channels with orphan support"
```

---

### Task 2: Threads + messages with daemon-assigned seq + reactions

**Files:**
- Modify: `internal/store/store.go`
- Modify: `internal/store/store_test.go`

**Interfaces:**
- Consumes: `Open`, `Store` from Task 1.
- Produces: `type Thread struct { ID, ChannelID int64; Title, CreatedAt string; ArchivedAt *string }`; `type Message struct { ID, ThreadID, Seq int64; Author, AuthorType, Role, Content, CreatedAt string }`; `func (s *Store) CreateThread(channelID int64, title string) (int64, error)`; `func (s *Store) ListThreads(channelID int64) ([]Thread, error)`; `func (s *Store) AppendMessage(threadID int64, author, authorType, role, content string) (int64, error)` (assigns `max(seq)+1` in tx); `func (s *Store) ListMessages(threadID int64, lastN int) ([]Message, error)` (ORDER BY seq ASC, last N); `func (s *Store) AddReaction(messageID int64, emoji, author, authorType string) error`.

- [ ] **Step 1: Write the failing test**

```go
func TestAppendAssignsSeqInOrder(t *testing.T) {
    s, _ := Open(":memory:")
    defer s.Close()
    ch, _ := s.CreateChannel("c", "/r", "", "", false)
    th, err := s.CreateThread(ch, "Schema migration")
    if err != nil { t.Fatal(err) }
    s1, _ := s.AppendMessage(th, "alice", "human", "user", "first")
    s2, _ := s.AppendMessage(th, "pi-agent", "agent", "assistant", "second")
    if s1 != 1 || s2 != 2 { t.Fatalf("seqs %d %d", s1, s2) }
    msgs, _ := s.ListMessages(th, 1)
    if len(msgs) != 1 || msgs[0].Content != "second" { t.Fatalf("%+v", msgs) }
}

func TestReactionUniquePerAuthorEmoji(t *testing.T) {
    s, _ := Open(":memory:")
    defer s.Close()
    ch, _ := s.CreateChannel("c", "/r", "", "", false)
    th, _ := s.CreateThread(ch, "t")
    s.AppendMessage(th, "a", "human", "user", "hi")
    msgs, _ := s.ListMessages(th, 10)
    if err := s.AddReaction(msgs[0].ID, "👀", "bob", "human"); err != nil { t.Fatal(err) }
    if err := s.AddReaction(msgs[0].ID, "👀", "bob", "human"); err == nil { t.Fatal("expected duplicate error") }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run 'TestAppendAssignsSeqInOrder|TestReactionUniquePerAuthorEmoji' -v`
Expected: FAIL with `undefined: CreateThread` (or `AppendMessage`).

- [ ] **Step 3: Write minimal implementation**

Append to `internal/store/store.go`:
```go
type Thread struct {
    ID, ChannelID int64
    Title, CreatedAt string
    ArchivedAt *string
}

type Message struct {
    ID, ThreadID, Seq int64
    Author, AuthorType, Role, Content, CreatedAt string
}

func (s *Store) CreateThread(channelID int64, title string) (int64, error) {
    if strings.TrimSpace(title) == "" { return 0, errors.New("title required") }
    var n int
    if err := s.db.QueryRow(`SELECT COUNT(*) FROM channels WHERE id = ? AND archived_at IS NULL`, channelID).Scan(&n); err != nil { return 0, err }
    if n == 0 { return 0, ErrNotFound }
    res, err := s.db.Exec(`INSERT INTO threads(channel_id, title) VALUES(?,?)`, channelID, title)
    if err != nil { return 0, err }
    return res.LastInsertId()
}

func (s *Store) ListThreads(channelID int64) ([]Thread, error) {
    rows, err := s.db.Query(`SELECT id, channel_id, title, COALESCE(created_at,''), archived_at FROM threads WHERE channel_id = ? AND archived_at IS NULL ORDER BY id`, channelID)
    if err != nil { return nil, err }
    defer rows.Close()
    var out []Thread
    for rows.Next() {
        var th Thread
        if err := rows.Scan(&th.ID, &th.ChannelID, &th.Title, &th.CreatedAt, &th.ArchivedAt); err != nil { return nil, err }
        out = append(out, th)
    }
    return out, rows.Err()
}

func (s *Store) AppendMessage(threadID int64, author, authorType, role, content string) (int64, error) {
    if strings.TrimSpace(author) == "" || strings.TrimSpace(content) == "" { return 0, errors.New("author and content required") }
    if authorType != "human" && authorType != "agent" { return 0, errors.New("bad author_type") }
    if role != "user" && role != "assistant" && role != "system" { return 0, errors.New("bad role") }
    tx, err := s.db.Begin()
    if err != nil { return 0, err }
    defer tx.Rollback()
    var n int
    if err := tx.QueryRow(`SELECT COUNT(*) FROM threads WHERE id = ? AND archived_at IS NULL`, threadID).Scan(&n); err != nil { return 0, err }
    if n == 0 { return 0, ErrNotFound }
    var maxSeq sql.NullInt64
    if err := tx.QueryRow(`SELECT MAX(seq) FROM messages WHERE thread_id = ?`, threadID).Scan(&maxSeq); err != nil { return 0, err }
    seq := int64(1)
    if maxSeq.Valid { seq = maxSeq.Int64 + 1 }
    if _, err := tx.Exec(`INSERT INTO messages(thread_id, seq, author, author_type, role, content) VALUES(?,?,?,?,?,?)`, threadID, seq, author, authorType, role, content); err != nil { return 0, err }
    if err := tx.Commit(); err != nil { return 0, err }
    return seq, nil
}

func (s *Store) ListMessages(threadID int64, lastN int) ([]Message, error) {
    q := `SELECT id, thread_id, seq, author, author_type, role, content, COALESCE(created_at,'') FROM messages WHERE thread_id = ? ORDER BY seq ASC`
    if lastN > 0 {
        q = `SELECT * FROM (` + q + `) ORDER BY seq DESC LIMIT ?`
        q = `SELECT * FROM (` + q + `) ORDER BY seq ASC`
        rows, err := s.db.Query(q, threadID, lastN)
        if err != nil { return nil, err }
        defer rows.Close()
        return scanMessages(rows)
    }
    rows, err := s.db.Query(q, threadID)
    if err != nil { return nil, err }
    defer rows.Close()
    return scanMessages(rows)
}

func scanMessages(rows *sql.Rows) ([]Message, error) {
    var out []Message
    for rows.Next() {
        var m Message
        if err := rows.Scan(&m.ID, &m.ThreadID, &m.Seq, &m.Author, &m.AuthorType, &m.Role, &m.Content, &m.CreatedAt); err != nil { return nil, err }
        out = append(out, m)
    }
    return out, rows.Err()
}

func (s *Store) AddReaction(messageID int64, emoji, author, authorType string) error {
    if strings.TrimSpace(emoji) == "" || strings.TrimSpace(author) == "" { return errors.New("emoji and author required") }
    if authorType != "human" && authorType != "agent" { return errors.New("bad author_type") }
    if _, err := s.db.Exec(`INSERT INTO reactions(message_id, emoji, author, author_type) VALUES(?,?,?,?)`, messageID, emoji, author, authorType); err != nil {
        if strings.Contains(err.Error(), "UNIQUE") { return ErrConflict }
        return err
    }
    return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/store/ -v`
Expected: PASS all 4 tests.

- [ ] **Step 5: Commit**

```bash
git add internal/store/store.go internal/store/store_test.go
git commit -m "feat(store): threads, seq-assigned messages, reactions"
```

---

### Task 3: JSONL codec (ACP-compatible line schema)

**Files:**
- Create: `internal/jsonl/jsonl.go`
- Create: `internal/jsonl/jsonl_test.go`

**Interfaces:**
- Consumes: `store.Message` shape (Task 2) for field parity.
- Produces: `type Line struct { Seq int64 \`json:"seq"\`; Role string \`json:"role"\`; Author string \`json:"author"\`; AuthorType string \`json:"author_type"\`; Content string \`json:"content"\`; Timestamp string \`json:"timestamp"\`; Metadata map[string]any \`json:"metadata"\` }`; `func MarshalLine(l Line) (string, error)`; `func ParseLine(s string) (Line, error)` (wraps errors as `BAD_JSONL: line N: …` via `ParseLines`); `func ParseLines(data []byte) ([]Line, error)`; `func EncodeLines(lines []Line) []byte`.

- [ ] **Step 1: Write the failing test**

```go
package jsonl

import "testing"

func TestRoundTripPreservesFields(t *testing.T) {
    in := []Line{{Seq: 99, Role: "assistant", Author: "pi-agent", AuthorType: "agent", Content: "hi", Timestamp: "2026-09-22T00:00:00Z", Metadata: map[string]any{}}}
    data := EncodeLines(in)
    out, err := ParseLines(data)
    if err != nil { t.Fatal(err) }
    if len(out) != 1 || out[0].Content != "hi" || out[0].Role != "assistant" { t.Fatalf("%+v", out) }
    if out[0].Seq != 99 { t.Fatalf("seq %+v", out) }
}

func TestBadJSONLNamesLine(t *testing.T) {
    _, err := ParseLines([]byte("{\"seq\":1}\nnot-json\n"))
    if err == nil { t.Fatal("expected error") }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/jsonl/ -v`
Expected: FAIL with `undefined: Line`.

- [ ] **Step 3: Write minimal implementation**

```go
package jsonl

import (
    "bufio"
    "bytes"
    "encoding/json"
    "fmt"
)

type Line struct {
    Seq        int64          `json:"seq"`
    Role       string         `json:"role"`
    Author     string         `json:"author"`
    AuthorType string         `json:"author_type"`
    Content    string         `json:"content"`
    Timestamp  string         `json:"timestamp"`
    Metadata   map[string]any `json:"metadata"`
}

func MarshalLine(l Line) (string, error) {
    if l.Metadata == nil { l.Metadata = map[string]any{} }
    b, err := json.Marshal(l)
    if err != nil { return "", err }
    return string(b), nil
}

func ParseLine(s string) (Line, error) {
    var l Line
    if err := json.Unmarshal([]byte(s), &l); err != nil { return Line{}, err }
    if l.Role == "" || l.Content == "" { return Line{}, fmt.Errorf("role and content required") }
    if l.Metadata == nil { l.Metadata = map[string]any{} }
    return l, nil
}

func ParseLines(data []byte) ([]Line, error) {
    var out []Line
    sc := bufio.NewScanner(bytes.NewReader(data))
    sc.Buffer(make([]byte, 1024*1024), 1024*1024)
    n := 0
    for sc.Scan() {
        n++
        line := sc.Text()
        if line == "" { continue }
        l, err := ParseLine(line)
        if err != nil { return nil, fmt.Errorf("BAD_JSONL: line %d: %w", n, err) }
        out = append(out, l)
    }
    return out, sc.Err()
}

func EncodeLines(lines []Line) []byte {
    var buf bytes.Buffer
    for _, l := range lines {
        s, _ := MarshalLine(l)
        buf.WriteString(s)
        buf.WriteByte('\n')
    }
    return buf.Bytes()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/jsonl/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/jsonl/jsonl.go internal/jsonl/jsonl_test.go
git commit -m "feat(jsonl): ACP-compatible line codec"
```

---

### Task 4: Repo resolution (canonicalize + strict git check)

**Files:**
- Create: `internal/repo/repo.go`
- Create: `internal/repo/repo_test.go`

**Interfaces:**
- Consumes: nothing (used by CLI before store calls).
- Produces: `func Canonicalize(path string) (string, error)` (`Abs` + `EvalSymlinks`); `func InspectGitDir(absPath string) (remote, headSHA string, isGit bool)` (checks `absPath/.git` exists as dir or file; runs `git -C dir remote get-url origin` and `git -C dir rev-parse HEAD`, empty string on failure, never errors on missing remote).

- [ ] **Step 1: Write the failing test**

```go
package repo

import (
    "os"
    "path/filepath"
    "testing"
)

func TestNonGitDirReportsFalse(t *testing.T) {
    dir := t.TempDir()
    _, _, isGit := InspectGitDir(dir)
    if isGit { t.Fatal("expected not-git") }
}

func TestGitDirReportsTrue(t *testing.T) {
    dir := t.TempDir()
    if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil { t.Fatal(err) }
    _, _, isGit := InspectGitDir(dir)
    if !isGit { t.Fatal("expected git") }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/repo/ -v`
Expected: FAIL with `undefined: InspectGitDir`.

- [ ] **Step 3: Write minimal implementation**

```go
package repo

import (
    "os"
    "os/exec"
    "path/filepath"
    "strings"
)

func Canonicalize(path string) (string, error) {
    abs, err := filepath.Abs(path)
    if err != nil { return "", err }
    resolved, err := filepath.EvalSymlinks(abs)
    if err != nil { return "", err }
    return resolved, nil
}

func InspectGitDir(absPath string) (remote, headSHA string, isGit bool) {
    fi, err := os.Stat(filepath.Join(absPath, ".git"))
    if err != nil || (fi != nil && !fi.IsDir() && fi.Size() == 0) {
        if err != nil { return "", "", false }
    }
    if fi == nil { return "", "", false }
    out, err := exec.Command("git", "-C", absPath, "remote", "get-url", "origin").Output()
    if err == nil { remote = strings.TrimSpace(string(out)) }
    out, err = exec.Command("git", "-C", absPath, "rev-parse", "HEAD").Output()
    if err == nil { headSHA = strings.TrimSpace(string(out)) }
    return remote, headSHA, true
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/repo/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/repo/repo.go internal/repo/repo_test.go
git commit -m "feat(repo): canonicalize and git inspection"
```

---

### Task 5: HTTP daemon + health + agent guards + error envelope

**Files:**
- Create: `internal/apiserver/server.go`
- Create: `internal/apiserver/server_test.go`

**Interfaces:**
- Consumes: `store.Store`, `store.ErrNotFound`, `store.ErrConflict` (Tasks 1–2).
- Produces: `func NewHandler(s *store.Store) http.Handler` serving exactly `GET /v1/health`, `GET /v1/channels?repo=&include-orphaned=1`, `POST /v1/channels`, `GET /v1/channels/{id}/threads`, `POST /v1/channels/{id}/threads`, `GET /v1/threads/{id}/messages?last=N`, `POST /v1/threads/{id}/messages`, `POST /v1/messages/{id}/reactions`; agent identity from `X-Fluffle-Agent` header non-empty; agents get `403 {"code":"AGENT_FORBIDDEN","message":"…"}` on the two POST create routes; all errors use `{"code","message"}` with spec codes.

- [ ] **Step 1: Write the failing permission-matrix test**

```go
package apiserver

import (
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"

    "github.com/NoRaincheck/fluffle/internal/store"
)

func TestAgentCannotCreateChannelButCanAppend(t *testing.T) {
    s, _ := store.Open(":memory:")
    defer s.Close()
    h := NewHandler(s)
    req := httptest.NewRequest("POST", "/v1/channels", strings.NewReader(`{"name":"x"}`))
    req.Header.Set("X-Fluffle-Agent", "pi")
    rec := httptest.NewRecorder()
    h.ServeHTTP(rec, req)
    if rec.Code != http.StatusForbidden { t.Fatalf("want 403 got %d %s", rec.Code, rec.Body.String()) }
    if !strings.Contains(rec.Body.String(), "AGENT_FORBIDDEN") { t.Fatalf("body %s", rec.Body.String()) }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/apiserver/ -run TestAgentCannotCreateChannelButCanAppend -v`
Expected: FAIL with `undefined: NewHandler`.

- [ ] **Step 3: Write minimal implementation**

```go
package apiserver

import (
    "encoding/json"
    "net/http"
    "strconv"
    "strings"

    "github.com/NoRaincheck/fluffle/internal/store"
)

type errBody struct {
    Code    string `json:"code"`
    Message string `json:"message"`
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    json.NewEncoder(w).Encode(errBody{Code: code, Message: msg})
}

func isAgent(r *http.Request) bool { return r.Header.Get("X-Fluffle-Agent") != "" }

func NewHandler(s *store.Store) http.Handler {
    mux := http.NewServeMux()
    mux.HandleFunc("/v1/health", func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "application/json")
        w.Write([]byte(`{"ok":true}`))
    })
    mux.HandleFunc("/v1/channels", func(w http.ResponseWriter, r *http.Request) {
        switch r.Method {
        case "GET":
            repo := r.URL.Query().Get("repo")
            inc := r.URL.Query().Get("include-orphaned") == "1"
            list, err := s.ListChannels(repo, inc)
            if err != nil { writeErr(w, 500, "DAEMON_DOWN", err.Error()); return }
            json.NewEncoder(w).Encode(list)
        case "POST":
            if isAgent(r) { writeErr(w, 403, "AGENT_FORBIDDEN", "agents cannot create channels"); return }
            var body struct {
                Name, RepoAbsPath, RepoRemote, RepoHeadSHA string
                Orphaned bool `json:"orphaned"`
            }
            if err := json.NewDecoder(r.Body).Decode(&body); err != nil { writeErr(w, 400, "BAD_JSONL", err.Error()); return }
            id, err := s.CreateChannel(body.Name, body.RepoAbsPath, body.RepoRemote, body.RepoHeadSHA, body.Orphaned)
            if err == store.ErrConflict { writeErr(w, 409, "CHANNEL_NOT_FOUND", "channel exists"); return }
            if err != nil { writeErr(w, 400, "NOT_A_GIT_REPO", err.Error()); return }
            json.NewEncoder(w).Encode(map[string]any{"id": id})
        default:
            writeErr(w, 405, "DAEMON_DOWN", "method not allowed")
        }
    })
    mux.HandleFunc("/v1/channels/", func(w http.ResponseWriter, r *http.Request) {
        rest := strings.TrimPrefix(r.URL.Path, "/v1/channels/")
        parts := strings.Split(rest, "/")
        if len(parts) != 2 || parts[1] != "threads" { writeErr(w, 404, "CHANNEL_NOT_FOUND", "unknown route"); return }
        id, _ := strconv.ParseInt(parts[0], 10, 64)
        switch r.Method {
        case "GET":
            list, err := s.ListThreads(id)
            if err != nil { writeErr(w, 500, "DAEMON_DOWN", err.Error()); return }
            json.NewEncoder(w).Encode(list)
        case "POST":
            if isAgent(r) { writeErr(w, 403, "AGENT_FORBIDDEN", "agents cannot create threads"); return }
            var body struct{ Title string `json:"title"` }
            if err := json.NewDecoder(r.Body).Decode(&body); err != nil { writeErr(w, 400, "BAD_JSONL", err.Error()); return }
            tid, err := s.CreateThread(id, body.Title)
            if err == store.ErrNotFound { writeErr(w, 404, "CHANNEL_NOT_FOUND", "no such channel"); return }
            if err != nil { writeErr(w, 400, "DAEMON_DOWN", err.Error()); return }
            json.NewEncoder(w).Encode(map[string]any{"id": tid})
        }
    })
    mux.HandleFunc("/v1/threads/", func(w http.ResponseWriter, r *http.Request) {
        rest := strings.TrimPrefix(r.URL.Path, "/v1/threads/")
        parts := strings.Split(rest, "/")
        if len(parts) != 2 || parts[1] != "messages" { writeErr(w, 404, "THREAD_NOT_FOUND", "unknown route"); return }
        id, _ := strconv.ParseInt(parts[0], 10, 64)
        switch r.Method {
        case "GET":
            last, _ := strconv.Atoi(r.URL.Query().Get("last"))
            msgs, err := s.ListMessages(id, last)
            if err != nil { writeErr(w, 500, "DAEMON_DOWN", err.Error()); return }
            json.NewEncoder(w).Encode(msgs)
        case "POST":
            var body struct {
                Author, Role, Content, AgentID string
            }
            if err := json.NewDecoder(r.Body).Decode(&body); err != nil { writeErr(w, 400, "BAD_JSONL", err.Error()); return }
            authorType := "human"
            if body.AgentID != "" || isAgent(r) { authorType = "agent" }
            author := body.Author
            if author == "" { author = body.AgentID }
            if author == "" { author = "unknown" }
            seq, err := s.AppendMessage(id, author, authorType, body.Role, body.Content)
            if err == store.ErrNotFound { writeErr(w, 404, "THREAD_NOT_FOUND", "no such thread"); return }
            if err != nil { writeErr(w, 400, "BAD_JSONL", err.Error()); return }
            json.NewEncoder(w).Encode(map[string]any{"seq": seq})
        }
    })
    mux.HandleFunc("/v1/messages/", func(w http.ResponseWriter, r *http.Request) {
        rest := strings.TrimPrefix(r.URL.Path, "/v1/messages/")
        parts := strings.Split(rest, "/")
        if len(parts) != 2 || parts[1] != "reactions" || r.Method != "POST" {
            writeErr(w, 404, "THREAD_NOT_FOUND", "unknown route")
            return
        }
        id, _ := strconv.ParseInt(parts[0], 10, 64)
        var body struct {
            Emoji, Author, AgentID string
        }
        if err := json.NewDecoder(r.Body).Decode(&body); err != nil { writeErr(w, 400, "BAD_JSONL", err.Error()); return }
        authorType := "human"
        if body.AgentID != "" || isAgent(r) { authorType = "agent" }
        author := body.Author
        if author == "" { author = body.AgentID }
        if author == "" { author = "unknown" }
        if err := s.AddReaction(id, body.Emoji, author, authorType); err == store.ErrConflict {
            writeErr(w, 409, "DAEMON_DOWN", "duplicate reaction")
            return
        } else if err != nil { writeErr(w, 400, "BAD_JSONL", err.Error()); return }
        json.NewEncoder(w).Encode(map[string]any{"ok": true})
    })
    return mux
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/apiserver/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/apiserver/server.go internal/apiserver/server_test.go
git commit -m "feat(api): daemon routes with agent guards"
```

---

### Task 6: Daemon binary wiring + client locate/auto-spawn + `flf daemon` commands

**Files:**
- Create: `internal/client/client.go`
- Create: `cmd/flf/main.go` (skeleton with `daemon start|stop|status`, `tui` stub, `--help`)

**Interfaces:**
- Consumes: `apiserver.NewHandler`, `store.Open` (Tasks 1–2, 5).
- Produces: `func FluffleHome() string`; `func DaemonBaseURL() (string, error)` (reads `daemon.json`, probes `GET /v1/health`); `func EnsureDaemon() (string, error)` (spawn `os.Executable daemon start --background` detached, retry probe 3x); CLI `flf daemon start [--background]` binds `127.0.0.1:0`, writes `daemon.json {port, pid, started_at}`; `flf daemon stop` kills pid; `flf daemon status` probes; `flf tui` exits 1 with `backend-only milestone: tui deferred`.

- [ ] **Step 1: Write the failing client test**

```go
package client

import "testing"

func TestDaemonBaseURLMissingFileErrors(t *testing.T) {
    t.Setenv("FLUFFLE_HOME", t.TempDir())
    if _, err := DaemonBaseURL(); err == nil { t.Fatal("expected error with no daemon.json") }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/client/ -v`
Expected: FAIL with `undefined: DaemonBaseURL`.

- [ ] **Step 3: Write minimal implementation**

`internal/client/client.go`:
```go
package client

import (
    "encoding/json"
    "errors"
    "net/http"
    "os"
    "os/exec"
    "path/filepath"
    "strconv"
    "time"
)

type daemonFile struct {
    Port      int    `json:"port"`
    PID       int    `json:"pid"`
    StartedAt string `json:"started_at"`
}

func FluffleHome() string {
    if v := os.Getenv("FLUFFLE_HOME"); v != "" { return v }
    home, _ := os.UserHomeDir()
    return filepath.Join(home, ".fluffle")
}

func daemonFilePath() string { return filepath.Join(FluffleHome(), "daemon.json") }

func DaemonBaseURL() (string, error) {
    b, err := os.ReadFile(daemonFilePath())
    if err != nil { return "", errors.New("DAEMON_DOWN: no daemon.json") }
    var df daemonFile
    if err := json.Unmarshal(b, &df); err != nil { return "", errors.New("DAEMON_DOWN: bad daemon.json") }
    base := "http://127.0.0.1:" + strconv.Itoa(df.Port)
    probe, err := http.Get(base + "/v1/health")
    if err != nil || probe.StatusCode != 200 { return "", errors.New("DAEMON_DOWN: probe failed") }
    probe.Body.Close()
    return base, nil
}

func EnsureDaemon() (string, error) {
    if base, err := DaemonBaseURL(); err == nil { return base, nil }
    exe, err := os.Executable()
    if err != nil { return "", err }
    cmd := exec.Command(exe, "daemon", "start", "--background")
    cmd.Stdout, cmd.Stderr = nil, nil
    if err := cmd.Start(); err != nil { return "", errors.New("DAEMON_DOWN: spawn failed") }
    var last error
    for i := 0; i < 3; i++ {
        time.Sleep(300 * time.Millisecond)
        if base, err := DaemonBaseURL(); err == nil { return base, nil } else { last = err }
    }
    return "", last
}
```

`cmd/flf/main.go` skeleton (daemon start/stop/status + tui stub + help):
```go
package main

import (
    "encoding/json"
    "fmt"
    "net"
    "net/http"
    "os"
    "path/filepath"
    "strconv"
    "time"

    "github.com/NoRaincheck/fluffle/internal/apiserver"
    "github.com/NoRaincheck/fluffle/internal/client"
    "github.com/NoRaincheck/fluffle/internal/store"
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
    if len(args) == 0 { fmt.Fprintln(os.Stderr, "usage: flf <daemon|channel|thread|message|react|agent|tui>"); return 1 }
    switch args[0] {
    case "daemon":
        return daemonCmd(args[1:])
    case "tui":
        fmt.Fprintln(os.Stderr, "backend-only milestone: tui deferred")
        return 1
    default:
        fmt.Fprintln(os.Stderr, "unknown command (backend slice implements daemon + tui stub; rest in Task 7-8)")
        return 1
    }
}

func daemonCmd(args []string) int {
    if len(args) == 0 { fmt.Fprintln(os.Stderr, "usage: flf daemon <start|stop|status>"); return 1 }
    switch args[0] {
    case "status":
        base, err := client.DaemonBaseURL()
        if err != nil { fmt.Fprintln(os.Stderr, "daemon down"); return 2 }
        fmt.Println("daemon up at", base)
        return 0
    case "start":
        background := len(args) > 1 && args[1] == "--background"
        return daemonStart(background)
    case "stop":
        return daemonStop()
    }
    return 1
}

func daemonStop() int {
    b, err := os.ReadFile(filepath.Join(client.FluffleHome(), "daemon.json"))
    if err != nil { fmt.Fprintln(os.Stderr, "DAEMON_DOWN: not running"); return 2 }
    var df struct {
        PID int `json:"pid"`
    }
    if err := json.Unmarshal(b, &df); err != nil { fmt.Fprintln(os.Stderr, "DAEMON_DOWN: bad daemon.json"); return 2 }
    proc, err := os.FindProcess(df.PID)
    if err != nil { fmt.Fprintln(os.Stderr, "DAEMON_DOWN:", err); return 2 }
    if err := proc.Kill(); err != nil { fmt.Fprintln(os.Stderr, "DAEMON_DOWN:", err); return 2 }
    os.Remove(filepath.Join(client.FluffleHome(), "daemon.json"))
    fmt.Println("daemon stopped")
    return 0
}

func daemonStart(background bool) int {
    home := client.FluffleHome()
    os.MkdirAll(home, 0o755)
    dbPath := filepath.Join(home, "fluffle.db")
    s, err := store.Open(dbPath)
    if err != nil { fmt.Fprintln(os.Stderr, "DAEMON_DOWN:", err); return 2 }
    ln, err := net.Listen("tcp", "127.0.0.1:0")
    if err != nil { fmt.Fprintln(os.Stderr, "DAEMON_DOWN:", err); return 2 }
    port := ln.Addr().(*net.TCPAddr).Port
    payload, _ := json.Marshal(map[string]any{"port": port, "pid": os.Getpid(), "started_at": time.Now().UTC().Format(time.RFC3339)})
    os.WriteFile(filepath.Join(home, "daemon.json"), payload, 0o644)
    srv := &http.Server{Handler: apiserver.NewHandler(s)}
    if !background {
        fmt.Println("fluffle daemon on 127.0.0.1:" + strconv.Itoa(port))
    }
    if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
        fmt.Fprintln(os.Stderr, "DAEMON_DOWN:", err)
        return 2
    }
    return 0
}
```

- [ ] **Step 4: Run tests + build to verify**

Run: `go test ./internal/client/ -v && go build ./...`
Expected: PASS + build ok.

- [ ] **Step 5: Commit**

```bash
git add internal/client/client.go cmd/flf/main.go
git commit -m "feat(daemon): wiring, auto-spawn client, daemon commands"
```

---

### Task 7: CLI for channels/threads/messages/reactions + error codes + `init`

**Files:**
- Modify: `cmd/flf/main.go`
- Create: `cmd/flf/main_test.go` (golden error-message test for `NOT_A_GIT_REPO` + `tui` stub)

**Interfaces:**
- Consumes: `client.EnsureDaemon`, `repo.Canonicalize`, `repo.InspectGitDir` (Tasks 4, 6); daemon routes (Task 5).
- Produces: `flf init [--orphaned]` (fail `NOT_A_GIT_REPO` exit 1 unless orphaned); `flf channel list --repo PATH [--include-orphaned]`; `flf channel create --name N (--repo PATH | --orphaned)`; `flf thread list --channel NAME --repo PATH`; `flf thread new --channel NAME --repo PATH --title T`; `flf message send --thread ID --text T [--as NAME] [--agent-id ID]`; `flf react add --message ID --emoji E [--as NAME] [--agent-id ID]`; typed stderr `CODE: message`, exits 1 user / 2 daemon.

- [ ] **Step 1: Write the failing CLI test**

```go
package main

import "testing"

func TestTuiStubMessage(t *testing.T) {
    if got := run([]string{"tui"}); got != 1 { t.Fatalf("want exit 1 got %d", got) }
}

func TestInitOutsideGitFails(t *testing.T) {
    dir := t.TempDir()
    if got := run([]string{"init", "--repo", dir}); got != 1 { t.Fatalf("want exit 1 got %d", got) }
}
```

Note: `run` must accept `init --repo DIR` for testability (CLI also supports bare `flf init` using cwd).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/flf/ -run 'TestTuiStubMessage|TestInitOutsideGitFails' -v`
Expected: FAIL on `TestInitOutsideGitFails` (`unknown command` → wrong path, or missing init branch).

- [ ] **Step 3: Write minimal implementation**

Extend `run()` in `cmd/flf/main.go` with branches (each: `EnsureDaemon`, build request, map HTTP error `code` → stderr `CODE: message`, exit 1; transport failure → `DAEMON_DOWN` exit 2):
- `init [--repo DIR] [--orphaned]`: resolve dir (flag or cwd) via `repo.Canonicalize`; `InspectGitDir`; if `!isGit && !orphaned` → stderr `NOT_A_GIT_REPO: <dir> is not a git repo (suggest --orphaned)`, return 1. Else print `initialized <dir>` return 0.
- `channel list --repo PATH [--include-orphaned]`: canonicalize repo, `GET /v1/channels?repo=<abs>&include-orphaned=0|1`, print `id name` lines.
- `channel create --name N (--repo PATH | --orphaned)`: if orphaned POST `{"name":N,"orphaned":true}`; else canonicalize + `InspectGitDir` (fail `NOT_A_GIT_REPO` exit 1), fetch remote/head, POST full body. Print `channel <id>`.
- `thread list|new`: resolve channel by `GET /v1/channels?repo=<abs>` name match (404 → `CHANNEL_NOT_FOUND` exit 1); then `GET` or `POST /v1/channels/{id}/threads`.
- `message send --thread ID --text T [--as NAME] [--agent-id ID]`: default `--as` to `$USER`; POST `/v1/threads/{id}/messages` with `{"author","role":"user","content","agent_id"}`; agent header set when `--agent-id` present. Print `seq <n>`.
- `react add --message ID --emoji E ...`: same author logic; POST `/v1/messages/{id}/reactions`.
- All JSON error bodies decoded to `{"code","message"}` and reprinted as `CODE: message`.
- List endpoints decode into `store.Channel`, `store.Thread`, `store.Message` (import `github.com/NoRaincheck/fluffle/internal/store` in `cmd/flf/main.go`); `store` structs are the wire shape, no separate DTOs.

(Implement with stdlib `flag.NewFlagSet` per subcommand; ~250 lines. No new deps.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./...`
Expected: PASS all packages.

- [ ] **Step 5: Commit**

```bash
git add cmd/flf/main.go cmd/flf/main_test.go
git commit -m "feat(cli): channels, threads, messages, reactions, init"
```

---

### Task 8: Agent read/append + export/import + e2e round-trip

**Files:**
- Modify: `cmd/flf/main.go`
- Modify: `internal/apiserver/server_test.go` (append round-trip test)

**Interfaces:**
- Consumes: `jsonl.ParseLines/EncodeLines` (Task 3), `store.AppendMessage/ListMessages` via HTTP (Tasks 2, 5), channel resolution (Task 7).
- Produces: `flf agent read --thread ID [--last N]` (GET messages → JSONL stdout); `flf agent append --thread ID --file F [--agent-id ID]` (parse file, POST each line, daemon reassigns seq; `BAD_JSONL: line N` exit 1, no partial write on parse failure — validate whole file before first POST); `flf thread export --thread ID --format jsonl` (full dump); `flf thread import --file F --channel NAME (--repo PATH | --orphaned)` (resolve/create channel, create thread titled `import <basename>`, replay lines); invariant export→import→export byte-identical modulo `seq` (metadata excluded — not persisted).

- [ ] **Step 1: Write the failing round-trip test**

```go
func TestExportImportRoundTrip(t *testing.T) {
    s, _ := store.Open(":memory:")
    defer s.Close()
    h := NewHandler(s)
    ch, _ := s.CreateChannel("c", "/r", "", "", false)
    th, _ := s.CreateThread(ch, "t")
    s.AppendMessage(th, "alice", "human", "user", "hello")
    req := httptest.NewRequest("GET", "/v1/threads/1/messages", nil)
    rec := httptest.NewRecorder()
    h.ServeHTTP(rec, req)
    if rec.Code != 200 { t.Fatalf("code %d", rec.Code) }
    var msgs []store.Message
    if err := json.NewDecoder(rec.Body).Decode(&msgs); err != nil { t.Fatal(err) }
    if len(msgs) != 1 || msgs[0].Seq != 1 { t.Fatalf("%+v", msgs) }
}
```

(add `encoding/json` import to `server_test.go`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/apiserver/ -run TestExportImportRoundTrip -v`
Expected: FAIL only if thread-id assumption wrong — fix by using returned `th` in URL (`fmt.Sprintf("/v1/threads/%d/messages", th)`). The red state is the CLI `agent/export/import` paths not existing yet: `go run ./cmd/flf agent --help` exits 1.

- [ ] **Step 3: Write minimal implementation**

In `cmd/flf/main.go` add branches:
- `agent read --thread ID [--last N]`: `GET /v1/threads/{id}/messages?last=N`, convert each `store.Message` to `jsonl.Line{Seq, Role, Author, AuthorType, Content, Timestamp: CreatedAt, Metadata: {}}`, write `jsonl.EncodeLines` to stdout.
- `agent append --thread ID --file F [--agent-id ID]`: read file, `jsonl.ParseLines` (on error stderr `BAD_JSONL: line N: …` exit 1 before any POST); for each line POST `/v1/threads/{id}/messages` with author/role/content + agent header; default role `user` when empty.
- `thread export --thread ID --format jsonl`: reject non-`jsonl` with `BAD_JSONL: only jsonl supported` exit 1; else identical to `agent read` full dump.
- `thread import --file F --channel NAME (--repo PATH | --orphaned)`: parse file first (same `BAD_JSONL` rule); resolve channel (list-match or create via Task 7 logic); `POST` new thread title `import <base>`; replay lines in order.
- Manual e2e check: `flf thread export --thread A | diff <(flf thread export --thread B)` after import — identical modulo `seq` (normalize with `jq -c 'del(.seq)'`).

- [ ] **Step 4: Run full suite + manual round-trip**

Run: `go test ./... && go vet ./... && go build ./...`
Expected: PASS, vet clean, build ok. Then live: `FLUFFLE_HOME=$(mktemp -d) go run ./cmd/flf daemon start --background` (background), exercise channel→thread→send→export→import→export, `diff` normalized output empty.

- [ ] **Step 5: Commit**

```bash
git add cmd/flf/main.go internal/apiserver/server_test.go
git commit -m "feat(cli): agent read/append, export/import round-trip"
```
