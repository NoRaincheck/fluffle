# TUI Key Bindings Reference

All key bindings for the Fluffle TUI (`flf tui`). Context-sensitive: some keys only work in specific views or states.

## Quick Reference

| Key | Inbox | Compose |
|-----|-------|---------|
| `↑` / `k` | Move cursor up / scroll up | Move cursor left |
| `↓` / `j` | Move cursor down / scroll down | Move cursor right |
| `Enter` | View details: open fullscreen scrollable thread | Send message |
| `r` / `c` | Reply: open compose | — |
| `n` | New thread: open compose | — |
| `l` / `L` | Toggle inbox layout: compact ↔ full | — |
| `p` | Toggle preview panel | — |
| `q` | Quit | — |
| `Ctrl+C` | Quit | Quit |
| `Esc` | No-op | Cancel |
| `Backspace` | — | Delete char before cursor |
| `Delete` | — | Delete char at cursor |
| `Left` | — | Move cursor left |
| `Right` | — | Move cursor right |

## Detail View

Fullscreen scrollable thread view. Entered via `Enter` from inbox. `Esc` returns to inbox.

| Key | Action |
|-----|--------|
| `↑` / `k` | Scroll up / move cursor up one message |
| `↓` / `j` | Scroll down / move cursor down one message |
| `r` | Reply: open compose with `threadID` + `parentID` |
| `Esc` | Return to inbox (preserves cursor position) |
| `q` | Quit |
| `Ctrl+C` | Quit TUI entirely |

**Message display:**
- Full content wrapped (no truncation)
- Format: `[TIME] #SEQ Author: wrapped content...`
- Continuation lines indented under content column
- Selected message highlighted with `▸` prefix

## Inbox View

Grouped table: one row per channel/thread, showing that group's most recent message. Two layouts, switched with `l` (or `L`); compact is the default. The active layout is always named in the panel title (`· layout:compact` / `· layout:full`).

| Key | Action |
|-----|--------|
| `↑` / `k` | Move cursor up one group / scroll up |
| `↓` / `j` | Move cursor down one group / scroll down |
| `r` / `c` | Reply: open compose with `threadID` + `parentID` set from cursor position |
| `Enter` | Open detail view: fullscreen scrollable thread |
| `n` | New thread: opens compose, uses channel from cursor position |
| `v` | Toggle sort: latest-desc ↔ channel/thread + time desc |
| `f` | Open filter (substring match on channel, then channel/thread) |
| `l` / `L` | Toggle layout: compact ↔ full (switching to full hides the preview pane) |
| `p` | Toggle preview panel (turning it on also switches to compact) |
| `q` | Quit |
| `Ctrl+C` | Quit |
| `Esc` | No-op (reserved for future filter clear) |

### Compact layout (default)

One line per channel/thread group, same columns in both layouts:

```
TIME(12) | CHANNEL(12) | THREAD(16) | NAME(12) | CONTENT(flex truncated)
```

- Cursor indicator: `▸` prefix on selected row
- Name color: Blue (human), Purple (agent), Gray (system)
- Content: single-line truncated with ellipsis
- Reply count appended to the content as a dim `(N+)` marker

### Full layout

Each group renders as a block: one head line for the original post, then one line per reply.

```
> 4 Sep 23 11:00  eng  preview-trunc...  dave   preview pane truncation looks wrong at 80 columns
  1 Sep 23 10:00  eng  inbox-layouts     alice  let's refactor the inbox so each thread shows its OP
                                              > ack — I will keep the reply marker dropped in full
                                              > the (3+) annotation is no longer needed
```

- Head line: the OP (lowest `seq`) with its own ID/TIME/NAME and content, single-line truncated
- Replies: one line each, in `seq` order, content prepended with `> `, starting at the CONTENT column, each truncated with `...`
- Left columns are blanked on reply lines so replies read as a block hanging off the OP
- No `(N+)` marker — replies are spelled out
- Loading state: `(loading replies…)` under the head line until that thread's messages arrive
- Narrow panes drop columns (NAME, then CHANNEL, then THREAD, then TIME) until the content column has room; replies stay aligned to the CONTENT column at every width

**Navigation in full layout:** `↑↓/j/k` still step group-to-group, and the entire block for the selected group is highlighted. Scrolling is line-based internally; `clampCursor` keeps the whole selected block on screen. `r`, `Enter`, `n`, `v`, and `f` behave exactly as in compact.

**Data source:** full layout fetches `GET /v1/threads/:id/messages` for the groups intersecting the viewport (`GET /v1/inbox` alone may not contain a thread's original post). Results are cached per thread and invalidated on inbox refetch.

## Preview Panel

Toggled by `p`. Shows a right-side panel with preview content for the cursor-highlighted group.

| Condition | Behavior |
|-----------|----------|
| Terminal < 80 cols | Preview forced off, `p` has no effect |
| Terminal 80-99 cols | Preview available, off by default, `p` toggles |
| Terminal ≥ 100 cols | Preview auto-enabled on startup, `p` toggles |
| Full inbox layout | Panel suppressed (rows take the full width); restored in compact |

**Preview content:**
- Title: `#channel › thread · last <time> · N replies`
- Full original post, word-wrapped with no truncation (via `wrapText`)
- Replies fill remaining pane height; newest-tail truncation when space runs out
- No truncation marker on root post — always fully visible

**Not shown in full layout.** The preview panel is suppressed while the full layout is active, the same way it is suppressed in the detail view — the rows already carry the original post and every reply, and a split pane leaves too little room for the content column.

**`p` implies compact.** Turning the preview **on** always switches the layout to compact, so `p` is never a silent no-op: enabling the preview guarantees you can see it. Turning the preview **off** leaves the layout alone, so `p` twice in a row is a no-op and `l` still gets you back to full.

## Compose Modal

Centered overlay. Activated by `r`, `c`, or `n`.

| Key | Action |
|-----|--------|
| `Enter` | Send message |
| `Esc` | Cancel and close |
| `Ctrl+C` | Quit TUI entirely |
| `Backspace` | Delete character before cursor |
| `Delete` | Delete character at cursor |
| `Left` / `Right` | Move cursor left/right |
| Any printable char | Insert at cursor position |

**Compose modes:**

| Trigger | Header | Send creates |
|---------|--------|-------------|
| `r`/`c`/`Enter` | `#CHANNEL › THREAD — reply` | Message (threaded, `parentID` set) |
| `n` | `New thread in #CHANNEL` | New thread |

**Validation:**
- Empty text → error "cannot be empty" (red text in modal)
- Thread creation requires a selected channel (from inbox cursor)

## Status Bar

Always visible at the bottom. Shows:

- **Normal**: `inbox — N messages · ↑↓/j/k nav · r reply · v sort · f filter · q quit`
- **Layout toggle**: `layout: full — l to switch`
- **Preview toggle**: `preview on — p to hide` (or `preview on — p to hide · layout:compact` when `p` also switched layout)
- **Error**: `error: DAEMON_DOWN: ...` (unreachable daemon), `error: DAEMON_ERROR: ...` (undecodable response), or `error: DELIVERY_UNKNOWN: ...` (a write that may have committed)
- **Empty state**: `no messages — press n for new thread`
- **Action feedback**: `sent`, `thread "name" created`
