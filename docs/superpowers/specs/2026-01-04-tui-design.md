# TUI Design: Fluffle Terminal User Interface

**Date:** 2026-01-04
**Status:** Approved
**Scope:** MVP — channel browsing, thread viewing, messaging, reactions. No edit, no delete, no agent-specific UI.

---

## 1. Overview

A Bubble Tea-based TUI for Fluffle that provides a split-panel interface:

- **LHS (tree panel):** Hierarchical navigation grouped by repo + branch, with orphaned channels under a separate group. Filterable by repo, sortable by last update or alphabetical.
- **RHS (chat panel):** Threaded chat view. Selecting a channel shows its threads; selecting a thread shows messages with reply chains; selecting a message highlights it at the top with its replies.

Message composition is triggered by pressing `c` on a selected message (reply) or on a thread (new message), opening a centered compose modal.

### Alignment with VISION

| VISION Tenet | How the TUI respects it |
|---|---|
| TUI-First | TUI is the primary interface; replaces the deferred `flf tui` placeholder |
| Repo-Anchored | LHS tree is organized by repo + branch |
| Strict Agent Boundaries | TUI has no agent-specific features; agents use CLI |
| No Hidden Agent Memory | TUI is human-facing; no memory beyond thread history |
| SQLite-First & Local | TUI connects to local `flf` daemon via HTTP |
| Append-only | No edit/delete in TUI |
| Glue, not replacement | TUI shows chat/context only; no Kanban, no PR review UI |

### Deliberate Exclusions (YAGNI)

No edit, no delete, no DMs, no workflows, no search, no canvas, no GIFs, no social features. These are buzz-cli features that Fluffle explicitly defers.

---

## 2. Data Model Changes

Two small additions to the SQLite schema.

### 2.1 `channels.repo_head_branch`

```sql
ALTER TABLE channels ADD COLUMN repo_head_branch TEXT;
```

Captures the git branch at channel creation time. Used by the LHS tree to group channels by repo + branch. Populated by `channel create` CLI subcommand (new `--branch` flag).

### 2.2 `messages.parent_id`

```sql
ALTER TABLE messages ADD COLUMN parent_id INTEGER REFERENCES messages(id);
CREATE INDEX IF NOT EXISTS idx_messages_parent ON messages(parent_id);
```

Enables Slack/GitHub-style reply threads within a thread. `parent_id = NULL` (default) means top-level message; `parent_id > 0` means reply to that message.

### 2.3 Migration Strategy

Both changes are additive (new columns, new index). No data migration needed. The `store` package handles schema versioning via `CREATE TABLE IF NOT EXISTS` with all columns present.

---

## 3. Package Structure

```
internal/tui/
├── tui.go          - Entry point: Run() → tea.Program(model)
├── model.go        - Bubble Tea model: state, Init, Update, View
├── tree.go         - LHS: hierarchical tree rendering + navigation
├── tree_filter.go  - LHS: repo filter input + sort toggle
├── chat.go         - RHS: threaded chat view, message rendering, reply chains
├── compose.go      - Compose modal: text box for sending messages/replies
├── api.go          - Thin HTTP wrapper over internal/client
└── styles.go       - Lipgloss styles: colors, borders, typography
```

### 3.1 Responsibilities

| Package | Responsibility |
|---|---|
| `tui.go` | `Run()` entry point, `tea.Model` interface, window resize handling |
| `model.go` | `model` struct, state transitions, key handling dispatch |
| `tree.go` | Tree rendering, expand/collapse, cursor navigation, node selection |
| `tree_filter.go` | Filter input (press `/`), sort toggle (press `s`), filter/sort logic |
| `chat.go` | Chat rendering, thread display, reply chain rendering, scroll handling, cursor |
| `compose.go` | Centered modal, text input, send/cancel, context header |
| `api.go` | HTTP calls to daemon, reuses `internal/client` |
| `styles.go` | Lipgloss style definitions, color scheme, borders |

### 3.2 Dependencies

```
internal/tui/
├── internal/client  (HTTP client, daemon location)
├── internal/store   (data types: Channel, Thread, Message, Reaction)
└── charm.land/bubbletea/v2
    ├── charm.land/lipgloss/v2
    └── github.com/mattn/go-runewidth
```

No new external dependencies beyond the Bubble Tea ecosystem.

---

## 4. UI Design

### 4.1 Layout

```
┌────────────────────┬──────────────────────────────────────────┐
│ 📂 my-project (main)│ # auth-refactor > Schema migration       │
│ ├─ 📂 my-project    │ ───────────────────────────────────────  │
│ │  ├─ 🗨 auth-ref   │ [12:01] Alice: Here's the migration     │
│ │  │  ├─ 🧵 Schema  │              ↓                          │
│ │  │  │  ├─ 💬 Alice│ [12:05] Bob: 👀            (selected)   │
│ │  │  │  │  └─ 💬 Bob│              ↓                          │
│ │  │  │  └─ 💬 Bob  │ [12:06] Alice: Pushing now              │
│ │  │  └─ 🧵 design  │              ↓                          │
│ │  └─ 🗨 ci-discuss│ [12:08] Charlie: LGTM!                   │
│ ├─ 📂 other-repo   │                                          │
│ │  └─ 🗨 general   │                                          │
│ 🗂 Orphaned        │                                          │
│ ├─ 🗨 scratch      │                                          │
│ └─ 🗨 brainstorm   │                                          │
│                    │ > type reply to Bob...                   │
└────────────────────┴──────────────────────────────────────────┘
```

- **LHS:** ~28 chars wide, scrollable tree. Expandable nodes (repos, channels, threads). Leaf nodes (messages) show preview text + timestamp.
- **RHS:** fills remaining width. Thread header at top, threaded messages in middle, compose preview at bottom (when a message is selected).
- **Orphaned channels** appear under a `🗂 Orphaned` group at the bottom of the tree.

### 4.2 Tree Panel (LHS)

#### Hierarchy

```
Repo (expandable)
├── Branch (expandable)
│   ├── Channel (expandable)
│   │   ├── Thread (expandable)
│   │   │   └── Message (leaf — shows preview)
│   │   └── Thread (expandable)
│   │       └── Message (leaf)
│   └── Channel (expandable)
└── Branch (expandable)
```

- **Repo node:** Shows repo name + branch. Expands to show channels.
- **Channel node:** Shows channel name. Expands to show threads.
- **Thread node:** Shows thread title. Expands to show messages.
- **Message leaf:** Shows `💬 Author: preview text [timestamp]`.

Selection cursor highlights the currently focused item. Pressing Enter on a repo/channel/thread expands it. Pressing Enter on a message selects it (RHS updates to show that thread with the message highlighted).

#### Filtering

Press `/` to open a text filter at the top of the tree:

```
Filter: [my-project ]
┌────────────────────┬──────────────────────────────────────────┐
│ 📂 my-project (main)│ ...                                        │
```

Filter matches repo names (case-insensitive substring). Clear with `Esc`.

#### Sorting

Press `s` to cycle sort order:

1. **Last updated** (default) — channels/threads ordered by most recent message timestamp
2. **Alphabetical by name** — sorted by channel/thread name
3. **Alphabetical by repo** — sorted by repo name, then branch, then channel

Sort applies within the tree at each level.

### 4.3 Chat Panel (RHS)

#### Channel View (flat thread list)

When a channel is selected in the tree, the RHS shows threads as a flat list:

```
# auth-refactor
────────────────────────────────────────────────────────────────
 > Schema migration                         12:05
   Alice: Migration script looks good
   Bob: 👀

   design-review                            11:30
   Alice: Here's the design doc...
   Bob: Looks reasonable
```

- `>` prefix indicates the selected thread.
- Each thread shows title, last message timestamp, and a preview of the last message.

#### Thread View (threaded messages)

When a thread is selected, the RHS shows threaded messages:

```
# auth-refactor > Schema migration
────────────────────────────────────────────────────────────────
 [12:01] Alice: Here's the migration script
           ↓
 [12:05] Bob: 👀                (reply to Alice)
           ↓
 [12:06] Alice: Pushing now
           ↓
 [12:08] Charlie: LGTM!
```

- Messages are ordered by `seq` (chronological).
- Reply chains are indented with `↓` connectors.
- Reply count shown for messages with children.

#### Message Selected (highlighted)

When a message is selected (cursor on it), it jumps to the top of the chat view with its reply chain:

```
# auth-refactor > Schema migration
────────────────────────────────────────────────────────────────
 > [12:05] Bob: 👀                (selected)
           ↓
   [12:01] Alice: Here's the migration script
           ↓
   [12:06] Alice: Pushing now
           ↓
   [12:08] Charlie: LGTM!
```

- `>` prefix on the selected message.
- Selected message's reply chain shown immediately below.
- Compose preview at bottom: `> type reply to Bob...`

#### Message Rendering

Each message line: `[HH:MM] Author: content preview`

- Timestamps are relative (e.g., "2m ago", "1h ago") for messages within the last 24h, otherwise absolute date.
- Author names are truncated to 12 chars with ellipsis if longer.
- Content is truncated to fit remaining width.
- Reactions shown as emoji badges after the message text.

### 4.4 Compose Modal

A centered modal overlay for composing messages and replies.

#### Reply Compose

Triggered by pressing `c` on a selected message:

```
┌─────────────────────────────────────────────────────┐
│ Reply to: Bob: 👀                                   │
│                                                     │
│ > type reply...                                     │
│                                                     │
│                              [Send] [Cancel]        │
└─────────────────────────────────────────────────────┘
```

- Header shows "Reply to: [last 40 chars of parent message]"
- Single-line text input (Enter to send)
- Send posts as a reply (parent_id set to selected message ID)
- Cancel closes without sending

#### Thread Compose

Triggered by pressing `c` when no message is selected but a thread is:

```
┌─────────────────────────────────────────────────────┐
│ # auth-refactor > Schema migration                  │
│                                                     │
│ > type message...                                   │
│                                                     │
│                              [Send] [Cancel]        │
└─────────────────────────────────────────────────────┘
```

- Header shows thread path: `# channel > thread title`
- Send posts as a new top-level message (parent_id = 0)

#### Compose Behavior

- `Esc` cancels
- `Enter` sends (validates non-empty content)
- Empty content is rejected (no-op, no beep)
- On successful send, modal closes, chat view refreshes
- On error, modal stays open with error shown in header

---

## 5. Keyboard Bindings

| Key | Context | Action |
|---|---|---|
| `Tab` | Any | Switch focus: tree → chat |
| `Shift+Tab` | Any | Switch focus: chat → tree |
| `↑` / `k` | Tree or chat | Move cursor up in focused panel |
| `↓` / `j` | Tree or chat | Move cursor down in focused panel |
| `g` | Tree or chat | Jump to top of focused panel |
| `G` | Tree or chat | Jump to bottom of focused panel |
| `Enter` | Tree | Expand/collapse node; or select leaf |
| `Enter` | Chat | Select message (highlights at top) |
| `c` | Tree | New message to selected thread |
| `c` | Chat | Compose reply to selected message |
| `/` | Any | Toggle repo filter input in tree |
| `s` | Any | Toggle sort order in tree |
| `Esc` | Chat | Deselect message; close compose |
| `Esc` | Compose | Cancel and close |
| `q` / `Ctrl+C` | Any | Quit |

### 5.1 Focus States

- **Tree focused:** Tree panel has a visible border highlight. Chat panel is dimmed.
- **Chat focused:** Chat panel has a visible border highlight. Tree panel is dimmed.
- **Compose open:** Both panels dimmed. Modal has focus.

---

## 6. API Layer

### 6.1 Reuses `internal/client`

The TUI uses the existing `internal/client` package for daemon communication. No new HTTP client. Same auto-spawn, same health probe, same error handling.

### 6.2 API Wrapper (`api.go`)

```go
type client struct {
    base string
    http *http.Client
}

func New(base string) *client

func (c *client) ListChannels(ctx context.Context, repo, filter string) ([]store.Channel, error)
func (c *client) CreateChannel(ctx context.Context, name, repo, branch string, orphaned bool) (int64, error)
func (c *client) ListThreads(ctx context.Context, channelID int64) ([]store.Thread, error)
func (c *client) CreateThread(ctx context.Context, channelID, title string) (int64, error)
func (c *client) ListMessages(ctx context.Context, threadID int64, last int) ([]store.Message, error)
func (c *client) SendMessage(ctx context.Context, threadID, parentID int64, text string) error
func (c *client) AddReaction(ctx context.Context, messageID int64, emoji string) error
```

### 6.3 Backend API Changes

The `POST /v1/threads/:id/messages` endpoint needs a small update:

```json
// Request body (new field)
{
    "author": "alice",
    "role": "user",
    "content": "Hello",
    "parent_id": 0,        // NEW: 0 = top-level, >0 = reply to message ID
    "agent_id": "...",
    "created_at": "..."
}
```

The `store.AppendMessage` method needs to accept and persist `parent_id`.

### 6.4 Data Fetch Strategy

- **On panel focus change:** Fetch channels (tree focus) or threads/messages (chat focus)
- **After actions:** Fetch updated data after send, create channel, create thread
- **No polling:** Data is refreshed on demand. Consistent with HTTP-only approach per VISION.

---

## 7. Error Handling

| Error | TUI Behavior |
|---|---|
| Daemon down | Status bar shows "daemon down — spawning..." then attempts auto-spawn |
| Network error | Status bar shows error; user can retry with `r` |
| Parse error | Status bar shows error; modal stays open for compose |
| Permission denied (agent) | Status bar shows "agents cannot create channels/threads" |
| Channel not found | Status bar shows error; tree refreshes |
| Thread not found | Status bar shows error; chat refreshes |

All errors are shown in a status bar at the bottom of the TUI (below the compose area when compose is closed). Compose errors are shown in the compose modal header.

---

## 8. Styling

### 8.1 Color Scheme

Uses lipgloss with a neutral, terminal-friendly palette:

- **Tree panel:** Dark background, light text. Selected item highlighted with a blue background.
- **Chat panel:** Light background, dark text. Selected message highlighted with a blue background.
- **Compose modal:** Centered overlay with dimmed background.
- **Status bar:** Bottom of screen, dim text on dark background.
- **Borders:** Light gray between panels.

### 8.2 Typography

- Monospace font (terminal default)
- Tree indentation: 2 spaces per level
- Reply indentation: 4 spaces with `↓` connector
- Timestamps: fixed-width (HH:MM format)
- Author names: truncated at 12 chars

---

## 9. Testing Strategy

### 9.1 API Wrapper (`api_test.go`)

Unit tests with `httptest` server. Same pattern as `internal/apiserver/server_test.go`.

- Test each API method against a mock server
- Test error handling (daemon down, 403, 404, parse errors)
- Test `parent_id` field in `SendMessage`

### 9.2 Tree Logic (`tree_test.go`)

Unit tests for tree data structures:

- Filter by repo (substring match, case-insensitive)
- Sort by last update, alphabetical by name, alphabetical by repo
- Expand/collapse state management
- Cursor navigation (up/down/jump to top/bottom)

### 9.3 Compose Validation (`compose_test.go`)

Unit tests for compose modal:

- Empty message rejected
- Non-empty message accepted
- Context header rendering (reply vs thread)

### 9.4 Manual Testing

The chat view rendering and overall TUI UX are best verified manually. The model is thin enough that manual testing is fast. Key test scenarios:

1. Navigate tree: expand repos → channels → threads → select message
2. Chat view: select channel → see threads → select thread → see messages
3. Threading: select message → see it at top with reply chain
4. Compose: press `c` on message → reply compose → send → verify
5. Compose: press `c` on thread → thread compose → send → verify
6. Filter: press `/` → type repo name → verify filtered tree
7. Sort: press `s` → cycle through sort orders → verify
8. Panel switch: Tab between tree and chat → verify focus
9. Error handling: stop daemon → verify error message → restart → verify recovery

---

## 10. Implementation Plan

### Phase 1: Foundation

1. **Data model changes** — Add `repo_head_branch` to channels, `parent_id` to messages, update `store` package, update `POST /v1/threads/:id/messages` endpoint
2. **CLI updates** — `channel create` accepts `--branch`, `message send` accepts `--parent-id`
3. **`internal/tui/styles.go`** — Lipgloss style definitions
4. **`internal/tui/api.go`** — HTTP wrapper over `internal/client`

### Phase 2: Tree Panel

5. **`internal/tui/tree.go`** — Tree data structure, rendering, expand/collapse, cursor navigation
6. **`internal/tui/tree_filter.go`** — Filter input and sort toggle
7. **Tree unit tests** — Filter, sort, navigation

### Phase 3: Chat Panel

8. **`internal/tui/chat.go`** — Chat rendering, thread display, reply chains, scroll handling
9. **Chat rendering tests** — Message formatting, reply chain layout

### Phase 4: Compose + Integration

10. **`internal/tui/compose.go`** — Compose modal, text input, send/cancel
11. **`internal/tui/model.go`** — Full model: state management, key handling, panel switching
12. **`internal/tui/tui.go`** — Entry point, `Run()` function
13. **Replace `tui` case in `cmd/flf/main.go`** — Launch TUI instead of "deferred" message
14. **Integration tests** — Full TUI workflow manual testing

### Phase 5: Polish

15. **Edge cases** — Empty states (no channels, no threads, no messages), error recovery
16. **Status bar** — Daemon status, action feedback
17. **Reaction support** — Press `r` on a message to open reaction picker (emoji input)

---

## 11. Risks and Mitigations

| Risk | Mitigation |
|---|---|
| Bubble Tea v2 API changes | Pin to specific version; v2 is still in active development |
| Tree rendering performance with large repos | Virtualize tree rendering; only render visible rows |
| Chat view scroll performance with long threads | Virtualize chat rendering; only render visible messages |
| Compose modal UX in narrow terminals | Graceful degradation: modal shrinks, text wraps |
| Daemon connection instability | Auto-reconnect on connection loss; show status in status bar |

---

## 12. Out of Scope (Deferred)

- Reaction picker (emoji input) — Phase 5 only if MVP is clean
- Multi-repo workspace switching — Single repo at a time for MVP
- Message editing/deleting — Append-only, never supported
- Agent invoke from TUI — Agents use CLI only
- WebSocket / live updates — HTTP polling on demand only
- Dark/light theme toggle — Single neutral theme for MVP
- Markdown rendering in messages — Plain text only for MVP
- Thread search — Not in VISION
