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

Single flat table showing all messages across all channels/threads.

| Key | Action |
|-----|--------|
| `↑` / `k` | Move cursor up one message |
| `↓` / `j` | Move cursor down one message |
| `r` / `c` | Reply: open compose with `threadID` + `parentID` set from cursor position |
| `Enter` | Open detail view: fullscreen scrollable thread |
| `n` | New thread: opens compose, uses channel from `inbox[cursor].ChannelID` |
| `L` / `l` | Toggle preview panel (shows full message + last 5 replies) |
| `q` | Quit |
| `Ctrl+C` | Quit |
| `Esc` | No-op (reserved for future filter clear) |

**Message line format:**
```
TIME(8) | CHANNEL(12) | THREAD(16) | SENDER(12, color-coded) | CONTENT(flex truncated)
```

- Cursor indicator: `▸` prefix on selected row
- Sender color: Blue (human), Purple (agent), Gray (system)
- Content: single-line truncated with ellipsis

## Preview Panel

Toggled by `L` or `l`. Shows a right-side panel with preview content for the cursor-highlighted message.

| Condition | Behavior |
|-----------|----------|
| Terminal < 80 cols | Preview forced off, `L` has no effect |
| Terminal 80-99 cols | Preview available, off by default, `L` toggles |
| Terminal ≥ 100 cols | Preview auto-enabled on startup, `L` toggles |

**Preview content:**
- Title: `Preview: #channel › thread · author`
- Full message content, word-wrapped with no truncation (via `wrapText`)
- Replies fill remaining pane height; newest-tail truncation when space runs out
- No truncation marker on root post — always fully visible

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

- **Normal**: `inbox — N messages · ↑↓ nav · Enter view · r reply · n new thread · L preview`
- **Error**: `error: DAEMON_DOWN: ...`
- **Empty state**: `no messages — press n for new thread`
- **Action feedback**: `sent`, `thread "name" created`
