# TUI Architecture

Bubble Tea v1.3.10 terminal UI for Fluffle. Flat inbox table with right-side preview panel (wide terminals), centered compose modal overlay. The preview pane has two modes — the thread, and the agent session triggered by the message under the cursor — switched with `s`.

## Overview

```
┌────────────────────────────────────────────────────────────────────────────────┐
│ fluffle                                                                       │
├────────────────────────────────────────────────────────────────────────────────┤
│Inbox — 3 messages · sort:latest ↓ · layout:compact                             │
│  42         TIME  CHANNEL       THREAD            NAME          CONTENT          │
│────────────────────────────────────────────────────────────────────────────────│
│> 42 Sep 23 15:04  eng     pr-review         ci-bot    ✅ build passed           │
│  17 Sep 23 11:00  eng     hello             alice     looks great!   (4+)     │
│                                        │                                      │
│                                        │ Preview: eng › hello                  │
│                                        │ Original Post #2 · Sep 23 09:12 …    │
│                                        │ wraps with no truncation              │
│                                        │ ───────────────────────────────────  │
│                                        │   TIME     NAME     MESSAGE          │
│                                        │  10:00    bob      ship it           │
│                                        │  11:00    ci-bot   ✅ build passed    │
├────────────────────────────────────────────────────────────────────────────────┤
│ ↑↓/j/k nav  Enter view  r reply  v sort  f filter  l layout  s session  p hide  q  │
└────────────────────────────────────────────────────────────────────────────────┘
```

Press `l` for the full layout, where each group expands to its original post plus one `> `-prefixed line per reply:

```
│  42 Sep 23 15:04  eng  pr-review  ci-bot  ✅ build passed                        │
│  17 Sep 23 11:00  eng  hello      alice   looks great!                          │
│                                         > ship it                              │
│                                         > ✅ build passed                      │
│                                         > one more thing                       │
```

Single flat **Global Inbox** table showing all channels/threads, one row per channel/thread group. `l` toggles between two layouts: **compact** (default — one line per group, most recent message) and **full** (original post plus every reply, inline). `p` toggles a right-side preview panel showing the word-wrapped original post + replies; `s` swaps that pane's content for the agent session triggered by the message under the cursor. Preview auto-enables at ≥100 cols, respects toggle at ≥80 cols, forced off below 80, and is suppressed in the detail view and in the full layout.

## Package Structure

```
internal/tui/
├── tui.go          - Entry point: Run() → tea.Program(model)
├── model.go        - Bubble Tea model: state, Init, Update, View, rendering
├── api.go          - Thin HTTP wrapper over daemon REST API
├── session.go      - Agent session pane: preview-mode state, 500 ms poll, session rendering
├── compose.go      - Centered compose modal: text input, send/cancel
└── styles.go       - Lipgloss styles: colors, borders, typography
```

### Responsibilities

| File | Responsibility |
|------|----------------|
| `tui.go` | `Run()` entry point, daemon ensure, `tea.Model` initialization, exit codes (0=success, 1=client/local error, 2=daemon or transport error) |
| `model.go` | Full `model` struct, state transitions via `Update()`, rendering via `View()`, key handling, data fetching, time formatting, preview logic |
| `api.go` | HTTP calls to daemon endpoints, strict response decoding, error classification (`readAPIError`), channel filtering, `jsonBody` helper |
| `session.go` | Agent session state (`sessions`, `sessionsByMsg`, the resolved session and its events), the gated 500 ms poll, the `s` key, `renderSessionPreview()` |
| `compose.go` | Compose modal state machine, text input handling, send/cancel, error display, context header rendering |
| `styles.go` | All lipgloss style definitions: tree panel, chat panel, modal, status bar, hints, colors |

## Dependencies

```
internal/tui/
├── internal/client   (daemon location, auto-spawn, health probe)
├── internal/store    (data types: Channel, Thread, Message, Reaction, Session, SessionEvent)
└── github.com/charmbracelet/bubbletea v1
    ├── github.com/charmbracelet/lipgloss v1
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

Single view: **Inbox Table** (tracked as `viewInbox`). Navigation is cursor-based: `↑/↓` or `j/k` moves through groups. `r`/`c` opens compose for reply. `Enter` opens detail view. `v` toggles sort, `f` opens the filter, `l`/`L` toggles layout, `p` toggles the preview panel.

### State Fields

| Field | Purpose |
|-------|---------|
| `inbox` | All messages across channels/threads (`[]InboxMessage`) |
| `cursor` | Index into the filtered/sorted group list (0-based, clamped to list length) |
| `inboxSort` | `inboxSortLatestDesc` or `inboxSortChannelThreadDesc` |
| `inboxLayout` | `inboxLayoutCompact` or `inboxLayoutFull` |
| `fullThreads` | Cache of `threadID → []Message` backing the full layout (nil = not fetched) |
| `preview` | Whether right-side preview panel is visible |
| `previewThreadID` | Thread ID for deduped preview fetch |
| `previewMessages` | Messages for preview panel (filtered replies) |
| `compose` | Active compose modal state (or inactive) |
| `channels` | Channel cache (for new-thread picker) |
| `status` | Status bar text (errors, counts, hints) |
| `width, height` | Current terminal dimensions (from `WindowSizeMsg`) |
| `previewMode` | `previewThread` or `previewSession` — which content the right pane shows |
| `sessions` | Sessions for `previewThreadID`, as returned by the last poll |
| `sessionsByMsg` | `triggerMessageID → Session` index built from `sessions`; the pane's lookup key |
| `session` | The session whose events are loaded (`nil` until `s` fetches them) |
| `sessionEvents` | Events for `session`, in `seq` order |
| `sessionPollThreadID` | Thread the current poll is armed for; `0` means no poll is armed |
| `sessionTickThread` | Thread a tick is in flight for, so arming stays idempotent |
| `sessionTickGen` | Monotonic tick generation; a `sessionTickMsg` with a stale gen is dropped |

### Data Fetching

Message data is fetched on demand. Agent session data is **polled**: while a session is live, the thread's session list is re-fetched every 500 ms.

| Event | Fetch |
|-------|-------|
| `Init()` | `ListInbox()` — all messages across channels/threads |
| Cursor moves to new thread | `GET /v1/threads/:id/messages` (debounced by thread ID) |
| Full layout, viewport intersects an uncached group | `GET /v1/threads/:id/messages` for every such group, batched concurrently |
| Cursor moves while the preview is visible | `GET /v1/threads/:id/messages` (debounced by thread ID) |
| After send/create | `fetchInbox()` — refetch entire inbox, clears `fullThreads` |
| Preview panel active + cursor moves | Preview data for highlighted item (deduped) |
| Preview messages land for a new thread | `GET /v1/threads/:id/sessions` — one fetch per thread, deduped by `sessionPollThreadID` |
| `s` pressed on a message with a session | `GET /v1/sessions/:id` — events for that session, once per press |
| A session is `queued`/`running` and the pane is visible | `GET /v1/threads/:id/sessions` again, on a 500 ms `tea.Tick` |
| The last session in the thread reaches a terminal status | Nothing further is scheduled for that thread — see the note below |

Preview message fetching is idempotent and gated on `previewVisible()`: skips if the cursor hasn't changed, if width < 80, if the same thread is already cached, or if the pane is not actually drawn (detail view, or full inbox layout).

The session fetch is gated on `previewVisible()` too, plus three more conditions: the view must be the inbox, the preview thread must be non-zero, and no poll may already be armed for that thread (`sessionPollThreadID`). The tick handler re-checks the generation, the armed thread, `previewVisible()`, and "is anything still live" before it re-fetches, and releases the poll (setting `sessionPollThreadID` back to `0`) when the pane is hidden or a fetch fails. A `sessionTickMsg` whose generation is stale is dropped without touching state, so an in-flight tick from a previous thread or a superseded arming cannot resurrect a poll.

The poll covers the *lifetime of a live session*, not the thread: once every session in the thread is terminal, `syncSessionTick()` arms no further tick. Because the per-thread dedupe key is only cleared when the pane is hidden, a fetch fails, or the cursor moves to a different thread, a session started in that thread afterwards arrives on the next fetch — moving to another thread and back, or toggling the pane — not on its own.

`syncVisibleData()` is the single entry point for cursor-move-driven fetches. It returns `tea.Batch(maybeFetchPreview(), maybeFetchFullRows(), fetchSessionsForPreview())`; `tea.Batch` returns the sole non-nil command directly, so the three fetches collapse to however many actually need to run.

## Inbox Layouts

`inboxLayout` selects the row renderer. Both layouts group by channel/thread, share column widths, the title, and the cursor model.

| Aspect | Compact | Full |
|--------|---------|------|
| Lines per group | 1 | 1 + reply count (1 extra while loading) |
| Content column shows | Most recent message | Original post (lowest `seq`) |
| Reply count marker | `(N+)` | none — replies are listed |
| `scroll` unit | Rows (== groups) | Rendered lines |
| Thread messages needed | No | Yes — `GET /v1/threads/:id/messages` |

**Key functions:**

| Function | Role |
|----------|------|
| `inboxFullBlocks()` | One `inboxFullBlock` per group: `im` (group representative, carries channel/thread), `head` (OP), `replies`, `loading` |
| `inboxFullBlockRanges()` | Start/end line offsets per group — width-independent, since every reply is exactly one line |
| `inboxFullGeometry(w, idW)` | Column layout for the current pane width; drops NAME → CHANNEL → THREAD → TIME until the content column has `minContentWidth` |
| `renderInboxFullWithWidth(w, h)` | Flattens all groups to styled lines tagged with their group index, then windows by line |
| `clampInboxFullScroll()` | Full-layout branch of `clampCursor`; keeps the cursor's whole block on screen |

`inboxContentCol(idW)` and `inboxRowLine(...)` are shared by both layouts so the compact and full CONTENT columns land on the same offset. `splitPaneWidths(totalW)` is shared between `baseView()` and `inboxListWidth()` so the geometry used for fetch decisions cannot drift from what is drawn.

### Preview Visibility

`previewVisible()` is the single predicate for "is the right-side pane drawn". It is false when the preference is off, below 80 cols, in the detail view, and in the full inbox layout. `baseView()`, `inboxListWidth()`, `listHeight()`, `maybeFetchPreview()`, and `fetchSessionsForPreview()`/`syncSessionTick()` all consult it, so neither message data nor session data can be fetched for a pane that is not on screen.

### Layout / Preview Invariant

**`m.preview == true` implies `m.inboxLayout == inboxLayoutCompact`.** Layout and preview are mutually exclusive, so neither key can be a silent no-op:

| Key | Effect |
|-----|--------|
| `p` → preview **on** | Forces compact (status: `preview on — p to hide · layout:compact`) |
| `p` → preview **off** | Layout untouched, so `p` twice is a no-op |
| `l`/`L` → **full** | Forces preview off (status: `layout: full — preview off · l to switch`) |
| `l`/`L` → compact | Preview untouched (stays off) |
| `WindowSizeMsg` ≥100 cols | Auto-enables preview only in compact, never in full |

`previewVisible()` no longer needs a full-layout clause to hide the pane — the pane is simply off whenever the layout is full — but it keeps the check so rendering stays correct even if the two fields are ever set independently (e.g. from a test or a future layout).

### Messages (Bubble Tea Msg Types)

| Msg Type | Carries | Triggers |
|----------|---------|----------|
| `inboxFetchedMsg` | `[]InboxMessage`, `error` | `fetchInbox()` completes |
| `previewMessagesFetchedMsg` | `threadID`, `[]Message`, `error` | `fetchPreviewMessages()` completes |
| `sessionsFetchedMsg` | `threadID`, `[]Session`, `error` | `fetchSessions()` completes — the initial fetch and every poll tick |
| `sessionEventsFetchedMsg` | `Session`, `[]SessionEvent`, `error` | `fetchSessionEvents()` completes after `s` |
| `sessionTickMsg` | `threadID`, `gen` | A `tea.Tick` fires; re-arms the next poll or releases it |
| `fullRowsFetchedMsg` | `map[threadID][]Message`, `error` | `fetchFullRows()` completes |
| `composeSendMsg` | `text`, `composeMode`, `context` | User presses Enter in compose |
| `threadCreatedMsg` | `channelID`, `threadID`, `title`, `error` | Thread creation response |
| `tea.WindowSizeMsg` | `Width`, `Height` | Terminal resize |
| `tea.KeyMsg` | key code/text | Any key press |

`sessionsFetchedMsg` is discarded when its `threadID` is no longer the preview thread, and a failed session fetch releases the poll instead of retrying. `sessionEventsFetchedMsg` also sets `previewMode = previewSession`, so the pane cannot show a session the user did not ask for.

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

Renders the inbox in the active layout (`inboxLayoutFull` dispatches to `renderInboxFullWithWidth`):

1. Title: `Inbox — N messages · sort:<sort> · layout:<layout>`
2. Header row: `TIME | CHANNEL | THREAD | NAME | CONTENT`
3. Fixed column widths: time 12, channel 12, thread 16, name 12, content fills remaining
4. Single-line truncation for content with ellipsis
5. Color-coded name column (`getNameStyle`)
6. Cursor row highlighted (`chatMsgSelectedStyle`)
7. Empty state: `(no messages)` or `(no messages — filtered, press f to clear)`
8. Truncated to panel height, padded with empty lines if short

### `renderPreview(w int)`

Renders the preview panel for the cursor-highlighted inbox group. In the inbox view it first checks `previewMode`: if it is `previewSession`, it delegates to `renderSessionPreview(w, h)` and never draws thread content.

The thread branch:

- Title: `#channel › thread · last <time> · N replies`
- Root post: word-wrapped fully with `wrapText`, no truncation
- Replies: fill remaining height, newest-tail truncation when space runs out
- Empty state: `(no message)` or `(no replies)`

### `renderSessionPreview(w, h)` (`session.go`)

Renders the agent session triggered by the message under the cursor. It re-resolves the session from `sessionForCursor()` on every render, so moving the cursor updates the pane without another fetch, and it falls back to the loaded `m.session` when the polled list has no entry for the row.

- Header: `SESSION  <agent> · <status> · <reply mode> · #<id>`, plus elapsed duration once terminal, `replied #<seq>` when the daemon posted the reply, and the failure reason when there is one. Stripped of ANSI and newlines, then truncated to the pane width.
- Events: one line per event, `<type>` in a fixed 8-column field, then the **first line** of the event content with tabs expanded, truncated to the pane width. Events whose session differs from the loaded `m.session` are not shown — the pane will not display one session's events under another's header.
- Overflow: the oldest events that fit are shown and a `… N hidden …` line counts the rest. The tail of a verbose run is not reachable from the pane.
- Placeholders: `no session on this message`, `(no events loaded)`, `(running…)`, `(no events)`.
- Returns `""` when the pane is under 3 rows tall, and clamps its width up to `minContentWidth` otherwise, so a narrow terminal cannot panic the renderer.

### `helpView()`

Context-sensitive help bar at bottom of list. Shows available key bindings for current view. The inbox hint row carries a session hint that reflects the pane's current mode — `s session` normally, `s thread` while the session pane is showing — so the key always advertises where it will take you back to.

### `center(s string, width int)`

Centers a multi-line string horizontally within `width` columns. Used for compose modal overlay.

## Error Handling

| Error | TUI Behavior |
|-------|-------------|
| Daemon down (startup or read transport) | `tui.Run()` returns exit code 2 for startup; a read failure shows `error: DAEMON_DOWN: ...` |
| Read response cannot be decoded | Status bar or compose modal shows `error: DAEMON_ERROR: ...`; prior data is retained |
| Session list fetch fails | Status bar shows `error: ...`; the poll is released rather than retried, and the previously rendered sessions stay on screen |
| Session events fetch fails | Status bar shows `error: ...`; the pane stays in session mode showing `(no events loaded)` |
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
              ├─ ↑↓/j/k ──▶ cursor++, cursor-- ──▶ syncVisibleData()
              ├─ r/c ──▶ handleReply() ──▶ compose.Open(composeModeMessage)
              ├─ Enter ──▶ viewInboxDetail (fullscreen thread)
              ├─ n ──▶ handleNewThread() ──▶ compose.Open(composeModeNewThread)
              ├─ l/L ──▶ toggle inbox layout ──▶ syncVisibleData()
              ├─ p ──▶ toggle preview ──▶ syncVisibleData()
              ├─ s ──▶ handleSessionKey() ──▶ toggle previewMode
              │         └─ thread → session ──▶ fetchSessionEvents()
              ├─ Esc ──▶ no-op (inbox) / return to inbox (detail)
              └─ q ──▶ quitting = true ──▶ tea.Quit
  │
  ▼
fetch cmd resolves ──▶ *FetchedMsg ──▶ Update() applies it ──▶ re-arms the next cmd
  │
  └─ sessionTickMsg ──▶ drop if gen is stale, else release or re-fetch
  │
  ▼
tea.View() called by Bubble Tea runtime
  │
  └─ renders based on current model state
      └─ preview pane = renderSessionPreview() when previewMode == previewSession
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
2. **Preview panel**: Shows the word-wrapped original post + replies. Adaptive Y prevents overflow. Toggle with `p`.
3. **Reply via `r`/`c`**: Opens compose with `threadID` + `parentID` set from cursor position for threaded replies.
4. **Enter opens detail view**: On inbox, `Enter` opens the selected thread in a fullscreen scrollable detail view. `Esc` returns to inbox, preserving cursor position. Detail view is read-only; `r` from detail opens compose for reply.
5. **Full-post wrapping in preview and detail**: Preview root post is word-wrapped with no truncation (via `wrapText`), replies fill remaining pane height with newest-tail truncation. Detail view wraps all messages fully — no ellipsis truncation on message content.
6. **Fill-to-height preview**: Replies in the preview pane fill available space; when content exceeds height, oldest replies are dropped and newest are kept (tail truncation).
7. **No filter/sort**: Explicitly deferred. YAGNI.
8. **Preview auto-enables at ≥100 cols**: Below 100 cols, user must press `p` to enable. At 80-99 cols, preview available but off by default. Below 80 cols, forced off.
9. **Status bar at bottom**: Always visible, shows action feedback, error messages, and context-sensitive hints.
10. **Compose modal centered**: Full width of terminal, height scales with terminal height (min 5, max 12 lines).
11. **Channel cache retained**: Only for new-thread picker, not for inbox rendering.
12. **Grouping via columns only**: No injected date/group headers; deferred to follow-up.
13. **Cursor starts at top (0)**: Keeps `↑↓` natural; start-at-bottom can be added with `G` key later.
14. **Detail view hides preview**: When in detail view, the split-pane preview is suppressed — the thread fills the full terminal width.
15. **Inbox cursor preserved on Esc**: Entering detail saves `cursor`/`scroll`; returning via `Esc` restores them.
16. **Preview panel hidden in detail**: `View()` early-returns for `viewInboxDetail` with single-pane rendering; the preview split condition guards `&& m.view != viewInboxDetail`.
17. **Preview and full layout are mutually exclusive**: full layout already inlines the original post and every reply, and a split pane is too narrow for the content column, so the rows take the full width. Rather than leave `p` and `l` fighting over one pane, the two keys are defined so `preview == true` always implies `compact`: `p`-on forces compact, and switching to full turns the preview off.
18. **Session preview reuses the preview pane instead of adding a view**: a session belongs to a message, so showing it anywhere but next to the message that triggered it would hide the trigger. `s` flips the pane's content rather than pushing a view, which keeps `previewVisible()` the one gate for both content modes and keeps the detail view, the full layout, and every width rule unchanged.
19. **Session data is polled, message data is not**: messages arrive when the human acts, so on-demand fetching is enough. A session is a subprocess the human is not typing into, and the daemon exposes no push channel, so the pane re-reads the thread's session list every 500 ms — but only while something is actually `queued` or `running`, and only while the pane is drawn. An idle TUI issues no session requests at all.

## wrapText Helper

`func wrapText(s string, width int) []string` — splits `s` on `\n`, then word-wraps each paragraph to `width` columns (preserving words, breaking long words at `width`). Returns slice of lines, all `len(line) <= width`. Used by `renderPreview` (inbox branch) and `renderInboxDetail` for full-post wrapping with no truncation.

## Deferred Features

- Date grouping / relative timestamps
- Reply count inline (` ↳ 5 replies`)
- Reaction display in list view
- Visual reply threading (indentation + tree lines)
- Density modes beyond the compact/full inbox layouts
- Search/filter with visual feedback
- Unread indicators per channel/thread
- Start-at-bottom cursor (e.g. `G` key)
- Thread grouping in inbox (by channel/thread)
