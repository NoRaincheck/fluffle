# Agent Contract Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Fluffle’s agent loop portable, cursor-resumable, stdin-friendly, atomic, and reliably classified without adding hidden memory, autonomous workflows, or a new state store.

**Architecture:** Keep the existing local daemon, SQLite schema, explicit threads, and JSONL surface. Extend the JSONL codec as a projection of existing messages/reactions, add thread-scoped sequence resolution and a single-transaction batch append in the store, expose the existing inbox and a thread cursor through the CLI, and centralize transport/error handling while preserving legacy database-ID flags.

**Tech Stack:** Go 1.26, `database/sql` with modernc SQLite, `net/http`, `encoding/json`, `flag`, and the existing Bubble Tea TUI. No new module dependencies.

**Spec:** `docs/superpowers/specs/2025-01-15-agentic-interface-design.md`

## Global Constraints

- Keep the daemon on `127.0.0.1:0` with one SQLite file and `SetMaxOpenConns(1)`.
- Agents may append messages and reactions only; they cannot create channels/threads or delete data.
- Never trust client-supplied `author_type` for live-write authorization; derive live identity from `X-Fluffle-Agent`.
- Preserve exactly the `{"code","message"}` error envelope and exit codes `0=success`, `1=client`, `2=daemon/transport`.
- Preserve existing database-ID CLI flags and API response shapes unless a new additive field is required.
- Use thread-local `seq` as the portable reference; reaction sequence commands must include `--thread`.
- Parse the complete input before the first write; a multi-line append must commit all events or none.
- Do not automatically retry mutations; ambiguous writes return `DELIVERY_UNKNOWN` with instructions to read before retrying.
- Set `Content-Type: application/json` on every HTTP success and error response.
- Do not add Nostr, remote access, memory, canvas state, workflow scheduling, webhooks, plugins, media, DMs, moderation, or first-class Git features.
- Do not change `store.Message` JSON encoding in a way that breaks existing clients or e2e fixtures.
- No comments in production code unless explicitly requested.

---

### Task 1: Repair and verify the tagged e2e baseline

**Files:**
- Modify: `cmd/flf/e2e_test.go:492-505`
- Test: `cmd/flf/e2e_test.go`

**Interfaces:**
- Consumes: existing e2e test helper and build tag.
- Produces: a compiling `TestE2E_AgentRead` and a reliable tagged-suite gate.

- [ ] **Step 1: Fix the stale local test type**

Change the test-only `Message` declaration from the removed `Author` field to the current `Name` field:

```go
type Message struct {
	Seq     int64  `json:"seq"`
	Name    string `json:"name"`
	Content string `json:"content"`
}
```

- [ ] **Step 2: Run the tagged suite in compile-only mode**

Run:

```bash
go test -count=1 -tags=e2e -run '^$' ./cmd/flf
```

Expected: PASS with no compile error.

- [ ] **Step 3: Run the complete tagged suite**

Run:

```bash
go test -count=1 -tags=e2e ./cmd/flf
```

Expected: PASS for all e2e tests, or report a pre-existing runtime failure separately without weakening the test.

- [ ] **Step 4: Commit the baseline repair**

```bash
git add cmd/flf/e2e_test.go
git commit -m "test: repair tagged e2e agent read fixture"
```

---

### Task 2: Establish the HTTP error and response contract

**Files:**
- Modify: `internal/apiserver/server.go:13-22,117,163,243,245,281`
- Modify: `internal/apiserver/server_test.go`
- Test: `internal/apiserver/server_test.go`

**Interfaces:**
- Consumes: `store` errors and existing HTTP routes.
- Produces: `writeJSON(w http.ResponseWriter, status int, value any)`, correct `METHOD_NOT_ALLOWED`/`DAEMON_ERROR` codes, and JSON content types on all responses.

- [ ] **Step 1: Add failing HTTP contract tests**

Add tests that send successful channel, thread, message, and reaction POSTs and assert:

```go
if got := rec.Header().Get("Content-Type"); got != "application/json" {
	t.Fatalf("content type = %q", got)
}
```

Add tests asserting `PUT /v1/channels` returns `METHOD_NOT_ALLOWED` and that an internal store failure is represented as `DAEMON_ERROR`, not `DAEMON_DOWN`.

- [ ] **Step 2: Run the focused tests and verify RED**

Run:

```bash
go test -count=1 ./internal/apiserver
```

Expected: FAIL on the current missing success headers and incorrect error codes.

- [ ] **Step 3: Implement the minimal response helper and code corrections**

Add:

```go
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
```

Route all JSON success/error bodies through it. Change the three method-not-allowed branches from `DAEMON_DOWN` to `METHOD_NOT_ALLOWED`, and change server-side store failures from `DAEMON_DOWN` to `DAEMON_ERROR`. Preserve existing response field names and status codes.

- [ ] **Step 4: Run the focused tests and verify GREEN**

```bash
go test -count=1 ./internal/apiserver
```

Expected: PASS.

- [ ] **Step 5: Commit the HTTP contract**

```bash
git add internal/apiserver/server.go internal/apiserver/server_test.go internal/apiserver/response_test.go
git commit -m "fix: enforce HTTP error and content type contract"
```

---

### Task 3: Add a tested CLI error renderer and exit mapping

**Files:**
- Create: `cmd/flf/errors.go`
- Modify: `cmd/flf/main.go:27-137` and all direct CLI error exits
- Test: `cmd/flf/main_test.go`

**Interfaces:**
- Produces:

```go
type cliError struct {
	Code    string
	Message string
}

func writeCLIError(w io.Writer, code, message string) int
func fail(code, message string) int
func exitCodeFor(code string) int
```

`writeCLIError` emits exactly `{"code":"...","message":"..."}` on stderr and returns `0` for no error only through callers’ success paths. `exitCodeFor` maps `DAEMON_DOWN`, `DAEMON_ERROR`, and `DELIVERY_UNKNOWN` to 2; all input, file, not-found, permission, and validation codes to 1.

- [ ] **Step 1: Add failing renderer tests**

Use a `bytes.Buffer` and assert exact JSON fields, no extra fields, and exit classification:

```go
if got := writeCLIError(&buf, "DAEMON_ERROR", "storage failed"); got != 2 {
	t.Fatalf("exit = %d", got)
}
if strings.Contains(buf.String(), "retryable") {
	t.Fatal("unexpected retryable field")
}
```

- [ ] **Step 2: Run the focused tests and verify RED**

```bash
go test -count=1 ./cmd/flf -run 'TestCLI|TestError|TestExit'
```

Expected: FAIL because the helper does not exist and existing paths print plaintext.

- [ ] **Step 3: Implement the helper and route command errors through it**

Use `encoding/json` to marshal a two-field struct. Replace direct `fmt.Fprintln`/`Fprintf` error output in `main.go` with `fail`. Make `flag.NewFlagSet` parse failures call `fail("BAD_ARGS", err.Error())`; set each flag set’s output to `io.Discard` so the flag package does not emit an unstructured line first.

For `apiGet`, classify request/response failures as `DAEMON_ERROR` or `DAEMON_DOWN`. For `apiPost`, classify transport failures after dispatch as `DELIVERY_UNKNOWN`; classify local JSON marshal/request construction failures as client errors.

- [ ] **Step 4: Run the focused tests and verify GREEN**

```bash
go test -count=1 ./cmd/flf -run 'TestCLI|TestError|TestExit'
```

Expected: PASS.

- [ ] **Step 5: Commit the CLI contract**

```bash
git add cmd/flf/errors.go cmd/flf/main.go cmd/flf/main_test.go
git commit -m "fix: emit structured CLI errors"
```

---

### Task 4: Extend the JSONL codec for portable events

**Files:**
- Modify: `internal/jsonl/jsonl.go:11-80`
- Modify: `cmd/flf/main.go:479-501`
- Test: `internal/jsonl/jsonl_test.go`

**Interfaces:**
- Produces a `jsonl.Line` that can represent messages and reactions:

```go
type Line struct {
	Type       string         `json:"type,omitempty"`
	Seq        int64          `json:"seq,omitempty"`
	ParentSeq  int64          `json:"parent_seq,omitempty"`
	MessageSeq int64          `json:"message_seq,omitempty"`
	Role       string         `json:"role,omitempty"`
	Name       string         `json:"name"`
	AuthorType string         `json:"author_type,omitempty"`
	Content    string         `json:"content,omitempty"`
	Emoji      string         `json:"emoji,omitempty"`
	Timestamp  string         `json:"timestamp"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}
```

- [ ] **Step 1: Add failing JSONL tests**

Cover missing `type` defaulting to `message`, parent sequence round-trip, reaction parsing, required reaction fields, and rejection of unknown event types. Keep `TestBadJSONLNamesLine` as a message-validation regression.

- [ ] **Step 2: Run the focused tests and verify RED**

```bash
go test -count=1 ./internal/jsonl
```

Expected: FAIL because the new fields and event discriminator are absent.

- [ ] **Step 3: Implement normalization and validation**

Add a `normalize` step in `ParseLine` that defaults empty `Type` to `message`. Require `name` for both event types, require `role` and `content` for messages, require `message_seq` and `emoji` for reactions, and reject any other type. Accept the legacy `author` input key as a name alias during parsing; always emit `name`.

- [ ] **Step 4: Project store parent IDs to parent sequences in the CLI**

When converting `store.Message` to JSONL, build an `id -> seq` map from the current thread response and set `ParentSeq` from `Message.ParentIDValue()`. Keep `store.Message`’s existing wire shape unchanged.

- [ ] **Step 5: Run JSONL and CLI tests and verify GREEN**

```bash
go test -count=1 ./internal/jsonl ./cmd/flf
```

Expected: PASS.

- [ ] **Step 6: Commit the codec**

```bash
git add internal/jsonl/jsonl.go internal/jsonl/jsonl_test.go cmd/flf/main.go
git commit -m "feat: add portable JSONL event fields"
```

---

### Task 5: Add thread-scoped store reads, references, and atomic batch append

**Files:**
- Modify: `internal/store/store.go:191-195,239-423`
- Modify: `internal/store/store_test.go`
- Test: `internal/store/store_test.go`

**Interfaces:**
- Produces:

```go
type Reaction struct {
	ID         int64
	MessageID  int64
	MessageSeq int64
	Emoji      string
	Name       string
	AuthorType string
	CreatedAt  string
}

type AppendEvent struct {
	Type       string
	SourceSeq  int64
	ParentSeq  int64
	MessageSeq int64
	Name       string
	AuthorType string
	Role       string
	Content    string
	Emoji      string
	CreatedAt  string
}

type AppendResult struct {
	SourceSeq  int64
	Seq        int64
	MessageID  int64
	ReactionID int64
}

func (s *Store) ListMessagesAfter(threadID, afterSeq int64) ([]Message, error)
func (s *Store) MessageIDBySeq(threadID, seq int64) (int64, error)
func (s *Store) ListReactions(threadID int64) ([]Reaction, error)
func (s *Store) AppendMessageByParentSeq(threadID, parentSeq int64, name, authorType, role, content, createdAt string) (int64, int64, error)
func (s *Store) AddReactionBySeq(threadID, messageSeq int64, emoji, name, authorType string) error
func (s *Store) AppendBatch(threadID int64, events []AppendEvent) ([]AppendResult, error)
```

`Message` may gain an internal `ParentSeq sql.NullInt64` field tagged `json:"-"`; the left join that populates it must not change existing API JSON.

- [ ] **Step 1: Add failing store tests**

Cover exclusive `after_seq` ordering, empty cursor results, parent sequence resolution, reaction target validation, reaction listing, source-sequence remapping during batch import, and rollback when a late batch event is invalid. Use a file-backed store in migration-sensitive tests and `:memory:` for transaction behavior.

- [ ] **Step 2: Run the focused tests and verify RED**

```bash
go test -count=1 ./internal/store
```

Expected: FAIL because the new methods and transaction behavior do not exist.

- [ ] **Step 3: Add read and sequence-resolution methods without changing existing `ListMessages` behavior**

Keep `ListMessages(threadID,lastN)` intact. Add `ListMessagesAfter` using `WHERE thread_id = ? AND seq > ? ORDER BY seq ASC`. Add a left join for `parent_seq` in the internal scan. Add explicit message existence/thread checks because SQLite foreign keys are not enabled by default.

- [ ] **Step 4: Refactor the single-message transaction into transaction-local helpers**

Move the body of `AppendMessageAtWithParent` into an `appendMessageTx(*sql.Tx, ...)` helper that returns both the assigned sequence and row ID. Add `appendMessageByParentSeqTx` and `addReactionBySeqTx` that resolve thread-local sequences through the same transaction. Existing single-message methods delegate to the helper and retain their signatures.

- [ ] **Step 5: Implement `AppendBatch` atomically**

Start one transaction, validate event types and required fields, build a source-sequence map, resolve existing thread sequences and references within that transaction, insert messages/reactions in order, and return one `AppendResult` per event. Roll back on every error. Do not call a method that opens a second transaction while the batch transaction is open.

- [ ] **Step 6: Run store tests and verify GREEN**

```bash
go test -count=1 ./internal/store
```

Expected: PASS, including a test that proves a mid-batch invalid event leaves zero new rows.

- [ ] **Step 7: Commit the store transaction layer**

```bash
git add internal/store/store.go internal/store/store_test.go
git commit -m "feat: add atomic thread event operations"
```

---

### Task 6: Expose cursor reads, reaction reads, and batch events over HTTP

**Files:**
- Modify: `internal/apiserver/server.go:61-285`
- Modify: `internal/apiserver/server_test.go`
- Test: `internal/apiserver/server_test.go`

**Interfaces:**
- Adds:
  - `GET /v1/threads/:id/messages?after_seq=N`
  - `GET /v1/threads/:id/reactions`
  - `POST /v1/threads/:id/events` with body `{"events":[...],"import":false}`.
  - Extend the existing message POST body with `parent_seq` and add a thread-scoped reaction POST route for `(thread_id, message_seq)`.
- Existing `POST /v1/threads/:id/messages` and `/v1/messages/:id/reactions` remain available.

- [ ] **Step 1: Add failing handler tests**

Assert strict `after_seq` parsing, rejection of `after_seq` together with `last`, empty arrays, reaction ordering by message sequence/time, same-thread reaction targets, and batch rollback/attribution behavior. Assert that live `author_type` comes from `X-Fluffle-Agent`, not a body field; only the human-controlled import path may preserve serialized provenance.

- [ ] **Step 2: Run handler tests and verify RED**

```bash
go test -count=1 ./internal/apiserver
```

Expected: FAIL for missing routes and query behavior.

- [ ] **Step 3: Implement the read routes**

Parse `after_seq` with `strconv.ParseInt`; reject malformed values and mutual exclusion with `last` using the client-error envelope. Return `store.Message` values without changing their existing JSON shape. Add reaction reads using `ListReactions`.

- [ ] **Step 4: Implement the batch route**

Decode `events` as a bounded JSON array, convert each item to `store.AppendEvent`, reject empty/unknown events, call `AppendBatch`, and return assigned sequence/reference results. For a live request with `X-Fluffle-Agent`, derive each event’s author type from the header. For a human import request with `import=true`, preserve valid serialized author types as provenance without granting permissions.

- [ ] **Step 5: Run handler tests and verify GREEN**

```bash
go test -count=1 ./internal/apiserver
```

Expected: PASS.

- [ ] **Step 6: Commit the HTTP agent surface**

```bash
git add internal/apiserver/server.go internal/apiserver/server_test.go
git commit -m "feat: expose cursor and atomic event APIs"
```

---

### Task 7: Add the CLI inbox, cursor, sequence targets, and stdin paths

**Files:**
- Modify: `cmd/flf/main.go:27-57,479-609,683-823`
- Modify: `cmd/flf/main_test.go`
- Test: `cmd/flf/main_test.go` and `cmd/flf/e2e_test.go`

**Interfaces:**
- Adds:

```text
flf inbox [--limit N] [--json]
flf agent read --thread ID --after-seq N [--json]
flf message send --thread ID --reply-to-seq N --text -
flf react add --thread ID --message-seq N --emoji E
flf agent append --thread ID --file -
```

The existing `--reply-to ID`, `--message ID`, and literal `--text` forms remain valid. Sequence and ID flags are mutually exclusive. Reaction sequence mode requires `--thread`.

- [ ] **Step 1: Add failing CLI tests**

Test `run(["inbox", ...])` against a temporary daemon fixture, strict `after_seq` query construction, stdin parsing before `EnsureDaemon`, sequence/ID mutual exclusion, and the exact JSONL projection of parent/reaction events. Add an e2e test for the complete no-raw-HTTP sequence flow.

- [ ] **Step 2: Run focused CLI tests and verify RED**

```bash
go test -count=1 ./cmd/flf -run 'TestInbox|TestAgent|TestStdin|TestSequence|TestE2E_AgentPortableLoop'
```

Expected: FAIL because the commands and flags do not exist.

- [ ] **Step 3: Implement `flf inbox`**

Add the command to dispatch and usage strings. Reuse `GET /v1/inbox?limit=N`, decode `[]store.InboxMessage`, emit `[]` for empty JSON output, and keep the endpoint’s existing global ordering and limit behavior. Text output must include channel, thread, sequence, name, and content.

- [ ] **Step 4: Implement cursor and portable JSONL reads**

Add `--after-seq` to `agentReadCmd` and `dumpThreadMessages`. Fetch messages and reactions, build the ID-to-sequence map, emit message lines with `type:"message"` and `parent_seq`, then emit reaction lines with `type:"reaction"` and `message_seq`. Keep the existing `--last` behavior unchanged.

- [ ] **Step 5: Implement sequence-aware writes**

Add `--reply-to-seq` to `messageSendCmd`; resolve it in the daemon using the existing thread transaction. Add `--thread` plus `--message-seq` to `reactAddCmd`; reject use without `--thread`, reject mixing it with `--message`, and route it to the thread-scoped reaction operation. Preserve database-ID paths.

- [ ] **Step 6: Implement stdin and atomic append**

Read all of `--text -` and all of `--file -` before daemon startup. Convert parsed lines to `AppendEvent` values and send one batch request instead of `postJSONLLines`’ per-line loop. For import, set `import:true` and rely on server-side sequence remapping. Return `DELIVERY_UNKNOWN` without automatic write retry when the transport loses a response after dispatch.

- [ ] **Step 7: Run focused and tagged tests and verify GREEN**

```bash
go test -count=1 ./cmd/flf
go test -count=1 -tags=e2e ./cmd/flf
```

Expected: PASS.

- [ ] **Step 8: Commit the CLI agent loop**

```bash
git add cmd/flf/main.go cmd/flf/main_test.go cmd/flf/e2e_test.go
git commit -m "feat: complete CLI agent read and append loop"
```

---

### Task 8: Add finite transport timeouts and context propagation

**Files:**
- Modify: `internal/client/client.go:14-74`
- Modify: `internal/client/client_test.go`
- Modify: `cmd/flf/main.go:79-137`
- Modify: `internal/tui/api.go:15-208`
- Modify: `internal/tui/api_test.go`

**Interfaces:**
- Produces:

```go
func NewHTTPClient() *http.Client
func DaemonBaseURLContext(ctx context.Context) (string, error)
func EnsureDaemonContext(ctx context.Context) (string, error)
```

The existing no-context wrappers remain for compatibility. The shared client has a finite request timeout; the health probe has a shorter finite timeout. TUI methods normalize a nil context to `context.Background()` and use `http.NewRequestWithContext`.

- [ ] **Step 1: Add failing transport tests**

Use an `httptest.Server` that sleeps beyond the configured health timeout and assert `DaemonBaseURLContext` returns an error. Add a TUI test that cancels a request context and asserts the client returns promptly. Add a CLI test that a stalled post returns `DELIVERY_UNKNOWN` without retrying.

- [ ] **Step 2: Run focused tests and verify RED**

```bash
go test -count=1 ./internal/client ./internal/tui ./cmd/flf -run 'Timeout|Context|Delivery'
```

Expected: FAIL because the clients are unbounded and ignore contexts.

- [ ] **Step 3: Implement shared finite clients and context-aware daemon discovery**

Add a package-owned `http.Client` with a finite timeout. Add context-aware health and startup polling functions while retaining current wrappers. Replace `http.Get` and `http.DefaultClient` in CLI helpers with the shared client and request contexts.

- [ ] **Step 4: Wire TUI request contexts and shared timeout**

Replace `c.http.Get`/`Post` calls with requests built with `NewRequestWithContext`. Normalize nil contexts before request creation. Do not change TUI view state or add new TUI features.

- [ ] **Step 5: Run transport tests and verify GREEN**

```bash
go test -count=1 ./internal/client ./internal/tui ./cmd/flf
```

Expected: PASS.

- [ ] **Step 6: Commit transport hardening**

```bash
git add internal/client/client.go internal/client/client_test.go internal/tui/api.go internal/tui/api_test.go cmd/flf/main.go cmd/flf/main_test.go
git commit -m "fix: bound daemon and client HTTP requests"
```

---

### Task 9: Complete the end-to-end agent contract and final gates

**Files:**
- Modify: `cmd/flf/e2e_test.go`
- Modify: `docs/backend.md` only where the implemented CLI/API contract is now stale
- Test: `cmd/flf/e2e_test.go`

**Interfaces:**
- Produces a verified journey: discover/read, reply by sequence, react by sequence, resume by cursor, export/import, and bounded failures.

- [ ] **Step 1: Add the failing end-to-end journey test**

Add `TestE2E_AgentPortableLoop` covering:

1. Create a repo-anchored channel, thread, and human message.
2. Read JSONL and capture the message sequence.
3. Pipe a reply into `message send --reply-to-seq`.
4. Pipe a reaction into `react add --thread --message-seq`.
5. Read with `--after-seq` and assert only newer events appear.
6. Export the stream, import it into a second thread, and verify message/reply/reaction relationships survive.
7. Assert no raw HTTP lookup is required.

- [ ] **Step 2: Run the journey and verify RED**

```bash
go test -count=1 -tags=e2e ./cmd/flf -run TestE2E_AgentPortableLoop
```

Expected: FAIL until every preceding task is integrated.

- [ ] **Step 3: Make the journey pass without weakening assertions**

Keep stdout/stderr/exit status separate in the e2e helper so structured errors can be asserted. Use the public CLI only. Do not change the core invariants to make the test pass.

- [ ] **Step 4: Run all required gates**

```bash
gofmt -l .
go test -count=1 ./...
go test -count=1 -tags=e2e ./cmd/flf
go vet ./...
```

Expected: all commands succeed. If a command fails, fix the cause before committing the final task.

- [ ] **Step 5: Review the final diff and commit**

```bash
git status --short
git diff --check
git diff
git log --oneline -10
git add docs/superpowers docs/backend.md cmd/flf/e2e_test.go
git commit -m "docs: finalize agent contract guidance"
```

Do not amend earlier commits. Do not commit unrelated files or secrets.

---

## Plan self-review

- **Spec coverage:** baseline repair, JSONL event typing/references/reactions, thread cursor, inbox CLI, sequence-based reply/reaction, stdin, atomic batch append, delivery uncertainty, finite transport, error envelope, content types, and the complete agent journey each have a task.
- **Out-of-scope coverage:** Nostr, memory, canvas, workflow automation, Git hosting, DMs, media, moderation, compact output, search, and TUI redesign are explicitly excluded.
- **Type consistency:** store methods and CLI/API names are defined before later tasks consume them; `--thread` is required for reaction sequence mode.
- **Unfinished-marker scan:** no unfinished placeholders or unspecified “handle errors” steps remain.
- **TDD coverage:** every production change has a failing test step before implementation, followed by a focused green step and a repository-wide gate.
