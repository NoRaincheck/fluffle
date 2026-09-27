# sqlc-Generated SQL Layer — Design

Date: 2026-09-27
Status: draft for review
Scope: `internal/store`, new `internal/db`, new `sqlc.yaml`, `docs/backend.md`

## 1. Summary

Move the entire SQL layer of fluffle onto [sqlc](https://sqlc.dev). The schema becomes
an ordered, versioned set of embedded migration files. All ~40 inline SQL queries move
out of Go into annotated `.sql` files that sqlc compiles into typed Go. Foreign key
enforcement is switched on, and SQLite error codes replace substring matching on error
strings.

The public surface of `internal/store` does not change. The HTTP API, the TUI, the
daemon, and all existing tests keep working. What changes is everything underneath:
schema evolution becomes formal and ordered, queries become compile-time checked
against the schema, and hand-written row-scanning disappears.

Tooling: sqlc v1.31.1, pinned as a Go tool. No new module dependency is introduced to
the compiled binary.

## 2. Goals

1. **Formal migrations.** An ordered, versioned, embedded migration set with a
   `schema_migrations` table replaces the `PRAGMA table_info` heuristic in
   `internal/store/store.go:162-190`.
2. **Typed queries.** Every query lives in a `.sql` file and is verified against the
   schema by sqlc. Column-name and arity mistakes become compile-time failures.
3. **No duplicated transaction logic.** The six hand-duplicated `*sql.DB` / `*sql.Tx`
   helper pairs collapse into one function each via sqlc's `WithTx`.
4. **Real constraint handling.** SQLite extended error codes are classified into the
   existing `ErrNotFound` / `ErrConflict` / `ErrInvalid` sentinels, so the HTTP error
   envelope is driven by the database rather than by string matching.
5. **Enforced foreign keys.** Declared FKs become real constraints.
6. **Append-only enforced mechanically.** A `sqlc vet` rule rejects any `DELETE` in a
   query file, matching the domain rule in `AGENTS.md`.

## 3. Non-goals

- No change to the HTTP API, CLI surface, TUI layout, or agent protocol.
- No ORM, no query builder, no `context`-free convenience wrappers — with one
  documented exception. `session.Manager`'s store calls necessarily pass
  `context.Background()` rather than `m.ctx`: `Shutdown()` calls `m.cancel()`
  *before* writing the terminal `agent_sessions` rows, so passing `m.ctx` to
  those writes returns `context.Canceled` and strands sessions in a
  non-terminal state. Five existing tests catch that. `m.ctx` is still used for
  the runner and for cancellation checks, which is what it is for (§10).
- No `sqlc batch*` queries (PostgreSQL-only upstream; `AppendBatch` uses `WithTx`).
- No `sqlc verify` / `sqlc push`. Both require sqlc Cloud, which is out of scope for a
  localhost-only daemon.
- No reconstruction of historical schema states as individual `ALTER` migrations. The
  destructive legacy rebuild is retained deliberately (see §6.3).
- No new `README` or tutorial. `docs/backend.md` is updated in place.
- No signal-bound daemon root context. `cmd/flf` keeps using `context.Background()`
  for its store calls, as it does today for its HTTP calls (§10).

## 4. Current state: the problems being fixed

### 4.1 Schema drift is silent

The schema is a 75-line Go string constant at `internal/store/store.go:21-95`, applied
via `CREATE TABLE IF NOT EXISTS`. A near-verbatim copy is duplicated in prose at
`docs/backend.md:31-110`. `docs/backend.md:117-119` documents the consequence:

> There is no schema version table, and the probe keys on one specific column, so drift
> that does not involve `messages.parent_id` takes the additive path — where
> `CREATE TABLE IF NOT EXISTS` silently skips a table that already exists, and a
> missing column on that table is never added.

There is no way to ask the database what schema it is running, and no way to evolve it
in a recorded order.

### 4.2 The migration path can destroy data

`migrate` (`store.go:162-190`) probes for a column literally named `parent_id`. If it
is absent, `store.go:186-188` drops `reactions`, `messages`, `threads`, and `channels`
and rebuilds. A hand-patched `dropArchivedAtColumns` (`store.go:139-153`) exists
because `IF NOT EXISTS` cannot remove an orphaned column. The schema constant has
changed exactly once in the repository's history (`bb21d55`).

### 4.3 Transaction logic is duplicated

Six query implementations exist twice, once against `*sql.DB` and once against
`*sql.Tx`, differing only in the receiver. Examples: `store.go:476-486` vs
`store.go:488-498`; `store.go:780-794` vs `store.go:409-423`.

### 4.4 Error handling is string matching

`strings.Contains(err.Error(), "UNIQUE")` appears at `store.go:214`,
`store.go:686`, and `session.go:100`. Because `CHECK` violations arrive as opaque
strings, the schema's constraints are re-implemented in Go by `validateMessage`
(`store.go:456-467`) and `validateReaction` (`store.go:619-627`) rather than being
trusted. A constraint that slips through surfaces as HTTP 500.

### 4.5 Foreign keys are documentation

No `PRAGMA foreign_keys=ON` exists anywhere, so SQLite does not enforce the declared
FKs. Referential integrity is maintained by hand-rolled `SELECT COUNT(*)` round-trips
(`store.go:316`, `store.go:410`, `store.go:780`, `session.go:185`) and cross-thread
checks (`store.go:429-431`, `session.go:94-96`).

### 4.6 Queries are not checked against the schema

All SQL is inline in Go. Nothing verifies that a `SELECT` column exists or that
parameter arity matches. `store.go:222-237` and `store.go:500-518` build SQL by string
concatenation.

## 5. Architecture

### 5.1 File layout

```
sqlc.yaml                                       repo root; sqlc v2 config
internal/store/
  migrations/
    00001_initial_schema.sql                    goose-formatted; verbatim move of
                                                the store.go:21-95 constant
  queries/
    channels.sql                                -- name: annotated
    threads.sql
    messages.sql
    reactions.sql
    agent_sessions.sql
    agent_session_events.sql
  migrate.go                                    hand-rolled runner + //go:embed
  classify.go                                   SQLite error code -> sentinel
  store.go                                      Store facade, wire types, sentinels
  session.go
internal/db/                                    GENERATED, committed
  db.go  models.go  querier.go
  channels.sql.go  threads.sql.go  messages.sql.go
  reactions.sql.go  agent_sessions.sql.go  agent_session_events.sql.go
```

`internal/db` is generated and is imported only by `internal/store`. No other package
may import it; this is enforced by review, not by tooling, because `internal/` already
restricts it to the module.

### 5.2 Layer contract

```
apiserver / session / tui / devseed / cmd/flf
                    |
                    v
          internal/store            hand-written: wire types, sentinels,
                    |                validation, orchestration
                    v
           internal/db               generated: models, typed queries, WithTx
                    |
                    v
              *sql.DB                 modernc.org/sqlite
```

The boundary that matters: **only `internal/db` speaks SQL, and only `internal/store`
defines the types that get marshaled to JSON.** This keeps the JSON wire contract
independently verifiable — see §8.

### 5.3 Store shape

```go
type Store struct {
    db *sql.DB
    q  *db.Queries
}
```

`db` is retained for three reasons: `Close()`, the migration runner, and
`store_test.go:882`, which reaches into the unexported field to assert a column is
gone. Retaining it keeps that test compiling and passing unchanged.

`q` is the generated `*db.Queries`, constructed by `db.New(db)`.

## 6. Migrations

### 6.1 The migration file

`internal/store/migrations/00001_initial_schema.sql` is a **verbatim move** of the
`schema` constant at `store.go:21-95`, with three changes and no others:

1. Wrapped in `-- +goose Up` / `-- +goose Down` sections.
2. The Down section lists `DROP TABLE` in reverse dependency order.
3. The `IF NOT EXISTS` clauses are **kept** (see §6.3).

Verified: sqlc's SQLite engine parses the existing DDL unchanged, including the partial
unique index `uniq_orphan_channel_name ... WHERE is_orphaned = 1`, the
`DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))` expression defaults, and the
multi-column table-level `CHECK` on `channels`.

Filenames are `NNNNN_snake_case_name.sql` with zero-padded numeric prefixes. This is
required: sqlc parses migration files in **lexicographic** order, so a prefix must be
padded such that lexicographic order matches numeric order for every future migration.

### 6.2 The runner

sqlc parses migrations but never applies them, so `internal/store/migrate.go` is
hand-written. Its full responsibility:

- `//go:embed migrations/*.sql` bound to a package-level `embed.FS`, passed into a
  loader that takes an `fs.FS` parameter. The production call site passes the embedded
  FS; tests pass an `fstest.MapFS`. This is what makes the runner testable against a
  multi-migration sequence without adding throwaway migration files to the repository.
- The loader reads the FS into a sorted slice of `{version int64, name string, up
  string}`, parsing the `NNNNN_` filename prefix. Sorting is by parsed version, not by
  string, so a future mis-padded filename is caught rather than silently reordered.
- Split each file's content on the `-- +goose Down` marker; the Down section is
  retained for readers and for sqlc, and never executed. No down-migration support
  exists in the runner.
- Create the version table idempotently, outside the migration set:
  ```sql
  CREATE TABLE IF NOT EXISTS schema_migrations(
    version    INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
  );
  ```
  It is deliberately not declared in a migration file, so sqlc generates no model for
  it. There is no chicken-and-egg problem: the runner creates the table directly with
  `ExecContext` before reading any version.
- Read the current version with `SELECT COALESCE(MAX(version), 0) FROM
  schema_migrations`. The preceding step guarantees the table exists, so an empty table
  yields 0 rather than erroring.
- For each embedded migration with `version > current`, in ascending order: open a
  transaction, execute the Up SQL, insert the version row, commit. On any error,
  return without committing, leaving the database at the last good version.
- Apply migrations before constructing `*db.Queries`.

Approximate size: 100 lines including the version-table DDL.

### 6.3 Adoption policy for pre-versioning databases

A database with no `schema_migrations` table is at version 0, so `00001` always runs.
Because that file keeps `IF NOT EXISTS`, applying it to an already-current database is
a no-op that also creates any missing table. There is no separate "stamp" path.

One existing behavior is retained deliberately. If `messages` exists and lacks
`parent_id`, the database predates threading and is rebuilt destructively, exactly as
`store.go:186-188` does today: drop `reactions`, `messages`, `threads`, `channels` in
that order, then let `00001` recreate them. This is a conscious choice to keep the
current semantics rather than invent a historical `ALTER` chain; the alternative
(reconstructing per-commit schema states as migrations) was considered and rejected as
scope creep. `TestFileStoreMigrationRebuildsLegacySchemaWithoutParentColumn`
(`store_test.go:783`) continues to pass and continues to assert that legacy rows do
**not** survive.

The drop statements will now have their errors checked. `store.go:187` currently
ignores the returned error.

### 6.4 The `archived_at` cleanup

`dropArchivedAtColumns` (`store.go:139-153`) stays, for two reasons. It addresses a
legacy shape that the `parent_id` probe does not cover — a database that has
`parent_id` and also has the long-removed `archived_at` column, which takes the
additive path and would keep the column forever. And SQLite has no
`ALTER TABLE ... DROP COLUMN IF EXISTS`, so it cannot be expressed as an ordinary
migration that is also correct on a fresh database.

It runs once, after migrations are applied, in `migrate.go`. It is not a migration and
is not visible to sqlc. `TestFileStoreDropsArchivedAtColumnsAndKeepsRows` continues to
pass unchanged.

### 6.5 Foreign key enforcement

`migrate.go` issues `PRAGMA foreign_keys=ON` on every connection before any
migration or query runs.

Two constraints on this design:

- The pragma is per-connection, not per-database. Because `SetMaxOpenConns(1)` is
  already required for `:memory:` support (`AGENTS.md`), a single pooled connection
  makes the pragma reliable. It is issued immediately after `sql.Open`, before
  `SetMaxOpenConns` is relied upon for any query.
- `SetMaxOpenConns(1)` is retained.

Consequences, all of which are intended:

- `SELECT COUNT(*)` existence pre-checks that exist only to avoid FK violations become
  redundant and are deleted, in the same commit that enables the pragma (§12, step 4).
- An insert against a missing parent now yields
  `SQLITE_CONSTRAINT_FOREIGNKEY` and is mapped to `ErrNotFound` → HTTP 404, which is
  what those pre-checks produced.
- The cross-thread checks (`store.go:429-431`, `session.go:94-96`) are **retained**.
  They enforce a stronger invariant than FK existence — that a parent message belongs
  to the same thread — which no FK can express. They stay in Go.

Risk: an existing development database containing rows that violate a declared FK
would begin rejecting writes. This is the reason step 4 of the rollout (§11) is an
isolated commit.

## 7. sqlc configuration

### 7.1 Config

`sqlc.yaml` at the repository root. Every setting below was verified against this
project's real schema with sqlc v1.31.1.

```yaml
version: "2"

rules:
  - name: append-only
    message: "agents are append-only: DELETE is not permitted in query files"
    rule: |
      query.sql.contains("DELETE")
  - name: no-pragma
    message: "PRAGMA belongs in migrate.go, not in query files"
    rule: |
      query.sql.contains("PRAGMA")

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

`schema` points at the migrations **directory**, not a schema file, which is what
enables sqlc to read goose-formatted migrations and ignore their Down sections.

`rules` is declared twice on purpose. The top-level `rules` block *defines* the CEL
rules; the per-package `rules` list *enables* them by name. Omitting the per-package
list silently disables the rule — this was confirmed empirically.

Both rules are **file-scoped**, not per-query: `query.sql` is the entire file's
contents, so a file containing one `DELETE` fails the rule for every query in that
file. This is coarser than ideal but is a correct and useful gate: it means a query
file either contains no `DELETE` at all or it does not belong in `queries/`.

A sqlc vet rule states the **offending** condition: sqlc reports a violation when
the expression evaluates to true. The `!`-negated form shown in earlier drafts of
this spec was therefore inverted — it would have failed every query file in a clean
tree. `sqlc.yaml` as written above is the correct polarity; the rules carry no `!`
prefix.

### 7.2 Why each emit setting

| Setting | Value | Reason |
|---|---|---|
| `emit_json_tags` | `false` | **Load-bearing.** With `true` and `json_tags_case_style: pascal`, sqlc emits `json:"RepoHeadSha"` while the Go field is `RepoHeadSHA`. The API today emits the Go field name because no tag exists. `false` reproduces today's behavior exactly. `initialisms` does not affect tag casing — verified. |
| `json_tags_case_style` | unset | Irrelevant while `emit_json_tags` is `false`. Deliberately omitted so it cannot drift into effect. |
| `emit_pointers_for_null_types` | `true` | Makes generated `AgentSession` field-for-field identical to the hand-written `store.Session`, including `Cwd *string`, `ExitCode *int64`, `StartedAt *string`. Supported for SQLite. |
| `emit_exact_table_names` | `false` | Singularization yields exactly the current type names: `Channel`, `Thread`, `Message`, `Reaction`, `AgentSession`, `AgentSessionEvent`. `true` would produce `Channels`, `Messages`, `AgentSessions`. |
| `emit_empty_slices` | `false` | Preserves today's `nil`-on-empty behavior from `return out, rows.Err()`. |
| `emit_interface` | `true` | Emits a `Querier` interface, which lets tests substitute a fake. |
| `query_parameter_limit` | `0` | Always emit a named params struct. Most store methods take 4-8 parameters, and named structs make call sites self-documenting. |
| `rename` | `repo_head_sha: RepoHeadSHA` | Restores the `RepoHeadSHA` spelling (default would be `RepoHeadSha`). |
| `overrides` | `messages.parent_id` → `sql.NullInt64` | Reproduces `Message.ParentID sql.NullInt64`, whose `.Valid` field the TUI reads at `tui/model.go:2442` and `tui/model.go:2468`. The override key format is `[catalog.][schema.]tablename.colname`; for SQLite that is `messages.parent_id`. |

Two settings deliberately **not** used:

- `emit_result_struct_pointers` / `emit_params_struct_pointers` — would add pointer
  indirection to every row for no benefit.
- `omit_unused_structs` — all six tables are queried, so there is nothing to omit, and
  leaving it `false` keeps the models stable if a table goes temporarily unused.

### 7.3 Known type mismatches that stay in the facade

| Column | sqlc type | Wire type today | Resolution |
|---|---|---|---|
| `channels.is_orphaned` | `int64` | `bool` | Converted in the facade. SQLite has no boolean type; a per-column override to `bool` would be fragile because `is_orphaned` is the only such column today and a future `INTEGER` column would inherit it. |
| `channels.repo_*` | `*string` | `string` | `COALESCE(col,'')` retained in the read queries, per §8, except on the two `sql.NullInt64` read paths below. |
| `agent_sessions.*` nullable | `*string` / `*int64` | same | Matched. |
| `messages.parent_id` | `sql.NullInt64` | `sql.NullInt64` | Matched by override, and deliberately **not** coalesced on the read side: `store.Message.ParentID` is `sql.NullInt64`, so coalescing would yield `0` and the TUI's `.Valid` check would read a real parent as absent. |
| `p.seq AS parent_seq` | `sql.NullInt64` | `sql.NullInt64` | Matched, and deliberately **not** coalesced: "no parent" arrives as NULL, and coalescing to `0` would invent a `seq = 0` message that never existed. |

`IsOrphaned` is the only `bool` on the wire. The conversion is one line in the facade
and is asserted by existing tests.

### 7.4 Query inventory

All ~40 queries become static, sqlc-generated. **After this change, no hand-written SQL
exists anywhere outside `migrate.go`.** The three that were previously built by string
concatenation are resolved as follows.

| Current location | Problem | Resolution |
|---|---|---|
| `store.go:222-237` `ListChannels` | `WHERE` clause assembled from a `conds []string` slice | Four static queries — all, by-repo, non-orphaned, by-repo-non-orphaned — and a four-way switch in the facade on `(repoAbsPath != "", includeOrphaned)` |
| `store.go:500-518` `ListMessages` | Triple-nested subselect string-wrapped for `lastN > 0` | Two static queries. The "last N then re-sort" is fixed-shape SQL; sqlc parses and types it. Facade picks by `lastN > 0`. |
| `session.go:158` `execSessionUpdate` | SQL passed as a function argument | Each `UPDATE` becomes its own `:execrows` query; the `RowsAffected() == 0 → ErrNotFound` rule is preserved at each call site |

Annotation mapping:

| Need | Annotation |
|---|---|
| Insert, need only the new id | `:execlastid` (matches today's `res.LastInsertId()`) |
| Insert or update, need affected count | `:execrows` |
| Insert or update, ignore result | `:exec` |
| Single row | `:one` |
| Row set | `:many` |
| Count / MAX aggregates | `:one` returning a scalar |

Specific query outcomes:

- `SELECT MAX(seq) FROM messages WHERE thread_id = ?` (`store.go:778-794`) becomes `:one`
  returning `sql.NullInt64`, deleting the manual null handling in `nextThreadSeqTx`.
- `SELECT COUNT(*) FROM channels WHERE id = ?` (`store.go:316`) and its three siblings
  are **deleted**. With `PRAGMA foreign_keys=ON` (§6.5) the insert itself rejects a
  missing parent, and `classify` maps that to `ErrNotFound`. This removes four round-trips
  per write path and one failure mode.
- `sessionColumns` (`session.go:59-60`), concatenated into two queries, is replaced by
  explicit column lists in `agent_sessions.sql`.
- `AppendBatch` (`store.go:699`) becomes Go orchestration: `tx, err := s.db.Begin()`,
  then `q := s.q.WithTx(tx)`, then a sequence of generated calls, then `tx.Commit()`.
  sqlc's `:batch*` annotations are PostgreSQL-only and are not used.

Code deleted outright: `scanMessages` (`store.go:529-539`), `scanSession`
(`session.go:62-71`), the `scanner` interface, and the six duplicated `*sql.DB`/`*sql.Tx`
pairs.

## 8. Wire contract preservation

`store.Channel`, `store.Thread`, `store.Message`, `store.Reaction`, `store.Session`,
and `store.ThreadContext` are marshaled directly to JSON by `apiserver`
(`server.go:394`, `sessions.go`) and unmarshaled directly by `tui/api.go:35-155` and
`internal/client`. They carry almost no JSON tags — only `json:"-"` on
`Message.ParentSeq` (`store.go:270`) and three tags on `InboxMessage`
(`store.go:306-308`). **Go field names are therefore the API field names.** Any
generated model promoted to a wire type would silently become the API.

Mitigation, in three parts:

1. `internal/db` types are never marshaled. `internal/store` keeps hand-written wire
   types and converts from generated row structs.
2. `emit_json_tags: false` ensures generated types carry no tags, so no accidental
   promotion could change a field name.
3. `apiserver.TestListMessagesAfterSequenceReturnsStrictlyNewerMessages`, which asserts
   `ParentSeq` never appears in a response, continues to guard the contract.

Nullable columns keep the existing `COALESCE(col, '')` treatment in every read query.
This is deliberate and must be preserved verbatim when queries move: it is what keeps
`Channel.RepoAbsPath` and friends pointer-free. `Message.ParentID` and
`Message.ParentSeq` are the only read-side exceptions, and both are `sql.NullInt64`
before and after.

`ParentSeq` is not a column. It is a projection: `LEFT JOIN messages p ON p.id =
m.parent_id` selecting `p.seq` (`store.go:501`). It stays an explicit column in the
query and a hand-written field on `store.Message`.

## 9. Error classification

`internal/store/classify.go` replaces every substring match:

```go
func classify(err error) error {
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

Codes were verified empirically against `modernc.org/sqlite` v1.59.0. The driver
returns **extended** codes, unmasked to the base `SQLITE_CONSTRAINT` (19):

| Condition | Code | Sentinel | HTTP |
|---|---|---|---|
| UNIQUE violation | 2067 | `ErrConflict` | 409 |
| FOREIGN KEY violation | 787 | `ErrNotFound` | 404 |
| CHECK violation | 275 | `ErrInvalid` | 400 |
| NOT NULL violation | 1299 | `ErrInvalid` | 400 |

Constants come from `modernc.org/sqlite/lib`, which is inside the module already
required by `go.mod`. No new module dependency.

`sql.ErrNoRows` continues to map to `ErrNotFound` via `errors.Is` at the same call
sites, unchanged.

### 9.1 Hand-rolled validators become removable

`validateMessage` (`store.go:456-467`) and `validateReaction` (`store.go:619-627`)
re-implement the schema's `CHECK` and `NOT NULL` constraints in Go. They exist only
because a `CHECK` failure could not previously be told apart from an internal error.
With `classify`, a constraint violation is a first-class `ErrInvalid`.

They are removed in step 3 of the rollout. The `ErrInvalid` messages they produced
(`"content required"`, `"emoji required"`, and similar) are asserted by existing tests,
so the removal is staged behind updating those assertions. The session status and
reply-mode `switch` validations (`session.go:77-86`, `session.go:149-153`) are
**retained**: they produce specific, user-facing error text that a bare
`ErrInvalid: constraint failed` would degrade, and they run before the database call.

## 10. Context

sqlc's generated methods take a `context.Context` as their first argument. The store
is currently context-free, with no `QueryContext` or `ExecContext` anywhere.

Decision: **thread a real `context.Context` through the store's public methods.**

What already exists in the codebase:

- `apiserver` handlers have `r.Context()`.
- `session.Manager` already owns a root context, `m.ctx`
  (`internal/session/manager.go:45`), and already passes it down to `internal/runner`,
  which applies a timeout per subprocess (`internal/runner/exec.go:25`). Store calls
  made from the manager should use `m.ctx`.
- `cmd/flf` uses `context.Background()` inline for its HTTP calls
  (`cmd/flf/main.go:89`, `cmd/flf/main.go:142`) and installs no `signal.NotifyContext`
  handler, so there is no daemon-scoped root context to adopt. Its store calls pass
  `context.Background()` after this change, unchanged from today. Introducing a
  signal-bound daemon context is a separate concern and is out of scope.
- `devseed` follows the same pattern as `cmd/flf`.

`Store.Open` does not take a context; it returns a `*Store` bound to a `*db.Queries`.
Contexts are per-operation, not per-store.

This is the largest single source of churn in the change, touching every store call
site. The alternative — having each facade method pass `context.Background()` — was
rejected because it discards cancellation, which is a primary reason to adopt sqlc.

This is recorded as a decision point: if the churn proves unacceptable, the fallback is
a `context.Background()` inside the facade, which confines the change to
`internal/store`. Nothing else in this design depends on the choice.

## 11. Testing strategy

Per `AGENTS.md`, work is TDD: failing test, then implementation, then commit. For a
refactor of this size the 462 existing tests are the primary safety net, and the rule
for steps 1-3 is that **no existing test is modified to accommodate the new
implementation**. A test that must change means a behavior changed, which requires
explicit sign-off.

### 11.1 New tests

Migration runner (`internal/store/migrate_test.go`). The loader takes an `fs.FS` (§6.2),
so the multi-migration cases use `fstest.MapFS` and need no extra files in the
repository:

- Applies `00001` to a fresh file-backed database and records version 1.
- Reopening the same file applies nothing further and is idempotent.
- A two-migration `fstest.MapFS` sequence applies both, in ascending version order.
- A migration that fails mid-body leaves the recorded version unchanged.
- A pre-versioning database with the current shape is adopted without data loss.
- A pre-versioning database whose `messages` lacks `parent_id` is rebuilt destructively
  (extends `TestFileStoreMigrationRebuildsLegacySchemaWithoutParentColumn` to assert
  the version table is repopulated).
- `archived_at` is dropped and rows survive (extends
  `TestFileStoreDropsArchivedAtColumnsAndKeepsRows`).
- `PRAGMA foreign_keys` returns 1 after `Open`.

Error classification (`internal/store/classify_test.go`): a table test asserting all
four codes map to the intended sentinel, and that a non-`*sqlite.Error` passes through
unchanged.

Foreign key enforcement: an insert against a nonexistent parent returns `ErrNotFound`,
not a 500 and not a driver error.

### 11.2 Existing tests that constrain the design

- `TestFileStoreMigrationRebuildsLegacySchemaWithoutParentColumn` — asserts the
  destructive rebuild still destroys legacy rows.
- `TestFileStoreDropsArchivedAtColumnsAndKeepsRows` — reaches into `s.db` for
  `PRAGMA table_info`; compiles only because §5.3 retains the `*sql.DB` field.
- `TestFileStorePersistsParentSequenceProjection` — file-backed persistence across the
  parent-sequence projection.
- `TestDaemonStartReconcilesStaleSessions` — reopen and verify data survived.
- `apiserver.TestListMessagesAfterSequenceReturnsStrictlyNewerMessages` — asserts
  `ParentSeq` is absent from responses.
- 12 call sites use `newStoreWithThread(t, ":memory:")`, which depends on
  `SetMaxOpenConns(1)` being retained (§6.5).

## 12. Rollout

Six commits, each with tests green, each independently revertable.

| Step | Commit | Risk |
|---|---|---|
| 1 | `sqlc.yaml`, `migrations/00001_initial_schema.sql`, `migrate.go`, `schema_migrations`. `Open` switches to the runner. Inline SQL untouched. | None — no behavior change by construction |
| 2 | Move queries into `queries/*.sql`, generate `internal/db`, rewrite store methods one entity at a time (channels, threads, messages, reactions, agent_sessions, agent_session_events). Collapse the `*Tx` pairs via `WithTx`. | Low — tests are the net; no wire change |
| 3 | Introduce `classify`, replace substring matching, delete the two hand-rolled validators. | Medium — changes error mapping for constraint violations |
| 4 | `PRAGMA foreign_keys=ON`, then delete the four `SELECT COUNT(*)` existence pre-checks. | **Highest** — isolated so a failure is trivially attributable |
| 5 | Thread `context.Context` through the store and its callers. | Medium — mechanical, wide |
| 6 | Tooling: `go get -tool`, `Makefile`, `//go:generate`, vet rules. Docs: `docs/backend.md` schema copy at `:31-110` and the drift caveat at `:117-119`. | None |

Steps 4 and 5 are the only commits that can change behavior, which is why each is
isolated.

The existence pre-checks are deleted in step 4 and not step 3, and this ordering is
load-bearing: while foreign keys are still unenforced, removing
`SELECT COUNT(*) FROM channels WHERE id = ?` would let an insert against a missing
parent succeed instead of returning `ErrNotFound`. The pre-checks and the FK pragma are
removed in the same commit, in that order.

## 13. Tooling

sqlc is pinned as a Go tool so the version is locked in `go.mod` and it never enters the
binary's import graph:

```
go get -tool github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1
```

invoked as `go tool sqlc`. If this is unacceptable, the documented fallback is
`go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1`; the version must then be stated
in `docs/backend.md` instead.

A minimal `Makefile` provides the four targets the workflow needs — the repository
currently has no build automation of any kind:

```
generate:  go tool sqlc generate
diff:      go tool sqlc diff
vet:       go tool sqlc vet
test:      go vet ./... && gofmt -l . && go test ./...
```

`sqlc diff` is the enforcement mechanism: it regenerates in memory and fails if the
committed output differs, catching both a forgotten `generate` and a hand-edit of
`internal/db`. `//go:generate go tool sqlc generate` is added to `internal/store/store.go`
so `go generate ./...` works.

Per `AGENTS.md`, `gofmt -l .` must be empty and `go vet ./...` must be clean at every
commit. Generated files satisfy both, since sqlc emits gofmt'd output.

## 14. Risks and mitigations

| Risk | Mitigation |
|---|---|
| A moved query changes behavior silently | The 462 existing tests are the net; step 2 forbids modifying a test to fit the implementation |
| A generated type is accidentally marshaled and changes the API | Generated types carry no JSON tags; wire types stay hand-written in `store`; §8 |
| `PRAGMA foreign_keys=ON` rejects writes on an existing database with orphaned rows | Isolated as commit 4; `classify` maps the violation to `ErrNotFound` (404) rather than a 500 |
| `sqlc diff` not run, generated code drifts | `make diff` target; recommended as a pre-commit/CI step |
| sqlc's SQLite engine rejects a future migration's DDL | `sqlc generate` fails loudly at authoring time, which is the point of the tool |
| Hand-rolled runner has a bug | ~100 lines, fully covered by §11.1, and the version table makes partial application observable rather than silent |

## 15. Open decision points

1. **Context threading** (§10). Recommended: thread real `ctx`. Fallback:
   `context.Background()` inside the facade, confining the change to `internal/store`.
2. **Go tool pinning** (§13). Recommended: `go get -tool`. Fallback: documented
   `go install`.
3. **Validator removal** (§9.1). Recommended: remove `validateMessage` and
   `validateReaction`. Fallback: keep them as a friendlier-error layer ahead of the
   database, accepting the duplication.
