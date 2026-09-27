# TUI Key Bindings Reference

Every key the TUI (`flf tui`) answers. There are no unbound-but-documented keys, no `?` overlay, and no context-sensitive help: the hint line at the bottom names the navigable ones and nothing else.

## List

| Key | Action |
|-----|--------|
| `↑` / `k` | Move the cursor up one row |
| `↓` / `j` | Move the cursor down one row |
| `g` | Cycle granularity: `message` → `thread` → `channel` → `message` |
| `v` | Reverse the order: newest first ↔ oldest first |
| `Enter` | Read the selected row's thread — in a split terminal it fills the terminal, in a stacked one it replaces the list |
| `Esc` | Back to the list, with the cursor where it was |
| `r` | Reply: open the compose box and show the thread being answered |
| `q` | Quit |
| `ctrl+c` | Quit, on any key the TUI is reading — including a terminal too narrow to draw the list, and excluding the open reply box |

`↑`/`k` and `↓`/`j` move the row cursor, in both geometries. There is no thread scroll: a thread longer than the pane is cut from the top, so the newest messages are the ones on screen and the pane has no keys of its own. In the detail geometry the list is not drawn, so the cursor marker is invisible — but the keys still move the selection, and the pane follows the row you land on.

Every other key does nothing. `l`, `p`, `s`, `n`, `C`, `e`, `f`, and `c` are unbound, and a test asserts that pressing them changes no state and returns no command — rebinding one of those would bring back a view with no other trace.

## Granularity

`g` cycles three different lists. What one row is, and which of the row's fields mean what, is the daemon's answer to `GET /v1/rows?g=<granularity>`:

| Granularity | One row is | `Content` is | `Count` is | `r` and `Enter` |
|---|---|---|---|---|
| `message` | a message | that message | 1, and drawn blank | Reply to its thread; open its thread |
| `thread` | a thread | the original post | replies | Reply to it; open it |
| `channel` | a channel | the newest message | messages | `no thread on this row — press g` |

`g` resets the cursor to the top and drops the loaded thread, because a message row has no counterpart at channel granularity. `v` keeps the cursor: the same rows in the other order, so the row under the cursor is still there. The title line names the granularity, the row count, and the order.

## Geometry

| Rule | Value |
|------|-------|
| Minimum usable terminal | 71 columns × 24 rows |
| Below that | One line naming the minimum and the actual size; every key but `ctrl+c` is inert |
| Split (list beside thread) | 110 columns or wider |
| Stacked (list, or thread on `Enter`) | 71–109 columns |
| List width when split | 74 cells, fixed |
| Row prefix | 61 cells: a 2-cell cursor, four 12-cell columns, four 2-cell gaps, a 3-cell count |
| Hint line | 68 cells, which is what bounds how much of the keymap can be said out loud |

A row is drawn whole or not at all: there is no column dropping, and no per-width key that stops working. `Enter` and `Esc` mean the same thing in both geometries, so neither is a no-op at any width.

## Compose box

Opened by `r`; the thread is shown behind it.

| Key | Action |
|-----|--------|
| Any printable character | Insert at the cursor |
| `Backspace` | Delete the rune before the cursor |
| `Delete` | Delete the rune at the cursor |
| `←` / `→` | Move the cursor one rune |
| `Enter` | Send the reply |
| `Esc` | Cancel and close |
| `ctrl+c` | **Not bound.** `Update` routes every key to the box while it is open and the box has no `ctrl+c` case, so the keystroke is swallowed. `Esc` is the way out of a box you cannot send. |

The box is 80% of the terminal width, titled `reply in <channel> › <thread>`, and draws a caret at the cursor. Positions are rune indices, so a multi-byte character is edited whole. Whitespace-only text is refused in the box with `cannot be empty`. A send appends to the thread and never sets `parent_id`.

`r` with no row under the cursor says `no row selected`; `r` on a channel-granularity row says `no thread on this row — press g`. A send refused because the thread is not loaded says so and keeps the error that explains why.

## Status line

One row above the bottom edge, always present:

| Text | When |
|------|------|
| *(empty)* | Nothing has happened yet |
| `sent` | A reply was acknowledged |
| `no row selected` | `Enter` or `r` with an empty or out-of-range list |
| `no thread on this row — press g` | `Enter` or `r` on a channel-granularity row |
| `cannot reply — the thread is not loaded` | Appended after a send the TUI cannot make; the reason stays on screen |
| `error: <CODE>: <message>` | `DAEMON_DOWN`, `DAEMON_ERROR`, or `DELIVERY_UNKNOWN` |

Notes later in the same band are joined with ` · ` rather than replacing what is there, so the reason an action was refused is still readable after a note explains it. The line is sanitized and truncated to the terminal width.

## Not in the TUI

| Operation | Where it lives |
|-----------|----------------|
| Create a channel | `flf channel create` |
| Create a thread | `flf thread new` |
| React to a message | `flf react add` |
| Filter, search, cycle a sort field | Nowhere — `g` and `v` are the whole of it |
| Read an agent run's transcript | `flf agent session --id N` |
| Cancel an agent run | `POST /v1/sessions/:id/cancel`, human-only; no key |

An agent's *reply* does appear in the thread while you watch, because the TUI re-reads the selected thread every two seconds. The run behind it is not on screen, and there is no key that would show it.
