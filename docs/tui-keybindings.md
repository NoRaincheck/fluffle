# TUI Key Bindings Reference

All key bindings for the Fluffle TUI (`flf tui`). Context-sensitive: some keys only work in specific views or states.

## Quick Reference

| Key | Channels | Threads | Messages | Compose |
|-----|----------|---------|----------|---------|
| `↑` / `k` | Move cursor up | Move cursor up | Move cursor up | Move cursor left |
| `↓` / `j` | Move cursor down | Move cursor down | Move cursor down | Move cursor right |
| `Enter` | Open channel | Open thread | Post message | Send / Create thread |
| `Esc` | Quit (no, use `q`) | Back to Channels | Back to Threads | Cancel |
| `n` | New thread in selected | New thread in selected | — | — |
| `c` | — | — | Post message | — |
| `L` / `l` | Toggle preview | Toggle preview | Toggle preview | — |
| `q` | Quit | Quit | Quit | — |
| `Ctrl+C` | Quit | Quit | Quit | Quit |
| `Backspace` | — | — | — | Delete char before cursor |
| `Delete` | — | — | — | Delete char at cursor |
| `Left` | — | — | — | Move cursor left |
| `Right` | — | — | — | Move cursor right |

## View-Specific Details

### Channels View

First screen on startup. Shows all channels (repo-anchored and orphaned).

| Key | Action |
|-----|--------|
| `↑` / `k` | Move cursor up one channel |
| `↓` / `j` | Move cursor down one channel |
| `Enter` | Open selected channel → Threads view |
| `n` | Open compose modal: "New thread in #CHANNEL" |
| `L` / `l` | Toggle preview panel (shows threads of highlighted channel) |
| `q` | Quit |
| `Ctrl+C` | Quit |

**Channel line format:**
- Orphaned: `▸ channel-name  (orphaned)  · created`
- Repo-anchored: `▸ channel-name  [branch]  repo-name  · created`

### Threads View

Entered by pressing `Enter` on a channel. Shows threads in the selected channel.

| Key | Action |
|-----|--------|
| `↑` / `k` | Move cursor up one thread |
| `↓` / `j` | Move cursor down one thread |
| `Enter` | Open selected thread → Messages view |
| `n` | Open compose modal: "New thread in #CHANNEL" |
| `Esc` | Back to Channels view (resets cursor to 0) |
| `L` / `l` | Toggle preview panel (shows messages of highlighted thread) |
| `q` | Quit |
| `Ctrl+C` | Quit |

**Thread line format:**
`▸ # thread-title  · created`

### Messages View

Entered by pressing `Enter` on a thread. Shows messages in the selected thread.

| Key | Action |
|-----|--------|
| `↑` / `k` | Move cursor up one message |
| `↓` / `j` | Move cursor down one message |
| `Enter` | Post message (same as `c`) |
| `c` | Post message (appends to end of thread) |
| `Esc` | Back to Threads view (resets cursor to 0) |
| `L` / `l` | Toggle preview panel (shows full content of highlighted message) |
| `q` | Quit |
| `Ctrl+C` | Quit |

**Message line format:**
`[time] author: content`
- Replies show `↳` prefix: `[time] author ↳ reply-text`
- Time is relative for <24h ("now", "5m", "2h"), absolute date for older ("01/02")

### Compose Modal

Centered overlay. Activated by `n` (new thread) or `c` (post/reply).

| Key | Action |
|-----|--------|
| `Enter` | Send text (creates thread or posts message) |
| `Esc` | Cancel and close |
| `Ctrl+C` | Quit TUI entirely |
| `Backspace` | Delete character before cursor |
| `Delete` | Delete character at cursor |
| `Left` / `Right` | Move cursor left/right |
| Any printable char | Insert at cursor position |

**Compose modes:**

| Trigger | Header | Send creates |
|---------|--------|-------------|
| `n` in Channels/Threads | "New thread in #CHANNEL" | New thread |
| `c` in Messages | "#CHANNEL › THREAD" | Message appended to thread |

**Validation:**
- Empty text → error "cannot be empty" (red text in modal)
- Thread creation requires a selected channel
- Message posting requires an active thread

## Preview Panel

Toggled by `L` or `l`. Shows a right-side panel with preview content for the cursor-highlighted item.

| Condition | Behavior |
|-----------|----------|
| Terminal < 80 cols | Preview forced off, `L` has no effect |
| Terminal 80-99 cols | Preview available, off by default, `L` toggles |
| Terminal ≥ 100 cols | Preview auto-enabled on startup, `L` toggles |

**Preview content by view:**

| Main View | Highlighted Item | Preview Shows |
|-----------|-----------------|---------------|
| Channels | Channel | Threads in that channel |
| Threads | Thread | Messages in that thread |
| Messages | Message | Full content of that message |

Preview data is fetched lazily when cursor moves to a new item.

## Status Bar

Always visible at the bottom. Shows:

- **Normal**: `N channels — ↑↓ nav · Enter open · n new thread · q quit`
- **Error**: `error: DAEMON_DOWN: ...`
- **Empty state**: `no channels — n to create (orphaned) or flf channel create --orphaned --name demo`
- **Action feedback**: `thread "name" created`, `sent`

## Navigation Tips

1. **Fast navigation**: `j`/`k` for vim-style, `↑`/`↓` for arrow keys — both work everywhere.
2. **Deep navigation**: Channels → Enter → Threads → Enter → Messages → `Esc` → `Esc` → back to start.
3. **Quick thread creation**: In Channels view, navigate to a channel with `↑`/`↓`, press `n`, type title, press `Enter`.
4. **Quick message posting**: Open a thread (Enter), press `c`, type message, press `Enter`.
5. **Preview for context**: Press `L` to see the next screen without navigating. Press `L` again to hide.
