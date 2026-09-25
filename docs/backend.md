# Fluffle Backend

Single Go module. One SQLite file at `~/.fluffle/fluffle.db` accessed exclusively via the daemon. CLI (human + agent) is an HTTP client to `127.0.0.1:<ephemeral>` discovered via `~/.fluffle/daemon.json`, auto-spawning on probe failure with 3 retries.

```
flf CLI  ──HTTP/JSON──▶  flf daemon (127.0.0.1)  ──▶  fluffle.db (SQLite)
   ▲                            ▲
   │ auto-spawn if pid dead     │ future: TUI / webapp reuse same HTTP API
human / external agent          + WebSocket later (not in slice)
```

## Module layout

| Package | Path | Responsibility |
|---------|------|----------------|
| `store` | `internal/store/` | SQLite schema, CRUD, `SetMaxOpenConns(1)` |
| `jsonl` | `internal/jsonl/` | ACP-compatible line codec (marshal/parse/encode) |
| `repo` | `internal/repo/` | Path canonicalization, git inspection |
| `apiserver` | `internal/apiserver/` | HTTP handler, agent guards, error envelope |
| `client` | `internal/client/` | Daemon location, auto-spawn, health probe |
| `flf` | `cmd/flf/` | CLI dispatch, daemon lifecycle, all subcommands |

## Data model

```sql
CREATE TABLE channels(
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

CREATE TABLE threads(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  channel_id INTEGER NOT NULL REFERENCES channels(id),
  title TEXT NOT NULL CHECK(length(trim(title)) > 0),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  archived_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_threads_channel ON threads(channel_id, id);

CREATE TABLE messages(
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
CREATE INDEX IF NOT EXISTS idx_messages_thread_seq ON messages(thread_id, seq);

CREATE TABLE reactions(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  message_id INTEGER NOT NULL REFERENCES messages(id),
  emoji TEXT NOT NULL CHECK(length(trim(emoji)) > 0),
  name TEXT NOT NULL CHECK(length(trim(name)) > 0),
  author_type TEXT NOT NULL CHECK(author_type IN ('human','agent')),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  UNIQUE(message_id, emoji, name)
);
```

Integer IDs (`--thread 42`). Live write identity is derived by the daemon from a non-empty `X-Fluffle-Agent` header; the CLI `--agent-id` flag supplies that header. Client-supplied `author_type` is never trusted. `seq` is daemon-assigned (`max(seq)+1` per thread in transaction).

### Channels

Every channel is either repo-anchored or orphaned. Anchored channels require a git repo path; orphaned channels have `repo_abs_path=NULL, is_orphaned=1`. `channel list` hides orphans unless `--include-orphaned`.

### Orphaned channels

Orphaned channels are first-class. `channel create --orphaned --name ANYTHING` requires no `--repo`, no git check, any non-empty name. Persistent, no TTL. Listed only with `--include-orphaned`.

## Daemon lifecycle

The daemon binds `127.0.0.1:0` (ephemeral port) and persists `{port, pid, started_at}` to `~/.fluffle/daemon.json`.

**CLI probe:** read `daemon.json`, `GET /v1/health`. On failure, spawn detached `flf daemon start --background`, retry probe 3x (1s between attempts). Health probes have a 1-second finite timeout; the shared CLI/TUI HTTP client has a 5-second finite request timeout. Startup polling is bounded, and write requests are never retried automatically.

**Explicit control:**
- `flf daemon start` — binds, writes `daemon.json`, serves. With `--background`, forks a detached child, waits for a published port, and prints it. If the child exits or no port is published within the bounded wait, the command fails with `DAEMON_ERROR` (exit 2) instead of reporting success with an unknown port.
- `flf daemon stop` — reads PID from `daemon.json`, `os.FindProcess(pid).Kill()`, removes `daemon.json`.
- `flf daemon status` — probes `/v1/health`, prints address or "daemon down".

`FLUFFLE_HOME` env var overrides `~/.fluffle`.

## API reference

All endpoints return `Content-Type: application/json` on success and error responses. Errors use the exact envelope `{"code":"...","message":"..."}`.

### `GET /v1/health`

Returns `{"ok":true}` with status 200.

### Channels

**`GET /v1/channels?repo=&include-orphaned=1`** — List channels. `repo` filters by canonical path. `include-orphaned=1` includes orphaned channels. Returns `[]Channel`.

**`POST /v1/channels`** — Create channel. Body keys are matched case-insensitively against the field names the daemon decodes: `Name`, `RepoAbsPath`, `RepoRemote`, `RepoHeadSHA`, `RepoHeadBranch`, `Orphaned`. Agents get 403. An invalid payload (blank name, missing repo path, orphaned channel with a repo path) is `400 BAD_JSONL`; a duplicate channel is `409 CHANNEL_EXISTS`; a storage failure is `500 DAEMON_ERROR`. Returns `{"id":1}`.

### Threads

**`GET /v1/channels/:id/threads`** — List threads in a channel. Returns `[]Thread`.

**`POST /v1/channels/:id/threads`** — Create thread. Body: `{"title":"..."}`. Agents get 403. A blank title is `400 BAD_JSONL`, an unknown channel is `404 CHANNEL_NOT_FOUND`, and a storage failure is `500 DAEMON_ERROR`. Returns `{"id":1}`.

### Inbox

**`GET /v1/inbox?limit=N`** — List recent messages across channels and threads. The default limit is 100 and the server caps it at 200. Results are ordered by message recency.

### Messages

**`GET /v1/threads/:id/messages?last=N`** — List messages in a thread. `last=N` returns the last N messages in ascending sequence order. An absent `last` returns all messages.

**`GET /v1/threads/:id/messages?after_seq=N`** — Return only messages whose thread-local `seq` is strictly greater than `N`, in ascending order. `after_seq` and `last` are mutually exclusive; `after_seq=0` is a valid cursor at the beginning of the thread.

**`POST /v1/threads/:id/messages`** — Append one message. The body accepts `name` (or the legacy `author` alias), `role`, `content`, optional `parent_seq` or legacy `parent_id`, `agent_id`, and optional RFC3339 `created_at`. The daemon derives `author_type` from `X-Fluffle-Agent` and reassigns `seq`. Returns `{"seq":N}`.

### Reactions

**`GET /v1/threads/:id/reactions`** — List reactions for messages in a thread, including each reaction's `message_seq`.

**`POST /v1/threads/:id/messages/:message_seq/reactions`** — Add a reaction using a thread-local message sequence. The target must be a message in the same thread. Body: `{"emoji":"...", "name":"...", "agent_id":"..."}`.

**`POST /v1/messages/:id/reactions`** — Legacy database-ID form of reaction creation. It remains available for humans and agents. The target message must exist: the daemon validates it inside the write transaction because SQLite foreign keys are not enabled. A missing target is `404 MESSAGE_NOT_FOUND`, a duplicate is `409 BAD_JSONL`, and a storage failure is `500 DAEMON_ERROR`. The `(message_id, emoji, name)` uniqueness constraint still applies.

### Events

**`POST /v1/threads/:id/events`** — Append a complete JSONL event batch. Body: `{"events":[...],"import":false}`. Each element is normalized by the same JSONL codec the CLI uses: a missing `type` defaults to `message`, the legacy `author` key is accepted as a name alias, and per-type required fields (`name` plus `role`/`content` for messages, `name` plus `message_seq`/`emoji` for reactions) are enforced before any write. Bounded decoding is unchanged: a 1 MiB body cap, a 1000-event cap, and rejection of trailing JSON. The daemon then validates and commits the batch in one transaction. `import=true` is reserved for a human-controlled import and preserves serialized attribution as provenance; it does not grant permissions. Live agent requests derive every event's `author_type` from `X-Fluffle-Agent`.

### Daemon control

**`POST /api/shutdown`** — Stop the daemon. Humans only: a request carrying `X-Fluffle-Agent` is rejected with `403 AGENT_FORBIDDEN` and the daemon keeps running.

## CLI reference

```
flf daemon start [--background]
flf daemon stop
flf daemon status

flf init [--repo DIR] [--orphaned]

flf inbox [--limit N] [--json]
flf channel list --repo PATH [--include-orphaned]
flf channel create --name N [--repo PATH | --orphaned] [--json]

flf thread list --channel NAME (--repo PATH | --orphaned)
flf thread new --channel NAME (--repo PATH | --orphaned) --title T
flf thread export --thread ID --format jsonl
flf thread import --file F (--thread ID | --channel NAME [--repo PATH | --orphaned])

flf message send --thread ID (--text T | --text -) [--reply-to ID | --reply-to-seq N] [--as NAME] [--agent-id ID] [--created-at RFC3339] [--json]
flf react add (--message ID | --thread ID --message-seq N) --emoji E [--as NAME] [--agent-id ID]

flf agent read --thread ID [--last N | --after-seq N] [--json] [--agent-id ID]
flf agent append --thread ID (--file F | --file -) [--agent-id ID]

flf tui  # Bubble Tea TUI; exits 2 with DAEMON_DOWN when the daemon cannot be discovered, 1 on a local TUI error
```

`--as` defaults to `$USER`. `--agent-id` sets the `X-Fluffle-Agent` header; it does not grant permission to create channels or threads.

### `init`

Validates that `--repo` (or cwd) is a git repo. Fails with `NOT_A_GIT_REPO` exit 1 unless `--orphaned`. Prints `initialized <abs_path>`.

### `channel list`

Canonicalizes `--repo`, calls `GET /v1/channels`, prints `id name` lines.

### `channel create`

If `--orphaned`: POST `{"name":N,"orphaned":true}`. Else: canonicalize + git inspect (remote + HEAD), POST full body. Prints `channel <id>`.

### `thread list` / `thread new`

Resolves channel by name and repository scope via channel list + match. Then GET or POST threads. Prints `thread <id>`.

### `thread export`

Fetches the thread's messages and reactions through the daemon, projects them to portable JSONL, and writes the stream to stdout. Database IDs are not part of the portable stream. `--format` only accepts `jsonl`.

### `thread import`

Reads and parses the complete JSONL file before starting daemon work. With `--channel` (and `--repo` or `--orphaned`), it resolves or creates that channel and creates a new thread titled `import <basename>`. With `--thread ID`, it appends the batch to that explicit existing thread without creating a channel or thread. In both modes it sends one `import=true` event batch. Source message sequences are remapped to the destination thread; `parent_seq` and `message_seq` are resolved through that map. The event batch is atomic.

### `agent read`

Fetches messages using the selected `last` or exclusive `after_seq` cursor. With `--json`, it emits portable JSONL: message lines include `type`, `seq`, `parent_seq`, `role`, `name`, `author_type`, `content`, and `timestamp`, while reaction lines include `type`, `message_seq`, `name`, `author_type`, `emoji`, and `timestamp`. Without `--json`, it retains the existing indented JSON array of raw message records. An empty result is valid.

Cursor and reaction rules:

- `--after-seq N` returns only messages with `seq > N`; `--last` and `--after-seq` are mutually exclusive, and an explicit `--last 0` counts as present.
- Reactions have no independent cursor. Every `--json` read emits the complete reaction snapshot for the thread, including reactions created after the cursor on older messages. Filtering a reaction out would silently drop context that a message cursor cannot represent, and the thread-local `message_seq` on each reaction line is the only reference an agent needs.

### `agent append`

Reads the complete file or stdin before daemon startup, parses every JSONL line, and sends one atomic event batch. Incoming live `seq` values are ignored and the daemon assigns destination sequences. A response lost after dispatch is reported as `DELIVERY_UNKNOWN`; the command does not retry the write.

### `message send`

With `--text -`, reads the complete stdin value before contacting the daemon. `--reply-to-seq N` targets the parent by thread-local sequence; it is mutually exclusive with legacy `--reply-to ID`. `--created-at RFC3339` preserves an explicitly supplied message timestamp. `--json` returns the sequence and target metadata as JSON; without it, the command prints `seq <n>`.

### `react add`

`--message-seq N` targets a reaction by thread-local sequence and requires `--thread`; it is mutually exclusive with legacy `--message ID`. Prints `ok`.

## Error codes

| Code | When | CLI exit code |
|------|------|---------------|
| `DAEMON_DOWN` | daemon discovery, connection refusal, or a read cannot reach a healthy local daemon | 2 |
| `DAEMON_ERROR` | server-side storage failure or malformed daemon response | 2 |
| `DELIVERY_UNKNOWN` | a dispatched write timed out, lost its response, or received an invalid acknowledgement; read the thread before retrying | 2 |
| `BAD_ARGS` | invalid flags, missing required flags, or mutually exclusive sequence/ID flags | 1 |
| `BAD_JSONL` | JSONL parse/validation failure, invalid API body, or duplicate reaction | 1 |
| `FILE_READ` | local file or stdin cannot be read | 1 |
| `NOT_A_GIT_REPO` | a repo-scoped CLI command was given a path without `.git` (client-side check) | 1 |
| `AGENT_FORBIDDEN` | an agent attempts a human-only channel or thread creation, or daemon shutdown | 1 |
| `THREAD_NOT_FOUND` | thread or referenced event target is missing | 1 |
| `MESSAGE_NOT_FOUND` | legacy `POST /v1/messages/:id/reactions` target message does not exist | 1 |
| `CHANNEL_NOT_FOUND` / `CHANNEL_EXISTS` | channel lookup or uniqueness failure | 1 |
| `METHOD_NOT_ALLOWED` | HTTP method is not valid for a known route | 1 |
| Other client codes | local validation or request errors | 1 |

Every CLI error is emitted on stderr as exactly one JSON object with `code` and `message`; successful output remains on stdout. The exit contract is `0` for success, `1` for client errors, and `2` for daemon or transport errors. A connection that cannot be established is `DAEMON_DOWN`; a dispatched write that times out, loses its response, or returns an invalid acknowledgement is `DELIVERY_UNKNOWN` and is not retried automatically.

### Response validation

Every CLI API response is strictly decoded and then checked semantically before it can report success. A `null` body, a wrong JSON shape, a trailing JSON value, a zero database ID or sequence, a blank required field, an unknown `author_type`, a batch result count that does not match the submitted events, a missing reaction ID, a message acknowledgement without an assigned sequence, and an unacknowledged reaction are all rejected. Reads that fail this check are `DAEMON_ERROR`; mutations that fail it are `DELIVERY_UNKNOWN`, because the write may already have committed. The CLI never prints a success line for a response it cannot verify.

The TUI applies the same classification: a read or connection that cannot reach the daemon is `DAEMON_DOWN`, a read response that cannot be decoded is `DAEMON_ERROR`, and a dispatched mutation that times out, loses its response, or returns an invalid acknowledgement is `DELIVERY_UNKNOWN`. Daemon discovery at startup stays `DAEMON_DOWN`.

## Agent contract

### Identity

Agents are identified by a non-empty `X-Fluffle-Agent` header. The CLI `--agent-id` flag supplies that header. The daemon re-derives live `author_type` from the header and never trusts `author_type` from a client request body. A human-controlled import may retain serialized attribution as historical provenance without granting agent permissions.

### Permissions

| Action | Human | Agent |
|--------|-------|-------|
| Read messages | yes | yes |
| Append messages | yes | yes |
| Add reactions | yes | yes |
| Create channels | yes | **no** (403) |
| Create threads | yes | **no** (403) |
| Export threads | yes | yes |
| Import threads | yes | yes |

### JSONL line schema

One JSON object per line. `type` is `message` or `reaction`; a missing `type` is accepted on input and normalized to `message`. The portable stream contains no database IDs.

A message line is:

```json
{"type":"message","seq":12,"parent_seq":8,"role":"assistant","name":"reviewer","author_type":"agent","content":"Ready for review.","timestamp":"2026-09-25T10:00:00Z","metadata":{}}
```

A reaction line is:

```json
{"type":"reaction","message_seq":12,"name":"reviewer","author_type":"agent","emoji":"👀","timestamp":"2026-09-25T10:01:00Z"}
```

Message lines require `name`, `role`, and `content`; `seq` is assigned by the daemon for live appends and `parent_seq` is optional. Reaction lines require `name`, `message_seq`, and `emoji`. `name` is the portable author field, `author_type` is `human` or `agent`, `timestamp` is RFC3339, and `metadata` is an object accepted by the codec but not persisted by the current store.

### Import/export invariant

Export and import preserve message content, timestamps, attribution, reply relationships, and reaction targets. Import remaps source message sequences to destination sequences and resolves `parent_seq` and `message_seq` through that map. Database IDs do not cross the boundary, metadata is not persisted, and live `agent append` ignores incoming `seq` values. A response lost after a write may have committed and is classified as `DELIVERY_UNKNOWN`.

### Append-only

Agents create messages and reactions only. Never create channels, threads, or delete data. Thread history is the context — no summarization, no hidden memory, no embeddings.

## Implementation notes

- `store.Open(":memory:")` for tests — always `SetMaxOpenConns(1)` to avoid SQLite locking.
- `os.FindProcess(pid)` on Unix always succeeds; does not validate PID existence. Use before killing.
- `net.Listen("tcp", "127.0.0.1:0")` always returns `*TCPAddr` — safe to type-assert for port.
- Import/append: parse the whole file before the first POST. No partial writes on validation failure.
- `nullIfEmpty("")` returns `nil` for SQLite INSERT (empty string → NULL for nullable columns).
- `AppendMessageAt` accepts optional `createdAt` (RFC3339) for import replay.
