# sqlc-Generated SQL Layer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the schema-constant-plus-PRAGMA-probe DB layer in `internal/store` with versioned embedded migrations and sqlc-generated typed queries, while keeping the HTTP API, the TUI, and all existing tests unchanged.

**Architecture:** sqlc v1.31.1 owns the SQL. A `migrations/` directory of goose-formatted files is both sqlc's schema input and the runtime migration source; a ~100-line hand-written runner applies them. All 35 queries move into annotated `.sql` files and compile into a generated `internal/db` package. A hand-written `internal/store` facade keeps the JSON wire types, the error sentinels, and validation, and is the only thing that talks to `internal/db`.

**Tech Stack:** Go 1.26, sqlc v1.31.1, `modernc.org/sqlite` v1.59.0 (`database/sql`).

**Spec:** `docs/superpowers/specs/2026-09-27-sqlc-sql-layer-design.md` — read it before starting; this plan argues from it and the two must agree.

## Global Constraints

These apply to every task. They are copied verbatim from the spec.

- `gofmt -l .` must be empty and `go vet ./...` must be clean at every commit (`AGENTS.md`).
- TDD: failing test, then implementation, then commit. Never commit without tests passing.
- No comments in code unless explicitly required. SQL `-- name:` annotations are required by sqlc and are not comments in this sense.
- Error envelope: `{"code","message"}` at every layer boundary. Store sentinels stay `ErrNotFound` / `ErrConflict` / `ErrInvalid`.
- `Store.db` must remain an unexported `*sql.DB` field. `store_test.go:882` reaches into it for `PRAGMA table_info`.
- `SetMaxOpenConns(1)` is retained. Twelve tests call `newStoreWithThread(t, ":memory:")` and depend on it.
- **The JSON wire contract must not change.** `store.Channel`, `store.Thread`, `store.Message`, `store.Reaction`, `store.Session`, `store.SessionEvent`, and `store.ThreadContext` are marshaled directly by `apiserver` and unmarshaled by `tui`. They carry almost no JSON tags, so Go field names *are* the API field names. Generated `internal/db` types are never marshaled.
- **`emit_json_tags: false` is load-bearing.** With it `true`, sqlc emits `json:"RepoHeadSha"` while the field is `RepoHeadSHA`, silently changing the API.
- Every read query keeps its `COALESCE(col,'')` wrappers. This is what keeps `Channel.RepoAbsPath` and friends pointer-free.
- **Empty-slice convention is per-method and API-visible.** `null` and `[]` are different JSON. Preserve exactly:
  - return **nil** (marshals to `null`): `ListChannels`, `ListThreads`, `ListMessages`, `ListReactions`
  - return **non-nil** (marshals to `[]`): `ListInbox`, `ListSessions`, `ListSessionEvents`
  With `emit_empty_slices: false`, sqlc's `:many` returns nil, so the three non-nil methods need an explicit `if out == nil { out = []T{} }`.
- **No existing test may be modified to accommodate a new implementation** in tasks 1-9. A test that must change means behavior changed, which requires sign-off. Task 3 (validator removal) is the single sanctioned exception and must update the affected assertions in the same commit.
- Identifiers named `db` collide with the new `db` package. `store.go` lines 106, 118, 139, 155, 162 all bind a variable or parameter named `db`; rename to `conn`.
- Two-phase context rollout: tasks 5-9 keep the existing `Store` method signatures and pass `context.Background()` internally. Task 10 is the one isolated commit that threads real contexts. Do not thread context early.

---

## File Structure

**Created:**

| Path | Responsibility |
|---|---|
| `sqlc.yaml` | sqlc v2 config: engine, schema/queries paths, emit settings, rename, override, vet rules |
| `internal/store/migrations/00001_initial_schema.sql` | goose-formatted; verbatim move of the `schema` const; sqlc's schema input |
| `internal/store/queries/channels.sql` | 6 channel queries |
| `internal/store/queries/threads.sql` | 3 thread queries |
| `internal/store/queries/messages.sql` | 11 message queries |
| `internal/store/queries/reactions.sql` | 3 reaction queries |
| `internal/store/queries/agent_sessions.sql` | 9 session queries |
| `internal/store/queries/agent_session_events.sql` | 3 event queries |
| `internal/store/migrate.go` | embedded migration runner, legacy adoption, `archived_at` cleanup |
| `internal/store/migrate_test.go` | runner tests |
| `internal/store/classify.go` | SQLite error code to sentinel mapping |
| `internal/store/classify_test.go` | table test for all four codes |
| `internal/db/*` | GENERATED and committed: `db.go`, `models.go`, `querier.go`, `*.sql.go` |
| `Makefile` | `generate`, `diff`, `vet`, `test` targets |

**Modified:** `internal/store/store.go`, `internal/store/session.go`, `docs/backend.md`, `go.mod`, `go.sum`.

**Deleted:** the `schema` const, `hasColumn` (repurposed into `migrate.go`), `applySchema`, the old `migrate`, `scanMessages`, `scanSession`, the `scanner` interface, `sessionColumns`, `execSessionUpdate`, `nullIfEmpty`, `nullIfInt64`, `nullIfEmptyPtr`, and the six duplicated `*sql.DB`/`*sql.Tx` pairs.

---

## Query Inventory

The complete set of 35 queries, all previously inline. Annotation is `:execlastid` where only the new row id is needed (matching today's `res.LastInsertId()`), `:one` for scalars and single rows, `:many` for row sets, `:execrows` where affected-row count drives `ErrNotFound`.

**`queries/channels.sql`**

| # | sqlc name | Annotation | Replaces |
|---|---|---|---|
| 1 | `CreateChannel` | `:execlastid` | `store.go:212` |
| 2 | `ListChannelsAll` | `:many` | `store.go:223` variant |
| 3 | `ListChannelsByRepo` | `:many` | `store.go:223` variant |
| 4 | `ListChannelsNonOrphaned` | `:many` | `store.go:223` variant |
| 5 | `ListChannelsByRepoNonOrphaned` | `:many` | `store.go:223` variant |
| 6 | `CountChannelsByID` | `:one` | `store.go:316` — **deleted in task 10** |

**`queries/threads.sql`**

| # | sqlc name | Annotation | Replaces |
|---|---|---|---|
| 7 | `CreateThread` | `:execlastid` | `store.go:322` |
| 8 | `ListThreads` | `:many` | `store.go:330` |
| 9 | `CountThreadsByID` | `:one` | `store.go:410`, `store.go:780` — **deleted in task 10** |

**`queries/messages.sql`**

| # | sqlc name | Annotation | Replaces |
|---|---|---|---|
| 10 | `InsertMessage` | `:execlastid` | `store.go:439` |
| 11 | `InsertMessageDefaultCreatedAt` | `:execlastid` | `store.go:444` |
| 12 | `GetMessageMaxSeq` | `:one` | `store.go:417`, `store.go:787` |
| 13 | `GetMessageThreadIDByID` | `:one` | `store.go:426`, `store.go:609` |
| 14 | `GetMessageIDByThreadSeq` | `:one` | `store.go:478`, `store.go:490` |
| 15 | `ListMessages` | `:many` | `store.go:512` |
| 16 | `ListMessagesLastN` | `:many` | `store.go:505` |
| 17 | `ListMessagesAfter` | `:many` | `store.go:521` |
| 18 | `ListInbox` | `:many` | `store.go:555` |
| 19 | `CountAgentMessagesAfter` | `:one` | `session.go:232` |
| 20 | `GetMessageByIDWithParent` | `:one` | `session.go:252` |

**`queries/reactions.sql`**

| # | sqlc name | Annotation | Replaces |
|---|---|---|---|
| 21 | `InsertReaction` | `:execlastid` | `store.go:681` |
| 22 | `InsertReactionDefaultCreatedAt` | `:execlastid` | `store.go:683` |
| 23 | `ListReactions` | `:many` | `store.go:630` |

**`queries/agent_sessions.sql`**

| # | sqlc name | Annotation | Replaces |
|---|---|---|---|
| 24 | `CreateSession` | `:execlastid` | `session.go:97` |
| 25 | `ListSessions` | `:many` | `session.go:116` |
| 26 | `GetSession` | `:one` | `session.go:133` |
| 27 | `MarkSessionRunning` | `:execrows` | `session.go:141` |
| 28 | `SetSessionReply` | `:execrows` | `session.go:145` |
| 29 | `FinishSession` | `:execrows` | `session.go:154` |
| 30 | `CountSessionsByID` | `:one` | `session.go:185` — **deleted in task 10** |
| 31 | `ReconcileSessions` | `:execrows` | `session.go:263` |
| 32 | `GetThreadContext` | `:one` | `session.go:238` |

**`queries/agent_session_events.sql`**

| # | sqlc name | Annotation | Replaces |
|---|---|---|---|
| 33 | `GetSessionEventMaxSeq` | `:one` | `session.go:192` |
| 34 | `InsertSessionEvent` | `:execlastid` | `session.go:199` |
| 35 | `ListSessionEvents` | `:many` | `session.go:214` |

Because `query_parameter_limit: 0`, every query with parameters also gets an `XParams` struct, including single-parameter queries.

---

## Task 1: Pin sqlc and add the Makefile

No behavior change. Establishes the tool so every later task can regenerate.

**Files:**
- Modify: `go.mod`, `go.sum`
- Create: `Makefile`

**Interfaces:**
- Consumes: nothing
- Produces: `go tool sqlc` on `PATH` for this module; `make generate|diff|vet|test`

- [ ] **Step 1: Pin sqlc as a Go tool**

```bash
cd /Users/crn/dev/projects/fluffle
go get -tool github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1
```

First invocation compiles sqlc from source and takes one to two minutes. Subsequent `go tool sqlc` calls are cached. This is expected, not a hang.

- [ ] **Step 2: Verify the tool runs**

Run: `go tool sqlc version`
Expected: `v1.31.1`

If `go get -tool` fails on this Go version, fall back to `go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1` and use bare `sqlc` in the Makefile instead of `go tool sqlc`. Note the substitution in `docs/backend.md` at task 11.

- [ ] **Step 3: Confirm the build is unaffected**

Run: `go build ./... && go vet ./...`
Expected: clean, no output. `tool` directives do not enter the binary's import graph.

- [ ] **Step 4: Create the Makefile**

```makefile
generate:
	go tool sqlc generate

diff:
	go tool sqlc diff

vet:
	go tool sqlc vet

test:
	go vet ./...
	gofmt -l .
	go test ./...
```

- [ ] **Step 5: Verify the Makefile parses**

Run: `make test`
Expected: full suite passes; `gofmt -l .` prints nothing.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum Makefile
git commit -m "build: pin sqlc v1.31.1 as a go tool and add Makefile targets"
```

---

## Task 2: Extract the schema into a migration file, add a version table, and switch `Open` to a runner

The behavior-preserving foundation. `Open` stops using the `PRAGMA table_info` probe for versioning while still performing the identical destructive legacy rebuild, so `TestFileStoreMigrationRebuildsLegacySchemaWithoutParentColumn` keeps passing untouched.

**Files:**
- Create: `internal/store/migrations/00001_initial_schema.sql`
- Create: `internal/store/migrate.go`
- Create: `internal/store/migrate_test.go`
- Modify: `internal/store/store.go:21-95` (delete `schema` const), `:105-190` (rewrite `Open`, replace `migrate`/`applySchema`)

**Interfaces:**
- Consumes: nothing
- Produces:
  - `func migrateUp(conn *sql.DB, fsys fs.FS) error`
  - `func loadMigrations(fsys fs.FS) ([]migration, error)` where `migration` is `{version int64, name, up string}`
  - `func hasColumn(conn *sql.DB, table, column string) (bool, error)` (moved, signature unchanged)
  - `func dropArchivedAtColumns(conn *sql.DB) error` (moved, signature unchanged)
  - `var errMigrationConflict = errors.New("migration conflict")`

- [ ] **Step 1: Write the migration file**

Create `internal/store/migrations/00001_initial_schema.sql`. The body is a **verbatim** copy of the `schema` constant at `store.go:21-95`, including every `IF NOT EXISTS` clause, every `CHECK`, the partial unique index, and the `strftime` defaults:

```sql
-- +goose Up
CREATE TABLE IF NOT EXISTS channels(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL CHECK(length(trim(name)) > 0),
  repo_abs_path TEXT,
  repo_remote TEXT,
  repo_head_sha TEXT,
  repo_head_branch TEXT,
  is_orphaned INTEGER NOT NULL DEFAULT 0 CHECK(is_orphaned IN (0,1)),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  CHECK ((is_orphaned = 1 AND repo_abs_path IS NULL)
      OR (is_orphaned = 0 AND repo_abs_path IS NOT NULL)),
  UNIQUE(name, repo_abs_path)
);
CREATE UNIQUE INDEX IF NOT EXISTS uniq_orphan_channel_name ON channels(name) WHERE is_orphaned = 1;
CREATE TABLE IF NOT EXISTS threads(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  channel_id INTEGER NOT NULL REFERENCES channels(id),
  title TEXT NOT NULL CHECK(length(trim(title)) > 0),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_threads_channel ON threads(channel_id, id);
CREATE TABLE IF NOT EXISTS messages(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  thread_id INTEGER NOT NULL REFERENCES threads(id),
  seq INTEGER NOT NULL,
  parent_id INTEGER REFERENCES messages(id),
  name TEXT NOT NULL CHECK(length(trim(name)) > 0),
  author_type TEXT NOT NULL CHECK(author_type IN ('human','agent')),
  role TEXT NOT NULL CHECK(role IN ('user','assistant','system')),
  content TEXT NOT NULL CHECK(length(trim(content)) > 0),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  UNIQUE(thread_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_messages_parent ON messages(parent_id);
CREATE INDEX IF NOT EXISTS idx_messages_thread_seq ON messages(thread_id, seq);
CREATE TABLE IF NOT EXISTS reactions(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  message_id INTEGER NOT NULL REFERENCES messages(id),
  emoji TEXT NOT NULL CHECK(length(trim(emoji)) > 0),
  name TEXT NOT NULL CHECK(length(trim(name)) > 0),
  author_type TEXT NOT NULL CHECK(author_type IN ('human','agent')),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  UNIQUE(message_id, emoji, name)
);
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

-- +goose Down
DROP TABLE IF EXISTS agent_session_events;
DROP TABLE IF EXISTS agent_sessions;
DROP TABLE IF EXISTS reactions;
DROP TABLE IF EXISTS messages;
DROP TABLE IF EXISTS threads;
DROP TABLE IF EXISTS channels;
```

Do not alter any DDL. This file is the single source of truth for the schema; `docs/backend.md` must point at it rather than restate it (task 12).

- [ ] **Step 2: Write the failing runner tests**

Create `internal/store/migrate_test.go`:

```go
package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestMigrateUpAppliesInitialSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var v int64
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != 1 {
		t.Fatalf("version = %d, want 1", v)
	}
	channels, err := s.ListChannels("", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 0 {
		t.Fatalf("want empty, got %d", len(channels))
	}
}

func TestMigrateUpIsIdempotentOnReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "b.db")
	s1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s1.CreateChannel("c", "/r", "", "", "", false); err != nil {
		t.Fatal(err)
	}
	s1.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	channels, err := s2.ListChannels("", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 1 {
		t.Fatalf("want 1 channel, got %d", len(channels))
	}
}

func TestMigrateUpAppliesInAscendingOrder(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations/00001_one.sql": &fstest.MapFile{Data: []byte("-- +goose Up\nCREATE TABLE IF NOT EXISTS one(id INTEGER);\n-- +goose Down\nDROP TABLE one;\n")},
		"migrations/00002_two.sql": &fstest.MapFile{Data: []byte("-- +goose Up\nCREATE TABLE IF NOT EXISTS two(id INTEGER);\n-- +goose Down\nDROP TABLE two;\n")},
		"migrations/00003_three.sql": &fstest.MapFile{Data: []byte("-- +goose Up\nCREATE TABLE IF NOT EXISTS three(id INTEGER);\n-- +goose Down\nDROP TABLE three;\n")},
	}
	path := filepath.Join(t.TempDir(), "c.db")
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	if err := migrateUp(conn, fsys); err != nil {
		t.Fatal(err)
	}
	var v int64
	if err := conn.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != 3 {
		t.Fatalf("version = %d, want 3", v)
	}
	for _, tbl := range []string{"one", "two", "three"} {
		var n int
		if err := conn.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("table %s missing", tbl)
		}
	}
}

func TestMigrateUpLeavesVersionUnchangedOnFailure(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations/00001_ok.sql":   &fstest.MapFile{Data: []byte("-- +goose Up\nCREATE TABLE IF NOT EXISTS ok(id INTEGER);\n")},
		"migrations/00002_bad.sql":  &fstest.MapFile{Data: []byte("-- +goose Up\nCREATE TABLE IF NOT EXISTS ok(id INTEGER);\nTHIS IS NOT SQL;\n")},
	}
	path := filepath.Join(t.TempDir(), "d.db")
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	if err := migrateUp(conn, fsys); err == nil {
		t.Fatal("want error from malformed migration")
	}
	var v int64
	if err := conn.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != 1 {
		t.Fatalf("version = %d, want 1 (failed migration must not record)", v)
	}
}

func TestMigrateUpAdoptsCurrentShapeWithoutDataLoss(t *testing.T) {
	path := filepath.Join(t.TempDir(), "e.db")
	s1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s1.CreateChannel("keepme", "/r", "", "", "", false); err != nil {
		t.Fatal(err)
	}
	s1.Close()
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`DROP TABLE schema_migrations`); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	channels, err := s2.ListChannels("", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 1 || channels[0].Name != "keepme" {
		t.Fatalf("adoption lost data: %+v", channels)
	}
}

func TestLoadMigrationsSortsByParsedVersion(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations/00010_ten.sql":  &fstest.MapFile{Data: []byte("-- +goose Up\nSELECT 10;\n-- +goose Down\nSELECT 10;\n")},
		"migrations/00002_two.sql":  &fstest.MapFile{Data: []byte("-- +goose Up\nSELECT 2;\n-- +goose Down\nSELECT 2;\n")},
		"migrations/00009_nine.sql": &fstest.MapFile{Data: []byte("-- +goose Up\nSELECT 9;\n-- +goose Down\nSELECT 9;\n")},
	}
	ms, err := loadMigrations(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 3 {
		t.Fatalf("got %d migrations, want 3", len(ms))
	}
	if ms[0].version != 2 || ms[1].version != 9 || ms[2].version != 10 {
		t.Fatalf("wrong order: %d %d %d", ms[0].version, ms[1].version, ms[2].version)
	}
}

func TestLoadMigrationsStripsDownSection(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations/00001_x.sql": &fstest.MapFile{Data: []byte("-- +goose Up\nCREATE TABLE t(id INTEGER);\n-- +goose Down\nDROP TABLE t;\n")},
	}
	ms, err := loadMigrations(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 {
		t.Fatalf("got %d, want 1", len(ms))
	}
	if strings.Contains(ms[0].up, "DROP TABLE") {
		t.Fatalf("down section leaked into up: %q", ms[0].up)
	}
	if !strings.Contains(ms[0].up, "CREATE TABLE t") {
		t.Fatalf("up section missing: %q", ms[0].up)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/store/ -run 'TestMigrate|TestLoadMigrations' -v`
Expected: FAIL to compile — `undefined: migrateUp`, `undefined: loadMigrations`.

- [ ] **Step 4: Write the runner**

Create `internal/store/migrate.go`:

```go
package store

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

const schemaMigrationsDDL = `CREATE TABLE IF NOT EXISTS schema_migrations(
  version    INTEGER PRIMARY KEY,
  name       TEXT NOT NULL,
  applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
)`

type migration struct {
	version int64
	name    string
	up      string
}

var errMigrationConflict = errors.New("migration conflict")

func loadMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, "migrations")
	if err != nil {
		return nil, err
	}
	out := make([]migration, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		version, err := parseMigrationVersion(e.Name())
		if err != nil {
			return nil, err
		}
		raw, err := fs.ReadFile(fsys, "migrations/"+e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, migration{
			version: version,
			name:    e.Name(),
			up:      upSection(string(raw)),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	for i := 1; i < len(out); i++ {
		if out[i].version == out[i-1].version {
			return nil, fmt.Errorf("%w: duplicate version %d (%s, %s)", errMigrationConflict, out[i].version, out[i-1].name, out[i].name)
		}
	}
	return out, nil
}

func parseMigrationVersion(name string) (int64, error) {
	base, _, ok := strings.Cut(name, "_")
	if !ok {
		return 0, fmt.Errorf("migration %q: want NNNNN_name.sql", name)
	}
	v, err := strconv.ParseInt(base, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("migration %q: bad version prefix: %w", name, err)
	}
	return v, nil
}

func upSection(content string) string {
	up := content
	if i := strings.Index(content, "-- +goose Down"); i >= 0 {
		up = content[:i]
	}
	if i := strings.Index(up, "-- +goose Up"); i >= 0 {
		up = up[i+len("-- +goose Up"):]
	}
	return up
}

func migrateUp(conn *sql.DB, fsys fs.FS) error {
	if _, err := conn.Exec(schemaMigrationsDDL); err != nil {
		return err
	}
	ms, err := loadMigrations(fsys)
	if err != nil {
		return err
	}
	var current int64
	if err := conn.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return err
	}
	for _, m := range ms {
		if m.version <= current {
			continue
		}
		if err := applyMigration(conn, m); err != nil {
			return fmt.Errorf("migration %s: %w", m.name, err)
		}
	}
	return nil
}

func applyMigration(conn *sql.DB, m migration) error {
	tx, err := conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(m.up); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations(version, name) VALUES(?, ?)`, m.version, m.name); err != nil {
		return err
	}
	return tx.Commit()
}

func legacyRebuildRequired(conn *sql.DB) (bool, error) {
	present, err := tableExists(conn, "messages")
	if err != nil || !present {
		return false, err
	}
	return !hasColumnChecked(conn, "messages", "parent_id"), nil
}

func tableExists(conn *sql.DB, table string) (bool, error) {
	var n int
	err := conn.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n)
	return n > 0, err
}

func hasColumnChecked(conn *sql.DB, table, column string) bool {
	ok, err := hasColumn(conn, table, column)
	return err == nil && ok
}

func dropLegacyTables(conn *sql.DB) error {
	for _, tbl := range []string{"reactions", "messages", "threads", "channels"} {
		if _, err := conn.Exec(`DROP TABLE IF EXISTS ` + tbl); err != nil {
			return err
		}
	}
	return nil
}

func hasColumn(conn *sql.DB, table, column string) (bool, error) {
	rows, err := conn.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var dfltValue sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dfltValue, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func dropArchivedAtColumns(conn *sql.DB) error {
	for _, table := range []string{"channels", "threads"} {
		present, err := hasColumn(conn, table, columnArchivedAt)
		if err != nil {
			return err
		}
		if !present {
			continue
		}
		if _, err := conn.Exec(`ALTER TABLE ` + table + ` DROP COLUMN ` + columnArchivedAt); err != nil {
			return err
		}
	}
	return nil
}

const columnArchivedAt = "archived_at"
```

- [ ] **Step 5: Rewrite `Open`**

In `internal/store/store.go`, delete the `schema` constant (lines 21-95) and replace `Open`, `hasColumn`, `dropArchivedAtColumns`, `applySchema`, and `migrate` with:

```go
func Open(path string) (*Store, error) {
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	conn.SetMaxOpenConns(1)
	if err := prepare(conn); err != nil {
		conn.Close()
		return nil, err
	}
	return &Store{db: conn}, nil
}

func prepare(conn *sql.DB) error {
	legacy, err := legacyRebuildRequired(conn)
	if err != nil {
		return err
	}
	if legacy {
		if err := dropLegacyTables(conn); err != nil {
			return err
		}
	}
	if err := migrateUp(conn, migrationFS); err != nil {
		return err
	}
	return dropArchivedAtColumns(conn)
}
```

- [ ] **Step 6: Run the store tests**

Run: `go test ./internal/store/ -v`
Expected: PASS, including the pre-existing `TestFileStoreMigrationRebuildsLegacySchemaWithoutParentColumn` and `TestFileStoreDropsArchivedAtColumnsAndKeepsRows`, both unmodified.

- [ ] **Step 7: Run the full suite**

Run: `make test`
Expected: all packages pass, `gofmt -l .` empty, `go vet ./...` clean.

- [ ] **Step 8: Commit**

```bash
git add internal/store/migrations internal/store/migrate.go internal/store/migrate_test.go internal/store/store.go
git commit -m "refactor(store): replace schema probe with versioned embedded migrations"
```

---

## Task 3: Add error classification and remove the hand-rolled validators

Replaces `strings.Contains(err.Error(), "UNIQUE")` with exact SQLite extended codes, and deletes the two Go validators that existed only because `CHECK` violations could not be classified. This is the one task that updates existing test assertions, because validator removal changes `ErrInvalid` message text.

**Files:**
- Create: `internal/store/classify.go`, `internal/store/classify_test.go`
- Modify: `internal/store/store.go:456-467` (delete `validateMessage`), `:619-627` (delete `validateReaction`), `:214-218`, `:686-689` (use `classify`)
- Modify: `internal/store/session.go:100-103` (use `classify`)

**Interfaces:**
- Consumes: nothing
- Produces: `func classify(err error) error`

- [ ] **Step 1: Write the failing classify test**

Create `internal/store/classify_test.go`. The FK subtest is skipped here and un-skipped in task 10, because `PRAGMA foreign_keys=ON` does not land until then:

```go
package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestClassifyPassesThroughNonSQLiteError(t *testing.T) {
	sentinel := errors.New("boom")
	if got := classify(sentinel); !errors.Is(got, sentinel) {
		t.Fatalf("got %v, want the original error", got)
	}
}

func TestClassifyPassesThroughUnrelatedSQLError(t *testing.T) {
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	_, err = conn.Exec(`SELECT * FROM nonexistent_table`)
	if err == nil {
		t.Fatal("expected an error")
	}
	got := classify(err)
	if errors.Is(got, ErrNotFound) || errors.Is(got, ErrConflict) || errors.Is(got, ErrInvalid) {
		t.Fatalf("unexpectedly classified as a sentinel: %v", got)
	}
}

func TestClassifyConstraintCodes(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.CreateChannel("c", "/r", "", "", "", false); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		query string
		want  error
	}{
		{"unique", `INSERT INTO channels(name, repo_abs_path, is_orphaned) VALUES('c', '/r', 0)`, ErrConflict},
		{"check", `INSERT INTO channels(name, repo_abs_path, is_orphaned) VALUES('   ', '/r', 0)`, ErrInvalid},
		{"notnull", `INSERT INTO channels(name, repo_abs_path, is_orphaned) VALUES('x', NULL, 0)`, ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.db.Exec(tc.query)
			if err == nil {
				t.Fatal("expected a constraint violation")
			}
			if got := classify(err); !errors.Is(got, tc.want) {
				t.Fatalf("classify(%v) = %v, want %v", err, got, tc.want)
			}
		})
	}
}

func TestClassifyForeignKeyViolation(t *testing.T) {
	t.Skip("PRAGMA foreign_keys=ON lands in task 10; un-skip this test there")
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/store/ -run TestClassify -v`
Expected: FAIL to compile — `undefined: classify`.

- [ ] **Step 3: Write classify.go**

```go
package store

import (
	"errors"

	"modernc.org/sqlite"
	"modernc.org/sqlite/lib"
)

func classify(err error) error {
	if err == nil {
		return nil
	}
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return err
	}
	switch se.Code() {
	case lib.SQLITE_CONSTRAINT_UNIQUE:
		return ErrConflict
	case lib.SQLITE_CONSTRAINT_FOREIGNKEY:
		return ErrNotFound
	case lib.SQLITE_CONSTRAINT_CHECK, lib.SQLITE_CONSTRAINT_NOTNULL:
		return ErrInvalid
	}
	return err
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/store/ -run TestClassify -v`
Expected: PASS for both pass-through cases and the `unique`, `check`, and `notnull` subtests. `TestClassifyForeignKeyViolation` reports SKIP.

- [ ] **Step 5: Wire classify into the three substring sites**

In `store.go` `CreateChannel`, replace:

```go
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return 0, ErrConflict
		}
		return 0, err
	}
```

with:

```go
	if err != nil {
		return 0, classify(err)
	}
```

In `store.go` `addReactionTx`, replace the equivalent block ending in `return 0, 0, err` with `return 0, 0, classify(err)`. In `session.go` `CreateSession`, replace its block with `return 0, classify(err)`.

- [ ] **Step 6: Run the store tests and note the failures**

Run: `go test ./internal/store/ 2>&1 | head -40`
Expected: failures whose messages assert the old validator text, for example `"name and content required"` or `"emoji and name required"`. Record each failing assertion; these are the tests task step 7 updates.

- [ ] **Step 7: Delete the validators and update the affected assertions**

Delete `validateMessage` (`store.go:456-467`) and `validateReaction` (`store.go:619-627`). Remove their call sites in `appendMessageTx`, `addReactionTx`, `addReactionBySeqTx`, `AddReaction`, and `validateAppendEvent`.

For each failure recorded in step 6, change the assertion to match the constraint-derived message. A `CHECK` or `NOT NULL` violation now surfaces as `ErrInvalid` wrapping the driver message, so assert on the sentinel rather than exact text:

```go
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("got %v, want ErrInvalid", err)
	}
```

Replace exact-string assertions such as `err.Error() != "invalid: name and content required"` with this sentinel check. Where a test asserted a specific substring, assert the substring that the driver reliably produces — for a `CHECK(length(trim(name)) > 0)` violation that is `CHECK constraint failed`.

- [ ] **Step 8: Run the full suite**

Run: `make test`
Expected: all packages pass.

- [ ] **Step 9: Confirm the sentinels still reach the HTTP layer**

Run: `go test ./internal/apiserver/ -v 2>&1 | tail -20`
Expected: PASS. `writeThreadMutationError` maps `ErrNotFound` to 404, `ErrConflict` to 409, `ErrInvalid` to 400, and its behavior is unchanged because the sentinels are unchanged.

- [ ] **Step 10: Commit**

```bash
git add internal/store/classify.go internal/store/classify_test.go internal/store/store.go internal/store/session.go internal/store/store_test.go internal/store/session_test.go
git commit -m "refactor(store): classify SQLite constraint codes, drop duplicate validators"
```

---

## Task 4: Add sqlc config, queries, and generate `internal/db`

Generates the typed query layer. At the end of this task `internal/db` exists and compiles but nothing calls it yet, so the suite is unaffected.

**Files:**
- Create: `sqlc.yaml`
- Create: `internal/store/queries/*.sql` (6 files, 35 queries)
- Create (generated): `internal/db/`

**Interfaces:**
- Consumes: `internal/store/migrations/00001_initial_schema.sql` from task 2
- Produces: package `db` at `github.com/NoRaincheck/fluffle/internal/db` with `db.New(DBTX) *Queries`, `Queries.WithTx(*sql.Tx) *Queries`, models `Channel`, `Thread`, `Message`, `Reaction`, `AgentSession`, `AgentSessionEvent`, and the 35 named query methods listed in the Query Inventory, each taking `context.Context` first, plus an `XParams` struct per parameterized query.

- [ ] **Step 1: Write sqlc.yaml**

```yaml
version: "2"

rules:
  - name: append-only
    message: "agents are append-only: DELETE is not permitted in query files"
    rule: |
      !query.sql.contains("DELETE")
  - name: no-pragma
    message: "PRAGMA belongs in migrate.go, not in query files"
    rule: |
      !query.sql.contains("PRAGMA")

sql:
  - engine: "sqlite"
    name: "store"
    schema: "internal/store/migrations"
    queries: "internal/store/queries"
    rules:
      - append-only
      - no-pragma
    gen:
      go:
        package: "db"
        out: "internal/db"
        sql_package: "database/sql"
        emit_json_tags: false
        emit_pointers_for_null_types: true
        emit_empty_slices: false
        emit_exact_table_names: false
        emit_interface: true
        query_parameter_limit: 0
        rename:
          repo_head_sha: "RepoHeadSHA"
        overrides:
          - column: "messages.parent_id"
            go_type:
              import: "database/sql"
              type: "NullInt64"
```

Both `rules` blocks are required. The top-level block defines the CEL rules; the per-package list enables them by name. Omitting the per-package list silently disables both.

- [ ] **Step 2: Write queries/channels.sql**

```sql
-- name: CreateChannel :execlastid
INSERT INTO channels(name, repo_abs_path, repo_remote, repo_head_sha, repo_head_branch, is_orphaned)
VALUES(?, ?, ?, ?, ?, ?);

-- name: ListChannelsAll :many
SELECT id, name,
       COALESCE(repo_abs_path, '') AS repo_abs_path,
       COALESCE(repo_remote, '') AS repo_remote,
       COALESCE(repo_head_sha, '') AS repo_head_sha,
       COALESCE(repo_head_branch, '') AS repo_head_branch,
       is_orphaned,
       COALESCE(created_at, '') AS created_at
FROM channels
ORDER BY id;

-- name: ListChannelsByRepo :many
SELECT id, name,
       COALESCE(repo_abs_path, '') AS repo_abs_path,
       COALESCE(repo_remote, '') AS repo_remote,
       COALESCE(repo_head_sha, '') AS repo_head_sha,
       COALESCE(repo_head_branch, '') AS repo_head_branch,
       is_orphaned,
       COALESCE(created_at, '') AS created_at
FROM channels
WHERE repo_abs_path = ?
ORDER BY id;

-- name: ListChannelsNonOrphaned :many
SELECT id, name,
       COALESCE(repo_abs_path, '') AS repo_abs_path,
       COALESCE(repo_remote, '') AS repo_remote,
       COALESCE(repo_head_sha, '') AS repo_head_sha,
       COALESCE(repo_head_branch, '') AS repo_head_branch,
       is_orphaned,
       COALESCE(created_at, '') AS created_at
FROM channels
WHERE is_orphaned = 0
ORDER BY id;

-- name: ListChannelsByRepoNonOrphaned :many
SELECT id, name,
       COALESCE(repo_abs_path, '') AS repo_abs_path,
       COALESCE(repo_remote, '') AS repo_remote,
       COALESCE(repo_head_sha, '') AS repo_head_sha,
       COALESCE(repo_head_branch, '') AS repo_head_branch,
       is_orphaned,
       COALESCE(created_at, '') AS created_at
FROM channels
WHERE repo_abs_path = ? AND is_orphaned = 0
ORDER BY id;

-- name: CountChannelsByID :one
SELECT COUNT(*) FROM channels WHERE id = ?;
```

The `AS` aliases are required. Without them sqlc derives column names from the expression text and will not match the `rename` entry for `repo_head_sha`.

- [ ] **Step 3: Write queries/threads.sql**

```sql
-- name: CreateThread :execlastid
INSERT INTO threads(channel_id, title) VALUES(?, ?);

-- name: ListThreads :many
SELECT id, channel_id, title, COALESCE(created_at, '') AS created_at
FROM threads
WHERE channel_id = ?
ORDER BY id;

-- name: CountThreadsByID :one
SELECT COUNT(*) FROM threads WHERE id = ?;
```

- [ ] **Step 4: Write queries/messages.sql**

```sql
-- name: InsertMessage :execlastid
INSERT INTO messages(thread_id, seq, parent_id, name, author_type, role, content, created_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?);

-- name: InsertMessageDefaultCreatedAt :execlastid
INSERT INTO messages(thread_id, seq, parent_id, name, author_type, role, content)
VALUES(?, ?, ?, ?, ?, ?, ?);

-- name: GetMessageMaxSeq :one
SELECT MAX(seq) FROM messages WHERE thread_id = ?;

-- name: GetMessageThreadIDByID :one
SELECT thread_id FROM messages WHERE id = ?;

-- name: GetMessageIDByThreadSeq :one
SELECT id FROM messages WHERE thread_id = ? AND seq = ?;

-- name: ListMessages :many
SELECT m.id, m.thread_id, m.seq, m.parent_id, p.seq AS parent_seq, m.name, m.author_type, m.role, m.content,
       COALESCE(m.created_at, '') AS created_at
FROM messages m
LEFT JOIN messages p ON p.id = m.parent_id
WHERE m.thread_id = ?
ORDER BY m.seq ASC;

-- name: ListMessagesLastN :many
SELECT * FROM (
  SELECT * FROM (
    SELECT m.id, m.thread_id, m.seq, m.parent_id, p.seq AS parent_seq, m.name, m.author_type, m.role, m.content,
           COALESCE(m.created_at, '') AS created_at
    FROM messages m
    LEFT JOIN messages p ON p.id = m.parent_id
    WHERE m.thread_id = ?
    ORDER BY m.seq ASC
  ) ORDER BY seq DESC LIMIT ?
) ORDER BY seq ASC;

-- name: ListMessagesAfter :many
SELECT m.id, m.thread_id, m.seq, m.parent_id, p.seq AS parent_seq, m.name, m.author_type, m.role, m.content,
       COALESCE(m.created_at, '') AS created_at
FROM messages m
LEFT JOIN messages p ON p.id = m.parent_id
WHERE m.thread_id = ? AND m.seq > ?
ORDER BY m.seq ASC;

-- name: ListInbox :many
SELECT m.id, m.thread_id, m.seq, m.parent_id, m.name, m.author_type, m.role, m.content,
       COALESCE(m.created_at, '') AS created_at,
       c.name AS channel_name, c.id AS channel_id, t.title AS thread_title
FROM messages m
JOIN threads t ON t.id = m.thread_id
JOIN channels c ON c.id = t.channel_id
ORDER BY m.created_at DESC, m.id DESC
LIMIT ?;

-- name: CountAgentMessagesAfter :one
SELECT COUNT(*) FROM messages
WHERE thread_id = ? AND name = ? AND author_type = 'agent' AND seq > ?;

-- name: GetMessageByIDWithParent :one
SELECT m.id, m.thread_id, m.seq, m.parent_id, p.seq AS parent_seq, m.name, m.author_type, m.role, m.content,
       COALESCE(m.created_at, '') AS created_at
FROM messages m
LEFT JOIN messages p ON p.id = m.parent_id
WHERE m.id = ?;
```

`ListInbox` keeps `ORDER BY ... DESC` because `store.go:577-579` reverses the slice in Go. The facade must keep that reversal.

- [ ] **Step 5: Write queries/reactions.sql**

```sql
-- name: InsertReaction :execlastid
INSERT INTO reactions(message_id, emoji, name, author_type, created_at)
VALUES(?, ?, ?, ?, ?);

-- name: InsertReactionDefaultCreatedAt :execlastid
INSERT INTO reactions(message_id, emoji, name, author_type)
VALUES(?, ?, ?, ?);

-- name: ListReactions :many
SELECT r.id, r.message_id, m.seq AS message_seq, r.emoji, r.name, r.author_type,
       COALESCE(r.created_at, '') AS created_at
FROM reactions r
JOIN messages m ON m.id = r.message_id
WHERE m.thread_id = ?
ORDER BY m.seq ASC, r.created_at ASC, r.id ASC;
```

- [ ] **Step 6: Write queries/agent_sessions.sql**

```sql
-- name: CreateSession :execlastid
INSERT INTO agent_sessions(thread_id, trigger_message_id, agent_name, status, reply_mode, command, cwd)
VALUES(?, ?, ?, ?, ?, ?, ?);

-- name: ListSessions :many
SELECT id, thread_id, trigger_message_id, agent_name, status, reply_mode, command,
       cwd, exit_code, error, reply_message_id, started_at, finished_at, created_at
FROM agent_sessions
WHERE thread_id = ?
ORDER BY id ASC;

-- name: GetSession :one
SELECT id, thread_id, trigger_message_id, agent_name, status, reply_mode, command,
       cwd, exit_code, error, reply_message_id, started_at, finished_at, created_at
FROM agent_sessions
WHERE id = ?;

-- name: MarkSessionRunning :execrows
UPDATE agent_sessions SET status = ?, started_at = ? WHERE id = ? AND status = ?;

-- name: SetSessionReply :execrows
UPDATE agent_sessions SET reply_message_id = ? WHERE id = ?;

-- name: FinishSession :execrows
UPDATE agent_sessions SET status = ?, exit_code = ?, error = ?, finished_at = ? WHERE id = ?;

-- name: CountSessionsByID :one
SELECT COUNT(*) FROM agent_sessions WHERE id = ?;

-- name: ReconcileSessions :execrows
UPDATE agent_sessions
SET status = ?, finished_at = ?, error = 'daemon restarted while ' || status
WHERE status IN (?, ?);

-- name: GetThreadContext :one
SELECT t.id AS thread_id, t.title AS thread_title, c.id AS channel_id, c.name AS channel_name, c.repo_abs_path
FROM threads t
JOIN channels c ON c.id = t.channel_id
WHERE t.id = ?;
```

- [ ] **Step 7: Write queries/agent_session_events.sql**

```sql
-- name: GetSessionEventMaxSeq :one
SELECT MAX(seq) FROM agent_session_events WHERE session_id = ?;

-- name: InsertSessionEvent :execlastid
INSERT INTO agent_session_events(session_id, seq, type, content)
VALUES(?, ?, ?, ?);

-- name: ListSessionEvents :many
SELECT id, session_id, seq, type, content, COALESCE(created_at, '') AS created_at
FROM agent_session_events
WHERE session_id = ?
ORDER BY seq ASC;
```

- [ ] **Step 8: Generate**

Run: `make generate`
Expected: no output. `internal/db/` contains `db.go`, `models.go`, `querier.go`, and one `.sql.go` per query file.

If sqlc errors on a query, fix the query. Do not work around it by hand-editing `internal/db`.

- [ ] **Step 9: Verify the generated models match the wire contract**

Run: `rg -n "json:" internal/db/`
Expected: **no matches.** Any tag here means `emit_json_tags` was not applied and the wire contract is at risk.

Run: `rg -n "type (Channel|Thread|Message|Reaction|AgentSession|AgentSessionEvent) struct" -A 16 internal/db/models.go`
Expected: `Channel`, `Thread`, `Message`, `Reaction`, `AgentSession`, `AgentSessionEvent` — singular, not plural. `Message.ParentID` is `sql.NullInt64`. `Channel.RepoHeadSHA` is spelled with the capital SHA. `AgentSession.Cwd` is `*string` and `AgentSession.ExitCode` is `*int64`.

- [ ] **Step 10: Verify the vet rules fire**

Run: `printf '\n-- name: DeleteAll :exec\nDELETE FROM channels;\n' >> internal/store/queries/channels.sql && make vet; git checkout internal/store/queries/channels.sql`
Expected: `sqlc vet` exits non-zero and reports `append-only` for each query in `channels.sql`. Then restore the file.

If it exits zero, the per-package `rules:` list is missing from `sqlc.yaml`.

- [ ] **Step 11: Verify the suite is still green**

Run: `make test`
Expected: all packages pass. Nothing calls `internal/db` yet, so no behavior changed.

- [ ] **Step 12: Commit**

```bash
git add sqlc.yaml internal/store/queries internal/db
git commit -m "feat(store): add sqlc config, 35 annotated queries, and generated db package"
```

---

## Task 5: Wire `Store` to `internal/db` and migrate the channels and threads queries

First entity migration. Adds the `q` field and the row-to-wire converters, then rewrites `CreateChannel`, `ListChannels`, `CreateThread`, and `ListThreads`. The `ListChannels` WHERE-builder becomes a four-way switch, removing string-concatenated SQL.

**Files:**
- Modify: `internal/store/store.go`

**Interfaces:**
- Consumes: package `db` from task 4; `classify` from task 3
- Produces:
  - `type Store struct { db *sql.DB; q *db.Queries }`
  - `func (s *Store) tx() (*sql.Tx, *db.Queries, error)`
  - `func channelFromFields(id int64, name, repoAbsPath, repoRemote, repoHeadSHA, repoHeadBranch string, isOrphaned int64, createdAt string) Channel`
  - `func channelsFromRows[T any](rows []T, f func(T) Channel) []Channel`
  - `func threadFromFields(id, channelID int64, title, createdAt string) Thread`

- [ ] **Step 1: Write the failing test for the four-way switch**

Append to `internal/store/store_test.go`:

```go
func TestListChannelsCombinations(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.CreateChannel("anchored", "/repo/a", "", "", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateChannel("anchored2", "/repo/b", "", "", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateChannel("orphan", "", "", "", "", true); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name             string
		repoAbsPath      string
		includeOrphaned  bool
		want             []string
	}{
		{"all", "", true, []string{"anchored", "anchored2", "orphan"}},
		{"by repo", "/repo/a", true, []string{"anchored"}},
		{"non orphaned", "", false, []string{"anchored", "anchored2"}},
		{"by repo non orphaned", "/repo/b", false, []string{"anchored2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.ListChannels(tc.repoAbsPath, tc.includeOrphaned)
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, c := range got {
				names = append(names, c.Name)
			}
			if len(names) != len(tc.want) {
				t.Fatalf("got %v, want %v", names, tc.want)
			}
			for i := range names {
				if names[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", names, tc.want)
				}
			}
		})
	}
}
```

- [ ] **Step 2: Run the test**

Run: `go test ./internal/store/ -run TestListChannelsCombinations -v`
Expected: PASS immediately if the current implementation is correct. This test pins existing behavior across all four branches before the rewrite, which is its purpose. If it fails, the current dynamic-WHERE behavior differs from the table above — reconcile the table with reality before continuing, and note the discrepancy.

- [ ] **Step 3: Add the `q` field and the tx helper**

In `store.go`, change the struct and add the helper:

```go
type Store struct {
	db *sql.DB
	q  *db.Queries
}

func (s *Store) tx() (*sql.Tx, *db.Queries, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, nil, err
	}
	return tx, s.q.WithTx(tx), nil
}
```

Add the import `"github.com/NoRaincheck/fluffle/internal/db"`.

In `Open`, after `prepare` succeeds, return `&Store{db: conn, q: db.New(conn)}`.

- [ ] **Step 4: Add the row converters**

sqlc emits **one row struct per query and never dedupes**, so the four `ListChannels*` queries produce four distinct types: `ListChannelsAllRow`, `ListChannelsByRepoRow`, `ListChannelsNonOrphanedRow`, `ListChannelsByRepoNonOrphanedRow`. A single shared converter taking scalars avoids depending on those names:

```go
func channelFromFields(id int64, name, repoAbsPath, repoRemote, repoHeadSHA, repoHeadBranch string, isOrphaned int64, createdAt string) Channel {
	return Channel{
		ID:             id,
		Name:           name,
		RepoAbsPath:    repoAbsPath,
		RepoRemote:     repoRemote,
		RepoHeadSHA:    repoHeadSHA,
		RepoHeadBranch: repoHeadBranch,
		IsOrphaned:     isOrphaned == 1,
		CreatedAt:      createdAt,
	}
}

func channelsFromRows[T any](rows []T, f func(T) Channel) []Channel {
	var out []Channel
	for _, r := range rows {
		out = append(out, f(r))
	}
	return out
}

func threadFromFields(id, channelID int64, title, createdAt string) Thread {
	return Thread{ID: id, ChannelID: channelID, Title: title, CreatedAt: createdAt}
}
```

`channelsFromRows` is a generic helper, not a generated type: it accepts any of the four row slices and returns the `nil`-when-empty slice the wire contract requires. In step 6, pass a closure per query:

```go
	rows, err = s.q.ListChannelsAll(ctx)
	// then
	return channelsFromRows(rows, func(r db.ListChannelsAllRow) Channel {
		return channelFromFields(r.ID, r.Name, r.RepoAbsPath, r.RepoRemote, r.RepoHeadSha, r.RepoHeadBranch, r.IsOrphaned, r.CreatedAt)
	}), nil
```

Field names inside the closure come from the generated struct. Confirm them with
`rg -n "type ListChannels\w+Row struct" -A 9 internal/db/channels.sql.go` before writing. The `COALESCE`-aliased columns yield plain `string`; `is_orphaned` yields `int64` because SQLite has no boolean.

- [ ] **Step 5: Rewrite CreateChannel**

First change `nullIfEmpty` to return `*string`. Its current `any`-returning form cannot be assigned to the generated `*string` params fields, so this change is required here rather than deferred:

```go
func nullIfEmpty(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
```

Then rewrite `CreateChannel`:

```go
func (s *Store) CreateChannel(name, repoAbsPath, repoRemote, repoHeadSHA, repoHeadBranch string, orphaned bool) (int64, error) {
	if strings.TrimSpace(name) == "" {
		return 0, invalid("channel name required")
	}
	var abs *string
	var isOrphan int64
	if orphaned {
		if repoAbsPath != "" {
			return 0, invalid("orphaned channel must not have repo path")
		}
		isOrphan = 1
	} else {
		if strings.TrimSpace(repoAbsPath) == "" {
			return 0, invalid("repo path required")
		}
		abs = &repoAbsPath
	}
	id, err := s.q.CreateChannel(context.Background(), db.CreateChannelParams{
		Name:           name,
		RepoAbsPath:    abs,
		RepoRemote:     nullIfEmpty(repoRemote),
		RepoHeadSha:    nullIfEmpty(repoHeadSHA),
		RepoHeadBranch: nullIfEmpty(repoHeadBranch),
		IsOrphaned:     isOrphan,
	})
	if err != nil {
		return 0, classify(err)
	}
	return id, nil
}
```

Add `"context"` and `"github.com/NoRaincheck/fluffle/internal/db"` to the imports.

- [ ] **Step 6: Rewrite ListChannels as a four-way switch**

`repo_abs_path` is a **nullable** column, so sqlc infers the comparison parameter as nullable too. With `emit_pointers_for_null_types: true` that is `*string`, so these two params take a pointer:

```go
func (s *Store) ListChannels(repoAbsPath string, includeOrphaned bool) ([]Channel, error) {
	ctx := context.Background()
	switch {
	case repoAbsPath != "" && !includeOrphaned:
		rows, err := s.q.ListChannelsByRepoNonOrphaned(ctx, db.ListChannelsByRepoNonOrphanedParams{RepoAbsPath: &repoAbsPath})
		if err != nil {
			return nil, err
		}
		return channelsFromRows(rows, func(r db.ListChannelsByRepoNonOrphanedRow) Channel {
			return channelFromFields(r.ID, r.Name, r.RepoAbsPath, r.RepoRemote, r.RepoHeadSha, r.RepoHeadBranch, r.IsOrphaned, r.CreatedAt)
		}), nil
	case repoAbsPath != "":
		rows, err := s.q.ListChannelsByRepo(ctx, db.ListChannelsByRepoParams{RepoAbsPath: &repoAbsPath})
		if err != nil {
			return nil, err
		}
		return channelsFromRows(rows, func(r db.ListChannelsByRepoRow) Channel {
			return channelFromFields(r.ID, r.Name, r.RepoAbsPath, r.RepoRemote, r.RepoHeadSha, r.RepoHeadBranch, r.IsOrphaned, r.CreatedAt)
		}), nil
	case !includeOrphaned:
		rows, err := s.q.ListChannelsNonOrphaned(ctx)
		if err != nil {
			return nil, err
		}
		return channelsFromRows(rows, func(r db.ListChannelsNonOrphanedRow) Channel {
			return channelFromFields(r.ID, r.Name, r.RepoAbsPath, r.RepoRemote, r.RepoHeadSha, r.RepoHeadBranch, r.IsOrphaned, r.CreatedAt)
		}), nil
	default:
		rows, err := s.q.ListChannelsAll(ctx)
		if err != nil {
			return nil, err
		}
		return channelsFromRows(rows, func(r db.ListChannelsAllRow) Channel {
			return channelFromFields(r.ID, r.Name, r.RepoAbsPath, r.RepoRemote, r.RepoHeadSha, r.RepoHeadBranch, r.IsOrphaned, r.CreatedAt)
		}), nil
	}
}
```

`channelsFromRows` returns nil for an empty slice, which is required: `ListChannels`
must marshal to `null`, not `[]`.

- [ ] **Step 7: Rewrite CreateThread and ListThreads**

```go
func (s *Store) CreateThread(channelID int64, title string) (int64, error) {
	if strings.TrimSpace(title) == "" {
		return 0, invalid("title required")
	}
	n, err := s.q.CountChannelsByID(context.Background(), channelID)
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, ErrNotFound
	}
	return s.q.CreateThread(context.Background(), db.CreateThreadParams{ChannelID: channelID, Title: title})
}

func (s *Store) ListThreads(channelID int64) ([]Thread, error) {
	rows, err := s.q.ListThreads(context.Background(), channelID)
	if err != nil {
		return nil, err
	}
	var out []Thread
	for _, r := range rows {
		out = append(out, threadFromFields(r.ID, r.ChannelID, r.Title, r.CreatedAt))
	}
	return out, nil
}
```

`CountChannelsByID` is still used here. It is removed in task 10, together with the FK pragma that replaces it.

- [ ] **Step 8: Build and test**

Run: `make test`
Expected: all packages pass. `ListChannels`, `CreateChannel`, `CreateThread`, `ListThreads` are covered by existing tests plus the new one from step 1.

- [ ] **Step 9: Confirm the wire contract is byte-identical**

Run: `go test ./internal/apiserver/ ./internal/tui/ -v 2>&1 | tail -20`
Expected: PASS. These exercise JSON encoding of `Channel` and `Thread`.

- [ ] **Step 10: Commit**

```bash
git add internal/store/store.go internal/store/store_test.go
git commit -m "refactor(store): route channel and thread queries through sqlc"
```

---

## Task 6: Migrate the messages queries and collapse the `*Tx` duplication

The highest-value task: removes four duplicated `*sql.DB`/`*sql.Tx` pairs and all string-concatenated SQL in `ListMessages`.

**Files:**
- Modify: `internal/store/store.go`

**Interfaces:**
- Consumes: `s.tx()` and `s.q` from task 5
- Produces: `func messageFromFields(id, threadID, seq int64, parentID, parentSeq sql.NullInt64, name, authorType, role, content, createdAt string) Message`, `func (s *Store) appendMessage(q *db.Queries, ...) (int64, int64, error)`

- [ ] **Step 1: Write the failing test for the lastN ordering**

Append to `store_test.go`. Add `"fmt"` to its import block first — it is not currently imported:

```go
func TestListMessagesLastNKeepsAscendingOrder(t *testing.T) {
	s, _, threadID := newStoreWithThread(t, ":memory:")
	for i := 0; i < 5; i++ {
		if _, err := s.AppendMessage(threadID, "human", "human", "user", fmt.Sprintf("m%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ListMessages(threadID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2", len(got))
	}
	if got[0].Seq >= got[1].Seq {
		t.Fatalf("not ascending: %d then %d", got[0].Seq, got[1].Seq)
	}
	if got[1].Seq != 5 {
		t.Fatalf("last seq = %d, want 5 (last 2 of 1..5)", got[1].Seq)
	}
}
```

- [ ] **Step 2: Run it**

Run: `go test ./internal/store/ -run TestListMessagesLastNKeepsAscendingOrder -v`
Expected: PASS. It pins the current triple-subselect behavior before the rewrite.

- [ ] **Step 3: Add the message converter**

Four queries project the same message columns — `ListMessages`, `ListMessagesLastN`,
`ListMessagesAfter`, `GetMessageByIDWithParent` — so sqlc emits four distinct row
types. Use a field-level constructor, as in task 5:

```go
func messageFromFields(id, threadID, seq int64, parentID sql.NullInt64, parentSeq sql.NullInt64, name, authorType, role, content, createdAt string) Message {
	return Message{
		ID:         id,
		ThreadID:   threadID,
		Seq:        seq,
		ParentID:   parentID,
		ParentSeq:  parentSeq,
		Name:       name,
		AuthorType: authorType,
		Role:       role,
		Content:    content,
		CreatedAt:  createdAt,
	}
}
```

`parent_id` is `sql.NullInt64` because of the `overrides` entry in `sqlc.yaml`.
`parent_seq` comes from `LEFT JOIN messages p` on a nullable column, so it is also
nullable — most likely `sql.NullInt64` as well. Confirm before writing:

Run: `rg -n "type ListMessagesRow struct" -A 12 internal/db/messages.sql.go`
Expected: `ParentID sql.NullInt64` and `ParentSeq sql.NullInt64`. If `ParentSeq` comes
out as `sql.NullInt64`, pass it straight through; if it comes out as `int64`, wrap it
with `sql.NullInt64{Int64: r.ParentSeq, Valid: true}` so the `json:"-"` field keeps
its type.

- [ ] **Step 4: Rewrite the four AppendMessage variants onto one helper**

Delete `appendMessageTx` and `appendMessageByParentSeqTx`. Replace with a single `*db.Queries`-taking helper plus thin transaction wrappers:

```go
func (s *Store) appendMessage(q *db.Queries, threadID int64, name, authorType, role, content, createdAt string, parentID int64) (int64, int64, error) {
	ctx := context.Background()
	n, err := q.CountThreadsByID(ctx, threadID)
	if err != nil {
		return 0, 0, err
	}
	if n == 0 {
		return 0, 0, ErrNotFound
	}
	maxSeq, err := q.GetMessageMaxSeq(ctx, threadID)
	if err != nil {
		return 0, 0, err
	}
	seq := int64(1)
	if maxSeq.Valid {
		seq = maxSeq.Int64 + 1
	}
	if parentID > 0 {
		parentThreadID, err := q.GetMessageThreadIDByID(ctx, parentID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return 0, 0, ErrNotFound
			}
			return 0, 0, err
		}
		if parentThreadID != threadID {
			return 0, 0, invalid("parent message not in this thread")
		}
	}
	var id int64
	if createdAt != "" {
		if _, err := time.Parse(time.RFC3339, createdAt); err != nil {
			return 0, 0, invalid("created_at %q is not RFC3339: %v", createdAt, err)
		}
		id, err = q.InsertMessage(ctx, db.InsertMessageParams{
			ThreadID:   threadID,
			Seq:        seq,
			ParentID:   nullIfInt64(parentID),
			Name:       name,
			AuthorType: authorType,
			Role:       role,
			Content:    content,
			CreatedAt:  createdAt,
		})
	} else {
		id, err = q.InsertMessageDefaultCreatedAt(ctx, db.InsertMessageDefaultCreatedAtParams{
			ThreadID:   threadID,
			Seq:        seq,
			ParentID:   nullIfInt64(parentID),
			Name:       name,
			AuthorType: authorType,
			Role:       role,
			Content:    content,
		})
	}
	if err != nil {
		return 0, 0, classify(err)
	}
	return seq, id, nil
}

func (s *Store) AppendMessageAtWithParent(threadID int64, name, authorType, role, content, createdAt string, parentID int64) (int64, error) {
	tx, q, err := s.tx()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	seq, _, err := s.appendMessage(q, threadID, name, authorType, role, content, createdAt, parentID)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return seq, nil
}

func (s *Store) AppendMessageByParentSeq(threadID, parentSeq int64, name, authorType, role, content, createdAt string) (int64, int64, error) {
	tx, q, err := s.tx()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	if parentSeq < 0 {
		return 0, 0, invalid("parent_seq must be non-negative")
	}
	var parentID int64
	if parentSeq > 0 {
		parentID, err = q.GetMessageIDByThreadSeq(context.Background(), db.GetMessageIDByThreadSeqParams{
			ThreadID: threadID,
			Seq:      parentSeq,
		})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return 0, 0, ErrNotFound
			}
			return 0, 0, err
		}
	}
	seq, id, err := s.appendMessage(q, threadID, name, authorType, role, content, createdAt, parentID)
	if err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return seq, id, nil
}
```

Keep `AppendMessage`, `AppendMessageWithParent`, and `AppendMessageAt` delegating to these two, unchanged.

Update `nullIfInt64` to return `*int64`:

```go
func nullIfInt64(v int64) *int64 {
	if v == 0 {
		return nil
	}
	return &v
}
```

Its old `any`-returning form will not satisfy the generated `ParentID *int64` field.

- [ ] **Step 5: Rewrite MessageIDBySeq, dropping the duplicate**

Delete `messageIDBySeqTx`. `MessageIDBySeq` becomes:

```go
func (s *Store) MessageIDBySeq(threadID, seq int64) (int64, error) {
	id, err := s.q.GetMessageIDByThreadSeq(context.Background(), db.GetMessageIDByThreadSeqParams{
		ThreadID: threadID,
		Seq:      seq,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}
```

Inside `AppendBatch` and `AppendSessionEvent`, replace `messageIDBySeqTx(tx, ...)` with `q.GetMessageIDByThreadSeq(context.Background(), ...)`.

- [ ] **Step 6: Rewrite ListMessages, ListMessagesAfter, and delete scanMessages**

```go
func (s *Store) ListMessages(threadID int64, lastN int) ([]Message, error) {
	ctx := context.Background()
	if lastN > 0 {
		rows, err := s.q.ListMessagesLastN(ctx, db.ListMessagesLastNParams{ThreadID: threadID, Limit: int64(lastN)})
		if err != nil {
			return nil, err
		}
		var out []Message
		for _, r := range rows {
			out = append(out, messageFromFields(r.ID, r.ThreadID, r.Seq, r.ParentID, r.ParentSeq, r.Name, r.AuthorType, r.Role, r.Content, r.CreatedAt))
		}
		return out, nil
	}
	rows, err := s.q.ListMessages(ctx, threadID)
	if err != nil {
		return nil, err
	}
	var out []Message
	for _, r := range rows {
		out = append(out, messageFromFields(r.ID, r.ThreadID, r.Seq, r.ParentID, r.ParentSeq, r.Name, r.AuthorType, r.Role, r.Content, r.CreatedAt))
	}
	return out, nil
}

func (s *Store) ListMessagesAfter(threadID, afterSeq int64) ([]Message, error) {
	rows, err := s.q.ListMessagesAfter(context.Background(), db.ListMessagesAfterParams{
		ThreadID: threadID,
		AfterSeq: afterSeq,
	})
	if err != nil {
		return nil, err
	}
	var out []Message
	for _, r := range rows {
		out = append(out, messageFromFields(r.ID, r.ThreadID, r.Seq, r.ParentID, r.ParentSeq, r.Name, r.AuthorType, r.Role, r.Content, r.CreatedAt))
	}
	return out, nil
}
```

Delete `scanMessages` (`store.go:529-539`). Check the generated `ListMessagesLastNParams` field names before writing; the `LIMIT ?` parameter may be named `Limit` or `Limit_2`.

- [ ] **Step 7: Replace `nextThreadSeqTx` with `nextThreadSeq`**

```go
func nextThreadSeq(q *db.Queries, threadID int64) (int64, error) {
	ctx := context.Background()
	n, err := q.CountThreadsByID(ctx, threadID)
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, ErrNotFound
	}
	maxSeq, err := q.GetMessageMaxSeq(ctx, threadID)
	if err != nil {
		return 0, err
	}
	if maxSeq.Valid {
		return maxSeq.Int64 + 1, nil
	}
	return 1, nil
}
```

- [ ] **Step 8: Build and test**

Run: `make test`
Expected: all packages pass, including the new `TestListMessagesLastNKeepsAscendingOrder`.

- [ ] **Step 9: Commit**

```bash
git add internal/store/store.go internal/store/store_test.go
git commit -m "refactor(store): route message queries through sqlc and drop tx duplication"
```

---

## Task 7: Migrate the reactions, inbox, and message-lookup queries

**Files:**
- Modify: `internal/store/store.go`, `internal/store/session.go`

**Interfaces:**
- Consumes: `s.tx()`, `s.q` from task 5
- Produces: `func reactionFromRow(r db.ListReactionsRow) Reaction`, `func inboxFromRow(r db.ListInboxRow) InboxMessage`, `func messageFromDetailRow(r db.GetMessageByIDWithParentRow) Message`

- [ ] **Step 1: Write the failing test for the inbox empty-slice contract**

Append to `store_test.go`:

```go
func TestListInboxReturnsEmptySliceNotNil(t *testing.T) {
	s, _, threadID := newStoreWithThread(t, ":memory:")
	if _, err := s.AppendMessage(threadID, "human", "human", "user", "hi"); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListInbox(10)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("ListInbox returned nil; must return an empty slice so JSON is [] not null")
	}
	if len(got) != 1 {
		t.Fatalf("got %d, want 1", len(got))
	}
}
```

- [ ] **Step 2: Run it**

Run: `go test ./internal/store/ -run TestListInboxReturnsEmptySliceNotNil -v`
Expected: PASS. It pins the existing non-nil contract.

- [ ] **Step 3: Add the converters**

```go
func reactionFromRow(r db.ListReactionsRow) Reaction {
	return Reaction{
		ID:         r.ID,
		MessageID:  r.MessageID,
		MessageSeq: r.MessageSeq,
		Emoji:      r.Emoji,
		Name:       r.Name,
		AuthorType: r.AuthorType,
		CreatedAt:  r.CreatedAt,
	}
}

func inboxFromRow(r db.ListInboxRow) InboxMessage {
	return InboxMessage{
		Message: Message{
			ID:         r.ID,
			ThreadID:   r.ThreadID,
			Seq:        r.Seq,
			ParentID:   r.ParentID,
			Name:       r.Name,
			AuthorType: r.AuthorType,
			Role:       r.Role,
			Content:    r.Content,
			CreatedAt:  r.CreatedAt,
		},
		ChannelName: r.ChannelName,
		ChannelID:   r.ChannelID,
		ThreadTitle: r.ThreadTitle,
	}
}
```

Use the generated field names verbatim; verify with `rg -n "type ListInboxRow" -A 14 internal/db/messages.sql.go`.

- [ ] **Step 4: Rewrite the reaction methods, dropping the duplicates**

Delete `addReactionTx`, `addReactionBySeqTx`, and `messageThreadIDTx`. Replace with:

```go
func (s *Store) addReaction(q *db.Queries, messageID int64, emoji, name, authorType, createdAt string) (int64, int64, error) {
	ctx := context.Background()
	var (
		reactionID int64
		err        error
	)
	if createdAt != "" {
		if _, err := time.Parse(time.RFC3339, createdAt); err != nil {
			return 0, 0, invalid("created_at %q is not RFC3339: %v", createdAt, err)
		}
		reactionID, err = q.InsertReaction(ctx, db.InsertReactionParams{
			MessageID:  messageID,
			Emoji:      emoji,
			Name:       name,
			AuthorType: authorType,
			CreatedAt:  createdAt,
		})
	} else {
		reactionID, err = q.InsertReactionDefaultCreatedAt(ctx, db.InsertReactionDefaultCreatedAtParams{
			MessageID:  messageID,
			Emoji:      emoji,
			Name:       name,
			AuthorType: authorType,
		})
	}
	if err != nil {
		return 0, 0, classify(err)
	}
	return messageID, reactionID, nil
}

func (s *Store) AddReaction(messageID int64, emoji, name, authorType string) error {
	tx, q, err := s.tx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := s.messageThreadID(q, messageID); err != nil {
		return err
	}
	if _, _, err := s.addReaction(q, messageID, emoji, name, authorType, ""); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) messageThreadID(q *db.Queries, messageID int64) (int64, error) {
	if messageID <= 0 {
		return 0, ErrNotFound
	}
	threadID, err := q.GetMessageThreadIDByID(context.Background(), messageID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return threadID, err
}

func (s *Store) AddReactionBySeq(threadID, messageSeq int64, emoji, name, authorType string) error {
	tx, q, err := s.tx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, _, err := s.addReactionBySeq(q, threadID, messageSeq, emoji, name, authorType, ""); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) addReactionBySeq(q *db.Queries, threadID, messageSeq int64, emoji, name, authorType, createdAt string) (int64, int64, error) {
	messageID, err := q.GetMessageIDByThreadSeq(context.Background(), db.GetMessageIDByThreadSeqParams{
		ThreadID: threadID,
		Seq:      messageSeq,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, 0, ErrNotFound
		}
		return 0, 0, err
	}
	return s.addReaction(q, messageID, emoji, name, authorType, createdAt)
}
```

- [ ] **Step 5: Rewrite ListReactions**

```go
func (s *Store) ListReactions(threadID int64) ([]Reaction, error) {
	rows, err := s.q.ListReactions(context.Background(), threadID)
	if err != nil {
		return nil, err
	}
	var out []Reaction
	for _, r := range rows {
		out = append(out, reactionFromRow(r))
	}
	return out, nil
}
```

`out` stays nil when empty, so `ListReactions` marshals to `null`.

- [ ] **Step 6: Rewrite ListInbox, keeping the Go-side reversal**

```go
func (s *Store) ListInbox(limit int) ([]InboxMessage, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := s.q.ListInbox(context.Background(), int64(limit))
	if err != nil {
		return nil, err
	}
	out := []InboxMessage{}
	for _, r := range rows {
		out = append(out, inboxFromRow(r))
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}
```

`out := []InboxMessage{}` and the reversal are both required. The SQL orders `DESC`; the facade reverses to `ASC`.

- [ ] **Step 7: Rewrite AppendBatch**

```go
func (s *Store) AppendBatch(threadID int64, events []AppendEvent) ([]AppendResult, error) {
	tx, q, err := s.tx()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for _, event := range events {
		if err := validateAppendEvent(event); err != nil {
			return nil, err
		}
	}
	nextSeq, err := nextThreadSeq(q, threadID)
	if err != nil {
		return nil, err
	}
	sourceSeqs, err := buildSourceSequenceMap(events, nextSeq)
	if err != nil {
		return nil, err
	}
	results := make([]AppendResult, 0, len(events))
	for _, event := range events {
		switch event.Type {
		case "message":
			parentSeq := event.ParentSeq
			if mapped, ok := sourceSeqs[parentSeq]; ok {
				parentSeq = mapped
			}
			var parentID int64
			if parentSeq > 0 {
				parentID, err = q.GetMessageIDByThreadSeq(context.Background(), db.GetMessageIDByThreadSeqParams{
					ThreadID: threadID,
					Seq:      parentSeq,
				})
				if err != nil {
					if errors.Is(err, sql.ErrNoRows) {
						return nil, ErrNotFound
					}
					return nil, err
				}
			}
			seq, messageID, err := s.appendMessage(q, threadID, event.Name, event.AuthorType, event.Role, event.Content, event.CreatedAt, parentID)
			if err != nil {
				return nil, err
			}
			results = append(results, AppendResult{SourceSeq: event.SourceSeq, Seq: seq, MessageID: messageID})
		case "reaction":
			messageSeq := event.MessageSeq
			if mapped, ok := sourceSeqs[messageSeq]; ok {
				messageSeq = mapped
			}
			messageID, reactionID, err := s.addReactionBySeq(q, threadID, messageSeq, event.Emoji, event.Name, event.AuthorType, event.CreatedAt)
			if err != nil {
				return nil, err
			}
			results = append(results, AppendResult{SourceSeq: event.SourceSeq, MessageID: messageID, ReactionID: reactionID})
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return results, nil
}
```

- [ ] **Step 8: Rewrite MessageByID, CountAgentMessagesAfter, and ThreadContext in session.go**

```go
func (s *Store) MessageByID(id int64) (Message, error) {
	r, err := s.q.GetMessageByIDWithParent(context.Background(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	if err != nil {
		return Message{}, err
	}
	return messageFromDetailRow(r), nil
}

func (s *Store) CountAgentMessagesAfter(threadID int64, name string, afterSeq int64) (int, error) {
	n, err := s.q.CountAgentMessagesAfter(context.Background(), db.CountAgentMessagesAfterParams{
		ThreadID: threadID,
		Name:     name,
		AfterSeq: afterSeq,
	})
	return int(n), err
}

func (s *Store) ThreadContext(threadID int64) (ThreadContext, error) {
	r, err := s.q.GetThreadContext(context.Background(), threadID)
	if errors.Is(err, sql.ErrNoRows) {
		return ThreadContext{}, ErrNotFound
	}
	if err != nil {
		return ThreadContext{}, err
	}
	return ThreadContext{
		ThreadID:    r.ThreadID,
		ThreadTitle: r.ThreadTitle,
		ChannelID:   r.ChannelID,
		ChannelName: r.ChannelName,
		RepoAbsPath: r.RepoAbsPath,
	}, nil
}
```

Note `GetThreadContext.repo_abs_path` is a nullable column and therefore generates as `*string` with no `COALESCE`, which is exactly what `ThreadContext.RepoAbsPath` needs. Do not add a `COALESCE` to that query.

- [ ] **Step 9: Build and test**

Run: `make test`
Expected: all packages pass.

- [ ] **Step 10: Commit**

```bash
git add internal/store/store.go internal/store/session.go internal/store/store_test.go
git commit -m "refactor(store): route reaction, inbox, and lookup queries through sqlc"
```

---

## Task 8: Migrate the agent session queries and delete `execSessionUpdate`

**Files:**
- Modify: `internal/store/session.go`

**Interfaces:**
- Consumes: `s.tx()`, `s.q` from task 5
- Produces: `func sessionFromRow(r db.AgentSession) Session` (or the generated row type, whichever sqlc emits)

- [ ] **Step 1: Write the failing test for the zero-rows-affected contract**

Append to `internal/store/session_test.go`:

```go
func TestMarkSessionRunningUnknownIDIsNotFound(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.MarkSessionRunning(9999, "2026-01-01T00:00:00Z"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}
```

- [ ] **Step 2: Run it**

Run: `go test ./internal/store/ -run TestMarkSessionRunningUnknownIDIsNotFound -v`
Expected: PASS. It pins the `RowsAffected() == 0` behavior that `execSessionUpdate` implements.

- [ ] **Step 3: Delete `sessionColumns`, `scanner`, and `scanSession`**

All three become unnecessary: sqlc generates the scan. Verify no remaining references with `rg -n "sessionColumns|scanSession|scanner" internal/` before continuing.

- [ ] **Step 4: Rewrite CreateSession**

```go
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
	ctx := context.Background()
	msgThread, err := s.q.GetMessageThreadIDByID(ctx, triggerMessageID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	if msgThread != threadID {
		return 0, invalid("trigger message not in this thread")
	}
	id, err := s.q.CreateSession(ctx, db.CreateSessionParams{
		ThreadID:         threadID,
		TriggerMessageID: triggerMessageID,
		AgentName:        agentName,
		Status:           status,
		ReplyMode:        replyMode,
		Command:          command,
		Cwd:              nullIfEmptyPtr(cwd),
	})
	if err != nil {
		return 0, classify(err)
	}
	return id, nil
}
```

The status and reply-mode `switch` validations are **retained**. They produce specific user-facing text that a bare constraint error would degrade. Update `nullIfEmptyPtr` to return `*string`:

```go
func nullIfEmptyPtr(v *string) *string {
	if v == nil || *v == "" {
		return nil
	}
	return v
}
```

- [ ] **Step 5: Rewrite ListSessions and GetSession**

```go
func (s *Store) ListSessions(threadID int64) ([]Session, error) {
	rows, err := s.q.ListSessions(context.Background(), threadID)
	if err != nil {
		return nil, err
	}
	out := []Session{}
	for _, r := range rows {
		out = append(out, sessionFromRow(r))
	}
	return out, nil
}

func (s *Store) GetSession(id int64) (Session, error) {
	r, err := s.q.GetSession(context.Background(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, err
	}
	return sessionFromRow(r), nil
}

func sessionFromRow(r db.AgentSession) Session {
	return Session{
		ID:               r.ID,
		ThreadID:         r.ThreadID,
		TriggerMessageID: r.TriggerMessageID,
		AgentName:        r.AgentName,
		Status:           r.Status,
		ReplyMode:        r.ReplyMode,
		Command:          r.Command,
		Cwd:              r.Cwd,
		ExitCode:         r.ExitCode,
		Error:            r.Error,
		ReplyMessageID:   r.ReplyMessageID,
		StartedAt:        r.StartedAt,
		FinishedAt:       r.FinishedAt,
		CreatedAt:        r.CreatedAt,
	}
}
```

`ListSessions` keeps `out := []Session{}` so it marshals to `[]`, not `null`. Check the generated return type of `ListSessions` and `GetSession`; sqlc may emit a dedicated `ListSessionsRow` rather than `db.AgentSession` when the column list is explicit. Use whichever it emits.

- [ ] **Step 6: Replace execSessionUpdate with :execrows queries**

Delete `execSessionUpdate` and rewrite the three callers:

```go
func (s *Store) MarkSessionRunning(id int64, startedAt string) error {
	n, err := s.q.MarkSessionRunning(context.Background(), db.MarkSessionRunningParams{
		Status:    SessionRunning,
		StartedAt: startedAt,
		ID:        id,
		Status_2:  SessionQueued,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetSessionReply(id int64, replyMessageID int64) error {
	n, err := s.q.SetSessionReply(context.Background(), db.SetSessionReplyParams{
		ReplyMessageID: replyMessageID,
		ID:             id,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) FinishSession(id int64, status string, exitCode *int64, errMsg *string, finishedAt string) error {
	switch status {
	case SessionSucceeded, SessionFailed, SessionCanceled:
	default:
		return invalid("bad terminal session status %q", status)
	}
	n, err := s.q.FinishSession(context.Background(), db.FinishSessionParams{
		Status:     status,
		ExitCode:   exitCode,
		Error:      nullIfEmptyPtr(errMsg),
		FinishedAt: finishedAt,
		ID:         id,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
```

`MarkSessionRunning`'s SQL uses `status` twice (`SET status = ?` and `AND status = ?`), so sqlc disambiguates the fields as `Status` and `Status_2`. Confirm against `rg -n "type MarkSessionRunningParams" -A 6 internal/db/agent_sessions.sql.go`.

- [ ] **Step 7: Rewrite AppendSessionEvent and ListSessionEvents**

```go
func (s *Store) AppendSessionEvent(sessionID int64, eventType, content string) (int64, int64, error) {
	switch eventType {
	case SessionEventPrompt, SessionEventStdout, SessionEventStderr, SessionEventExit, SessionEventError:
	default:
		return 0, 0, invalid("bad session event type %q", eventType)
	}
	tx, q, err := s.tx()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	ctx := context.Background()
	exists, err := q.CountSessionsByID(ctx, sessionID)
	if err != nil {
		return 0, 0, err
	}
	if exists == 0 {
		return 0, 0, ErrNotFound
	}
	maxSeq, err := q.GetSessionEventMaxSeq(ctx, sessionID)
	if err != nil {
		return 0, 0, err
	}
	seq := int64(1)
	if maxSeq.Valid {
		seq = maxSeq.Int64 + 1
	}
	id, err := q.InsertSessionEvent(ctx, db.InsertSessionEventParams{
		SessionID: sessionID,
		Seq:       seq,
		Type:      eventType,
		Content:   content,
	})
	if err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return seq, id, nil
}

func (s *Store) ListSessionEvents(sessionID int64) ([]SessionEvent, error) {
	rows, err := s.q.ListSessionEvents(context.Background(), sessionID)
	if err != nil {
		return nil, err
	}
	out := []SessionEvent{}
	for _, r := range rows {
		out = append(out, SessionEvent{
			ID:        r.ID,
			SessionID: r.SessionID,
			Seq:       r.Seq,
			Type:      r.Type,
			Content:   r.Content,
			CreatedAt: r.CreatedAt,
		})
	}
	return out, nil
}
```

Both keep the non-nil empty slice.

- [ ] **Step 8: Rewrite ReconcileSessions**

```go
func (s *Store) ReconcileSessions(finishedAt string) (int, error) {
	n, err := s.q.ReconcileSessions(context.Background(), db.ReconcileSessionsParams{
		Status:     SessionCanceled,
		FinishedAt: finishedAt,
		Status_2:   SessionQueued,
		Status_3:   SessionRunning,
	})
	return int(n), err
}
```

- [ ] **Step 9: Build and test**

Run: `make test`
Expected: all packages pass. `internal/apiserver` has 33 session tests and `internal/session` has 28; all must be green.

- [ ] **Step 10: Commit**

```bash
git add internal/store/session.go internal/store/session_test.go
git commit -m "refactor(store): route agent session queries through sqlc"
```

---

## Task 9: Remove the last hand-written SQL and verify the split

Confirms the spec's central claim: after this task, no hand-written SQL exists outside `migrate.go`.

**Files:**
- Modify: `internal/store/store.go`, `internal/store/session.go`

**Interfaces:**
- Consumes: everything from tasks 5-8
- Produces: `nullIfEmpty` returning `*string`

- [ ] **Step 1: Assert no hand-written SQL remains**

Run: `rg -n "s\.db\.(Exec|Query|QueryRow)|tx\.(Exec|Query|QueryRow)|conn\.(Exec|Query|QueryRow)" internal/store/ --glob '!*_test.go'`
Expected: matches **only** inside `migrate.go`. Every hit in `store.go` or `session.go` is a leftover to convert in step 2.

- [ ] **Step 2: Convert any remaining call sites**

For each hit, add the matching query to the appropriate `.sql` file, run `make generate`, and switch the call site to the generated method. Do not add raw SQL to `migrate.go` to make this check pass.

- [ ] **Step 3: Update nullIfInt64 and nullIfEmptyPtr if still any-typed**

`nullIfEmpty` was converted to return `*string` in task 5 and `nullIfEmptyPtr` in task 8. Confirm each returns a pointer:

Run: `rg -n "func nullIf" internal/store/`
Expected: all three return `*string` or `*int64`. None may still return `any`.

- [ ] **Step 4: Remove the now-unused imports and helpers**

`go build ./...` reports unused imports. Remove `"strings"` from `session.go` only if `strings.TrimSpace` is no longer used there — it still is, in `CreateSession`. Remove `"time"` from `session.go` if unused. Verify with `go vet ./...`.

Delete any helper left with no callers. Confirm `rg -n "nullIfInt64|nullIfEmptyPtr|nullIfEmpty" internal/store/` shows each still used.

- [ ] **Step 5: Verify the layer boundary holds**

Run: `rg -n "internal/db" --glob '*.go' -l`
Expected: exactly two files — `internal/store/store.go` and `internal/store/session.go`. Nothing in `apiserver`, `tui`, `session`, `client`, `devseed`, or `cmd/flf` may import it.

- [ ] **Step 6: Verify generated code is in sync**

Run: `make diff`
Expected: no output, exit 0. Any diff means a query was edited without regenerating, or a generated file was hand-edited. Fix by running `make generate` and discarding hand edits.

- [ ] **Step 7: Run the full suite and the static checks**

Run: `make test`
Expected: all packages pass; `gofmt -l .` empty; `go vet ./...` clean.

- [ ] **Step 8: Commit**

```bash
git add internal/store internal/db
git commit -m "refactor(store): remove remaining hand-written SQL from the store"
```

---

## Task 10: Enable foreign keys and drop the redundant existence pre-checks

The highest-risk commit, isolated so any failure is trivially attributable. The pragma and the pre-check deletions must land together, in this order: while FKs are unenforced, removing the pre-checks would let inserts against missing parents succeed.

**Files:**
- Modify: `internal/store/migrate.go` (`prepare`), `internal/store/store.go`, `internal/store/session.go`
- Modify: `internal/store/queries/channels.sql`, `threads.sql`, `agent_sessions.sql` (delete three queries)
- Modify: `internal/store/classify_test.go`

**Interfaces:**
- Consumes: `prepare` from task 2; `classify` from task 3
- Produces: `func assertForeignKeys(conn *sql.DB) error`

- [ ] **Step 1: Write the failing test**

Append to `migrate_test.go`:

```go
func TestOpenEnablesForeignKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fk.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var fk int
	if err := s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Fatalf("PRAGMA foreign_keys = %d, want 1", fk)
	}
}

func TestForeignKeyViolationMapsToNotFound(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "fk2.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.CreateThread(99999, "t"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/store/ -run 'TestOpenEnablesForeignKeys|TestForeignKeyViolationMapsToNotFound' -v`
Expected: FAIL. The first reports `PRAGMA foreign_keys = 0`. The second currently passes only because `CreateThread` still does a `SELECT COUNT(*)` pre-check; once the pre-check is removed in step 5 it depends on the pragma from step 3, so keep both tests.

- [ ] **Step 3: Enable the pragma first**

In `migrate.go`, add and call:

```go
func assertForeignKeys(conn *sql.DB) error {
	var fk int
	if err := conn.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		return err
	}
	if fk != 1 {
		return fmt.Errorf("sqlite refused PRAGMA foreign_keys=ON (got %d)", fk)
	}
	return nil
}
```

In `prepare`, as the very first statement, before `legacyRebuildRequired`:

```go
	if _, err := conn.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		return err
	}
	if err := assertForeignKeys(conn); err != nil {
		return err
	}
```

- [ ] **Step 4: Run the FK tests**

Run: `go test ./internal/store/ -run 'TestOpenEnablesForeignKeys|TestForeignKeyViolation' -v`
Expected: PASS.

- [ ] **Step 5: Run the full suite before removing the pre-checks**

Run: `make test`
Expected: all packages pass with the pragma on and the pre-checks still present. **If anything fails here, stop.** The failure means an existing test relies on writing rows that violate a declared FK — a real finding about the schema's integrity assumptions. Report it rather than working around it.

- [ ] **Step 6: Delete the redundant existence pre-checks**

In `store.go`, remove the `CountChannelsByID` call from `CreateThread`. In `appendMessage`, remove the `CountThreadsByID` call. In `nextThreadSeq`, remove the `CountThreadsByID` call. In `session.go`, remove the `CountSessionsByID` call from `AppendSessionEvent`.

Delete the now-unused queries from the `.sql` files:

- `CountChannelsByID` from `queries/channels.sql`
- `CountThreadsByID` from `queries/threads.sql`
- `CountSessionsByID` from `queries/agent_sessions.sql`

Run: `make generate`
Expected: regeneration succeeds; the three params types disappear from `internal/db`.

- [ ] **Step 7: Re-enable the classify subtest that was skipped**

In `classify_test.go`, remove the task-3 skip on the `foreignkey` subtest so it now asserts the full mapping.

- [ ] **Step 8: Run the full suite**

Run: `make test`
Expected: all packages pass. `apiserver` must still map the FK violation to 404, not 500.

- [ ] **Step 9: Verify the wire contract once more**

Run: `go test ./internal/apiserver/ ./internal/tui/ ./internal/session/ -v 2>&1 | tail -20`
Expected: PASS.

- [ ] **Step 10: Commit**

```bash
git add internal/store
git commit -m "feat(store): enforce foreign keys and drop redundant existence pre-checks"
```

---

## Task 11: Thread context through the store and its callers

Mechanical signature sweep. It is last so that no other change is entangled with it.

**Files:**
- Modify: `internal/store/store.go`, `internal/store/session.go`
- Modify: every caller of a `Store` method outside `internal/store`

**Interfaces:**
- Consumes: all `Store` methods from tasks 5-8
- Produces: every exported `Store` method takes `context.Context` as its first parameter

- [ ] **Step 1: List every call site**

Run: `rg -n "\.(AppendMessage|AppendMessageWithParent|AppendMessageAt|AppendMessageAtWithParent|AppendMessageByParentSeq|AppendBatch|ListMessages|ListMessagesAfter|ListChannels|ListThreads|CreateChannel|CreateThread|MessageIDBySeq|MessageByID|ListInbox|ListReactions|AddReaction|AddReactionBySeq|CountAgentMessagesAfter|ThreadContext|CreateSession|ListSessions|GetSession|MarkSessionRunning|SetSessionReply|FinishSession|AppendSessionEvent|ListSessionEvents|ReconcileSessions)\(" --glob '*.go' -l`
Expected: `internal/store/store.go`, `internal/store/session.go`, `internal/apiserver/*.go`, `internal/session/*.go`, `internal/devseed/*.go`, `cmd/flf/*.go`, `internal/tui/*.go` as applicable. Record the list; every one needs a context argument.

- [ ] **Step 2: Add the parameter to the Store methods**

Change each exported method to take `ctx context.Context` first, replacing the internal `context.Background()` with the parameter. For example:

```go
func (s *Store) ListChannels(ctx context.Context, repoAbsPath string, includeOrphaned bool) ([]Channel, error) {
```

For the transaction helpers, thread `ctx` through `s.tx(ctx)`:

```go
func (s *Store) tx(ctx context.Context) (*sql.Tx, *db.Queries, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	return tx, s.q.WithTx(tx), nil
}
```

Also add `ctx` to the private helpers that currently call `context.Background()`: `appendMessage`, `addReaction`, `addReactionBySeq`, `nextThreadSeq`, `messageThreadID`, and the `sessionFromRow` callers.

`Open` does **not** take a context. Contexts are per-operation, not per-store.

- [ ] **Step 3: Fix the apiserver call sites**

Use `r.Context()`. Every handler already has the request. `go build ./...` will list each site; work through them.

- [ ] **Step 4: Fix the session manager call sites**

Use the existing `m.ctx` from `internal/session/manager.go:45`. Do not introduce a new root context.

- [ ] **Step 5: Fix the cmd/flf, devseed, and tui call sites**

Use `context.Background()`. Do not introduce a signal-bound daemon context; that is an explicit non-goal in the spec.

- [ ] **Step 6: Build, vet, and test**

Run: `make test`
Expected: all packages pass; `gofmt -l .` empty; `go vet ./...` clean.

- [ ] **Step 7: Verify no stray Background remains inside the store**

Run: `rg -n "context.Background\(\)" internal/store/`
Expected: no matches in `store.go` or `session.go`. Any hit means a method was missed.

- [ ] **Step 8: Commit**

```bash
git add internal/store internal/apiserver internal/session internal/devseed internal/tui cmd/flf
git commit -m "refactor(store): thread context through store methods and callers"
```

---

## Task 12: Update the documentation

The spec's schema copy at `docs/backend.md:31-110` and the drift caveat at `:117-119` are now both wrong.

**Files:**
- Modify: `docs/backend.md`

**Interfaces:**
- Consumes: `internal/store/migrations/00001_initial_schema.sql` as the single source of truth
- Produces: no code changes

- [ ] **Step 1: Replace the duplicated schema block**

Replace the schema listing at `docs/backend.md:31-110` with a pointer to the single source of truth:

```markdown
The schema lives in [`internal/store/migrations/`](../internal/store/migrations/) as an
ordered, goose-formatted migration set. It is applied at `Open` by
`internal/store/migrate.go` and read by `sqlc` for type generation. Do not duplicate
it here; edit the migrations instead.
```

- [ ] **Step 2: Replace the drift caveat**

Delete the paragraph at `docs/backend.md:117-119` about there being no schema version
table. Replace it with:

```markdown
Migrations are versioned. `schema_migrations` records the highest applied version and
`internal/store/migrate.go` applies pending files in ascending order, each in its own
transaction. Adding a schema change means adding a new `NNNNN_name.sql` file and
running `make generate`; never edit an already-applied migration.
```

- [ ] **Step 3: Document the workflow**

Add a short section covering:

- `make generate` after any change to `queries/` or `migrations/`
- `make diff` must be clean before committing; it is what catches a forgotten generate
- `make vet` enforces the append-only rule (no `DELETE` in a query file)
- `internal/db` is generated and committed; never hand-edit it
- a new migration gets a zero-padded numeric prefix so lexicographic order matches numeric order

Also record the `go tool sqlc generate` invocation, or the `go install` fallback if task 1 step 2 required it.

- [ ] **Step 4: Document the legacy rebuild**

Add a note that a database whose `messages` table predates the `parent_id` column is dropped and rebuilt, and that this is intentional and covered by
`TestFileStoreMigrationRebuildsLegacySchemaWithoutParentColumn`. This preserves the
warning that `docs/backend.md` already carries for the destructive path.

- [ ] **Step 5: Verify the docs match the code**

Run: `rg -n "IF NOT EXISTS archived_at|archived_at" docs/backend.md`
Expected: no claim that `archived_at` is dropped by schema evolution. The cleanup is `dropArchivedAtColumns` in `migrate.go`, run once after migrations, not a migration.

Run: `make test`
Expected: still green. This task changes no code.

- [ ] **Step 6: Commit**

```bash
git add docs/backend.md
git commit -m "docs: point backend.md at the migration set and document the sqlc workflow"
```

---

## Verification

Run after every task:

```bash
make test     # go vet ./... && gofmt -l . && go test ./...
make diff     # generated code must be in sync
make vet      # append-only and no-pragma rules
```

Before declaring the work complete, run the end-to-end suite and confirm the daemon
starts against a real database file:

```bash
go test ./... -count=1
rm -rf /tmp/fluffle-verify && mkdir -p /tmp/fluffle-verify
go build -o /tmp/fluffle-verify/flf ./cmd/flf
/tmp/fluffle-verify/flf daemon --help
```

Then confirm the JSON contract is unchanged by comparing a rendered message list
before and after against `main`:

```bash
/tmp/fluffle-verify/flf --help
```

Spot-check that an empty thread renders as `[]` and an empty inbox as `[]`, and that
`Message.ParentID` still appears as a nullable integer rather than a pointer-encoded
field.
