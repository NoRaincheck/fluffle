# TUI Key Bindings Reference

All key bindings for the Fluffle TUI (`flf tui`). Context-sensitive: some keys only work in specific views or states.

## Quick Reference

| Key | Inbox | Compose |
|-----|-------|---------|
| `↑` / `k` | Move cursor up / scroll up | Move cursor left |
| `↓` / `j` | Move cursor down / scroll down | Move cursor right |
| `Enter` | View details: open fullscreen scrollable thread | Send message |
| `r` | Reply: open compose | — |
| `c` | **not bound** — listed from an earlier design, asserts nothing | — |
| `n` | **not bound** — listed from an earlier design, asserts nothing | — |
| `l` / `L` | Toggle inbox layout: compact ↔ full | — |
| `p` | Toggle preview panel | — |
| `s` | Preview the agent session for the selected message (preview pane must be visible) | — |
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
| `r` | Reply: open compose with `threadID` + `parentID` set from cursor position |
| `c` | **not bound** |
| `Enter` | Open detail view: fullscreen scrollable thread |
| `n` | **not bound** |
| `v` | Toggle sort: latest-desc ↔ channel/thread + time desc |
| `f` | Open filter (substring match on channel, then channel/thread) |
| `l` / `L` | Toggle layout: compact ↔ full (switching to full turns the preview off) |
| `p` | Toggle preview panel (turning it on also switches to compact) |
| `s` | Switch the preview pane between the thread and the agent session started by the selected row's newest message |
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

**Navigation in full layout:** `↑↓/j/k` still step group-to-group, and the entire block for the selected group is highlighted. Scrolling is line-based internally; `clampCursor` keeps the whole selected block on screen. `r`, `Enter`, `n`, `v`, and `f` behave exactly as in compact. `s` does nothing here, because the preview pane it drives is never drawn in this layout.

**Data source:** full layout fetches `GET /v1/threads/:id/messages` for the groups intersecting the viewport (`GET /v1/inbox` alone may not contain a thread's original post). Results are cached per thread and invalidated on inbox refetch.

## Preview Panel

Toggled by `p`. Shows a right-side panel with preview content for the cursor-highlighted group.

| Condition | Behavior |
|-----------|----------|
| Terminal < 80 cols | Preview forced off, `p` has no effect |
| Terminal 80-99 cols | Preview available, off by default, `p` toggles |
| Terminal ≥ 100 cols | Preview auto-enabled on startup, `p` toggles |
| Full inbox layout | Never drawn — switching to full turns the preview off |

**Preview content:**
- Title: `#channel › thread · last <time> · N replies`
- Full original post, word-wrapped with no truncation (via `wrapText`)
- Replies fill remaining pane height; newest-tail truncation when space runs out
- No truncation marker on root post — always fully visible
- `s` replaces this content with the agent session started by the selected row's newest message — see [Session Preview](#session-preview-s)

**Not shown in full layout.** The preview panel is suppressed while the full layout is active, the same way it is suppressed in the detail view — the rows already carry the original post and every reply, and a split pane leaves too little room for the content column.

**Layout and preview are mutually exclusive.** Exactly one of these is true at any moment:

| | compact | full |
|---|---|---|
| Preview off | yes | yes |
| Preview on | yes | **never** |

- `p` (preview **on**) switches to compact, so the key is never a silent no-op.
- `l` (switching to **full**) turns the preview off, reporting `layout: full — preview off`.
- `p` (preview **off**) leaves the layout alone, so `p` twice in a row is a no-op.
- Resizing to ≥100 cols auto-enables the preview only in compact; it is never auto-enabled in full.

In other words `preview == true` implies `layout == compact`, always.

## Session Preview (`s`)

`s` switches the right-hand pane between the thread and the agent session started by the selected row's newest message. It is a pane mode, not a separate view, and it is only live in the inbox view.

| Condition | Behavior |
|-----------|----------|
| Preview pane not drawn (`p` off, < 80 cols, full layout, detail view) | `s` does nothing |
| Selected row's newest message has no session | Pane unchanged; status bar reads `no session on this message` |
| Selected row's newest message has a session (first press) | Pane switches to the session and fetches its events |
| Already in session mode (second press) | Pane switches back to the thread and drops the loaded session |

**Pane contents** (top to bottom):

- Header: `SESSION  <agent> · <status> · <reply mode> · #<id>`, with the elapsed duration appended once the run is terminal, `replied #<seq>` when the daemon posted the reply itself, and the failure reason when there is one.
- One line per session event, `<type> <first line of content>`, truncated to the pane width, in `seq` order. Only the first line of each event's content is shown, so a multi-line stdout chunk is elided to its first line rather than reflowed.
- When the run has more events than the pane has rows, the pane shows the **oldest** events and a `… N hidden …` line counts what is below the fold. The tail of a verbose run is not reachable from the pane.
- Placeholders: `no session on this message` when the row has no session at all; `(no events loaded)` when the row's session is not the one whose events are loaded, i.e. after the cursor moved onto a different message with a session; `(running…)` for a live session that has produced no events yet; `(no events)` for a finished session with none.
- Note that `s` installs the session *before* it fetches the events, so a slow or failed `GET /v1/sessions/:id` shows `(running…)` or `(no events)` — never `(no events loaded)`.

**Live updates:** while the pane is visible and at least one session in the thread is `queued` or `running`, the TUI re-fetches the thread's sessions every 500 ms. The poll covers a *live run's lifetime*, not the thread. Once every session in the thread is terminal no further poll is armed, and a session started in that thread afterwards is **not** discovered on its own — it appears when the cursor moves to a different thread and back, which is the only thing that clears the per-thread fetch lock in that state. Moving to another thread does not stop the poll so much as re-aim it: the new thread's response arms it against the new thread. Hiding the pane with `p` clears it only if a poll tick happens to land while the pane is hidden; toggling `p` back on does not, and a failed session fetch does.

While in session mode the pane re-resolves the session from the polled list on every render, so moving the cursor updates it without another keypress. If the cursor lands on a message whose session is not the one whose events are loaded, the pane says `(no events loaded)` until you press `s` on it.

**Which message `s` looks for:** an inbox row is a channel/thread *group*, and its representative is the group's **newest** message. Sessions are indexed by the message that triggered them, and `s` looks up only that representative's id. So the session appears on a row only while the `@mention` that started it is still that group's most recent message — once the agent's reply lands (or anyone posts again), that row resolves to a message with no session and `s` reports `no session on this message`. Earlier messages in the thread keep their sessions in the API; the inbox row just stops pointing at them. `GET /v1/threads/:id/sessions` lists a thread's runs and `flf agent session --id N` reads one — note that `flf thread export` carries messages and reactions only, so it will not include session transcripts.

**Two agents on one message:** a message may start one session per mentioned name, but the pane indexes sessions by the message that triggered them, so with more than one session on the same message it shows the most recently created one; `flf agent session --id N` is how you read the others.

**Not bound:** there is no TUI key to cancel a session. Cancellation is `POST /v1/sessions/:id/cancel` and is human-only.

## Compose Modal

Centered overlay. Activated by `r` (`c` and `n` are not bound).

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
| `r` | `#CHANNEL › THREAD — reply` | Message (threaded, `parentID` set) |

**Validation:**
- Empty text → error "cannot be empty" (red text in modal)
- Thread creation requires a selected channel (from inbox cursor)

## Status Bar

Always visible at the bottom. Shows:

- **Normal**: `inbox — N messages · ↑↓/j/k nav · r reply · v sort · f filter · q quit`
- **Layout toggle**: `layout: full — l to switch`
- **Preview toggle**: `preview on — p to hide` (or `preview on — p to hide · layout:compact` when `p` also switched layout)
- **Layout toggle**: `layout: full — l to switch` (or `layout: full — preview off · l to switch` when `l` also turned the preview off)
- **Error**: `error: DAEMON_DOWN: ...` (unreachable daemon), `error: DAEMON_ERROR: ...` (undecodable response), or `error: DELIVERY_UNKNOWN: ...` (a write that may have committed)
- **Session pane**: `no session on this message` when `s` is pressed on a row with no session
- **Empty state**: `no messages — press n for new thread`
- **Action feedback**: `sent`, `thread "name" created`
