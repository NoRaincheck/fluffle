# Agent Sessions Design

Date: 2026-09-26
Status: draft for review

## Problem

A human in a fluffle thread can name a configured local agent with a leading
`@mention`. The daemon runs that agent as a local subprocess against the thread's
context, the agent's reply lands back in the thread, and the entire run is
recorded as a session that the TUI can show as a preview next to the message that
started it.

Today fluffle has the read side of an agent loop (`flf agent read`,
`flf agent append`, the JSONL codec) but nothing runs an agent, nothing records
a run, and there is no `session` concept anywhere in the schema, the API, or the
TUI.

## Reference patterns

### roborev (kenn-io/roborev)

- Named agent profiles in config, layered: CLI flags > per-repo `.roborev.toml` >
  global `~/.roborev/config.toml` > defaults. Per-repo wins per key.
- The **daemon owns execution**. It queues a job, runs the agent as a local
  subprocess, and stores the resulting document in its own database. The TUI
  reads that database; the TUI does not run anything.
- `*_cmd` settings point at a non-default binary path.
- The TUI is a split screen: a queue on the left, the selected document on the
  right.
- `reuse_review_session` (experimental) resumes prior agent sessions on the same
  branch.

Adopted: daemon-owned execution, named profiles in layered config, per-agent
command override, split-screen queue + document, one document per job.

Rejected: post-commit git hooks, panels/subagents, synthesis, backup agents,
per-workflow agent/model/reasoning matrices, model routing, project tables keyed
by git remote. Those are roborev's own problem, not fluffle's.

### buzz (block/buzz)

`buzz-agent` speaks ACP — JSON-RPC 2.0 over stdio: `initialize`, `session/new`,
`session/prompt`, `session/cancel`, with outbound `agent_message_chunk`,
`tool_call`, and `tool_call_update` notifications. Per-session MCP servers, one
prompt per session at a time.

Its configuration is **environment variables only, deliberately, with no config
file** — "we are a subprocess; subprocess config is environment". fluffle takes
the opposite position for its *own* agent profiles, because fluffle owns the
repo layout and wants profiles committed alongside the code. The distinction is
deliberate and worth stating: buzz-agent is a binary fluffle does not own;
a fluffle agent profile is fluffle's own configuration.

Adopted from buzz:

- **Bounded everything.** Finite timeouts, capped output, capped history. Never
  unbounded.
- **Process-group kill on every exit path** (`setpgid` + `killpg`), so a timeout
  or shutdown cannot orphan grandchildren.
- **Per-session isolation.** Each session gets its own subprocess, its own
  environment, its own event stream.
- **A silent turn is a failure, not a success.** buzz's reply guard exists
  because "an agent that does the work and never publishes is a silent failure
  — the requester waits on a result that was produced and thrown away." fluffle
  makes this deterministic instead of statistical: see *Reply resolution*.
- **Real subprocesses in tests, no mocks.** Each fault path is an actual process
  being abused.

Rejected: ACP, MCP, streaming into the client, self-summarizing context handoff,
LLM provider matrices, hook tools. fluffle shells out to an agent CLI that
already does all of that.

## Decisions

| Decision | Choice | Why |
|---|---|---|
| Where the agent runs | The daemon | Runs with no TUI open. `@reviewer` behaves identically from the TUI and the CLI. Reverses the current `docs/backend.md` rejection of daemon-owned execution. |
| Daemon-to-agent interface | One-shot prompt to stdout | Every coding CLI already supports it. Keeps this slice to a subprocess spawn. |
| Reply path | Per-agent `reply` = `stdout` \| `cli` \| `auto` | Covers the zero-cooperation case and the real-tool-work case with one knob. |
| Config format | TOML only | `[[agents]]` maps onto a list of named profiles and matches roborev. Three formats would mean three parsers and three test matrices for one feature. |
| Config location | `<repo>/.flf.toml` then `~/.fluffle/config.toml` | Repo-anchored by default, like every other fluffle entity. |
| Session lifetime | One new session per mention | A one-shot subprocess has no state to resume. The thread history is the context, so a follow-up mention already sees the prior reply. |
| Trigger detection | Daemon, leading mentions only | One code path for TUI and CLI. Leading-only keeps prose like "don't `@reviewer` do that" inert. |
| Preview surface | The existing `p` pane, in session mode | No new screen. Matches roborev's queue + document split. |

Three of these were open questions resolved by default rather than by
discussion. Flag any of them to change it:

- **Cancel is in scope**, minimally: `POST /v1/sessions/:id/cancel`, human-only,
  kills the process group, marks the session `canceled`; plus automatic cancel
  of every running session on daemon shutdown. No TUI key in this slice. A
  300-second agent that cannot be stopped is a real trap.
- **The concurrency cap is a hardcoded constant** (`maxConcurrentSessions = 4`),
  not a config key. YAGNI. It is the first knob to add if 4 is wrong.
- **The prompt carries the agent's identity and the exact command to post
  with.** Without this, `reply = cli` and `reply = auto` cannot work at all.

## Config

New package `internal/agentcfg`. One format, two locations, merged per agent
name.

```toml
# .flf.toml (repo root) or ~/.fluffle/config.toml
[[agents]]
name          = "reviewer"
description   = "reviews the diff"
command       = "claude"
args          = ["-p", "{prompt}"]
reply         = "auto"
timeout_secs  = 300
env           = { ANTHROPIC_BASE_URL = "http://127.0.0.1:11434" }
system_prompt = """
You are reviewing a change in this repository. Be concrete.
"""

[[agents]]
name    = "probe"
command = "/usr/local/bin/probe"
# no {prompt} in args, so the prompt arrives on stdin
# reply defaults to "auto", timeout defaults to 300
```

### Fields

| Field | Required | Default | Notes |
|---|---|---|---|
| `name` | yes | — | The `@mention` handle. Must match `^[a-z0-9][a-z0-9_-]*$`. Case-sensitive. |
| `description` | no | `""` | Shown by `flf agent list`. |
| `command` | yes | — | Resolved on `PATH`, or used as an absolute path. |
| `args` | no | `[]` | Base arguments, before prompt delivery. |
| `reply` | no | `"auto"` | `stdout`, `cli`, or `auto`. |
| `timeout_secs` | no | `300` | Must be > 0. |
| `env` | no | `{}` | Extra environment for the child. Merged over the daemon's own environment. |
| `system_prompt` | no | `""` | Prepended to the thread context. |

Unknown keys in a config file are a hard error, not a warning. A typo in
`timeout_second` should not silently mean 300.

### Prompt delivery

One rule, no extra config key:

- `args` contains the literal `{prompt}` → the prompt is substituted into that
  argument and the child gets no stdin.
- `args` does not contain `{prompt}` → the prompt is written to the child's
  stdin, which is then closed.

`{prompt}` is replaced everywhere it appears. A profile that needs the prompt
both on argv and stdin may use both forms; the argv substitution happens first
and the full prompt is still written to stdin.

Both forms are needed in practice. `claude -p`, `codex exec`, and `gemini -p` all
take the prompt as an argv argument, and thread history can exceed the platform
argv limit.

### Resolution

Given a thread, the daemon resolves the channel, then `repo_abs_path`, then
loads `<repo>/.flf.toml` and `~/.fluffle/config.toml` and merges them with the
repo entry winning per agent name. A channel with `repo_abs_path IS NULL` (an
orphaned channel) resolves against the global file only, which is correct
because it has no repo to be anchored to.

The daemon caches the merged result per repo path and reloads when a config
file's mtime changes. A missing file is not an error.

### Unresolvable names are inert

A leading `@token` that does not resolve to a configured agent is ordinary
message text. `@nosuchagent hi` posts as a normal human message and starts
nothing. Mentions are prose until proven otherwise.

This is the opposite of a resolvable name whose `command` is missing or whose
`timeout_secs` is invalid: that is a session that starts and then **fails
visibly**, with the error in the session events and visible in the TUI preview.
Configuration errors must not be silent, and unknown names must not be errors.

## Data model

Two tables. `agent_sessions` is the header and the audit record; 
`agent_session_events` is the bounded transcript.

```sql
CREATE TABLE agent_sessions(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  thread_id INTEGER NOT NULL REFERENCES threads(id),
  trigger_message_id INTEGER NOT NULL REFERENCES messages(id),
  agent_name TEXT NOT NULL CHECK(length(trim(agent_name)) > 0),
  status TEXT NOT NULL CHECK(status IN ('queued','running','succeeded','failed','canceled')),
  reply_mode TEXT NOT NULL CHECK(reply_mode IN ('stdout','cli','auto')),
  command TEXT NOT NULL,
  cwd TEXT,
  exit_code INTEGER,
  error TEXT,
  reply_message_id INTEGER REFERENCES messages(id),
  started_at TEXT,
  finished_at TEXT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE UNIQUE INDEX IF NOT EXISTS uniq_session_trigger
  ON agent_sessions(trigger_message_id, agent_name);
CREATE INDEX IF NOT EXISTS idx_sessions_thread ON agent_sessions(thread_id, id);
CREATE INDEX IF NOT EXISTS idx_sessions_status ON agent_sessions(status);

CREATE TABLE agent_session_events(
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

`UNIQUE(trigger_message_id, agent_name)` does two jobs. It makes session
creation idempotent, so a retried or double-processed append cannot run an agent
twice. And it makes "the session related to a single reply" exactly one row,
which is what the TUI preview and `reply_message_id` both rely on.

`reply_message_id` is `NULL` when the agent posted its own reply through the
CLI. That is a distinction the preview renders differently, not an oversight.

Events are the transcript, capped by the runner's output limit, not a
conversation. The thread JSONL is the agent's context; the events are an audit
log. Nothing in `agent_session_events` is ever fed back to an agent.

`type` is a closed set. `exit` carries the exit code as its content. `error`
carries a daemon-side failure (spawn failure, timeout, unresolvable command) as
opposed to something the agent itself printed.

### Migration

`migrate()` in `internal/store/store.go` currently detects a pre-`parent_id`
schema and drops all tables. The new tables are `CREATE TABLE IF NOT EXISTS`, so
they are added by the existing path with no version bump and no data loss for
any database already on the current schema. The destructive branch only fires
for the old pre-`parent_id` layout, which is already lossy by design.

## Mention parsing

New small package `internal/mentions`.

```go
func Parse(content string) (names []string, request string)
```

Leading-only. Repeatedly match `^@([A-Za-z0-9][A-Za-z0-9_-]*)` at the cursor,
skipping whitespace between matches, and stop at the first token that is not a
mention.

| Content | `names` | `request` |
|---|---|---|
| `@reviewer do xyz` | `[reviewer]` | `do xyz` |
| `@reviewer @fixer do xyz` | `[reviewer, fixer]` | `do xyz` |
| `@reviewer` | `[reviewer]` | `""` |
| `don't @reviewer do that` | `nil` | `""` |
| `hey @reviewer look` | `nil` | `""` |
| `@reviewer, can you look` | `[reviewer]` | `, can you look` |

The last row is intended: the mention is leading, and the comma is the
separator. Requiring whitespace-or-end after the name would also accept it and
would additionally reject `@reviewer-x`, which the name charset already allows.
The name charset, not the delimiter, defines the token.

`names` is deduplicated while preserving order, so `@a @a hi` starts one session.
`request` is the content after the last mention with surrounding whitespace
trimmed. A mention with an empty request is legal — the agent gets the thread
context and no explicit instruction.

### Trigger point

Session creation is a **side effect of a successful message append**, not its
own endpoint. Both `POST /v1/threads/:id/messages` and
`POST /v1/threads/:id/events` scan the content of each appended message event.

Ordering is load-bearing:

1. Commit the human message, assigning its `seq` and `id`.
2. Only then scan for mentions and create sessions.

So `trigger_message_id` always references a committed row, and the agent's own
read of the thread includes the message that started it. Scanning before the
commit would give the agent a context that omits its own trigger.

### Agents cannot trigger agents

If the appending request carries `X-Fluffle-Agent`, no session is created,
regardless of content. This is the anti-loop guard: an agent whose reply happens
to contain a leading `@mention` must not spawn another run. It matches the
existing `AGENT_FORBIDDEN` pattern and keeps the feature to human intent.

## Execution

### Runner

New package `internal/runner`, one method behind an interface so an ACP
implementation can be added later without touching the schema, the API, or the
TUI.

```go
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
```

`execRunner` is the only implementation in this slice.

- `exec.CommandContext` with `SysProcAttr{Setpgid: true}`.
- On timeout or cancellation, `killpg(-pid, SIGKILL)`, not `cmd.Process.Kill()`.
  buzz's stated property: grandchildren must die too. A coding agent spawns
  shell and build children; killing only the direct child leaks them.
- Two pipes drained by two goroutines, both feeding `OnChunk`.
- **Bounded output.** 1 MiB per stream. On overflow, keep the head and tail,
  middle-elide with a marker, and stop appending. buzz's rule: bounded output
  sizes, no unbounded accumulation.
- **Finite timeout always.** No agent profile can opt out. A zero or negative
  `timeout_secs` is a config error, not "no timeout".

### Daemon wiring

After a successful append with resolvable mentions, for each name:

1. Insert the session with `status = 'queued'`, `reply_mode`, the fully
   resolved argv (for the audit trail), and `cwd`.
   - `UNIQUE` violation → skip. Another process already claimed this pair.
   - `cwd` is the channel's `repo_abs_path`, or `NULL` for an orphaned channel.
     An orphaned channel has no repo; the agent still runs, in the daemon's
     working directory. This is a real, visible limitation, not a silent one —
     the session records `cwd = NULL`.
2. Hand the session to the scheduler.

The scheduler is a buffered semaphore of `maxConcurrentSessions` (4). A session
that cannot acquire it stays `queued` and is retried when a slot frees. Without
a cap, twenty mentions in one message means twenty concurrent coding agents on
one machine.

The worker goroutine:

1. Set `status = 'running'`, `started_at = now`.
2. Append a `prompt` event containing the exact prompt text.
3. Build the prompt (below) and run the subprocess.
4. Stream chunks into `stdout` / `stderr` events.
5. On exit, append an `exit` event with the code.
6. Resolve the reply (below).
7. Set terminal `status`, `finished_at`, `exit_code`, `error`,
   `reply_message_id`.

### Prompt construction

```
<system_prompt>            (if configured)

## Thread

<channel name> > <thread title>
repository: <repo_abs_path or "none">

## History

<the thread's messages and reactions as portable JSONL, via internal/jsonl>

## Request

<reply> #<seq>
<the triggering message's content, verbatim>
```

The history block is the same projection `flf agent read --json` emits, built
through `internal/jsonl`, so the daemon and the CLI cannot drift. Tenet 5 holds:
the thread history *is* the context. No summarization, no retrieval, no
embeddings.

Appended when `reply` is `cli` or `auto`:

```
## Replying

You are agent "<name>" in fluffle thread <id>.
Post your reply with:

    flf message send --thread <id> --text "<your reply>" --agent-id <name>
```

and when `reply` is `auto`, one more line: if you do not post, whatever you
write to stdout is posted for you.

Without this block, `cli` and `auto` cannot work. It is not optional
documentation — it is the interface.

The `prompt` event holds the full prompt and is **not** subject to the
runner's 1 MiB per-stream cap, which bounds agent output only. The prompt is
already bounded by the thread it is built from, and truncating it would make the
audit record a lie about what the agent was asked.

### Reply resolution

The daemon detects agent self-posts with no new plumbing. The CLI's `--agent-id`
sets `X-Fluffle-Agent`, so a message the agent posted already has
`author_type = 'agent'` and `name = <agent name>`. The count is:

```sql
SELECT COUNT(*) FROM messages
WHERE thread_id = ? AND name = ? AND author_type = 'agent'
  AND created_at >= ?
```

with the session's `started_at` as the parameter.

| `reply` | Behaviour |
|---|---|
| `stdout` | Post the trimmed stdout as a message (`name = <agent>`, `role = assistant`, `author_type = agent`, parented to the trigger). Empty after trimming → no message, session still `succeeded`. |
| `cli` | Post nothing. If no agent-authored message landed during the run, mark the session `failed` with `agent did not reply (reply=cli)`. |
| `auto` | If the agent posted at least one message, post nothing. Otherwise post stdout, exactly as `stdout` does. |

The reply message is written by the daemon as a normal append with
`author_type = 'agent'`. It must not carry `X-Fluffle-Agent` from an agent's own
request, because the daemon is the writer here, not the agent.

### The exit race

The agent's last `flf message send` can still be in flight when its process
exits. Concluding "the agent stayed silent" immediately after `wait()` produces
a phantom duplicate reply in `auto` mode, and a spurious `failed` in `cli` mode.

So `cli` and `auto` **poll for up to 2 seconds** (100ms interval) for an
agent-authored message before concluding silence. If the grace period expires,
the reply falls back to stdout and the session records that it did so. This is
the single most likely source of visible duplicate replies if implemented
naively.

### Shutdown

On daemon shutdown, cancel the context of every running session, `killpg` each
process group, and mark each session `canceled` with `finished_at` set. Without
this, `flf daemon stop` orphans every running coding agent. buzz treats
process-group kill on every exit path as a correctness property, not a
polish item.

Sessions still waiting on the concurrency semaphore are marked `canceled` too,
with `error = "daemon shut down before start"` and a NULL `started_at`. A
session left in `queued` forever is a stuck row the TUI would poll
indefinitely, and `status` has no `abandoned` value to fall back on.

### Restart

The semaphore is in-memory, so a daemon that dies hard — `kill -9`, a panic, a
reboot — leaves `queued` and `running` rows in SQLite that nothing will ever
advance. On startup, before serving, the daemon reconciles: every session in a
non-terminal status is marked `canceled` with
`error = "daemon restarted while <status>"` and `finished_at` set.

This also bounds the TUI tick. A stale `running` row would otherwise keep the
"any session non-terminal" condition true indefinitely, and the pane would poll
once a second for the rest of the session.

## API

Two read endpoints. Creation is a side effect of append, so there is no invoke
endpoint.

**`GET /v1/threads/:id/sessions`** — sessions in a thread, oldest first,
metadata only (no events). Lets the TUI build
`map[trigger_message_id]session` for the previewed thread. Keeping this off the
messages endpoint avoids touching the hottest read path and its tests.

**`GET /v1/sessions/:id`** — one session plus its events in `seq` order.
Returns `{"session":{...},"events":[...]}`.

**`GET /v1/agents?repo=<abs path>`** — resolved agent names for a repo scope,
each with its description, resolved command, `reply` mode, and the file it came
from. Backs `flf agent list`.

**`POST /v1/sessions/:id/cancel`** — human-only (`403 AGENT_FORBIDDEN` for an
agent request), kills the process group, marks `canceled`. Cancelling a terminal
session is `409`. Returns `{"ok":true}`.

Errors use the existing `{"code","message"}` envelope. The only new code is
`SESSION_NOT_FOUND` (exit 1), from `GET /v1/sessions/:id` and from cancel. There
is deliberately no `AGENT_NOT_FOUND`: no endpoint takes an agent name as a
parameter, and an unresolvable mention is inert text rather than a failed
lookup, so nothing in this design can produce it.

## CLI

```
flf agent list [--repo PATH] [--json]
flf agent session --id N [--json]
flf message send --thread N --text "@reviewer do xyz"     # triggers a session
```

`flf agent list` prints `name  command  reply  source` lines, where `source` is
the config file the entry came from. `--json` emits the full resolved set.

`flf agent session --id N` prints the session header followed by its events as
JSONL. This is the portable counterpart to `flf agent read` and the shell-level
way to inspect what an agent actually did.

There is no `flf agent invoke`. The mention path covers it, and a second
spelling of the same operation is a second thing to keep working.

## TUI

The real screens are `viewInbox` and `viewInboxDetail`. `p` toggles a
right-hand pane rendered by `renderPreview`, which delegates to
`renderThreadView(..., preview = true)`. The session preview replaces what that
pane shows; it does not add a screen.

### State

Go type names follow the existing `store` convention (`Channel`, `Thread`,
`Message`, `Reaction`): `store.Session` and `store.SessionEvent`.

```go
previewMode     previewMode // previewThread | previewSession
sessions        []store.Session         // for the previewed thread
sessionsByMsg   map[int64]store.Session // trigger message id -> session
session         *store.Session
sessionEvents   []store.SessionEvent
sessionScroll   int
```

`api.go` gains `ListSessions(ctx, threadID)` and `GetSession(ctx, id)`. Both
follow the existing `doJSON` path and the existing
`DAEMON_DOWN` / `DAEMON_ERROR` / `DELIVERY_UNKNOWN` classification.

### Keys

- `s` — flip the pane between the thread and the session for the cursor's
  message. No-op with a status hint when the cursor's message has no session.
- `p` — unchanged, still toggles the pane.

`s` is currently unbound. Note that `c` and `n` are documented in
`docs/tui-keybindings.md` but are **not** bound, and `simple_test.go:86-95`
asserts that they do nothing; that drift is pre-existing and out of scope here.

### Rendering

`renderSessionPreview(w, h)`:

```
SESSION  reviewer · succeeded · 42s · reply=auto · #7
  12:04:31  prompt    <the prompt, truncated>
  12:04:33  stdout    <stream, truncated>
  12:04:33  stderr    <stream, truncated>
  12:05:13  exit      0
```

The header carries the reply linkage directly: `reply=cli` and `auto` with a
self-post render `replied via cli #41`, while `auto` falling back to stdout
renders `replied via stdout #41`. The linkage is `reply_message_id` on the
session row, so no extra event type is needed for it.

Reusing the `… (N hidden) …` head-and-tail behaviour that
`renderThreadView` already implements for previews, so a long stdout stays
readable in a 50-column pane without new truncation machinery.

While a session is `queued` or `running`, the header shows the status and a
running indicator instead of a duration.

If the cursor's message has no session, `s` falls back to the existing thread
preview. The default pane behaviour is unchanged.

### The tick

The TUI has **no timers today** — no ticks, no spinners, no polling.
`docs/tui-architecture.md:112` records "data is fetched on demand, never
polled" as a rule, and this feature is the first thing that breaks it.

A session that runs for 300 seconds is invisible until something refetches. So:
while any known session is non-terminal, schedule a bounded `tea.Tick`
(500ms) that refetches the session list for the previewed thread, and **stop
scheduling once every known session is terminal**. A tick that reschedules
itself unconditionally is a permanent busy loop and a permanent battery drain,
and the "loading" convention in this codebase infers in-flight state from a
missing cache entry rather than tracking it — which does not work here, because
a running session is a cache hit that happens to be incomplete.

## Testing

Real subprocesses, no mocks, following buzz's stated strategy: each fault path
is an actual process being abused.

**`internal/agentcfg`** — valid parse; missing `name`; bad name charset;
unknown key; bad `reply`; zero and negative `timeout_secs`; empty `command`;
repo-over-global precedence for the same name; repo-only entry; global-only
entry; missing repo file falls back to global; mtime invalidation.

**`internal/mentions`** — every row of the table above, plus `@` alone, `@1abc`,
`@-x`, duplicate names, a mention with no request, and 10 KiB of content.

**`internal/store`** — session insert; the `UNIQUE(trigger_message_id,
agent_name)` violation returns `ErrConflict`; event append assigns monotonic
`seq`; duplicate `seq` rejected; status transition validation; listing by thread
in creation order; `ListSession` with events.

**`internal/runner`** — `echo` for stdout; `sh -c 'exit 3'` for the exit code; a
fixture writing to stderr; a fixture sleeping past the timeout, asserting the
process group is dead afterwards; a fixture emitting more than 1 MiB, asserting
head-and-tail middle-elide; a fixture spawning a grandchild, asserting the
grandchild dies with the group; `{prompt}` argv substitution; stdin delivery;
`Cwd` honoured; `Env` merged.

**`internal/apiserver`** — a leading resolvable mention creates a session; a
non-leading mention does not; an unresolvable name does not; an append carrying
`X-Fluffle-Agent` never creates a session; a session event batch scans every
message event; `GET /v1/threads/:id/sessions` and `GET /v1/sessions/:id` shapes;
cancel is 403 for an agent and 409 for a terminal session.

**Reply resolution** — `reply=stdout` with output posts a message; `reply=stdout`
with empty output posts nothing and still succeeds; `reply=cli` with a
self-post leaves `reply_message_id` NULL; `reply=cli` with a silent agent fails;
`reply=auto` with a self-post posts nothing; `reply=auto` without one posts
stdout. The exit-race grace period gets its own test using a fixture that posts
via the CLI and exits immediately.

**e2e (`cmd/flf/e2e_test.go`)** — the full loop with a fake agent script:
`flf message send --text "@probe hi"` → poll → assert the reply landed and
`flf agent session --id 1` returns the events. This is the test that proves the
feature works end to end rather than in each layer.

**TUI** — `s` flips the pane; `s` on a message without a session falls back to
the thread; a running session renders a status line; a session reaching a
terminal state stops the tick; `p` still toggles.

## Documentation

**`docs/backend.md`** — add the two tables to the data model, the two read
endpoints and cancel to the API reference, the config format and resolution
order, `@mention` trigger semantics, and the reply modes. Add cancel and
`GET /v1/agents` to the permission table. **Remove "daemon-owned agent
execution" from the rejected list**, along with "YAML workflows", since
daemon-owned execution is now the feature and YAML is explicitly not supported.

**`VISION.md`** — tenet 5 needs one added sentence. Sessions are an audit
transcript and a preview surface. The thread history remains the agent's
context, and nothing in `agent_session_events` is ever fed back to an agent.
Without this, storing transcripts reads as the "hidden agent memory" the
manifesto forbids.

Tenet 3 needs a line too: agents still cannot create channels or threads, and
now also cannot start sessions.

**`README.md`** — an `@mention` example, `flf agent list`,
`flf agent session`, and a `.flf.toml` snippet.

**`docs/tui-keybindings.md`** — add `s`. The existing `c`/`n` drift is
pre-existing and noted, not fixed here.

## Non-goals

Explicitly out of scope, so that a later change is a decision rather than an
accident:

- ACP over stdio, MCP servers, streaming agent output into the client.
- Session reuse or resumption across mentions.
- Agent-triggered agents.
- `@mention` autocomplete in the compose box.
- Cancellable sessions from the TUI (CLI and shutdown only).
- Configurable concurrency, configurable output caps, per-agent model routing,
  reasoning levels, backup agents, or workflow-specific agent tables.
- Agent profiles in the database, or a `flf agent add` / `flf agent edit` that
  writes config files. Config is edited by hand, like roborev's.
- Any webapp surface.
