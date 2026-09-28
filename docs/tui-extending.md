# Extending the TUI

How to change the Fluffle TUI without putting the state back. See [TUI Architecture](tui-architecture.md) for the model, the geometry, and the invariants; [TUI Key Bindings](tui-keybindings.md) for the keymap.

The shape to protect: one list, one `Row`, one thread renderer, one compose mode, one tick, and a keymap small enough to fit on a hint line. Every section below is written so that adding something does not require a mode field, a mutual-exclusion rule, or a second code path for the same drawing.

## The two render functions

There are exactly two, and they are the whole of the drawing:

| Function | File | Draws |
|----------|------|-------|
| `renderRows(w, h, rows, cursor, scroll)` | `rows.go` | The visible window of rows, padded to exactly `h` rows of `w` cells |
| `renderThread(w, h, title, msgs)` | `thread.go` | A title line, a rule, and one uniform block per message, padded to exactly `h` rows of `w` cells |

Both are pure functions of their arguments. They read no model field, take no callback, and know nothing about the other one, which is what lets the same `renderThread` draw the pane beside the list and the thread that fills the terminal, and what lets `bodyView` choose a geometry without either renderer learning a second mode.

**To change what a row looks like**, edit `rowLine`. It is exactly `w` cells wide for an ASCII row, and that is the invariant: sanitize, truncate, pad, in that order, all in cells. Note that `cell` pads the four fixed columns with `%-*s`, which counts runes, so a wide grapheme in a channel, thread, or author name overruns the column — the ASCII-only name rules are what keep that unreachable, not the renderer. `renderRows` supplies the window and the empty state; do not put per-row state in it.

**To change what the list looks like**, edit `rowLine` for a data row or `rowHeader` for the labels above them — they share the same prefix arithmetic, so a column width change belongs in `ColW` and reaches both. `renderRows` keeps the header outside the scrolling window, so anything added there costs the window a row and `listWindowH` has to follow.

**To change what a thread looks like**, edit `threadLines` or the header in `renderThread`. The uniform-block rule is the one to keep: the original post is not special-cased, so a change to "the first message" is a change to every message.

**To add a third pane**, add a case to `bodyView` and a width rule next to `paneWidth()`, which is the only place the thread's width is computed. If the new pane has its own scroll offset, the model is growing a mode field again — check whether the list's `cursor`/`scroll` can be shared before adding one.

## The one `Row`

```go
type Row struct {
	ID       int64  // message, thread, or channel id by granularity
	ThreadID int64  // 0 at g=channel
	Time     string // RFC3339 of the newest message in the group
	Channel  string // ≤ MaxSlug
	Thread   string // ≤ MaxSlug, "" at g=channel
	Name     string // ≤ MaxName
	Content  string // the whole representative message, not its first line
	Count    int    // messages in the group; 1 at g=message
}
```

A row is a line of a cross-channel feed, and the granularity decides what one of them is — not the client. The renderer never branches on granularity, which is why the count is blank at 1 and the thread column is empty on a channel row without either case existing.

Two rules hold for any change to a row:

- **`Content` is the whole message.** The renderer takes `firstLine`. Choosing a line in SQL would put presentation in the query layer, where there is no width to truncate to.
- **`Count` is the group's size, and `1` means "not grouped".** The renderer leaves a count of 1 blank. If you add a granularity whose rows are not groups, that is still the right encoding: a single message is a group of one.

## Untrusted text

Everything the TUI draws that came from a message, an author, a channel, a thread, or a daemon error message is untrusted. It reaches the terminal through `termtext` and nothing else:

| Need | Call |
|------|------|
| Make one line safe | `termtext.SanitizeLine` — removes every escape sequence plus control and format characters, flattens tabs and newlines to a space |
| Make a block safe | `termtext.SanitizeBlock` — same rules, keeps the line breaks |
| Bound it to a width | `termtext.Truncate(s, width, tail)` |
| Measure it | `termtext.DisplayWidth(s)` |
| Right-fill it | `pad(s, w)` in `rows.go` |

**Sanitize first, then measure.** The width of a column has to be decided from the text that will be drawn in it, not from the text that arrived: a styled string's length in bytes is not its width in cells, and measuring first is how an escape sequence ends up inside a width budget.

`pad` and `termtext.DisplayWidth` are the only two ways a line's width is computed here. `len()` and `utf8.RuneCountInString` are both wrong for a styled string, so a change that reaches for one shifts a row instead of failing a test.

`screen` and `termtext` are vendored copies of `go.kenn.io/kit/tui/`, each with a `NOTICE` pinning the commit. They are copied rather than imported so they can be diffed against upstream, and diffing is the only supported way to change them: a change here that needs behaviour neither package has belongs in this package.

## Adding a granularity

Granularity is a store and daemon question first. The TUI half is four edits:

1. Add the constant to `internal/store`, next to `GranularityMessage`/`Thread`/`Channel`, and a `ListRows` case that maps its query into `Row`s.
2. Add the new value to `nextGranularity` in `model.go` so `g` reaches it, and to the `default` branch that wraps the cycle back to `message`.
3. Nothing in the renderer. The count column, the empty state, and the columns are granularity-blind by design; if a new granularity needs a renderer change, the row type is wrong.
4. A test in `internal/tui` that `g` reaches it, and one in `internal/store` for the query: counts, order, and which message represents the group.

A granularity whose rows have no thread is the `channel` case, and it is already handled: `syncThread` loads nothing, the pane says `no thread — press g`, and `r` and `Enter` refuse. If a new granularity can be reached both with and without a thread, that is a `ThreadID` question and the answer belongs in `Row`, not in a renderer branch.

## Adding a key

`handleKey` in `model.go` is the whole list keymap, one `switch` on `msg.String()`. Add a case, and add the key to `helpItems` in the same commit: the footer is the only place a key is documented, and a key that is not on it is a key the user has to guess at. The item needs a description, not an abbreviation — the footer reflows into as many columns as the width holds, so a longer description costs rows only at the narrow floor and buys nothing anywhere else. `TestHelpNamesEveryBoundKey` and `TestHelpNeverExceedsTheTerminal` are the bounds.

Three things to know before you bind one:

- **Check the too-narrow guard.** `ctrl+c` is handled before it; everything else is inert below `MinWidth` × `MinHeight`, including `q`. A new key inherits that, which is usually what you want.
- **Check the compose guard.** `Update` routes a `tea.KeyMsg` to the compose box while it is active and to `handleKey` otherwise, so a new key is not reachable from inside compose unless you add it there too — and if you do, add it to the compose table in [tui-keybindings.md](tui-keybindings.md) as well.
- **A key that needs data it does not have is a fetch.** Follow the existing shape: a `fetchX` returning a `tea.Cmd`, an `xFetchedMsg` carrying the id that selects the response, and an `Update` case that drops the message when that id no longer matches. That check is the whole stale-response story and it is not optional; a late response that is applied lands the wrong thing on screen.

## Adding an endpoint the TUI calls

1. A store method in `internal/store` and its `sqlc` query, then `just generate`.
2. A handler in `internal/apiserver`, documented in [backend.md](backend.md#api-reference).
3. A wrapper in `internal/tui/api.go` on top of `get[T]`, which normalizes 4xx/5xx to the error envelope, a `null` or undecodable body to `DAEMON_ERROR`, and always yields a non-nil slice. A mutation follows `SendReply`: `mutationTransportError` separates a pre-dispatch `DAEMON_DOWN` from a `DELIVERY_UNKNOWN`, and an acknowledgement without an assigned sequence is a failure.
4. A fetch command, its message type, and its `Update` case, as above.
5. Tests in `internal/tui` for the wrapper and in `internal/apiserver` for the route.

Do not add a second decode path. `decodeStrictJSON` rejecting a second JSON value, a `null`, and trailing bytes is the same check for every response the TUI reads.

## Refreshing

The clock re-reads two things on a 2-second tick, and `refresh()` is the only thing that arms the tick. If you add a third thing to refresh, add it inside `refresh()`; arming from anywhere else forks the chain, and each fork arms its own, so two become four, then eight and the TUI hammers the daemon. The invariant is exactly one tick in flight, whatever the user is doing — which holds only because the tick is armed before the request is sent, so neither a response nor a keypress is on the chain.

`refresh` deliberately calls `refetchThread` rather than `syncThread`. `syncThread` is for the cursor — it returns nil when the selected thread is already loaded, which is right for `j` and `k` and wrong for the clock, because an agent's reply is appended to the thread already on screen. Keep the two calls separate; sharing one freezes the pane at the moment it matters.

## Adding a style

Every color and style is a `var` in `styles.go`, applied where the string is drawn. `nameStyle` is the only rule that branches on data — an author coloured by `author_type` — and adding a second data-dependent colour rule is how the renderer starts needing to know what a row is.

## What not to add back

Each of these is a key or an invariant, and each was removed with a reason recorded in [tui-architecture.md](tui-architecture.md#design-decisions):

| Feature | Why it is not here |
|---------|-------------------|
| Two layouts, or a `l` key | Column widths are one constant and a row is drawn whole; a second layout needs a mode field and a rule for what `l` does when a pane is on screen |
| A preview toggle, or a `p` key | The thread is already beside the list, and `Enter` already fills the terminal with it |
| A pane for an agent run, or an `s` key | A row is a group now, so the trigger-message mapping is gone; the clock already provides what the pane was for |
| Creating a channel or thread, or `n`/`C` | Would put an agent-reachable create path on a route that is human-only by contract |
| Reacting, or `e` | Same, and an emoji picker is a modal of its own |
| A filter, or `f` | Needs a filter model and a sort order, which `g` and `v` between them do not want |
| A `?` help overlay | One fixed line fits the 71-column floor; an overlay is a mode and a key |
| A generation counter on responses | There are only two requests in flight and each carries the id that selects it |

If one of these is genuinely needed, it is a new decision rather than a small addition: the TUI's value is that its state is small enough to hold in one head, and every entry above cost a field, an invariant, or a rule.
