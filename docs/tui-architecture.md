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
| `tui.go` | `Run()` entry point, daemon ensure, `tea.Model` initialization, exit codes (0=success, 1=error, 2=daemon down) |
| `model.go` | Full `model` struct, state transitions via `Update()`, rendering via `View()`, key handling, data fetching, time formatting, preview logic |
| `api.go` | HTTP calls to daemon endpoints, error parsing (`readAPIError`), channel filtering, `jsonBody` helper |
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
     └─────↑/j cursor nav ───┘    │                        │
                    │              │                        │
                    └──────────────┘                        │
```

Single view: **Inbox Table** (tracked as `viewInbox`). Navigation is cursor-based: `↑/↓` or `j/k` moves through messages. `r`/`c`/`Enter` opens compose for reply. `n` opens compose for new thread. `L` toggles preview panel.

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
- Full message content, truncated to Y lines (adaptive calculation)
- Truncation marker: `... (+N lines)` if truncated
- Last 5 replies, each one line truncated
- Empty state: `(no message)` or `(no replies)`

### `helpView()`

Context-sensitive help bar at bottom of list. Shows available key bindings for current view.

### `center(s string, width int)`

Centers a multi-line string horizontally within `width` columns. Used for compose modal overlay.

## Error Handling

| Error | TUI Behavior |
|-------|-------------|
| Daemon down | `tui.Run()` returns exit code 2, prints to stderr |
| HTTP fetch error | Status bar shows "error: ..."; retains prior inbox |
| Compose send error | Compose modal header shows error; modal stays open |
| Empty compose | Compose modal shows "cannot be empty" in red |
| Empty inbox | Status bar shows "no messages — press n for new thread" |

All errors go through the status bar or compose modal — never panic.

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
              ├─ r/c/Enter ──▶ handleReply() ──▶ compose.Open(composeModeMessage)
              ├─ n ──▶ handleNewThread() ──▶ compose.Open(composeModeNewThread)
              ├─ L/l ──▶ toggle preview ──▶ maybeFetchPreview()
              ├─ Esc ──▶ no-op (reserved for future filter clear)
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

## Adaptive Preview Y

```
hAvail := previewHeight - 4
replyReserve := 5 * 2
Y := clamp(3, 20, hAvail - replyReserve - 2)
```

## Design Decisions

1. **Flat inbox over 3-view stack**: All messages in one table. Eliminates Enter/Esc navigation for scanning. `↑↓` is the only nav.
2. **Preview panel**: Shows full message + last 5 replies. Adaptive Y prevents overflow. Toggle with `L`.
3. **Reply via `r`/`c`/`Enter`**: All three keys open compose. `threadID` + `parentID` set from cursor position for threaded replies.
4. **No filter/sort**: Explicitly deferred. YAGNI.
5. **Preview auto-enables at ≥100 cols**: Below 100 cols, user must press `L` to enable. At 80-99 cols, preview available but off by default. Below 80 cols, forced off.
6. **Status bar at bottom**: Always visible, shows action feedback, error messages, and context-sensitive hints.
7. **Compose modal centered**: Full width of terminal, height scales with terminal height (min 5, max 12 lines).
8. **Channel cache retained**: Only for new-thread picker, not for inbox rendering.
9. **Grouping via columns only**: No injected date/group headers; deferred to follow-up.
10. **Cursor starts at top (0)**: Keeps `↑↓` natural; start-at-bottom can be added with `G` key later.

## Deferred Features

- Date grouping / relative timestamps
- Reply count inline (` ↳ 5 replies`)
- Reaction display in list view
- Visual reply threading (indentation + tree lines)
- Toggle density modes (compact/standard/expanded)
- Search/filter with visual feedback
- Unread indicators per channel/thread
