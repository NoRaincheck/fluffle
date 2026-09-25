# Agentic Interface Design

**Status:** Revised proposal — agent-contract hardening
**Original date:** 2025-01-15
**Revised:** 2026-09-25
**Related analysis:** [Fluffle vs. Buzz CLI](../2025-01-15-fluffle-vs-buzz-cli-gap-analysis.md)

## 1. Decision

This specification replaces the original six-feature roadmap. The goal is not feature parity with Buzz. The goal is a small, reliable, repo-scoped agent loop:

1. A human creates or selects a thread in a repo-anchored channel.
2. An external agent reads the thread through the CLI.
3. The agent appends a reply and reaction using references from the read result.
4. The thread can be exported and imported as a complete, portable context stream.
5. The agent can resume from a monotonic cursor and discover new work through the inbox.

Autonomous workflows, hidden memory, a separate document store, Nostr interoperability, and first-class Git features are not part of this slice.

## 2. Goals

### In scope

- A lossless JSONL event stream containing messages, replies, and reactions.
- Portable references that remain usable after import into another local Fluffle instance.
- A CLI-readable inbox and a monotonic thread resume cursor.
- Stdin support for message and agent-append content.
- Parse-before-write and atomic behavior for a multi-line agent append.
- Finite HTTP timeouts and cancellation.
- One consistent `{"code","message"}` error envelope and the fixed 0/1/2 exit-code contract.
- A human-invoked agent workflow; the daemon does not autonomously launch agents.

### Out of scope

- Nostr, relays, remote access, cryptographic identity, or delegation.
- Agent memory, embeddings, summaries, or opaque per-agent state.
- Canvas revisions, channel documents, or any second source of project context.
- YAML workflows, schedules, webhooks, inotify reloads, plugins, or daemon-owned agent execution.
- First-class Git patches, issues, pull requests, repository hosting, or branch tracking.
- DMs, profiles, presence, moderation, media, GIFs, notes, and multi-repo projects.
- TUI redesign or new TUI-only state.

## 3. Invariants

These rules are inherited from `AGENTS.md` and `VISION.md`:

- The daemon binds only to `127.0.0.1:0` and persists one SQLite database with `SetMaxOpenConns(1)`.
- Agents may append messages and reactions to existing threads. They cannot create channels or threads, delete data, or acquire permissions from client-supplied fields.
- The daemon derives live-write identity from `X-Fluffle-Agent`; imported `author_type` is context/provenance, never authorization.
- The JSONL thread history is the agent's context. No hidden memory is introduced.
- Every channel and thread is repo-anchored by default. Discovery and actions must not target an ambiguous channel name across repositories.
- All layer boundaries use `Content-Type: application/json` and the `{"code","message"}` error envelope.
- Exit codes are exactly 0 for success, 1 for client errors, and 2 for daemon or transport errors.

## 4. Current gaps this design addresses

| Gap | Current evidence | Required result |
|-----|------------------|-----------------|
| Agent output has no portable message target | `internal/jsonl/jsonl.go:11-19` omits message references | An agent can target a message from the stream it just read |
| Replies and reactions cannot be represented | JSONL contains messages only | Replies and reaction events survive export/import |
| No inbox command | `/v1/inbox` exists at `internal/apiserver/server.go:61-79`, but CLI dispatch at `cmd/flf/main.go:37-57` omits it | Agents can discover recent work without using raw HTTP |
| No resume cursor | `ListMessages` supports `last=N` only at `internal/store/store.go:313-331` | A thread can be read incrementally without duplicates |
| Appends are incremental | `cmd/flf/main.go:518-533` posts one line at a time | A failed multi-line append does not leave an unexplained partial batch |
| HTTP requests can hang | `internal/client/client.go:40-73` uses clients without configured timeouts | Every daemon interaction has a finite failure boundary |
| Error codes and exits disagree | 405 responses use `DAEMON_DOWN` in `internal/apiserver/server.go:119,165,245` | Agents can distinguish input failures from daemon failures |
| Tagged e2e coverage does not compile | `cmd/flf/e2e_test.go:1` is build-tagged and fails at line 505 | The full test surface is verified before feature work |

## 5. Portable JSONL contract

### 5.1 Event types

The stream contains two event types. Existing message lines without a `type` field remain valid and default to `message`.

A message event is:

```json
{"type":"message","seq":12,"parent_seq":8,"role":"assistant","name":"reviewer","author_type":"agent","content":"Ready for review.","timestamp":"2026-09-25T10:00:00Z"}
```

A reaction event is:

```json
{"type":"reaction","message_seq":12,"emoji":"👀","name":"reviewer","author_type":"agent","timestamp":"2026-09-25T10:01:00Z"}
```

Required message fields in exported history are `seq`, `role`, `name`, and `content`. A live append may omit `seq`; the daemon assigns it. `parent_seq` is optional and identifies a message earlier in the same thread. Required reaction fields are `message_seq`, `emoji`, and `name`. `timestamp` is preserved when available.

### 5.2 Reference rules

- `seq` is a monotonic, thread-local sequence. It is the portable reference used by the agent-facing CLI.
- `parent_seq` refers to the parent message in the same thread.
- `message_seq` refers to the message receiving a reaction in the same thread.
- Database `id` remains an internal daemon identifier. It may appear in API responses, but it is not the portable cross-import reference.
- On import, the daemon replays messages in sequence order, maintains a source-sequence to destination-sequence map, and resolves `parent_seq` and `message_seq` through that map.
- On a live append, incoming `seq` values are ignored and the daemon assigns the next sequence in the destination thread.
- A reaction references an existing message in the same thread. Cross-thread references are rejected as client errors.

This model makes the stream portable without adding a second event database or pretending local integer IDs survive an export/import boundary.

### 5.3 Attribution rules

`name`, `role`, and `author_type` are serialized for context. A human-controlled import may preserve the serialized attribution as historical provenance. A live write always re-derives `author_type` from the request header and never grants permissions based on a JSONL field.

If a future design needs stronger provenance or identity verification, it must be a separate security decision and a separate specification.

## 6. Read and resume interfaces

### 6.1 Thread reads

Extend the existing thread message read with an exclusive cursor parameter:

```text
GET /v1/threads/:id/messages?after_seq=N
```

Semantics:

- `after_seq=0` or an absent value means read according to the existing `last` behavior.
- `after_seq=N` returns messages with `seq > N`, in ascending order.
- The CLI uses the `seq` of the last returned message as the next cursor; an empty result leaves the cursor unchanged.
- `after_seq` and `last` are mutually exclusive; an invalid combination is a client error. An explicit `--last 0` is treated as present, so `--after-seq 0 --last 0` is a client error rather than a silent precedence choice.
- The cursor bounds message events only. Reactions have no independent cursor, so every JSONL read emits the complete reaction snapshot for the thread, including reactions added after the cursor on older messages. Silently filtering those reactions would drop context the cursor cannot express.

### 6.2 Inbox

Keep the existing `/v1/inbox` endpoint and add a CLI wrapper:

```text
flf inbox [--limit N] [--json]
```

The first slice uses the endpoint for discovery and bounded output. A cross-channel opaque cursor can be added after the response shape and ordering semantics are exercised by real agents; it is not a reason to redesign the thread cursor first.

### 6.3 Reactions

Expose reaction reads as part of the thread event stream and add a read path for the TUI or CLI if required by the first end-to-end scenario. The minimum agent path is:

```text
flf message send --thread 42 --reply-to-seq 12 --text - --agent-id reviewer
flf react add --thread 42 --message-seq 12 --emoji 👀 --agent-id reviewer
```

The existing database-ID forms may remain for humans, but the agent-facing forms must be usable from JSONL output without an out-of-band HTTP lookup. Both reaction routes validate their target inside the write transaction — SQLite foreign keys are not enabled — and a missing target is a client error, not a silent success.

## 7. Write interfaces

### 7.1 Stdin

Support stdin for the two paths agents actually need:

```text
printf '%s\n' "response" | flf message send --thread 42 --text -
cat response.jsonl | flf agent append --thread 42 --file -
```

`agent append` accepts a complete JSONL stream on stdin. The command parses the entire stream before the first daemon write. The HTTP batch route runs every submitted line through the same JSONL codec, so a missing `type`, the legacy `author` alias, and per-type required fields behave identically for the CLI and for any other local client.

### 7.2 Batch safety

A multi-line append must be atomic: either every valid event in the batch is committed or none is. The daemon must perform validation and persistence in one transaction. The CLI must not report success after a partial commit.

If the response is lost after a write may have committed, the CLI reports a `DELIVERY_UNKNOWN` error and directs the caller to read the thread before retrying. The error envelope remains `{"code","message"}`; no `retryable` field is added.

### 7.3 Idempotency boundary

Until the contract has an idempotency key or an equivalent duplicate-detection rule, the CLI must not automatically retry message or reaction writes. Reads and health probes may use bounded retry. Write callers must choose whether to retry after inspecting the destination thread.

## 8. Transport and errors

- Use finite timeouts for daemon probes and API calls.
- Pass request context through CLI operations and TUI operations.
- Keep startup polling bounded and distinguish startup failure from a request failure.
- Use `DAEMON_ERROR` for server-side failures and reserve `DAEMON_DOWN` for inability to reach a daemon. A creation or mutation store failure is always `DAEMON_ERROR`; only typed validation and conflict errors are client errors.
- Validate every CLI and TUI response before reporting success. Reject `null`, wrong shapes, trailing JSON, zero IDs or sequences, and unacknowledged mutations. Reads that fail validation are `DAEMON_ERROR`; mutations are `DELIVERY_UNKNOWN` because the write may already have committed. A false success is never printed.
- Return `METHOD_NOT_ALLOWED` as a client error, not as a daemon-down condition.
- Emit JSON errors on stderr for every CLI error path, with exactly `code` and `message`.
- Set `Content-Type: application/json` on success responses as well as error responses.

Buzz's retry and error ideas are useful only after these local correctness rules are satisfied. A loopback daemon does not need relay-scale jitter or relay-specific 429 handling.

## 9. End-to-end data flow

```text
human
  │ creates repo-anchored channel and thread
  ▼
flf CLI ── GET /v1/threads/:id/messages?after_seq=N
  │
  ▼
agent reads JSONL with seq, parent_seq, and reaction targets
  │
  ├─ flf message send --reply-to-seq N --text -
  ├─ flf react add --thread N --message-seq M
  └─ flf agent append --file -
  │
  ▼
daemon validates, assigns sequence, and commits append atomically
  │
  ▼
thread export/import preserves the event stream
```

The daemon remains a transport and persistence layer. The external agent framework remains responsible for interpreting context and choosing when to run. Fluffle does not silently launch an agent from a message event.

## 10. Implementation order

1. **Baseline repair:** fix the tagged e2e compile failure and correct error-to-exit-code mapping.
2. **JSONL contract:** add event typing, portable references, reaction events, and import mapping with unit tests.
3. **Agent read loop:** add `after_seq`, expose `flf inbox`, and verify no duplicate or skipped messages.
4. **Agent write loop:** add stdin, atomic batch append, reaction-by-sequence, and response-loss handling.
5. **Transport hardening:** centralize finite HTTP clients, contexts, and structured CLI errors.
6. **Measured enhancements:** consider compact output, search, reaction browsing, and a human-triggered invocation command only after observing real usage.

Each step must preserve the existing daemon lifecycle, SQLite connection rule, and append-only permissions.

## 11. Testing strategy

- Unit-test JSONL parsing, serialization, sequence references, reaction targets, and import mapping.
- Integration-test an agent reading a thread and appending a reply plus reaction without raw HTTP lookups.
- Verify `after_seq` returns strictly newer messages in order, handles an empty result, and still returns a reaction added after the cursor on an older message.
- Verify a reaction added to a message that does not exist is a client error and creates no row.
- Verify CLI and TUI reads reject `null`, wrong shapes, trailing JSON, and zero identifiers, and that ambiguous mutation acknowledgements are `DELIVERY_UNKNOWN` rather than success.
- Force a failure during a multi-line append and verify that no partial batch is committed.
- Verify duplicate reactions and invalid cross-thread references return client errors.
- Verify every CLI error path emits the two-field JSON envelope and the correct exit code.
- Verify a stalled daemon times out rather than hanging the agent.
- Run both `go test ./...` and the build-tagged e2e suite before considering the slice complete.
- Keep the TUI unchanged unless the scenario requires a visible read surface.

## 12. Definition of done

The slice is complete when a fresh external agent can perform the following without hidden state, raw HTTP, or shell escaping:

1. Discover a repo-anchored thread.
2. Read its current context.
3. Identify a message to answer or react to.
4. Append a reply and reaction.
5. Resume from the returned sequence.
6. Export and import the thread without losing message, reply, reaction, or context relationships.

A feature that does not make this loop more reliable, portable, or observable does not belong in this slice.
