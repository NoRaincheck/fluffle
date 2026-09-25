# TUI Architecture

Bubble Tea v2 terminal UI for Fluffle. Flat inbox table with right-side preview panel (wide terminals), centered compose modal overlay.

## Overview

```
┌──────────────────────────────────────────────────────────────────┐
│ Inbox — 42 messages · last 2m ago                                │
├──────┬──────────┬─────────┬──────────┬──────────────────────────┤
│ TIME │ CHANNEL  │ THREAD  │ SENDER   │ CONTENT                  │
├──────┼──────────┼─────────┼──────────┼──────────────────────────┤
│ 11:05│ ▸general │ hello   │ 👤alice  │ looks great!             │
│ 11:00│ random   │ pr-rev  │ 🤖ci-bot │ ✅ build passed          │
│ 10:45│ general  │ hello   │ 👤bob    │ check out this PR        │
├──────┴──────────┴─────────┴──────────┴──────────────────────────┤
│ ↑↓ nav · r reply · n new thread · L preview · q quit            │
└──────────────────────────────────────────────────────────────────┘
```

Single flat **Global Inbox** table showing all messages across all channels/threads. `L` toggles a right-side preview panel showing the full selected message + last 5 replies. Preview auto-enables at ≥100 cols, respects toggle at ≥80 cols, forced off below 80.

## Package Structure

```
internal/tui/
├── tui.go          - Entry point: Run() → tea.Program(model)
├── model.go        - Bubble Tea model: state, Init, Update, View, rendering
├── api.go          - Thin HTTP wrapper over daemon REST API
├── compose.go      - Centered compose modal: text input, send/cancel
└── styles.go       - Lipgloss styles: colors, borders, typography
```

### Responsibilities

| File | Responsibility |
|------|----------------|
| `tui.go` | `Run()` entry point, daemon ensure, `tea.Model` initialization, exit codes (0=success, 1=client/local error, 2=daemon or transport error) |
| `model.go` | Full `model` struct, state transitions via `Update()`, rendering via `View()`, key handling, data fetching, time formatting, preview logic |
| `api.go` | HTTP calls to daemon endpoints, strict response decoding, error classification (`readAPIError`), channel filtering, `jsonBody` helper |
| `compose.go` | Compose modal state machine, text input handling, send/cancel, error display, context header rendering |
| `styles.go` | All lipgloss style definitions: tree panel, chat panel, modal, status bar, hints, colors |

## Dependencies

```
internal/tui/
├── internal/client   (daemon location, auto-spawn, health probe)
├── internal/store    (data types: Channel, Thread, Message, Reaction)
└── charm.land/bubbletea/v2
    ├── charm.land/lipgloss/v2
    └── github.com/mattn/go-runewidth
```

No new external dependencies beyond the Bubble Tea ecosystem. The TUI reuses `internal/client` for daemon communication — same auto-spawn, same health probe, same error handling.

## State Machine

```
                    ┌──────────────────────────────────────┐
                    │                                      │
                    ▼                                      │
  [Inbox Table] ──r/c/n──▶ [Compose]                      │
     ▲                       │    │                        │
     │                       │    │ Esc (cancel)           │
     │                       │    │                        │
     │     Enter             │    │                        │
     └─────▼── inbox detail ─┘    │                        │
                    │              │                        │
                    └─────Esc──────┘                        │
```

Single view: **Inbox Table** (tracked as `viewInbox`). Navigation is cursor-based: `↑/↓` or `j/k` moves through messages. `r`/`c` opens compose for reply. `n` opens compose for new thread. `Enter` opens detail view. `L` toggles preview panel.

### State Fields

| Field | Purpose |
|-------|---------|
| `inbox` | All messages across channels/threads (`[]InboxMessage`) |
| `cursor` | Index into inbox (0-based, clamped to list length) |
| `preview` | Whether right-side preview panel is visible |
| `previewThreadID` | Thread ID for deduped preview fetch |
| `previewMessages` | Messages for preview panel (filtered replies) |
| `compose` | Active compose modal state (or inactive) |
| `channels` | Channel cache (for new-thread picker) |
| `status` | Status bar text (errors, counts, hints) |
| `width, height` | Current terminal dimensions (from `WindowSizeMsg`) |

### Data Fetching

Data is fetched on demand, never polled:

| Event | Fetch |
|-------|-------|
| `Init()` | `ListInbox()` — all messages across channels/threads |
| Cursor moves to new thread | `GET /v1/threads/:id/messages` (debounced by thread ID) |
| After send/create | `fetchInbox()` — refetch entire inbox |
| Preview panel active + cursor moves | Preview data for highlighted item (deduped) |

Preview fetching is idempotent: skips if cursor hasn't changed, or if width < 80, or if same thread already fetched.

### Messages (Bubble Tea Msg Types)

| Msg Type | Carries | Triggers |
|----------|---------|----------|
| `inboxFetchedMsg` | `[]InboxMessage`, `error` | `fetchInbox()` completes |
| `previewMessagesFetchedMsg` | `threadID`, `[]Message`, `error` | `fetchPreviewMessages()` completes |
| `composeSendMsg` | `text`, `composeMode`, `context` | User presses Enter in compose |
| `threadCreatedMsg` | `channelID`, `threadID`, `title`, `error` | Thread creation response |
| `tea.WindowSizeMsg` | `Width`, `Height` | Terminal resize |
| `tea.KeyMsg` | key code/text | Any key press |

## Rendering Pipeline

```
View()
  ├─ compose active? ──yes──▶ render list (full width) + center(compose view)
  │
  └─ no ──▶ preview? ──yes──▶ render list (left half) + render preview (right half)
  │                            └── lipgloss.JoinHorizontal(Top, left, right)
  │
  └─ no ──▶ render list (full width)
  │
  └─ append helpView() + status
```

### `renderInboxWithWidth(w int)`

Renders the inbox as a `lipgloss/table`:

1. Title: `Inbox — N messages · last <time>`
2. Header row: `TIME | CHANNEL | THREAD | SENDER | CONTENT`
3. Fixed column widths: time 8, channel 12, thread 16, sender 12, content fills remaining
4. Single-line truncation for content with ellipsis
5. Color-coded sender column (`getAuthorStyle`)
6. Cursor row highlighted (`chatMsgSelectedStyle`)
7. Empty state: `(no messages — press n for new thread)`
8. Truncated to panel height, padded with empty lines if short

### `renderPreview(w int)`

Renders the preview panel for the cursor-highlighted inbox message:

- Title: `Preview: #channel › thread · author`
- Root post: word-wrapped fully with `wrapText`, no truncation
- Replies: fill remaining height, newest-tail truncation when space runs out
- Adaptive Y calculation for available space
- Empty state: `(no message)` or `(no replies)`

### `helpView()`

Context-sensitive help bar at bottom of list. Shows available key bindings for current view.

### `center(s string, width int)`

Centers a multi-line string horizontally within `width` columns. Used for compose modal overlay.

## Error Handling

| Error | TUI Behavior |
|-------|-------------|
| Daemon down (startup or read transport) | `tui.Run()` returns exit code 2 for startup; a read failure shows `error: DAEMON_DOWN: ...` |
| Read response cannot be decoded | Status bar or compose modal shows `error: DAEMON_ERROR: ...`; prior data is retained |
| Mutation connection failure before dispatch | `error: DAEMON_DOWN: ...`; no acknowledgement is possible |
| Dispatched mutation timeout, response loss, or acknowledgement failure | `error: DELIVERY_UNKNOWN: ...`; the modal stays open and the user re-reads the thread before retrying |
| Daemon error response | Rendered as `error: <CODE>: <message>` from the daemon error envelope |
| Empty compose | Compose modal shows "cannot be empty" in red |
| Empty inbox | Status bar shows "no messages — press n for new thread" |

Error strings are prefixed with a contract code: `DAEMON_DOWN` when the daemon cannot be reached or a connection cannot be established, `DAEMON_ERROR` when a response cannot be decoded or the daemon reports a server-side failure, and `DELIVERY_UNKNOWN` when a dispatched write may have committed without a verifiable acknowledgement. All errors go through the status bar or compose modal — never panic.

## Styling System

All colors use the **Tokyo Night** palette via lipgloss `color.RGBA` values:

| Element | Background | Foreground |
|---------|-----------|------------|
| Tree panel | `#1e1e2e` (dark) | `#c9d6f4` (light) |
| Tree selected | `#313244` | `#f5f5f5` |
| Chat panel | `#282838` | `#b4bee2` |
| Chat selected | `#313244` | `#f5f5f5` |
| Modal | `#313244` | `#f5f5f5` |
| Modal border | — | `#89b4fa` (blue accent) |
| Status bar | `#181825` | `#89b4fa` |
| Hints | — | `#646478` (dim) |
| Key hints | — | `#89b4fa` (accent, bold) |

Styles are defined as package-level `var`s in `styles.go`. Each style uses lipgloss's fluent builder pattern with `.Border()`, `.BorderForeground()`, `.Background()`, `.Foreground()`, `.Padding()`, `.Width()`, `.Margin*()`.

## Data Flow

```
User presses key
  │
  ▼
model.Update(tea.KeyMsg)
  │
  ├─ compose active? ──yes──▶ composeModel.Update(key) ──▶ composeSendMsg
  │
  └─ no ──▶ handleKey(key)
              │
              ├─ ↑↓/j/k ──▶ cursor++, cursor-- ──▶ maybeFetchPreview()
              ├─ r/c ──▶ handleReply() ──▶ compose.Open(composeModeMessage)
              ├─ Enter ──▶ viewInboxDetail (fullscreen thread)
              ├─ n ──▶ handleNewThread() ──▶ compose.Open(composeModeNewThread)
              ├─ L/l ──▶ toggle preview ──▶ maybeFetchPreview()
              ├─ Esc ──▶ no-op (inbox) / return to inbox (detail)
              └─ q ──▶ quitting = true ──▶ tea.Quit
  │
  ▼
tea.View() called by Bubble Tea runtime
  │
  └─ renders based on current model state
```

## Testing

### Unit Test (`simple_test.go`)

Tests the core inbox flow: window resize → inbox fetch → cursor nav → reply compose with threadID → adaptive preview truncation. Uses `toModel()` to extract `model` from `tea.Model` interface.

## InboxMessage Data Type

Enriched message type joining messages→threads→channels:

```go
type InboxMessage struct {
    Message
    ChannelName string `json:"channel_name"`
    ThreadTitle string `json:"thread_title"`
    ChannelID   int64  `json:"channel_id"`
}
```

## ListInbox Store Method

`ListInbox(limit int) ([]InboxMessage, error)` — SQL joins `messages → threads → channels`, excludes archived, orders DESC then reverses to ASC. Limits capped at 200, negative defaults to 100.

## GET /v1/inbox API Endpoint

`GET /v1/inbox?limit=100` → `[]InboxMessage` (JSON, `Content-Type: application/json`). Reuses daemon auth. Existing `GET /v1/threads/:id/messages` serves preview.

## Inbox Detail View

`viewInboxDetail` — fullscreen scrollable thread view. Entered via `Enter` from inbox. `Esc` returns to inbox, preserving `cursor`/`scroll`.

**Rendering:**
- Full terminal width, single pane (no preview split)
- Title: `#channel › thread · last <time>`
- Each message: `[TIME] #SEQ Author: wrapped content...`
- Continuation lines indented 20 spaces under content column
- `wrapText` used for all message content — no truncation
- `detailScroll` tracks vertical scroll offset; `detailCursor` tracks selected message index
- `clampDetailScroll()` keeps cursor visible within visible window
- Empty state: `(loading…)` or `(no messages — press r to reply)`

**State fields:**
| Field | Purpose |
|-------|---------|
| `detailThreadID` | Thread ID for the detail view (int64) |
| `detailScroll` | Vertical scroll offset (int) |
| `detailCursor` | Selected message index (int) |
| `savedInboxCursor` | Inbox cursor saved before entering detail |
| `savedInboxScroll` | Inbox scroll saved before entering detail |

## Adaptive Preview Y

```
hAvail := previewHeight - 4
replyReserve := 5 * 2
Y := clamp(3, 20, hAvail - replyReserve - 2)
```

## Design Decisions

1. **Flat inbox over 3-view stack**: All messages in one table. Eliminates Enter/Esc navigation for scanning. `↑↓` is the only nav.
2. **Preview panel**: Shows full message + last 5 replies. Adaptive Y prevents overflow. Toggle with `L`.
3. **Reply via `r`/`c`**: Opens compose with `threadID` + `parentID` set from cursor position for threaded replies.
4. **Enter opens detail view**: On inbox, `Enter` opens the selected thread in a fullscreen scrollable detail view. `Esc` returns to inbox, preserving cursor position. Detail view is read-only; `r` from detail opens compose for reply.
5. **Full-post wrapping in preview and detail**: Preview root post is word-wrapped with no truncation (via `wrapText`), replies fill remaining pane height with newest-tail truncation. Detail view wraps all messages fully — no ellipsis truncation on message content.
6. **Fill-to-height preview**: Replies in the preview pane fill available space; when content exceeds height, oldest replies are dropped and newest are kept (tail truncation).
7. **No filter/sort**: Explicitly deferred. YAGNI.
8. **Preview auto-enables at ≥100 cols**: Below 100 cols, user must press `L` to enable. At 80-99 cols, preview available but off by default. Below 80 cols, forced off.
9. **Status bar at bottom**: Always visible, shows action feedback, error messages, and context-sensitive hints.
10. **Compose modal centered**: Full width of terminal, height scales with terminal height (min 5, max 12 lines).
11. **Channel cache retained**: Only for new-thread picker, not for inbox rendering.
12. **Grouping via columns only**: No injected date/group headers; deferred to follow-up.
13. **Cursor starts at top (0)**: Keeps `↑↓` natural; start-at-bottom can be added with `G` key later.
14. **Detail view hides preview**: When in detail view, the split-pane preview is suppressed — the thread fills the full terminal width.
15. **Inbox cursor preserved on Esc**: Entering detail saves `cursor`/`scroll`; returning via `Esc` restores them.
16. **Preview panel hidden in detail**: `View()` early-returns for `viewInboxDetail` with single-pane rendering; the preview split condition guards `&& m.view != viewInboxDetail`.

## wrapText Helper

`func wrapText(s string, width int) []string` — splits `s` on `\n`, then word-wraps each paragraph to `width` columns (preserving words, breaking long words at `width`). Returns slice of lines, all `len(line) <= width`. Used by `renderPreview` (inbox branch) and `renderInboxDetail` for full-post wrapping with no truncation.

## Deferred Features

- Date grouping / relative timestamps
- Reply count inline (` ↳ 5 replies`)
- Reaction display in list view
- Visual reply threading (indentation + tree lines)
- Toggle density modes (compact/standard/expanded)
- Search/filter with visual feedback
- Unread indicators per channel/thread
- Start-at-bottom cursor (e.g. `G` key)
- Thread grouping in inbox (by channel/thread)
