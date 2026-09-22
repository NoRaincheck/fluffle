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
  author TEXT NOT NULL CHECK(length(trim(author)) > 0),
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
  author TEXT NOT NULL CHECK(length(trim(author)) > 0),
  author_type TEXT NOT NULL CHECK(author_type IN ('human','agent')),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  UNIQUE(message_id, emoji, author)
);
```

Integer IDs (`--thread 42`). `author_type` is `human` unless `--agent-id` flag or `X-Fluffle-Agent` header is present. `seq` is daemon-assigned (`max(seq)+1` per thread in transaction).

### Channels

Every channel is either repo-anchored or orphaned. Anchored channels require a git repo path; orphaned channels have `repo_abs_path=NULL, is_orphaned=1`. `channel list` hides orphans unless `--include-orphaned`.

### Orphaned channels

Orphaned channels are first-class. `channel create --orphaned --name ANYTHING` requires no `--repo`, no git check, any non-empty name. Persistent, no TTL. Listed only with `--include-orphaned`.

## Daemon lifecycle

The daemon binds `127.0.0.1:0` (ephemeral port) and persists `{port, pid, started_at}` to `~/.fluffle/daemon.json`.

**CLI probe:** read `daemon.json`, `GET /v1/health`. On failure, spawn detached `flf daemon start --background`, retry probe 3x (1s between attempts).

**Explicit control:**
- `flf daemon start` — binds, writes `daemon.json`, serves. With `--background`, runs detached.
- `flf daemon stop` — reads PID from `daemon.json`, `os.FindProcess(pid).Kill()`, removes `daemon.json`.
- `flf daemon status` — probes `/v1/health`, prints address or "daemon down".

`FLUFFLE_HOME` env var overrides `~/.fluffle`.

## API reference

All endpoints return `Content-Type: application/json`. Errors use envelope `{"code":"...","message":"..."}`.

### `GET /v1/health`

Returns `{"ok":true}` with status 200.

### Channels

**`GET /v1/channels?repo=&include-orphaned=1`** — List channels. `repo` filters by canonical path. `include-orphaned=1` includes orphaned channels. Returns `[]Channel`.

**`POST /v1/channels`** — Create channel. Body: `{"name":"...", "repo_abs_path":"...", "repo_remote":"...", "repo_head_sha":"...", "orphaned":false}`. Agents get 403. Returns `{"id":1}`.

### Threads

**`GET /v1/channels/:id/threads`** — List threads in a channel. Returns `[]Thread`.

**`POST /v1/channels/:id/threads`** — Create thread. Body: `{"title":"..."}`. Agents get 403. Returns `{"id":1}`.

### Messages

**`GET /v1/threads/:id/messages?last=N`** — List messages in a thread. `last=N` returns last N messages. ORDER BY seq ASC. Returns `[]Message`.

**`POST /v1/threads/:id/messages`** — Append message. Body: `{"author":"...", "role":"user", "content":"...", "agent_id":"...", "created_at":"RFC3339"}`. `agent_id` or `X-Fluffle-Agent` header sets `author_type=agent`. `created_at` is optional (defaults to now). Daemon reassigns `seq`. Returns `{"seq":1}`.

### Reactions

**`POST /v1/messages/:id/reactions`** — Add reaction. Body: `{"emoji":"...", "author":"...", "agent_id":"..."}`. `UNIQUE(message_id, emoji, author)` constraint. Returns `{"ok":true}`.

## CLI reference

```
flf daemon start [--background]
flf daemon stop
flf daemon status

flf init [--repo DIR] [--orphaned]

flf channel list --repo PATH [--include-orphaned]
flf channel create --name N (--repo PATH | --orphaned)

flf thread list --channel NAME --repo PATH
flf thread new --channel NAME --repo PATH --title T
flf thread export --thread ID --format jsonl
flf thread import --file F --channel NAME (--repo PATH | --orphaned)

flf message send --thread ID --text T [--as NAME] [--agent-id ID]
flf react add --message ID --emoji E [--as NAME] [--agent-id ID]

flf agent read --thread ID [--last N] [--agent-id ID]
flf agent append --thread ID --file F [--agent-id ID]

flf tui  # deferred: exits 1 with "backend-only milestone: tui deferred"
```

`--as` defaults to `$USER`. Presence of `--agent-id` sets `author_type=agent`.

### `init`

Validates that `--repo` (or cwd) is a git repo. Fails with `NOT_A_GIT_REPO` exit 1 unless `--orphaned`. Prints `initialized <abs_path>`.

### `channel list`

Canonicalizes `--repo`, calls `GET /v1/channels`, prints `id name` lines.

### `channel create`

If `--orphaned`: POST `{"name":N,"orphaned":true}`. Else: canonicalize + git inspect (remote + HEAD), POST full body. Prints `channel <id>`.

### `thread list` / `thread new`

Resolves channel by name via channel list + match. Then GET or POST threads. Prints `thread <id>`.

### `thread export`

GET all messages, convert to JSONL lines, write to stdout. `--format` only accepts `jsonl`.

### `thread import`

Parse JSONL file first (fail on bad lines before any POST). Resolve or create channel. POST new thread titled `import <basename>`. Replay lines.

### `agent read`

GET messages, convert to JSONL, write to stdout. Same as `thread export` but with optional `--last N`.

### `agent append`

Parse JSONL file (fail on bad lines before any POST). POST each line to daemon. Daemon reassigns `seq`. No partial write on parse failure.

### `message send`

POST to `/v1/threads/:id/messages` with `author, role=user, content, agent_id`. Prints `seq <n>`.

### `react add`

POST to `/v1/messages/:id/reactions`. Prints `ok`.

## Error codes

| Code | When | CLI exit code |
|------|------|---------------|
| `DAEMON_DOWN` | probe fails even after 3 spawn retries | 2 |
| `NOT_A_GIT_REPO` | `--repo` without `.git`, not `--orphaned` | 1 |
| `AGENT_FORBIDDEN` | agent hits human-only endpoint (channel create, thread new) | 1 |
| `THREAD_NOT_FOUND` | bad thread ID | 1 |
| `CHANNEL_NOT_FOUND` | bad channel ID or unknown route | 1 |
| `BAD_JSONL` | parse failure on append/import, validation failure, conflict (409/400) | 1 |
| `FILE_READ` | local file unreadable (agent append/import) | 1 |

All JSON error bodies are decoded and reprinted as `CODE: message` on stderr. Transport failures → `DAEMON_DOWN`.

## Agent contract

### Identity

Agents identified by `X-Fluffle-Agent` header (non-empty). Daemon re-derives `author_type` from header + `agent_id` body field — never trust `author_type` from client request bodies.

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

One JSON object per line. ACP-compatible.

```json
{"seq":12,"role":"user","author":"alice","author_type":"human","content":"...","timestamp":"2026-09-22T00:00:00Z","metadata":{}}
```

Fields: `seq` (int64), `role` (user/assistant/system), `author` (string), `author_type` (human/agent), `content` (string), `timestamp` (RFC3339), `metadata` (object, accepted but not persisted in MVP).

### Import/export invariant

`export → import → export` is byte-identical modulo `seq`. Metadata is accepted on import but not persisted (no column). `agent append` ignores incoming `seq` — daemon reassigns `max+1`.

### Append-only

Agents create messages and reactions only. Never create channels, threads, or delete data. Thread history is the context — no summarization, no hidden memory, no embeddings.

## Implementation notes

- `store.Open(":memory:")` for tests — always `SetMaxOpenConns(1)` to avoid SQLite locking.
- `os.FindProcess(pid)` on Unix always succeeds; does not validate PID existence. Use before killing.
- `net.Listen("tcp", "127.0.0.1:0")` always returns `*TCPAddr` — safe to type-assert for port.
- Import/append: parse the whole file before the first POST. No partial writes on validation failure.
- `nullIfEmpty("")` returns `nil` for SQLite INSERT (empty string → NULL for nullable columns).
- `AppendMessageAt` accepts optional `createdAt` (RFC3339) for import replay.
