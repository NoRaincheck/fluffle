# Fluffle Backend

Single Go module. One SQLite file at `~/.fluffle/fluffle.db` accessed exclusively via the daemon. CLI (human + agent) is an HTTP client to `127.0.0.1:<ephemeral>` discovered via `~/.fluffle/daemon.json`, auto-spawning on probe failure with 3 retries.

```
flf CLI  ──HTTP/JSON──▶  flf daemon (127.0.0.1)  ──▶  fluffle.db (SQLite)
   ▲                            ▲   │
   │ auto-spawn if pid dead     │   └──▶ local agent subprocess (one per @mention)
human / external agent          │        spawned and reaped by the daemon
                               + future: TUI / webapp reuse same HTTP API
                                 + WebSocket later (not in slice)
```

## Module layout

| Package | Path | Responsibility |
|---------|------|----------------|
| `store` | `internal/store/` | SQLite schema, CRUD, `SetMaxOpenConns(1)` |
| `jsonl` | `internal/jsonl/` | ACP-compatible line codec (marshal/parse/encode) |
| `repo` | `internal/repo/` | Path canonicalization, git inspection |
| `mentions` | `internal/mentions/` | Leading `@name` mention grammar |
| `agentcfg` | `internal/agentcfg/` | TOML agent config: parse, validate, repo-over-global resolution, mtime cache |
| `runner` | `internal/runner/` | Bounded subprocess execution: process group, finite timeout, capped output streams |
| `session` | `internal/session/` | Session manager: concurrency cap, prompt assembly, output streaming, reply resolution |
| `apiserver` | `internal/apiserver/` | HTTP handler, agent guards, error envelope, mention trigger |
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

CREATE TABLE agent_sessions(
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
CREATE UNIQUE INDEX uniq_session_trigger ON agent_sessions(trigger_message_id, agent_name);
CREATE INDEX idx_sessions_thread ON agent_sessions(thread_id, id);
CREATE INDEX idx_sessions_status ON agent_sessions(status);

CREATE TABLE agent_session_events(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id INTEGER NOT NULL REFERENCES agent_sessions(id),
  seq INTEGER NOT NULL,
  type TEXT NOT NULL CHECK(type IN ('prompt','stdout','stderr','exit','error')),
  content TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  UNIQUE(session_id, seq)
);
CREATE INDEX idx_session_events ON agent_session_events(session_id, seq);
```

The whole schema is applied with `CREATE TABLE IF NOT EXISTS` on every `store.Open`, so the two session tables appear in an existing database without a version bump.

Integer IDs (`--thread 42`). Live write identity is derived by the daemon from a non-empty `X-Fluffle-Agent` header; the CLI `--agent-id` flag supplies that header. Client-supplied `author_type` is never trusted. `seq` is daemon-assigned (`max(seq)+1` per thread in transaction). `agent_sessions` and `agent_session_events` are an audit trail for local agent runs, not part of the append-only message/reaction contract: nothing in them is portable JSONL and nothing in them is ever sent back to an agent.

### Channels

Every channel is either repo-anchored or orphaned. Anchored channels require a git repo path; orphaned channels have `repo_abs_path=NULL, is_orphaned=1`. `channel list` hides orphans unless `--include-orphaned`.

### Orphaned channels

Orphaned channels are first-class. `channel create --orphaned --name ANYTHING` requires no `--repo`, no git check, any non-empty name. Persistent, no TTL. Listed only with `--include-orphaned`.

## Agent sessions

A human message whose content opens with one or more `@name` mentions starts one session per resolvable name. The daemon owns the subprocess; there is no daemon-side agent loop and no agent framework SDK.

### Configuration

Agent definitions are TOML, not JSON. Two files are read: `~/.fluffle/config.toml` and, for a repo-anchored thread, `<repo>/.flf.toml`. Each file is a list of `[[agents]]` tables:

```toml
[[agents]]
name = "reviewer"           # required, ^[a-z0-9][a-z0-9_-]*$
description = "reviews diffs"
command = "claude"          # required
args = ["-p", "{prompt}"]  # optional
reply = "auto"             # stdout | cli | auto, default auto
timeout_secs = 300         # positive, default 300
env = { ANTHROPIC_API_KEY = "…" }
system_prompt = "You are a meticulous reviewer."
```

`name` and `command` are required — a name must match `^[a-z0-9][a-z0-9_-]*$` and a command must be non-blank. `reply` defaults to `auto` and must be one of the three values when set; `timeout_secs` defaults to 300 and must be positive. An unknown key anywhere in the file is a hard error naming every unrecognized key, not a warning — a typo in `timeouts_secs` must not silently fall back to the default. Parsed files are cached by path and re-read when the file's mtime changes.

`~/.fluffle/daemon.json` stays JSON; only agent definitions are TOML.

### Resolution order

Resolution is repo-first by agent name: the global file is loaded, then `<repo>/.flf.toml` is merged over it, and a same-named entry in the repo file replaces the global entry wholesale rather than merging field by field. A repo file may therefore redefine `reply` and lose the global `env`. A thread with no repo path (an orphaned channel) resolves against the global file only. `GET /v1/agents` refuses a `repo` whose `.flf.toml` is a symlink, and `flf agent list --repo` refuses a path that does not exist.

A mention of a name that does not resolve is inert: no session row, no subprocess, no error. The message is stored as written.

### Trigger

Sessions are created only from a **human** message append — `POST /v1/threads/:id/messages` or `POST /v1/threads/:id/events` without `X-Fluffle-Agent`. A request carrying that header is a human-only gate: an agent cannot start a session, and therefore an agent cannot start an agent. Scanning happens after the message is committed, so a session's prompt always includes its own trigger message. A mention that is not leading (`text @reviewer`) does not trigger.

`mentions.Parse` accepts a run of `@name` tokens at the very start of the content, allowing whitespace between them, and returns each distinct name once. A name is `[A-Za-z0-9]` plus `_`/`-` after the first byte. It also returns the text following the mention run, but the trigger discards that value: the prompt quotes the trigger message in full, mentions and all.

`UNIQUE(trigger_message_id, agent_name)` makes a repeated trigger on the same message a no-op rather than a second run.

### Execution

At most 4 sessions run concurrently; further sessions stay `queued` and are admitted as slots free. A queued session is visible in the API before it starts, so the TUI can show it.

The prompt is assembled as: the agent's `system_prompt` (if any), a `## Thread` header carrying channel, thread title, and repo path, a `## History` section of the thread's JSONL message and reaction lines, a `## Request` section quoting the trigger message by sequence, and — when the reply mode is not `stdout` — a `## Replying` section with the exact `flf message send --reply-to-seq` command to run.

How that prompt reaches the subprocess is decided by `{prompt}` in `args`, and the two branches are mutually exclusive:

| `args` | Prompt delivery | stdin |
|--------|-----------------|-------|
| Any element **contains** `{prompt}` | Substituted into every element that contains it, via `strings.ReplaceAll` — so `args = ["-p", "{prompt}"]` becomes `["-p", "<the whole prompt>"]` | **empty** |
| No element contains `{prompt}` | Passed through unchanged | the whole prompt, on stdin |

`{prompt}` is matched as a substring, not as a whole element, so `args = ["--query={prompt}"]` also works. A config that forgets `{prompt}` entirely therefore gets the prompt on stdin — which is the right default for a filter-style CLI, and silently the wrong thing for a tool that only accepts the prompt as an argument. There is no warning either way.

The subprocess runs with the thread's repo as its working directory. Where the thread has no repo — an orphaned channel — `req.Cwd` is left empty, so `cmd.Dir` is empty and the subprocess inherits the **daemon's** cwd, not the user's. `env` is layered over the daemon's environment by key, so a config `env` entry replaces the inherited value rather than adding a duplicate. Each run gets its own process group, so cancellation kills the whole tree, and it is bounded by a finite `timeout_secs` and at most 1 MiB of retained output per stream (head and tail kept, the middle elided).

On startup the daemon reconciles: any session still `queued` or `running` from a previous process is marked `canceled` with `daemon restarted while <status>`. On shutdown, still-queued sessions are marked `canceled` and running subprocesses are killed, without waiting for their goroutines to unwind.

### Reply modes

| `reply` | Behavior |
|---------|----------|
| `stdout` | The daemon posts the captured stdout as a new agent-authored message parented to the trigger. |
| `cli` | The agent posts its own reply with `flf message send --reply-to-seq`. If it does not, the session fails: `agent did not reply (reply=cli)`. |
| `auto` | Both. After the subprocess exits, the daemon waits up to 2 s for an agent message with this agent's name at a sequence after the trigger; if one appears it is used as-is, otherwise stdout is posted for the agent. |

A reply is posted as `author_type=agent` with the agent's name and the trigger as its parent, so it is an ordinary thread message and an agent's `flf agent read` sees it. A run whose captured output is empty finishes `succeeded` with no message rather than posting a blank one.

### A session transcript is never agent context

`agent_session_events` records what an agent was asked and what it printed. It is **never** included in any prompt, and no run's history is fed to the next run. The thread's JSONL history remains the only context an agent receives. The transcript exists so a human can audit a run, not so an agent can remember one.

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

**`POST /v1/threads/:id/messages`** — Append one message. The body accepts `name` (or the legacy `author` alias), `role`, `content`, optional `parent_seq` or legacy `parent_id`, `agent_id`, and optional RFC3339 `created_at`. The daemon derives `author_type` from `X-Fluffle-Agent` and reassigns `seq`. Returns `{"seq":N}`. A human append whose content opens with `@name` starts an agent session after the commit; an agent append never does.

### Reactions

**`GET /v1/threads/:id/reactions`** — List reactions for messages in a thread, including each reaction's `message_seq`.

**`POST /v1/threads/:id/messages/:message_seq/reactions`** — Add a reaction using a thread-local message sequence. The target must be a message in the same thread. Body: `{"emoji":"...", "name":"...", "agent_id":"..."}`.

**`POST /v1/messages/:id/reactions`** — Legacy database-ID form of reaction creation. It remains available for humans and agents. The target message must exist: the daemon validates it inside the write transaction because SQLite foreign keys are not enabled. A missing target is `404 MESSAGE_NOT_FOUND`, a duplicate is `409 BAD_JSONL`, and a storage failure is `500 DAEMON_ERROR`. The `(message_id, emoji, name)` uniqueness constraint still applies.

### Events

**`POST /v1/threads/:id/events`** — Append a complete JSONL event batch. Body: `{"events":[...],"import":false}`. Each element is normalized by the same JSONL codec the CLI uses: a missing `type` defaults to `message`, the legacy `author` key is accepted as a name alias, and per-type required fields (`name` plus `role`/`content` for messages, `name` plus `message_seq`/`emoji` for reactions) are enforced before any write. Bounded decoding is unchanged: a 1 MiB body cap, a 1000-event cap, and rejection of trailing JSON. The daemon then validates and commits the batch in one transaction. `import=true` is reserved for a human-controlled import and preserves serialized attribution as provenance; it does not grant permissions. Live agent requests derive every event's `author_type` from `X-Fluffle-Agent`. A human batch whose committed messages carry a leading mention starts agent sessions after the commit.

### Sessions

**Wire format.** The `store.Session` and `store.SessionEvent` structs carry no JSON tags, so `encoding/json` emits the **Go field names verbatim** — `ID`, `ThreadID`, `TriggerMessageID`, `AgentName`, `Status`, `ReplyMode`, `Command`, `Cwd`, `ExitCode`, `Error`, `ReplyMessageID`, `StartedAt`, `FinishedAt`, `CreatedAt`, and for events `ID`, `SessionID`, `Seq`, `Type`, `Content`, `CreatedAt`. This is not snake_case, and a client that assumes it is will silently read zero values. Nullable columns (`Cwd`, `ExitCode`, `Error`, `ReplyMessageID`, `StartedAt`, `FinishedAt`) are `null` until set. The one exception is `GET /v1/agents`, whose items are a tagged struct and therefore snake_case.

**`GET /v1/threads/:id/sessions`** — Sessions in a thread, oldest first (`ORDER BY id ASC`), metadata only: no events. An empty thread returns `[]`, never `null`. A non-positive id is `400 BAD_JSONL`, an unknown thread is `404 THREAD_NOT_FOUND`, and a non-`GET` method is `405 METHOD_NOT_ALLOWED`. Readable by agents.

**`GET /v1/sessions/:id`** — One session plus its events in `seq` order. Returns `{"session":{...},"events":[...]}`, with `events` `[]` rather than `null` for a session that has not run. A missing session is `404 SESSION_NOT_FOUND`; a non-positive id is `400 BAD_JSONL`; any other path under `/v1/sessions/` is `404 SESSION_NOT_FOUND`. Readable by agents.

**`POST /v1/sessions/:id/cancel`** — Cancel a non-terminal session: a running subprocess is killed, a queued one is finished `canceled` without running. Humans only — a request carrying `X-Fluffle-Agent` is `403 AGENT_FORBIDDEN`. A missing session is `404 SESSION_NOT_FOUND`, a session already `succeeded`/`failed`/`canceled` is `409 SESSION_FINISHED`, and a daemon with no execution wired is `503 DAEMON_ERROR`. Returns `{"ok":true}`.

**`GET /v1/agents?repo=<abs path>`** — Resolved agent names for a repo scope, each with `description`, `command`, `reply`, and the config `source` file it came from. Returns `{"agents":[{"name","description","command","reply","source"}]}`, sorted by name, `[]` when the daemon has no agent config loaded. An absent or empty `repo` resolves the global file only. A config that fails to parse, or a `.flf.toml` that is a symlink, is `500 DAEMON_ERROR` with the reason logged, never returned. Readable by agents. The symlink refusal lives on this route only — a session started from a thread whose repo has a symlinked `.flf.toml` still resolves through it, so the two paths do not currently agree.

There is no `POST /v1/sessions` and no `flf agent invoke`: a session can only be started by a human mention.

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

flf agent list [--repo PATH] [--json]
flf agent session --id N [--json]

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

### `agent list`

Resolves the agent set for a scope and prints one tab-separated line per agent: `name`, `command`, `reply`, `source`, `description` (`-` when the agent has no description). `--repo` must name an existing path and is canonicalized to an absolute path before it is sent; it is not required to be a git repository. Without `--repo`, the global `~/.fluffle/config.toml` alone is resolved. `--json` emits `{"agents":[{"name","description","command","reply","source"}]}` instead.

### `agent session`

Fetches one session and its full event list by database id. `--id` is required and must be a positive integer; anything else is `BAD_ARGS`. The text form prints a header line (`session N  agent=…  thread=…  trigger=…  status=…  reply=…`, with the start/finish timestamps appended once both exist), then `command:`, `exit:` (when the run produced one), `cwd:` (`none` when the thread had no repo), `reply: message N` (when the daemon posted the reply itself), `error:` (when the session failed), and finally one `seq  type  content` line per event in order. `--json` emits the raw `{"session":…,"events":[…]}` payload with Go field names, so the output is directly comparable to `GET /v1/sessions/:id`.

There is deliberately no `flf agent invoke`: the human `@mention` in a message is the only trigger, which keeps the reason a session exists visible in the thread.

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
| `NOT_A_GIT_REPO` | a repo-scoped CLI command was given a path without `.git`, or `flf agent list --repo` was given a path that does not exist (client-side check) | 1 |
| `AGENT_FORBIDDEN` | an agent attempts a human-only channel or thread creation, daemon shutdown, or session cancellation | 1 |
| `THREAD_NOT_FOUND` | thread or referenced event target is missing | 1 |
| `SESSION_NOT_FOUND` | the requested session does not exist, or the path is not a known session route | 1 |
| `SESSION_FINISHED` | a cancel was requested for a session that already reached a terminal status | 1 |
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
| Read agent sessions and list configured agents | yes | yes |
| Create channels | yes | **no** (403) |
| Create threads | yes | **no** (403) |
| Start an agent session | yes | **no** — only a human mention does, and an append carrying `X-Fluffle-Agent` is never scanned |
| Cancel an agent session | yes | **no** (403) |
| Export threads | yes | yes |
| Import threads | yes | yes |

The two session rows are the load-bearing part of the append-only rule. If an agent could start a session it could run arbitrary local commands chosen by another agent's output, and if it could cancel one it could kill a human's work; neither is an append, so neither is permitted.

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

Agents create messages and reactions only. Never create channels, threads, or delete data. They cannot start or cancel agent sessions, so an agent can never cause a subprocess to launch or die. Thread history is the context — no summarization, no hidden memory, no embeddings. The session tables are an audit trail for a human to read, and are never replayed into a prompt.

## Design rationale

### Scope decision

The agent contract hardening is driven by a single goal: a small, reliable, repo-scoped agent loop.

1. A human creates or selects a thread in a repo-anchored channel.
2. An external agent reads the thread through the CLI.
3. The agent appends a reply and reaction using references from the read result.
4. The thread can be exported and imported as a complete, portable context stream.
5. The agent can resume from a monotonic cursor and discover new work through the inbox.

This replaces the original six-feature roadmap. The goal is not feature parity with Buzz.

### What was rejected

These are product expansions, not prerequisites for a useful local agent loop. They are explicitly out of scope:

- Nostr protocol adapters, DMs, profiles, presence, moderation, media, GIFs, notes.
- Agent memory, embeddings, summaries, or opaque per-agent state. A session transcript is an audit record, not memory: it is never read back as context.
- Canvas revisions, channel documents, or any second source of project context.
- Schedules, webhooks, inotify reloads, and plugins. A session runs because a human mentioned an agent, not on a timer or a file event.
- First-class Git patches, issues, PRs, repository hosting, or branch tracking.
- Multi-repo project administration.
- TUI redesign or new TUI-only state.
- Compact output projections, server-side search, or reaction browsing beyond agent needs.

### Implementation order

1. **Baseline repair:** fix the tagged e2e compile failure and correct error-to-exit-code mapping.
2. **JSONL contract:** add event typing, portable references, reaction events, and import mapping.
3. **Agent read loop:** add `after_seq`, expose `flf inbox`, verify no duplicate or skipped messages.
4. **Agent write loop:** add stdin, atomic batch append, reaction-by-sequence, response-loss handling.
5. **Transport hardening:** centralize finite HTTP clients, contexts, structured CLI errors.
6. **Measured enhancements:** consider compact output, search, and reaction browsing only after observing real usage. Human-triggered invocation shipped as the `@mention` trigger rather than as a separate invoke command.

### Key design choices

- **Thread-local `seq` as the portable reference.** Database `id` is internal only. On import, source sequences are remapped to destination sequences and `parent_seq`/`message_seq` are resolved through that map.
- **Parse-before-write atomicity.** A multi-line append must commit all events or none. A response lost after dispatch is `DELIVERY_UNKNOWN` — no automatic retry.
- **No `retryable` field.** The error envelope remains exactly `{"code","message"}`. Changing this requires an explicit contract amendment.
- **Reactions have no independent cursor.** Every JSONL read emits the complete reaction snapshot. Filtering reactions out would silently drop context the cursor cannot represent.
- **`after_seq` and `last` are mutually exclusive.** An explicit `--last 0` counts as present, so `--after-seq 0 --last 0` is a client error rather than a silent precedence choice.
- **Live `seq` values are ignored on appends.** The daemon assigns destination sequences. Only import preserves source sequences for remapping.
- **A session is a transcript, not memory.** `agent_session_events` records what an agent was asked and what it printed. It is never included in any prompt, and one run's transcript is never carried into the next. The thread's JSONL remains the only agent context, which is what lets VISION tenet 5 hold while a run is still auditable after the fact.
- **A human mention is the only trigger.** There is no invoke command and no agent-reachable start route, so every session has a committed human message in the thread to point at and the append-only rule cannot be laundered into process execution.

## Implementation notes

- `store.Open(":memory:")` for tests — always `SetMaxOpenConns(1)` to avoid SQLite locking.
- `os.FindProcess(pid)` on Unix always succeeds; does not validate PID existence. Use before killing.
- `net.Listen("tcp", "127.0.0.1:0")` always returns `*TCPAddr` — safe to type-assert for port.
- Import/append: parse the whole file before the first POST. No partial writes on validation failure.
- `nullIfEmpty("")` returns `nil` for SQLite INSERT (empty string → NULL for nullable columns).
- `AppendMessageAt` accepts optional `createdAt` (RFC3339) for import replay.
