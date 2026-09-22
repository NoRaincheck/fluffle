# Fluffle Core Backend Design — Daemon + SQLite + CLI (MVP Slice)

Date: 2026-09-22 | Status: draft for review | Scope: backend only (TUI + webapp deferred)

## 1. Context

Fluffle (`flf`) is a local-first, TUI-first communication hub anchored to Git repos
(see `VISION.md`). The full vision spans daemon, CLI, TUI, webapp, and an agent
JSONL interface — too large for one spec. This spec covers the first buildable
slice: **core backend** (SQLite daemon + CLI control plane + agent append API).

Decisions locked during brainstorming:
- Daemon lifecycle: **auto-spawn** (CLI starts daemon if down, kata `daemon locate` pattern).
- Agent auth: **localhost-only, no token** for MVP.
- Repo-anchoring: **strict validation** (must be a git repo unless `--orphaned`).
- Message model: **unified table + API guards** (matches kata / roborev / agentsview precedent).
- Approach: **kata-aligned** (SQLite + localhost HTTP, alias-style repo resolution).

Ecosystem precedent surveyed:
- kata `~/.kata/kata.db`: `comments(issue_id, author TEXT, body)`, `events(actor, type, payload)`,
  `project_aliases(identity = normalized git remote | local://abs, kind git|local)`. No
  `author_type` column; human/agent distinguished by author string convention.
- roborev `~/.roborev/reviews.db`: `reviews(job_id, agent TEXT, output)` — same unified pattern.
- agentsview `~/.agentsview/sessions.db`: `messages(session_id, ordinal UNIQUE, role, content)` —
  role distinguishes, export is ordered dump.

## 2. Architecture

Single Go module. One SQLite file `~/.fluffle/fluffle.db` accessed **only** via the
daemon. CLI (including `flf agent …`) is an HTTP client to `127.0.0.1:<ephemeral>`.
Daemon address cached in `~/.fluffle/daemon.json` (`port, pid, started_at`).

```
flf CLI  ──HTTP/JSON──▶  flf daemon (127.0.0.1)  ──▶  fluffle.db (SQLite)
   ▲                            ▲
   │ auto-spawn if pid dead     │ future: TUI / webapp reuse same HTTP API
human / external agent          + WebSocket later (not in slice)
```

Non-goals (explicit): TUI, webapp, vector DB / embeddings, FTS, federation,
PostgreSQL sync, identity tokens, OpenAPI codegen.

## 3. Data model

```sql
CREATE TABLE channels(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL CHECK(length(trim(name)) > 0),
  repo_abs_path TEXT,               -- NULL when is_orphaned = 1
  repo_remote TEXT,                 -- `git remote get-url origin`, nullable
  repo_head_sha TEXT,               -- HEAD snapshot at creation, nullable
  is_orphaned INTEGER NOT NULL DEFAULT 0 CHECK(is_orphaned IN (0,1)),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  archived_at TEXT,
  CHECK ((is_orphaned = 1 AND repo_abs_path IS NULL)
      OR (is_orphaned = 0 AND repo_abs_path IS NOT NULL)),
  UNIQUE(name, repo_abs_path)
);
CREATE UNIQUE INDEX uniq_orphan_channel_name ON channels(name) WHERE is_orphaned = 1;
CREATE TABLE threads(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  channel_id INTEGER NOT NULL REFERENCES channels(id),
  title TEXT NOT NULL CHECK(length(trim(title)) > 0),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  archived_at TEXT
);
CREATE INDEX idx_threads_channel ON threads(channel_id, id);
CREATE TABLE messages(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  thread_id INTEGER NOT NULL REFERENCES threads(id),
  seq INTEGER NOT NULL,             -- daemon-assigned, max+1 per thread in tx
  author TEXT NOT NULL CHECK(length(trim(author)) > 0),
  author_type TEXT NOT NULL CHECK(author_type IN ('human','agent')),
  role TEXT NOT NULL CHECK(role IN ('user','assistant','system')),
  content TEXT NOT NULL CHECK(length(trim(content)) > 0),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  UNIQUE(thread_id, seq)
);
CREATE INDEX idx_messages_thread_seq ON messages(thread_id, seq);
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

Integer IDs match VISION CLI (`--thread 42`). ULIDs deferred until federation needs them.

## 4. Daemon + transport

- Bind `127.0.0.1:0`, persist `{port, pid, started_at}` to `~/.fluffle/daemon.json`.
- Every CLI run: read file, probe `/v1/health`; on failure spawn detached daemon, retry 3x.
- `flf daemon start|stop|status` for explicit control.
- Hand-rolled HTTP+JSON `GET/POST /v1/…` (no codegen for MVP):
  `GET /v1/health`, `GET /v1/channels?repo=`, `POST /v1/channels`,
  `GET /v1/channels/:id/threads`, `POST /v1/channels/:id/threads`,
  `GET /v1/threads/:id/messages?last=N`, `POST /v1/threads/:id/messages`,
  `POST /v1/messages/:id/reactions`.
- Errors as typed JSON `{code, message}`; CLI exit 0 ok / 1 user error / 2 daemon error.

## 5. CLI surface (MVP)

```
flf init [--orphaned]                    # validate cwd is git repo (skip if orphaned)
flf daemon start|stop|status
flf channel list --repo PATH [--include-orphaned]
flf channel create --name N (--repo PATH | --orphaned)
flf thread list --channel NAME --repo PATH
flf thread new --channel NAME --repo PATH --title T
flf message send --thread ID --text T [--as NAME] [--agent-id ID]
flf react add --message ID --emoji E [--as NAME] [--agent-id ID]
flf agent read --thread ID [--last N]    # JSONL to stdout
flf agent append --thread ID --file F [--agent-id ID]
flf thread export --thread ID --format jsonl
flf thread import --file F --channel NAME (--repo PATH | --orphaned)
flf tui                                  # deferred: error "backend-only milestone"
```

`--as` defaults to `$USER`. Presence of `--agent-id` (or `X-Fluffle-Agent` header)
sets `author_type=agent`; otherwise `human`.

## 6. Agent contract

- Guards enforced in daemon handlers (not DB splits): agents denied
  `channel create`, `thread new`, archive/delete ops → `403 {code: AGENT_FORBIDDEN}`.
  Agents allowed: message append, reaction add, read/export.
- JSONL line schema (ACP-compatible, one object per line):
  `{"seq":12,"role":"user|assistant|system","author":"alice|pi-agent",`
  `"author_type":"human|agent","content":"...","timestamp":"RFC3339","metadata":{}}`.
- `agent read` = `SELECT … ORDER BY seq ASC LIMIT last N`. `agent append` parses each
  line, ignores incoming `seq`, daemon reassigns `max+1` in a transaction.
- Thread history is the context: no summarization, no hidden memory, no embeddings.
- Handoff: `export` = full `read`; `import` creates a new thread and replays lines
  preserving `role/author/content/timestamp` (metadata accepted but not persisted in MVP — no column). Round-trip invariant:
  export → import → export is byte-identical modulo `seq`.

## 7. Repo-anchoring + orphaned channels

- `channel create --repo PATH`: canonicalize (`Abs` + `EvalSymlinks`); fail
  `NOT_A_GIT_REPO` when no `.git`. Persist abs path + `git remote get-url origin`
  (nullable) + HEAD SHA. Later divergence (moved dir, new remote) warns only.
- Every repo-scoped read resolves `--repo` → canonical path → channel lookup
  (kata `--workspace` pattern).
- **Orphaned channels are first-class and arbitrary**: `channel create --orphaned
  --name ANYTHING` requires no `--repo`, no git check, any name (modulo non-empty).
  Stored with `is_orphaned=1, repo_abs_path=NULL`. Persistent, no TTL for MVP;
  manual `channel archive` only. `channel list` hides orphans unless
  `--include-orphaned` (or `--orphaned-only`).

## 8. Error handling

| Code | When | CLI behaviour |
|---|---|---|
| `DAEMON_DOWN` | probe fails even after spawn | exit 2, stderr hint |
| `NOT_A_GIT_REPO` | `--repo` without `.git` | exit 1, suggest `--orphaned` |
| `AGENT_FORBIDDEN` | agent hits human-only endpoint | exit 1, list allowed cmds |
| `THREAD_NOT_FOUND` / `CHANNEL_NOT_FOUND` | bad ID | exit 1 |
| `BAD_JSONL` | append/import parse failure OR channel/thread/reaction conflict or validation failure (409/400) | exit 1, no partial write |
| `FILE_READ` | local file unreadable (agent append/import) | exit 1 |

## 9. Testing

- Store layer (`sqlite :memory:`): seq-assignment concurrency, unique-constraint
  behaviour, orphaned-vs-anchored creation matrix.
- HTTP layer (`httptest`): permission matrix — every endpoint × {human, agent} —
  asserting allow/deny; JSONL round-trip byte-identity test.
- CLI golden tests: `export → import → export` fixture, error-message snapshots.
- Out of scope for slice: TUI/web tests, load tests, migration tests (fresh DB).

## 10. Milestones (slice only)

1. Store + schema + validation. 2. Daemon + auto-spawn + health. 3. Channel/thread/
   message endpoints + CLI. 4. Agent guards + JSONL read/append/export/import.
   5. Orphaned flows + error codes + test matrix green.
