# TUI Rows Rewrite

A wholesale simplification of the Fluffle TUI and the naming rules underneath it,
shaped after `kenn-io/msgvault`'s TUI and the rendering primitives in
`kenn-io/kit`. The goal is a **net reduction in Go code** and a large reduction
in the number of interacting states a reader has to hold in their head.

## Problem

The TUI is 4153 non-test lines across `model.go` (2787), `api.go`, `compose.go`,
`session.go` and `styles.go`, plus 3637 lines of test. That size is not the
problem; the state count is. Today a reader has to account for:

- **Five view kinds.** `viewInbox`, `viewInboxDetail`, `viewChannels`,
  `viewThreads`, `viewMessages`. Three of them are unreachable: `Init()` fetches
  the inbox and switches to `viewInbox`, so the channels/threads/messages stack
  is never rendered, yet `handleKey` still carries a branch per view for `↑↓`,
  `Enter`, `Esc` and `r`.
- **Three ways to present one view.** Compact layout, full layout, and a
  right-hand preview pane, with the invariant `preview == true implies layout ==
  compact`. Two keys fight over one pane, and the doc for the invariant is
  fifteen lines of rules about which key silently does nothing.
- **A six-field poll state machine.** `sessions`, `sessionsByMsg`, `session`,
  `sessionEvents`, `sessionPollThreadID`, `sessionTickThread`, `sessionTickGen`,
  plus a mutex. `releaseSessionPoll()` has three call sites, none of which can
  fire in the all-terminal state, so a session started in a thread the user is
  already looking at is not discovered until they move away and back.
- **Five inbox widths.** `time 12, channel 12, thread 16, name 12`, content
  fills the rest, and `inboxFullGeometry.dropColumn` sheds NAME → CHANNEL →
  THREAD → TIME until the content column reaches `minContentWidth`.
- **Untrusted text in the terminal.** Message content is stripped by
  `ansiRegexp`, which matches only `\x1b[0-9;]*m`. An OSC, DCS, APC, C1 or plain
  control character in a message body reaches the terminal intact.
- **Three different name rules.** `store` accepts any non-blank string,
  `agentcfg` requires `^[a-z0-9][a-z0-9_-]*$`, and `mentions.Parse` walks bytes
  with a hand-written `isNameByte` scanner.
- **Duplicated status strings.** The same `inbox — N messages · …` line is
  written out six times inside `handleKey`.

## Vocabulary

Everything is an email. A **message** is an email: a body, an author, a
timestamp. A **thread** is an email plus its replies. A **channel** is a folder
that groups threads. Authors are **names**. Nothing else is a first-class thing
the list has to understand.

That gives exactly one thing to render — a row — at three granularities, chosen
by the reader rather than hard-coded:

| Granularity | One row is | `Name` is | `Content` is | `Count` is |
|---|---|---|---|---|
| `message` | a message | its author | its first line | 1 |
| `thread` | a thread | the newest reply's author | the original post's first line | replies |
| `channel` | a channel | the newest reply's author | the newest reply's first line | messages |

The grouping is a **query parameter, not a client mode**. The daemon owns what a
row is, so the client has no representative-selection rule, no group-then-filter
ordering rule, and no reply-count arithmetic.

## Naming rules

One package, one rule per kind, one maximum per kind. These are the only two
name rules in the system.

```go
package names

const MaxSlug = 12 // channels.name, threads.title
const MaxName = 12 // messages.name, reactions.name, agent_sessions.agent_name

// Slug: ^[A-Za-z][A-Za-z0-9-]{0,11}$   must start with a letter
// Name: ^[A-Za-z](?:[A-Za-z.]{0,10}[A-Za-z])?$   must start and end with a letter
```

| Kind | Charset | Max | Examples |
|---|---|---|---|
| slug | `A-Za-z0-9-`, leading `A-Za-z` | 12 | `eng`, `pr-review`, `pr-review-2` |
| name | `A-Za-z.`, leading and trailing `A-Za-z` | 12 | `alice`, `bob.smith`, `ci.bot` |

Consequences, all intended:

- `threads.title` is a **slug**, not prose. `flf thread new --title "Schema
  migration"` becomes `--title schema-migration`. The original post is the
  subject; the title is only a label.
- Agent names lose `-` and `_`. `ci-bot` is now `ci.bot`. The daemon, the CLI
  and `agentcfg` all read the one rule.
- `@mention` tokens are `[A-Za-z][A-Za-z.]*`, trailing dots trimmed, so
  `@alice. what changed?` yields `alice`. A mention that names no configured
  agent stays prose, as it does today.
- A name may not **end** in a dot, even though a mention token's trailing dot is
  trimmed. The two rules are not in conflict: trimming is the tokenizer being
  tolerant of sentence punctuation, while validation is strict, because an agent
  named `ci.bot.` could never be resolved — the trim would strip the dot and look
  up `ci.bot` — so it would silently never trigger. Doubled dots inside a name
  stay legal: `ci..bot` round-trips through the trim unchanged.
- All three list columns are 12 wide, so column width is a single constant.
- Digits are legal in a slug and illegal in a name. That asymmetry is
  deliberate: slugs need collision suffixes, names need to stay pronounceable
  and typeable.

Enforcement is in Go, at every write path, and **no schema change is made**. The
`length(trim(x)) > 0` CHECKs stay as they are; the character rules are Go's job,
in one place, so there is exactly one definition to keep true. There is no
migration and no data-repair path: `~/.fluffle` was deleted before this work
started, and the rules only bind new writes.

`threads.title` keeps its column, field and `--title` flag name. Renaming it to
`slug` would be honest but is pure churn — a migration, regenerated sqlc, and
edits across five packages for no line reduction.

## Data layer

### `store.Row`

```go
type Row struct {
    ID       int64  // message id, thread id, or channel id by granularity
    ThreadID int64  // 0 at g=channel
    Time     string // RFC3339 of the newest message in the group
    Channel  string // ≤ MaxSlug
    Thread   string // ≤ MaxSlug, "" at g=channel
    Name     string // ≤ MaxName
    Content  string // full content of the representative message
    Count    int    // messages in the group; 1 at g=message
}
```

`store.InboxMessage` is deleted. `Content` is the whole representative message,
not its first line: line selection is a rendering decision, and doing it in SQL
would put presentation in the query layer. `Count` is 1 for `g=message` and is
set by the Go mapper rather than by the query.

### `store.ListRows`

```go
const (
    GranularityMessage = "message"
    GranularityThread  = "thread"
    GranularityChannel = "channel"
)

func (s *Store) ListRows(ctx context.Context, granularity string, limit int) ([]Row, error)
```

Three sqlc queries behind a dispatcher, all `ORDER BY <newest> DESC, id DESC` so
newest-first is the server's answer and the client only reverses a slice for `v`.
`limit` defaults to 200 and is clamped to 1..500. An unknown granularity is
`ErrInvalid`.

- `ListMessageRows` — `messages JOIN threads JOIN channels`, one row per message.
- `ListThreadRows` — `threads JOIN channels`, with four correlated subqueries
  for `COUNT(*)`, the newest `created_at`, the newest `name`, and the original
  post's `content` (`ORDER BY seq ASC LIMIT 1`).
- `ListChannelRows` — `channels`, with the same four subqueries scoped through
  `threads`.

### Endpoint

`GET /v1/rows?g=<granularity>&limit=<n>` returns `[]Row` as
`application/json`. `GET /v1/inbox` is removed. An unknown or missing `g` is a
`400` with the standard `{"code","message"}` envelope.

`flf inbox --limit N --json` calls `?g=message` and prints `[]Row`. Its output
shape changes, and it loses the per-thread `seq` column, because a row is no
longer a message record. `flf message send --reply-to-seq` is unaffected: seq
still comes from `/v1/threads/:id/messages`.

## TUI

### Shape

`split := width >= 110 && height >= 24` selects two panes: the list at a fixed
`ListW = 74` and the thread in the remainder. Below either threshold the list
takes the full width. In both geometries `Enter` sets `detail`, which renders the
thread in place of the list, and `Esc` clears it — one rule, not two code paths,
and not the old `viewInboxDetail` with its own cursor, scroll, saved-cursor and
three clamp functions.

Below `rowPrefixW + MinContentW` columns, or below 24 rows, the TUI renders
`needs 71 columns and 24 rows (got NxM)` and accepts only `q` and `ctrl+c`.

### Files

```
internal/tui/
├── tui.go        Run(), exit codes 0/1/2
├── model.go      model, Init, Update, handleKey, View, geometry
├── rows.go       rowLine, renderRows, the width constants
├── thread.go     renderThread — the one thread renderer
├── compose.go    the one compose mode
├── api.go        ListRows, ListMessages, SendMessage, EnsureDaemon
├── styles.go
├── screen/       vendored from go.kenn.io/kit/tui/screen
└── termtext/     vendored from go.kenn.io/kit/tui/termtext
```

### Model

```go
type model struct {
    width, height int
    quitting       bool
    granularity    string // message | thread | channel
    reversed       bool
    detail         bool   // thread replaces the list
    rows           []store.Row
    cursor         int    // index into the visible row window
    scroll         int    // list row offset
    threadID       int64  // thread whose messages are loaded
    thread         []store.Message
    status         string
    api            *apiClient
    compose        composeModel
}
```

Fifteen fields, down from forty-two.

### Messages

| Msg | Carries | From |
|---|---|---|
| `rowsFetchedMsg` | `granularity`, `[]Row`, `err` | `GET /v1/rows` |
| `threadFetchedMsg` | `threadID`, `[]Message`, `err` | `GET /v1/threads/:id/messages` |
| `tickMsg` | — | `tea.Tick`, every 2s |
| `sentMsg` | `err` | `POST` reply |
| `composeSendMsg` | `text` | compose modal Enter |
| `tea.WindowSizeMsg` | — | terminal |
| `tea.KeyMsg` | — | keyboard |

A `rowsFetchedMsg` whose `granularity` is not the current one is dropped, which
is the whole stale-response story. A `threadFetchedMsg` whose `threadID` is not
`m.threadID` is dropped. Nothing else needs a generation counter, because there
is nothing to interleave: the only two requests in flight are the rows and the
thread, each identified by the field that selects it.

### Refresh

One unconditional 2s tick, replacing the six-field session poll. On each tick
the TUI re-reads its rows and the selected thread's messages. The cursor is
preserved by matching the previous `Row.ID`. New messages append, so row and
line indices are stable and no scroll is reset.

The clock is armed by the rows response and by nothing else, so a steady state
holds exactly one tick per outstanding refetch; a key that triggers its own
refetch briefly adds a second. A refresh that armed its own tick would double
the count every round — two become four, then eight — and the TUI would hammer
the daemon instead of reading it. The clock refetches the selected thread unconditionally
rather than only when it is unloaded, because an agent's reply is a message
appended to the thread already on screen.

This is what the session pane was buying — an agent's reply appearing without a
keypress — for two fields and no invariants. Run transcripts stay readable
through `flf agent session --id N`.

### Keymap

| Key | Action |
|---|---|
| `↑` / `k` | move cursor up |
| `↓` / `j` | move cursor down |
| `g` | cycle granularity: message → thread → channel |
| `v` | reverse sort |
| `Enter` | read the thread |
| `Esc` | back to the list; close compose |
| `r` | reply (compose modal) |
| `q` | quit |

`ctrl+c` quits everywhere. The help bar is one fixed line, so there is no `?`
overlay and no context-sensitive hint text.

Removed keys and what they took with them:

| Key | Removed | Also removed |
|---|---|---|
| `l` | compact/full layout | `inboxLayout`, `inboxFullBlocks`, `inboxFullGeometry`, `dropColumn`, `fullThreads`, `fullRowsFetchedMsg`, viewport-intersection batching |
| `p` | preview toggle | `preview`, its 80-col floor, its 100-col auto-enable, the mutual-exclusion invariant, `maybeFetchPreview` |
| `s` | session pane | all of `session.go` (279 lines) and the poll invariants |
| `n`, `C` | create thread/channel | 2 of 4 compose modes, `createThread`, `createChannel`, `handleNewThread`, `handleNewChannel` |
| `e` | react | `handleReact`, `addReaction`, the emoji picker |
| `f` | filter | `filterModel`, `parseInboxFilter`, `inboxFilterChan`, `inboxFilterThread`, the group-then-filter ordering rule |
| `Enter` | fullscreen detail **view** | `viewInboxDetail`, `detailThreadID`, `detailCursor`, `savedInbox*`, `clampDetailScroll`, `threadReplyLineCounts` — replaced by one `detail bool` and one renderer |
| — | dead stack | `viewChannels`, `viewThreads`, `viewMessages`, `hasPrev`, `prevView`, `previewThreads`, `previewMessages` |

### Rendering

```go
const (
    ColW    = 12 // time, channel, thread, name
    CountW  = 3
    ColGap  = 2
    CursorW = 2
    // rowPrefixW = 2+12+2+12+2+12+2+12+2+3 = 61
    MinContentW = 10
)
```

```
▸ Sep 23 15:04  eng         pr-review    ci.bot      3  build passed
  Sep 23 11:00  eng         hello        alice       4  looks great!
```

One `rowLine(w, row, selected)`. Columns are `termtext.Truncate`d to `ColW` so
a hand-edited database cannot shift the row, content is
`termtext.SanitizeLine`d before it is measured, and the name column is coloured
by `author_type` — which is still the only colour rule in the app. The count
column is blank when `Count <= 1`, so message-granularity rows do not carry a
column of `1`s, and the renderer never needs to know the granularity.

`renderThread(w, h, channel, title, msgs)` renders a title line, a rule, and
then one uniform block per message: `  TIME  NAME` followed by the content
word-wrapped at indent 4. The original post is not special-cased, so
`threadSplit` and `buildThreadOPlines` go away.

A channel row has no thread, so at `g=channel` the detail pane has nothing to
show and says `(no thread — press g)`, and `r` answers `no thread — press g`
instead of opening compose. One `if m.threadID == 0` covers both, and it is the
only place a row and a thread disagree.

A reply from the TUI always appends to the thread and never sets `parent_id`,
which is what `handleThreadReply` already does in practice. Threaded replies
remain reachable from `flf message send --reply-to-seq` and from import.

### Vendored rendering primitives

`screen` and `termtext` are copied verbatim, without upstream tests, from
`go.kenn.io/kit/tui/` (Apache-2.0, Copyright 2026 Kenn Software LLC) into
`internal/tui/`, each with a `NOTICE`. They are a fork rather than a module
requirement because `kit`'s `go.mod` declares `go 1.27.0` and this module is on
1.26, and because the module graph behind `kit` is far larger than these two
packages. Keeping the copies verbatim means they can be diffed against
upstream.

They are already in the dependency tree: both import only
`github.com/charmbracelet/x/ansi`, which `lipgloss` pulls in today, so promoting
it to a direct `require` adds no download.

`splitlayout` is not vendored. It needs `charm.land/lipgloss/v2` alongside the
`github.com/charmbracelet/lipgloss` v1 already in use, and its policy is one
width comparison this app can write itself.

`screen.OverlayCentered` replaces the hand-rolled `placeOverlay`; `screen.Width`
replaces the widest-line loop; `termtext.SanitizeLine` replaces `ansiRegexp`,
`termtext.Wrap` replaces `wrapText`, `termtext.Truncate` and `termtext.DisplayWidth`
replace `truncate` and `formatFixedName`, and the hand-written `min`/`max` go.

## Errors

Unchanged. `error: DAEMON_DOWN: …` when the daemon cannot be reached, `error:
DAEMON_ERROR: …` for an undecodable response or a daemon-reported failure, and
`error: DELIVERY_UNKNOWN: …` when a dispatched write may have committed. All of
them land in the status bar; nothing panics. An empty compose shows
`cannot be empty`. A bad slug or name from the CLI is `error: invalid: …` from
the store's `ErrInvalid`, as today.

## Testing

| Package | Coverage |
|---|---|
| `names` | accept/reject tables for both rules: leading digit, dash and dot; 12 vs 13 bytes; empty |
| `mentions` | the existing suite against the new charset, plus `@alice.` at end of sentence, plus a name that is a strict prefix of another |
| `store` | `ListRows` per granularity: counts, newest-first order, original-post content, `Count` at `g=message` and `g=channel`, unknown granularity |
| `apiserver` | `/v1/rows` for each `g`, `400` on a bad `g`, `/v1/inbox` is gone |
| `tui` | `rowLine` at 71, 80 and 200 columns; a 13-byte name in a 12-wide column; `renderThread`; `g` cycles; `v` reverses; `Enter`/`Esc`; `r` opens and `Esc` closes compose; a tick refetches and keeps the cursor; too-narrow |
| `cmd/flf` | `inbox` prints rows; `thread new --title "Schema migration"` fails; `thread import` generates a valid slug |
| `screen`, `termtext` | one smoke test each, so the fork is exercised rather than assumed |

The existing TUI tests are largely assertions about behavior this design deletes
— layout mutual exclusion, poll re-arming, full-layout geometry, detail scroll
clamping. They are replaced, not adapted. Any assertion that survives into new
code is kept verbatim; where an assertion changes because behavior changed, it
changes deliberately in the same commit.

## Docs

`docs/tui-architecture.md`, `docs/tui-keybindings.md` and `docs/tui-extending.md`
are rewritten against the new design rather than edited; each documents
behavior that no longer exists. `docs/agent-sessions.md` loses its section on
the preview pane and its polling notes, and keeps the daemon-side contract.
`README.md` and `VISION.md` have their examples re-slugged and their TUI
surface brought back in line with the keymap.

## Budget


| | now | after |
|---|---|---|
| `internal/tui` non-test | 4153 | ~1200 |
| `internal/tui` test | 3637 | ~800 |
| `internal/tui/screen` + `termtext` | 0 | ~250 |

Net reduction in the order of 3000 lines, with the shape of the model — not
merely its size — as the point.

## Non-goals

- Renaming `threads.title` to `slug`.
- A session pane, or any TUI view of a run's transcript.
- Creating channels or threads, or reacting, from the TUI. All three remain
  CLI operations.
- Search, filters, sort-field cycling, unread indicators, date grouping.
- Changes to the webapp, the agent contract, JSONL, or the daemon's session
  execution. The daemon still spawns agents on a human `@mention`; the TUI just
  no longer has a private view of the run.
- Pulling `splitlayout`, or upgrading to `charm.land/lipgloss/v2`.
