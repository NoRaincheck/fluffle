# Agent Sessions — Design

Why fluffle runs local agents the way it does, and what was deliberately left out.

This is the *rationale* document. For the reference — config format, resolution
order, endpoints, wire format, prompt assembly, reply modes — see
[backend.md](backend.md#agent-sessions). For the preview pane and its bounded
poll see [tui-architecture.md](tui-architecture.md). For key bindings see
[tui-keybindings.md](tui-keybindings.md).

## What a session is

A human message whose content **opens** with `@name` for a configured agent
starts exactly one session. The daemon spawns that agent's command as a local
subprocess against the thread's context, captures its output, and records the
run. The session is a **transcript, not memory**: it exists so a human can audit
what an agent was asked and what it printed, and no part of it is ever fed back
to an agent. The thread's JSONL history is the only context an agent receives.

Three properties follow, and they are the reason the feature is shaped this way:

- The daemon owns execution, so a session runs identically whether a human typed
  in the TUI or ran `flf message send`, and runs with no TUI open at all.
- The only trigger is a human message. There is no invoke command and no
  agent-reachable start route.
- One mention produces one run. A one-shot subprocess has no state to resume, and
  the thread history already contains the previous reply, so a follow-up mention
  needs nothing carried across runs.

## Prior art

Two projects were studied. Both shaped the design; neither is a dependency.

### roborev (kenn-io/roborev)

A local agent runner with a TUI split into a job queue and a selected document.

**Adopted**

- **The daemon owns execution.** It queues a job, runs the agent as a subprocess,
  and stores the result in its own database. The TUI reads that database; the TUI
  runs nothing.
- **Named agent profiles in layered config:** CLI flags > per-repo
  `.roborev.toml` > global config > defaults, merged per key. fluffle has no CLI
  flag layer, so it is the global file loaded first and the repo file merged over
  it, per agent name.
- **A per-agent command override**, so a profile can point at a non-default
  binary path.
- **A split screen: a list on one side, the selected document on the other.**
  fluffle reuses the existing `p` preview pane rather than adding a screen.
- **One document per job**, so a run is addressable and auditable.

**Rejected**

Post-commit git hooks, panels and subagents, synthesis, backup agents,
per-workflow agent/model/reasoning matrices, model routing, and project tables
keyed by git remote. Those are roborev's problem to solve, not prereerequisites
for fluffle's loop. `reuse_review_session` — resuming prior runs on the same
branch — is rejected as well; see [out of scope](#out-of-scope).

### buzz (block/buzz)

`buzz-agent` speaks ACP — JSON-RPC 2.0 over stdio, with `session/new`,
`session/prompt`, `session/cancel` and streaming notifications.

**Adopted**

- **Bounded everything.** Finite timeouts, capped output, capped history. Never
  unbounded. fluffle's bounds are 1 MiB of retained output per stream and a
  mandatory finite `timeout_secs` that no profile can opt out of.
- **Process-group kill on every exit path.** `Setpgid` plus `killpg` on the
  negative pid, so a timeout or a shutdown cannot orphan grandchildren. A coding
  agent spawns shells and build children; killing only the direct child leaks
  them. fluffle treats an `ESRCH` from `killpg` as "the group is already gone"
  and falls back rather than reporting an error.
- **Per-session isolation.** Each run gets its own subprocess, environment, and
  event stream.
- **A silent turn is a failure, not a success.** buzz's reply guard exists
  because an agent that does the work and never publishes is a silent failure —
  the requester waits on a result that was produced and thrown away. fluffle
  makes this deterministic rather than statistical; see
  [deliberate behaviours](#deliberate-behaviours).
- **Real subprocesses in tests, no mocks.** Each fault path is an actual process
  being abused.

**Rejected**

ACP itself, MCP servers, streaming agent output into the client, self-summarizing
context handoff, LLM provider matrices, and hook tools. fluffle shells out to an
agent CLI that already does all of that.

**A deliberate divergence.** buzz's configuration is environment variables only,
with no config file — "we are a subprocess; subprocess config is environment".
fluffle takes the opposite position for its *own* profiles, because fluffle owns
the repo layout and wants profiles committed alongside the code. The distinction
is intentional: `buzz-agent` is a binary fluffle does not own, while a fluffle
agent profile is fluffle's own configuration.

## Decisions

| Decision | Choice | Why |
|---|---|---|
| Where the agent runs | The daemon | Runs with no TUI open. `@reviewer` behaves identically from the TUI and the CLI. Reversed an earlier rejection; see [reversed decisions](#reversed-decisions). |
| Daemon-to-agent interface | One-shot prompt to stdout | Every coding CLI already supports it, which keeps the whole feature to a subprocess spawn. |
| Reply path | Per-agent `reply` = `stdout` \| `cli` \| `auto` | One knob covers the zero-cooperation case and the real-tool-work case. |
| Config format | TOML only | `[[agents]]` maps onto a list of named profiles and matches roborev. Three formats would mean three parsers and three test matrices for one feature. |
| Config location | `<repo>/.flf.toml` then `~/.fluffle/config.toml` | Repo-anchored by default, like every other fluffle entity. |
| Session lifetime | One new session per mention | A one-shot subprocess has no state to resume, and the thread history is the context, so a follow-up mention already sees the prior reply. |
| Trigger detection | Daemon, leading mentions only | One code path for the TUI and the CLI. Leading-only keeps prose like `don't @reviewer do that` inert. |
| Preview surface | The existing `p` pane, in session mode | No new screen, and it matches roborev's queue + document split. |

Three of these were open questions resolved by default rather than by discussion.

- **Cancel is in scope, minimally.** `POST /v1/sessions/:id/cancel`, human-only,
  kills the process group and marks the session `canceled`; plus automatic cancel
  of every running session on daemon shutdown. No TUI key. A 300-second agent
  that cannot be stopped is a real trap, and the TUI was left alone to keep the
  key surface small.
- **The concurrency cap is a hardcoded constant**, `session.MaxConcurrentSessions = 4`,
  not a config key. YAGNI. It is the first knob to add if 4 is wrong.
- **The prompt carries the agent's identity and the exact command to post
  with.** Without the `## Replying` block naming the agent and spelling out
  `flf message send --thread <id> --reply-to-seq <n> --text "<your reply>" --agent-id <name>`,
  `reply = cli` and `reply = auto` cannot work at all. It is not optional
  documentation — it is the interface.

One shape decision deserves its own note, because its payoff is deferred.
`runner.Runner` is an interface with a single method, `Run(ctx, Request)
(Result, error)`, and `execRunner` is the only implementation. The narrowness is
deliberate: an ACP-backed runner could be added later without touching the
schema, the API, or the TUI, because nothing outside `internal/runner` knows how
a session is executed. ACP itself is [out of scope](#out-of-scope) — the seam
exists so that adding it is additive, not so that it is planned.

## Reversed decisions

Two entries were removed from the rejected list in [backend.md](backend.md#what-was-rejected)
when sessions shipped. Both are worth recording, because the second was removed
without being adopted.

- **Daemon-owned agent execution** was rejected and is now the feature. The
  original reasoning — that the daemon should not run things — did not survive
  contact with the requirement that a session behave the same with no TUI open.
- **YAML workflows** was rejected and remains unsupported, but TOML-only is the
  positive decision and YAML is not on the list only because it was never a
  candidate. A third config format would mean a third parser and a third test
  matrix for one feature. If a YAML workflow ever becomes a request, it is a new
  decision, not an oversight to fix.

## Mention grammar

`mentions.Parse` accepts a run of `@name` tokens at the very start of the
content, allowing whitespace between them, and returns each distinct name once.

| Content | `names` | `request` |
|---|---|---|
| `@reviewer do xyz` | `[reviewer]` | `do xyz` |
| `@reviewer @fixer do xyz` | `[reviewer, fixer]` | `do xyz` |
| `@reviewer` | `[reviewer]` | `""` |
| `don't @reviewer do that` | `nil` | `""` |
| `hey @reviewer look` | `nil` | `""` |
| `@reviewer, can you look` | `[reviewer]` | `, can you look` |

The last row is intended. The mention is leading and the comma is the
separator. Requiring whitespace-or-end after the name would also accept it, and
would additionally reject `@reviewer-x`, which the name charset already allows.
**The name charset, not the delimiter, defines the token.**

The parse charset and the config charset are deliberately different widths:

- `mentions.Parse` accepts `[A-Za-z0-9]` then `[A-Za-z0-9_-]`, because a mention
  is a lexical scan of arbitrary text.
- `agentcfg` requires a profile name to match `^[a-z0-9][a-z0-9_-]*$`, lowercase
  and case-sensitive.

So `@Reviewer hi` parses as the name `Reviewer` and then resolves to nothing —
inert text, no session, no error. That is the same outcome as any unresolvable
name, and it is the right one: mentions are prose until proven otherwise.

The trigger discards the `request` value. The prompt quotes the trigger message
in full, mentions and all, because the agent should see exactly what the human
wrote.

## Deliberate behaviours

Three behaviours look like bugs and are not. Each has a test that says so.

**An unresolvable name is inert; a broken profile fails loudly.** A leading
`@token` that matches no configured agent is ordinary message text — no session
row, no subprocess, no error. A name that *does* resolve but whose `command` is
missing or whose `timeout_secs` is invalid is a session that starts and then
fails visibly, with the error in the session events and in the TUI preview.
Configuration errors must not be silent; unknown names must not be errors.

**`auto` waits two seconds before concluding the agent stayed silent.** The
agent's last `flf message send` can still be in flight when its process exits.
Concluding silence immediately after `wait()` produces a phantom duplicate reply
in `auto` mode and a spurious `failed` in `cli` mode. So both modes poll for up
to `ReplyGracePeriod` (2 s, at `ReplyGracePoll` = 100 ms) for an agent-authored
message. If the grace period expires, `auto` falls back to stdout and the session
records that it did so. This is the single most likely source of visible
duplicate replies if implemented naively.

Reply detection is scoped by **sequence, not wall clock**: an agent message
counts as a self-post if its seq is after the trigger's, authored by that agent
name. A second session's earlier post therefore does not satisfy a later
session's grace poll, and a human post never counts.

**Two live mentions of the same agent in one thread can cross.** In `auto` mode
the daemon decides a run replied itself by counting agent messages from that
name at a sequence after the trigger, which is scoped per thread and per name
and so cannot say *which* session a post belongs to. Two overlapping runs of the
same agent can therefore cross: one run's reply is credited to the other, and a
session can be left **silently unanswered and recorded as `succeeded` with
`reply_message_id = NULL` and no message in the thread from its run**. The output
is not lost — it is in that session's `agent_session_events` — but nothing in the
thread or the session row says so. Sequential mentions, and two *different*
agents in one thread, are unaffected. Distinguishing the runs needs a session
marker on the posted message, which is not implemented. Documented in
[backend.md](backend.md#reply-modes).

## Testing strategy

**Real subprocesses, no mocks.** Every fault path is an actual process being
abused, following buzz's stated strategy. A mock runner would let a test pass
while the real code leaked a process group or lost the exit code.

| Package | Covers |
|---|---|
| `internal/mentions` | Every row of the table above, plus `@` alone, `@1abc`, `@-x`, duplicate names, a mention with no request, and 10 KiB of content. |
| `internal/agentcfg` | Valid parse; missing `name`; bad name charset; unknown key; bad `reply`; zero, negative, and overflowing `timeout_secs`; empty `command`; repo-over-global precedence; repo-only and global-only entries; missing repo file falling back to global; mtime invalidation. |
| `internal/runner` | `echo` for stdout; a non-zero exit is not an error; a fixture writing stderr; a fixture sleeping past the timeout, then asserting the **process group** is dead; a fixture emitting more than 1 MiB, asserting head-and-tail middle-elide; a fixture spawning a grandchild and asserting it dies with the group; `killpg` `ESRCH` fallback; `{prompt}` argv substitution; stdin delivery; `Cwd`; `Env` merged by key; both pipes draining concurrently. |
| `internal/store` | Session insert; the `UNIQUE(trigger_message_id, agent_name)` violation returns `ErrConflict`; event append assigns monotonic `seq`; duplicate `seq` rejected; status transition validation; listing by thread in creation order; `ListSession` with events. |
| `internal/session` | Lifecycle and status transitions; the concurrency cap with `MaxConcurrentSessions+3` sessions; prompt assembly for all three reply modes; streaming coalescing; every reply-resolution row including the late self-post grace period; a cancel during the grace period; a persistent store error while posting a reply. |
| `internal/apiserver` | A leading resolvable mention creates a session; a non-leading mention does not; an unresolvable name does not; an append carrying `X-Fluffle-Agent` never creates a session; a session event batch scans every message event; the shapes of the three GET routes; cancel is 403 for an agent and 409 for a terminal session. |
| `internal/tui` | `s` flips the pane; `s` on a message without a session falls back to the thread; a running session renders a status line instead of a duration; the tick stops at all-terminal; the tick re-arms across hide/show, a detail round trip, a resize, and a thread switch; a stale or superseded tick chain is discarded; a hidden pane issues no fetches; the pane follows the cursor; one thread's session never renders in another's. |
| `cmd/flf` (e2e) | The full loop against a fake agent script: `flf message send --text "@probe hi"` → poll → assert the reply landed → `flf agent session --id 1` returns the events. This is the test that proves the feature works end to end rather than in each layer. |

## Out of scope

Explicitly not built, so that a later change is a decision rather than an
accident.

- **ACP over stdio, MCP servers, streaming agent output into the client.** fluffle
  shells out to a CLI that already speaks these.
- **Session reuse or resumption across mentions.** One mention, one run, no
  resume. The thread history is the continuity.
- **Agent-triggered agents.** An append carrying `X-Fluffle-Agent` is never
  scanned for mentions, so a reply containing a leading `@mention` cannot spawn a
  run. This is the anti-loop guard, and it is what keeps the feature to human
  intent.
- **`@mention` autocomplete in the compose box.**
- **Cancellable sessions from the TUI.** Cancellation is the API route and daemon
  shutdown only; there is no key.
- **Configurable concurrency, configurable output caps, per-agent model routing,
  reasoning levels, backup agents, or workflow-specific agent tables.** The cap of
  4 and the 1 MiB stream bound are hardcoded constants, and each is the first
  thing to become configurable if it proves wrong.
- **Agent profiles in the database, or a `flf agent add` / `flf agent edit` that
  writes config files.** Config is edited by hand, like roborev's, so it reviews
  and version-controls with the repo.
- **Any webapp surface.**
