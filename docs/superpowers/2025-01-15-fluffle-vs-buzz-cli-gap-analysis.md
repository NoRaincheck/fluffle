# Fluffle vs. Buzz CLI — Gap Analysis

**Source:** [block/buzz](https://github.com/block/buzz/tree/main/crates/buzz-cli)
**Original comparison date:** 2025-01-15
**Revised:** 2026-09-25
**Status:** Historical competitor note; not an implementation plan

## Executive decision

Use Buzz as a pattern library, not as a feature checklist. Fluffle should keep its local-first, repo-anchored, JSONL-based design and spend the next slice making the agent contract complete, reliable, and portable.

The highest-value work is not canvas, memory, workflows, Nostr, or Git events. It is making this loop work end to end:

1. A human creates a repo-anchored thread.
2. An agent reads the thread with portable message references.
3. The agent appends a reply and reaction through the CLI.
4. The thread exports and imports without losing context.
5. The agent resumes from a cursor instead of downloading the full thread again.

## 1. Architecture and product boundary

| Dimension | Fluffle | Current Buzz CLI |
|-----------|---------|------------------|
| **Backend** | Local SQLite daemon on `127.0.0.1:0` | Remote Buzz relay with HTTP and WebSocket support |
| **Identity** | `X-Fluffle-Agent` attribution header; not cryptographic authentication | Nostr keypair with signed NIP-98 requests and relay authorization |
| **Protocol** | Small local REST API over JSON | Nostr events, relay APIs, and ecosystem protocols |
| **Data model** | Repo-anchored channels → explicit threads → messages and reactions | Channels, events, DMs, repositories, documents, workflows, and ecosystem metadata |
| **Exchange format** | JSONL for agent read/append/export/import | Relay JSON and raw Nostr events; not a JSONL contract |
| **Primary strength** | Small, local, explicit, and easy to embed in a developer workflow | Broad relay ecosystem and authenticated remote collaboration |

The local-first decision remains correct. Nostr interoperability is not an agent-interface requirement and would conflict with the localhost-only product boundary. Buzz's current Git, repository, and channel-binding features are also broader than the original comparison described; they reflect a different product rather than a missing Fluffle primitive.

## 2. What remains valuable from the original comparison

### Keep

- Repo-anchored channels as Fluffle's differentiator.
- Explicit thread IDs instead of implicit reply relationships.
- JSONL as a simple, inspectable agent exchange format.
- Local HTTP and SQLite instead of relay availability and key management.
- A TUI-first human experience with a small CLI surface for agents.

### Reject as a Fluffle roadmap

- Nostr protocol adapters.
- DMs, profiles, presence, moderation, media upload, GIFs, and notes.
- Multi-repo project administration.
- First-class Git patches, issues, PRs, and repository hosting.
- Autonomous workflow engines, schedulers, webhooks, and plugin systems.

These are product expansions, not prerequisites for a useful local agent loop. Fluffle's manifesto explicitly says to provide communication glue and defer specialized execution to `kata` and `roborev` (`VISION.md:25-29`).

## 3. Corrections to the original gap analysis

The original table was a useful 2025-era snapshot, but it is no longer a current or complete comparison.

- **Threading:** Buzz now has message-thread retrieval and reply relationships. It does not have Fluffle's explicit thread object, but the original `❌` is too broad.
- **Pagination:** Fluffle already has bounded reads through `last=N` and an inbox limit. What it lacks is a durable resume cursor, not all pagination.
- **Reactions:** Fluffle can add reactions but cannot read them through the store or API. The original capability check was incomplete.
- **Inbox:** `/v1/inbox` already exists and is used by the TUI (`internal/apiserver/server.go:61-79`), but there is no `flf inbox` CLI command.
- **JSONL:** The current line schema (`internal/jsonl/jsonl.go:11-19`) omits message references, parent relationships, and reaction events. It is promising, but not yet a lossless handoff contract.
- **Error behavior:** The daemon already returns a JSON error envelope, while CLI error rendering and exit-code mapping are inconsistent. The real issue is correctness before adding more fields.
- **Repo metadata:** The schema contains branch fields, but the CLI's normal repository inspection does not populate them (`internal/repo/repo.go:22-38`). Do not claim full branch tracking yet.

## 4. Agent-contract gaps that matter now

| Gap | Evidence | Effect on an agent |
|-----|----------|---------------------|
| No portable message reference | JSONL has `seq` but no `id` or `parent_id`; replies and reactions require a message ID | An agent can read history but cannot target a message using only the documented agent output |
| No reaction export/import | The JSONL model contains messages only | Reaction context is lost during handoff |
| No CLI inbox surface | The endpoint exists, but CLI dispatch has no `inbox` command | Agents cannot efficiently discover work across channels |
| No resume cursor | `ListMessages` supports only `last=N` (`internal/store/store.go:313-331`) | Agents repeatedly download the full thread or guess what is new |
| No batch delivery guarantee | `postJSONLLines` posts one line at a time (`cmd/flf/main.go:518-533`) | A failed append can leave a partial batch |
| No HTTP timeout boundary | `http.Get` and default clients have no configured timeout (`internal/client/client.go:40-73`) | A wedged daemon can hang an agent indefinitely |
| Exit-code misclassification | 405s are reported as `DAEMON_DOWN`; some daemon errors are reported as client errors | Agents cannot reliably decide whether to retry or fix input |
| Tagged e2e suite is excluded from normal CI-like runs | `cmd/flf/e2e_test.go:1` has `//go:build e2e` and currently fails to compile at line 505 | The baseline can appear green while the end-to-end surface is broken |

## 5. Adopt / defer / reject

### Adopt now

- **Portable references:** make message sequence, parent relationship, and reaction target explicit in the JSONL contract.
- **CLI inbox:** expose the existing inbox endpoint to agents.
- **Resume cursor:** add an `after` or equivalent sequence cursor for thread reads.
- **Stdin writes:** allow agents to pipe message or JSONL content without shell escaping.
- **Batch safety:** parse the complete input first and make a failed append atomic or explicitly resumable.
- **Timeouts and context:** put finite timeouts around daemon health checks and API requests.
- **Error correctness:** preserve the required `{"code","message"}` envelope and the fixed 0/1/2 exit-code contract.

### Defer until measured

- Compact output projections.
- Search and server-side filtering.
- Reaction read commands beyond the minimum needed by the agent loop.
- A human-triggered `agent invoke` command.
- Repository links or handoff metadata to `kata` and `roborev`.

### Reject without a manifesto change

- Hidden per-agent memory.
- A separate channel canvas database.
- A YAML workflow engine with schedules or webhooks.
- First-class Git hosting or event ingestion.
- Nostr, DMs, media, moderation, profiles, and multi-repo projects.

## 6. Recommended implementation order

1. **Repair the baseline.** Fix the tagged e2e compile failure and verify the error-to-exit-code mapping before adding features.
2. **Define the portable event contract.** Decide how messages, replies, and reactions are represented, referenced, imported, and authorized. Preserve context without adding a second hidden store.
3. **Add read/resume primitives.** Expose `flf inbox` and add a monotonic thread cursor.
4. **Add safe write primitives.** Support stdin, parse-before-write, atomic batch behavior, and explicit response-loss handling.
5. **Harden transport and errors.** Add timeouts, cancellation, and consistent JSON errors at every boundary.
6. **Measure before optimizing.** Add compact output or search only after real agent traces show a material need.

## 7. Guardrails and kill criteria

- Do not add memory unless the explicit JSONL-only context rule is deliberately amended.
- Do not add a canvas store unless repository files plus message links demonstrably fail a real workflow.
- Do not add a workflow engine until trigger scope, recursion prevention, delivery identity, and failure recovery are specified and tested.
- Do not retry writes automatically until idempotency or an equivalent duplicate-prevention rule exists.
- Do not add a `retryable` field without explicitly changing the documented error-envelope rule.
- Do not build Git features that duplicate `kata` or `roborev`; link or invoke those tools instead.

## 8. See also

- [Agentic Interface Design](./specs/2025-01-15-agentic-interface-design.md) — the focused agent-contract specification derived from this review
