# TUI Architecture

Bubble Tea v1.3.10 terminal UI for Fluffle. One model, one list, one thread renderer, one compose mode, eight bound actions and `ctrl+c`. A list of rows, a thread pane beside the list or in its place, and a centered reply box over the top.

For the keys see [tui-keybindings.md](tui-keybindings.md). For how to extend it see [tui-extending.md](tui-extending.md).

## Overview

A terminal at least `MinSplitWidth` columns wide shows two panes. The list is a fixed `ListW` cells and the thread takes the rest:

```
flf · message · 2 rows · newest first
▸ Sep 23 15:04  eng           pr-review     ci.bot          3build passed eng › pr-review
  Sep 23 09:12  eng           hello         bob              ship it      ────────────────────────────────────
                                                                            09:12 bob
                                                                                  ship it
                                                                            15:04 ci.bot
                                                                                  build passed
↑↓ nav · g group · v sort · Enter read · Esc back · r reply · q quit
 sent
```

`Enter` gives the thread the whole terminal and drops the list:

```
flf · message · 3 rows · newest first
eng › pr-review
────────────────────────────────────────────────────────────────────────────────
  09:12 bob
        ship it
  11:00 alice
        looks great!
  15:04 ci.bot
        build passed
↑↓ nav · g group · v sort · Enter read · Esc back · r reply · q quit
 sent
```

`Esc` puts the list back with the cursor where it was. Below `MinWidth` columns or `MinHeight` rows there is no layout to draw, so the whole screen is one line:

```
flf needs 71 columns and 24 rows (got 60x24) — resize the terminal
```

and every key but `ctrl+c` is inert — including `q`, because a terminal too small for the list is a terminal the user has to leave by the key that always works.

## Package structure

```
internal/tui/
├── tui.go        Run(): daemon ensure, tea.Program, exit codes
├── model.go      model, Init, Update, handleKey, View, geometry, the clock
├── rows.go       the width constants, rowLine, renderRows, pad
├── thread.go     renderThread — the one thread renderer
├── compose.go    the one compose mode
├── api.go        ListRows, ListMessages, SendReply, get, readAPIError
├── styles.go     lipgloss styles and the one colour rule
├── screen/       vendored from go.kenn.io/kit/tui/screen
└── termtext/     vendored from go.kenn.io/kit/tui/termtext
```

| File | Responsibility |
|------|----------------|
| `tui.go` | `Run()` entry point, daemon ensure, `tea.Program` with the alt screen, exit codes (0 success, 1 client/local, 2 daemon or transport) |
| `model.go` | The model, `Update`, `handleKey`, `View`, `bodyView`, the chrome lines, the split/stacked decision, the tick, and the fetch commands |
| `rows.go` | Every width constant, the one row renderer, the empty-state placeholder, `formatTime`/`formatClock`, and `pad` |
| `thread.go` | `renderThread` at any width, the per-message block, and `padLines` |
| `compose.go` | The reply box: open, close, rune-indexed editing, the caret, the error line |
| `api.go` | The three endpoints the TUI uses, plus `get`, `readAPIError`, and the strict decoder |
| `styles.go` | Colors, styles, and `nameStyle`, which is the only colour rule in the app |

Dependencies are `internal/store` for the wire types, `internal/client` for daemon discovery and the shared HTTP client, and the Bubble Tea ecosystem. `internal/tui/screen` and `internal/tui/termtext` add no download: both import only `github.com/charmbracelet/x/ansi`, which lipgloss already pulls in.

## State

| Field | Purpose |
|-------|---------|
| `width, height` | Terminal size, from `tea.WindowSizeMsg` |
| `quitting` | Set by `q` and `ctrl+c`; `tea.Quit` is what ends the program, so this records an intentional exit rather than gating a render |
| `granularity` | `message`, `thread`, or `channel` — what one row is |
| `reversed` | `true` renders oldest first; the server always answers newest first |
| `detail` | The thread replaces the list instead of sitting beside it |
| `rows` | The feed at the current granularity, already reversed if `reversed` |
| `cursor` | Index into `rows`, carried across a refresh by `Row.ID` |
| `scroll` | First visible row; `clamp` moves it only as far as the cursor needs |
| `threadID` | The thread whose messages are loaded, or 0 for a channel row and for no row |
| `thread` | Those messages |
| `status` | The status band: errors, `sent`, and the two refusals |
| `api` | The daemon client |
| `compose` | The reply box, or inactive |

Fourteen fields. The whole of the mode state is `granularity`, `reversed`, and `detail`, and each is read by exactly one decision: the fetch, `applyRows`, and `bodyView`. There is no view stack, no layout field, no poll state, and no per-key mutual exclusion to keep true.

Two invariants live in the fields rather than in a method:

- **`threadID` is the pane's identity.** A `threadFetchedMsg` is applied only when its `threadID` equals `m.threadID`, and `syncThread` sets it before fetching, so a response for a thread the cursor has left is dropped and one for the thread on screen is applied. A failed load clears it to 0, which both stops the previous thread's messages from sitting under the new thread's title and lets the next `syncThread` retry instead of treating the failure as loaded.
- **`compose.active` owns the keyboard.** `Update` routes a `tea.KeyMsg` to the compose box while it is active and to `handleKey` otherwise, so the two keymaps cannot overlap and no key has to be re-checked inside either.

## Granularity: one row, three things

`g` cycles `message` → `thread` → `channel`. What one row *is* is the daemon's answer, not a client mode, so the client holds no representative-selection rule and no reply-count arithmetic:

| Granularity | One row is | `Thread` is | `Name` is | `Content` is | `Count` is | `ThreadID` |
|---|---|---|---|---|---|---|
| `message` | a message | that message's thread | that message's author | that message | 1 | set |
| `thread` | a thread | its own title | the newest reply's author | the **original** post | replies | set |
| `channel` | a channel | `""` | the newest message's author | that newest message | messages | 0 |

`Content` is the whole representative message and the row renderer takes its first line, because choosing a line is a rendering decision and doing it in SQL would put presentation in the query layer. `Count` is blank when it is 1, so message rows do not carry a column of `1`s and `rowLine` never has to know the granularity.

`Row.ID` is the message, thread, or channel id by granularity, which is what makes cursor carry-by-id a two-line operation. A channel row has no thread, and that is the only place a row and a thread disagree: `syncThread` loads nothing, the pane says `no thread — press g`, and `Enter` or `r` on it says `no thread on this row — press g`.

`GET /v1/rows?g=<granularity>&limit=<n>` is the feed. The flat inbox route it replaced is gone, along with the message-joined row type it returned. The limit defaults to 200 and the store caps it at 500; the TUI asks for 200 on every fetch, so the list is the newest 200 rows and nothing is paged. An unknown or missing `g` is `400 BAD_ARGS` in the standard envelope. `flf inbox --limit N --json` asks for `g=message` and prints `[]Row`; it no longer carries a per-thread `seq`, because a row is no longer a message record. `flf message send --reply-to-seq` is unaffected — `seq` still comes from `/v1/threads/:id/messages`.

## Geometry

```
ColW        = 12   // time, channel, thread, name
CountW      = 3
ColGap      = 2
CursorW     = 2
rowPrefixW  = CursorW + ColW + ColGap + ColW + ColGap + ColW + ColGap + ColW + ColGap + CountW  // 61
MinContentW = 10
MinWidth    = rowPrefixW + MinContentW   // 71
MinHeight   = 24
chromeH     = 3   // title, hint, status
MinSplitWidth = 110
ListW         = 74
```

`rowLine` emits the cursor, four `ColW` cells separated by `ColGap`, and a `CountW` count, then as much content as the remaining width holds, then padding. For an ASCII row it is exactly `w` cells wide, which is what lets a row sit beside a thread without either one shifting the other.

The content column is bounded in cells, by `termtext.Truncate`. The four fixed columns are bounded in cells too and then padded to `ColW` **runes** by `cell`'s `%-*s`, so a column holding a wide grapheme renders wider than `ColW` cells and the row overruns `w` — measured: 79 cells at `w = 71`. Nothing the store writes can do that, because both name rules are ASCII and a parsed timestamp formats as ASCII, so the invariant rests on `internal/names` rather than on the renderer. A hand-edited database can break it, and `pad` will not notice, because `pad` only right-fills.

**There is one column width, and there is no drop ladder.** Every text column is `MaxSlug` or `MaxName` wide, and both are 12, so `ColW` is a single constant and `rowPrefixW` is arithmetic on it. Before, the widths were per-column (time 12, channel 12, thread 16, name 12) and narrow terminals shed columns one at a time — `NAME` → `CHANNEL` → `THREAD` → `TIME` — until the content column had room, which made the *meaning* of a row depend on the terminal's width and needed a rule for what each column looks like at every step of the ladder. Now a row is either drawn whole or the terminal is too narrow to draw it at all, which is `tooNarrow()` and a single line of text.

`MinWidth` is derived from `rowPrefixW` rather than stated beside it, so the floor cannot drift from the columns: 61 cells of prefix plus 10 cells of content is 71, and `MinContentW` is the smallest content column that can carry a word. `MinHeight` is 24, which leaves 21 rows of body under the chrome — enough for a thread's title and rule and nine one-line messages.

The count is right-aligned in its three cells against the content, which is why it is drawn `3build passed` with no gap and blank at 1. The columns are fixed, so the content starts at the same cell on every row; a gap would have to come out of the content width.

`split()` is `width >= MinSplitWidth`, and nothing else. At or above 110 the thread takes `width - ListW` cells beside a list of exactly 74; below it the list takes the whole width and `Enter` shows the thread in its place. `paneWidth()` is the only place that arithmetic lives, so the pane beside the list and the pane that fills the terminal cannot drift apart, and `Enter` and `Esc` mean the same thing in both geometries.

## Rendering

```
View()
  ├─ tooNarrow?   ──yes──▶ one line naming MinWidth x MinHeight and the actual size
  ├─ compose active? ──yes──▶ screen.OverlayCentered(bodyView(), compose.view(), w, h)
  └─ bodyView()
       ├─ detail  ──▶ renderThread(paneWidth(), h, threadTitle(), thread)   // no list
       ├─ split   ──▶ sideBySide(renderRows(ListW, …), renderThread(…))    // list left, thread right
       └─ stacked ──▶ renderRows(width, h, …)                              // list only
```

`bodyView` is `titleLine`, the body, `hintLine`, and the status line: `chromeH` rows of chrome around exactly the rest, and both renderers pad to exactly `h` rows so the three bands keep their places.

`sideBySide` joins the two blocks row by row, one list row to one thread row. Joining them as blocks would stack them, which costs a row and is not a split.

| Line | Content |
|------|---------|
| `titleLine` | `flf · <granularity> · <n> rows · newest first`, or `oldest first` under `v` |
| `hintLine` | `↑↓ nav · g group · v sort · Enter read · Esc back · r reply · q quit` — 68 cells, and it has to fit the 71-column floor, which is what bounds how much of the keymap can be said out loud. `q` is on it; `ctrl+c` is left to muscle memory. There is no `?` overlay and no context-sensitive text. |
| status | `error: <CODE>: <message>`, `sent`, `no row selected`, `no thread on this row — press g`, or an appended `cannot reply — the thread is not loaded` |

`appendStatus` joins rather than overwrites: a later note explains an action, and replacing the reason with it loses why the action was refused.

### One thread renderer, two placements

`renderThread(w, h, title, msgs)` is called from both the split body and the detail body. It draws a title line, a rule, and then one uniform block per message — `  TIME  NAME` with the content wrapped under it, one cell past the clock. The original post is not special-cased, so there is one block shape and nothing to keep in sync between the first message and the rest.

The body column is exactly what is left after the indent, with no floor. A floor wider than that space wraps at one width and truncates at a narrower one, which silently drops words: with an 8-cell floor, `a b c d e f g h` wrapped at 8 and was truncated at 4, and rendered as `a b` / `e f` with `c d` and `g h` gone and nothing to show for it. Below the indent the body is blank, which is the one width at which content cannot be shown at all.

A thread longer than the pane is cut from the top, so the newest messages are the ones on screen. An empty pane says `no thread — press g`, truncated to the pane before it is padded, because `pad` only right-fills and a 19-cell placeholder would overrun a narrower pane and break the invariant that no line `renderThread` emits exceeds the width it was given.

### Compose

`r` opens the reply box and shows the thread, so you can see what you are answering. The box is 80% of the terminal (floored at 60 cells below 30 columns), centered by `screen.OverlayCentered`, and titled `reply in <channel> › <thread>`. `Enter` sends, `Esc` closes, and whitespace-only text is `cannot be empty` in the modal.

Editing is rune-indexed. The cursor is an index into `[]rune(text)` and never a byte offset, and every edit goes through `runes()`/`setRunes`, so there is one definition of a position and a multi-byte character cannot be cut in half. The caret is drawn between the two halves at that index, clamped, so the box shows where the next keystroke lands. The box's width comes from the `tea.WindowSizeMsg` handler, which is also the only place the terminal's size reaches it.

A send appends to the thread and never sets `parent_id`: a reply is a message in the thread, not a nested answer. Threaded replies stay reachable from `flf message send --reply-to-seq` and from import. When the thread is not loaded — which is what a failed thread load leaves behind — the send is refused, the typed text is dropped either way, and the status says so.

## The clock

One unconditional 2-second tick. On each tick the TUI re-reads its rows and the selected thread's messages, so an agent's reply appears in the thread without a keypress. That is the property the pane that showed a run was buying, for no state at all.

**The clock is armed by the rows response and by nothing else.** `Update` batches `syncThread()` and `tick()` on `rowsFetchedMsg`; `refresh()` arms no tick of its own. A refresh that armed one would double the count every round — a tick from the refresh and a tick from that refresh's own response — so two become four, then eight, and the TUI ends up hammering the daemon instead of reading it. The invariant is **one tick per outstanding refetch**: a key that triggers its own refetch briefly holds a second, and a steady state holds one.

The tick re-reads the thread *unconditionally*, through `refetchThread`, not through `syncThread`. `syncThread` returns nil when the selected thread is already loaded, which is right for `j` and `k` — you do not refetch a thread that is on screen every time you move within it — and wrong for the clock, because an agent's reply is a message appended to the thread already on screen and nothing about the selection has changed for `syncThread` to notice. The two are deliberately separate calls.

New messages **prepend** — the feed is newest-first — so the row under the cursor is still in the list, but it has moved down by however many arrived. `applyRows` records the previous `Row.ID`, finds it in the new rows, and falls back to 0 when it is gone; `clamp` then moves the window only as far as the carried cursor needs it to, so an arrival at the top pushes the row you are reading down rather than off the screen. `v` reverses the slice and reuses `applyRows`, which is why the cursor survives a sort and does not have to: the same rows are the same rows in the other order. `g` resets the cursor, the scroll, the loaded thread, and the rows themselves, because a message row has no counterpart at channel granularity and matching by id would either keep the wrong row or land on an arbitrary one — the id namespaces differ per granularity, so a channel id can equal a message id and the match succeeds on an unrelated row.

### Stale responses

Two rules, and they are the whole story:

- A `rowsFetchedMsg` whose `granularity` is not `m.granularity` is dropped.
- A `threadFetchedMsg` whose `threadID` is not `m.threadID` is dropped — **including its error**, because a late failure for a thread the cursor has left must not blank the pane the user is now looking at.

Nothing needs a generation counter, because there is nothing to interleave: the only two requests in flight are the rows and the thread, and each is identified by the field that selects it.

## Untrusted text

Message content, author names, channel and thread names, and the daemon's error text all reach the terminal. None of them goes to a renderer directly:

- `termtext.SanitizeLine` for a single line, `termtext.SanitizeBlock` for a message body. Both remove every terminal escape sequence — not just the SGR runs a regexp for — plus Unicode control and format characters, and replace invalid UTF-8. `SanitizeLine` also flattens tabs and newlines to one space.
- `termtext.Truncate` then bounds the cell width, and `pad` right-fills to the width the line was promised. A styled string's length in bytes is not its width in cells, so `termtext.DisplayWidth` is how anything here is measured.

Sanitize first, then measure: the width of a column must be decided from the text that will be drawn in it, not from the text that arrived. `formatTime` is the exception and it is deliberate — an unparseable timestamp is drawn as-is through `cell`, so a hand-edited database cannot silently shift a row.

`screen` and `termtext` are copied verbatim, without upstream tests, from `go.kenn.io/kit/tui/` (Apache-2.0, Copyright 2026 Kenn Software LLC) into `internal/tui/`, each directory carrying the `NOTICE` that says so and pins the commit. They are a fork rather than a module requirement for two reasons: `kit`'s `go.mod` declares `go 1.27.0` and this module is on 1.26, and the module graph behind `kit` is far larger than these two packages. Keeping the copies verbatim means they can be diffed against upstream. Each has one smoke test, so the fork is exercised rather than assumed.

`splitlayout` was not vendored: it needs `charm.land/lipgloss/v2` alongside the v1 lipgloss already in use, and its entire policy is one width comparison this app can write itself — `split()`.

## Names

Two rules, one package, `internal/names`, and no schema change. The database keeps its `length(trim(x)) > 0` CHECKs; the character rules are Go's job at every write path, so there is exactly one definition to keep true and no data-repair path.

| Kind | Rule | Bound | Examples |
|---|---|---|---|
| slug (`channels.name`, `threads.title`) | `^[A-Za-z][A-Za-z0-9-]*$` | 12 bytes | `eng`, `pr-review`, `pr-review-2` |
| name (an author) | `^[A-Za-z](?:[A-Za-z.]*[A-Za-z])?$` | 12 bytes | `alice`, `bob.smith`, `ci.bot` |

Both bounds are 12 because the row is: a name that does not fit `ColW` is truncated, and a truncated author is a lie about who wrote something. `threads.title` keeps its column, its field name, and its `--title` flag; it is a slug and not prose, so the original post is the subject and the title is only a label.

A name may not end with a dot, even though mention parsing trims trailing dots off a token. Trimming is the tokenizer being tolerant of sentence punctuation; validation is strict, because an agent named `ci.bot.` could never be mentioned — the trim would strip the dot and look up `ci.bot` — and would silently never trigger. Digits are legal in a slug and illegal in a name, and that asymmetry is deliberate: slugs need collision suffixes, names need to stay typeable. See [agent-sessions.md](agent-sessions.md#mention-grammar) for what the name rule does to a mention.

## What the TUI cannot do

Stated plainly, because each of these was a key once:

- **Create channels or threads.** `flf channel create`, `flf thread new`.
- **React.** `flf react add`.
- **Filter or search.** There is no filter model and no sort-field cycling; `v` reverses one order.
- **Show an agent run.** No run pane, no transcript, no `s`. The TUI shows the agent's *reply*, as an ordinary agent-authored message in the thread, and a run's transcript is read with `flf agent session --id N`. The `s` key's job — an agent's reply appearing without a keypress — is done by the clock instead, and the pane itself went: a session belongs to a trigger message, and a row is now a group, so the mapping the pane relied on no longer exists.

Creating anything from the TUI would also mean an agent-reachable create path through a screen, which the agent contract forbids by another route.

## Errors

| Error | Behavior |
|-------|----------|
| Daemon down at startup | `Run()` returns `DAEMON_DOWN`; the CLI exits 2 |
| A read cannot reach the daemon | status `error: DAEMON_DOWN: …` |
| A response cannot be decoded | status `error: DAEMON_ERROR: …`; prior data is retained |
| A dispatched write times out, loses its response, or returns no sequence | status `error: DELIVERY_UNKNOWN: …`; read the thread before retrying |
| A daemon error response | status `error: <CODE>: <message>` from the envelope |
| Bubble Tea runtime failure | `Run()` returns `TUI_ERROR`; the CLI exits 1 |

Every one lands in the status band, which is sanitized and truncated like everything else — `readAPIError` formats the envelope's message unquoted, so an escape sequence in it would otherwise reach the terminal. Nothing panics. An empty compose is the one error that appears in the modal rather than the status band, because the send is still pending.

## Styling

Tokyo Night values, all in `styles.go`:

| Element | Background | Foreground |
|---------|-----------|------------|
| Selected row | `#313244` | `#f5f5f5` |
| Title | — | `#89b4fa` bold |
| Thread rule, thread author | — | `#585062` |
| Thread body | — | `#cdd6f4` |
| Hint, placeholder | — | `#646478` dim |
| Human author | — | `#89b4fa` |
| Agent author | — | `#b7b4fa` |
| System or unknown author | — | `#646478` |
| Status band | `#181825` | `#89b4fa` |
| Modal border | — | `#89b4fa` |
| Modal text | — | `#f5f5f5` |
| Modal error | — | `#f38ba8` |

`nameStyle` is the only colour rule: an author is coloured by `author_type`, and everything else is chrome.

## Data flow

```
key ──▶ Update(tea.KeyMsg)
          ├─ compose active ──▶ composeModel.update ──▶ composeSendMsg ──▶ SendReply ──▶ sentMsg
          └─ otherwise     ──▶ handleKey
                                q, ctrl+c ──▶ tea.Quit
                                ↑ ↓ j k   ──▶ cursor, clamp, syncThread
                                v         ──▶ applyRows (reversed)
                                g         ──▶ nextGranularity, fetchRows
                                Enter     ──▶ detail, syncThread
                                Esc       ──▶ detail = false
                                r         ──▶ detail, compose.open, syncThread

tickMsg ──▶ refresh ──▶ fetchRows + refetchThread
rowsFetchedMsg ──▶ applyRows, syncThread, tick          // the only arming site
threadFetchedMsg ──▶ applied iff threadID == m.threadID
sentMsg ──▶ status, fetchRows + fetchThread
```

## Testing

| Package | Covers |
|---|---|
| `tui` | `rowLine` at 71, 80, and 200 columns; a 40-byte channel and a 40-byte author truncated into a 12-cell column without breaking the row width; only the content's first line is drawn; sanitizing the name, the content, the thread header, the author, and the status; `renderThread` at every width, the empty state, the tail cut, the narrow pane, an overlong author; `g` cycles and resets; `v` reverses and keeps the cursor; `Enter`/`Esc` in both geometries; the view is never wider than the terminal and is exactly `MinHeight` rows; `r` on a channel row; a refused send keeps the reason it was refused; the hint names every bound key and fits the floor; `l p s n C e f c` are not bound; a stale rows and a stale thread response are both dropped; a failed thread fetch clears the pane and retries |
| `tui` (clock) | A tick arms its successor; four rounds produce four ticks, not more; a tick keeps the cursor on the same row; a tick shows an appended reply with no keypress; a refresh re-reads the thread already on screen; a late thread error does not blank the thread on screen |
| `tui` (api) | `ListRows` decodes the feed, defaults the limit, and rejects a `null` body; `ListMessages`; the error envelope surfaces; an envelope-free 4xx is `DAEMON_ERROR`; `SendReply` posts the text with no parent, and a missing sequence is `DELIVERY_UNKNOWN`; a pre-dispatch connection failure is `DAEMON_DOWN`; a dispatched timeout is `DELIVERY_UNKNOWN`; a context cancellation returns promptly; the strict decoder rejects a second value, a `null`, and trailing bytes |
| `screen`, `termtext` | One smoke test each, so the fork is exercised rather than assumed |
| `store` | `ListRows` per granularity: counts, newest-first order, original-post content, a channel row carrying no thread, and an unknown granularity |
| `apiserver` | `/v1/rows` per `g`, `400` on a bad `g`, `500` on a store failure, and an empty result as `[]` rather than `null` |

## Design decisions

1. **The daemon decides what a row is.** Granularity is a query parameter on `GET /v1/rows`, not a client mode. Grouping, representative selection, and reply counts are the query's job, so the client has no rule about which message represents a group and no arithmetic to get wrong.
2. **One column width and no drop ladder.** Every text column is 12 because that is the name bound, so `rowPrefixW` is arithmetic and `MinWidth` derives from it. A row is drawn whole or not at all.
3. **`split` is a width comparison and nothing else.** Below it the list takes the whole width and `Enter` gives the thread the terminal; above it the thread sits beside the list. One rule, so `Enter` and `Esc` mean the same thing in both geometries and no key is a no-op at some widths.
4. **One `renderThread` in two placements.** The pane and the detail view are the same function at two widths, so the original post needs no special case and the two cannot drift.
5. **One unconditional tick, armed by the rows response.** An agent's reply appears without a keypress, which is what the pane that showed a run was buying, for two fields and no release rules. The old poll covered a live run's lifetime rather than the thread's, and a session started in the thread you were already looking at was not discovered until you moved away and back.
6. **One tick per outstanding refetch, and the rows response is the only arming site.** A tick armed anywhere else doubles the count every round.
7. **A response is stale when the field that selects it no longer matches.** `granularity` for rows, `threadID` for the thread, errors included. There are only two requests in flight, so there is nothing to interleave and no generation counter.
8. **The clock refetches the thread; the cursor does not.** `refetchThread` and `syncThread` are different calls because the reasons are different, and sharing one would have kept the pane frozen exactly when it mattered.
9. **The cursor is carried by `Row.ID`, and `g` drops the rows entirely.** Messages prepend, so a refreshed feed still holds the row the cursor was on; a different granularity is a different list, where a message row has no counterpart and the id namespaces overlap, so `g` clears `m.rows` rather than leaving an id for the next response to match.
10. **Reply is the only compose mode.** The TUI creates nothing, so there is one box, and it appends to the thread without setting `parent_id`.
11. **A channel row has no thread, and says so.** `r` and `Enter` refuse with `no thread on this row — press g` rather than opening a pane or a box with nothing behind them. It is the one place a row and a thread disagree, so it is one `if`.
12. **Names are bounded to the column that draws them.** Two rules in `internal/names`, enforced in Go with no schema change, so a rendered author is never truncated into a different person.
13. **The vendored primitives measure in cells.** A styled string's byte length is not its cell width; sanitize, then truncate, then pad.
14. **The status band joins, never overwrites.** A note that explains an action must not erase the reason the action was refused.
15. **One fixed hint line.** A `?` overlay is one more mode to render and one more key to bind; the floor is 71 columns, which bounds what can be said out loud anyway.
16. **No pane for a run, no create flow, no reactions, no filter.** A row is a group, so the trigger-message mapping the pane relied on is gone; creating from a screen would put an agent-reachable create path on a route that is human-only by design; and a filter needs a filter model and a sort order, which `g` and `v` between them do not want to have.

## Deferred

- Date grouping and relative timestamps.
- Unread indicators.
- Reactions in the list.
- Starting the cursor at the bottom.
- A `Row` that names the run that produced it, which is what a pane for a run would need to be rebuilt on.
