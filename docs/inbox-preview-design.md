# Design: Global Inbox with Adaptive Preview — Fluffle TUI

**Date:** 2026-09-23
**Status:** Draft (awaiting approval)
**Author:** brainstorming with user

## 1. Summary

Replace the current 3-view stack (Channels → Threads → Messages) with a single **Global Inbox** table that flattens all messages across all channels/threads into one scrollable, truncated view. Navigating the table updates an enhanced preview pane (when open / width ≥ 80) showing the full selected message (adaptive Y-line truncation) plus its last X=5 replies. This limits panel switches to zero for read-heavy workflows; write flows reuse the existing compose modal.

Approach chosen: **A — Flat Message Inbox** (no injected header rows; grouping via columns only, defer date/group headers to follow-up).

## 2. Goals / Non-Goals

**Goals:**
- Eliminate Enter/Esc stack navigation for scanning; cursor Up/Down is the only nav.
- Table columns: TIME | CHANNEL | THREAD | SENDER (color-coded human/agent/system) | CONTENT (single-line truncated).
- Preview shows full message + last 5 replies; adaptive truncation of the main message when content is long + many replies would overflow.
- Replaces stack entirely; preview toggle L remains.

**Non-Goals (YAGNI):**
- No pagination beyond `limit=100` latest messages.
- No search/filter, date grouping, density modes, or alternating row colors.
- No virtual scrolling or infinite scroll in v1.
- No change to localhost-only daemon binding or SQLite `SetMaxOpenConns(1)`.

## 3. Architecture

```
[SQLite] ── ListInbox() ──> [Daemon HTTP] GET /v1/inbox ──> [TUI apiClient] ──> [model.viewInbox]
                                                          ──> preview: GET /v1/threads/:id/messages
```

- `viewKind` in `internal/tui/model.go:15` reduces to single `viewInbox` (plus transient compose). Old `viewChannels/viewThreads/viewMessages` removed after migration.
- New enriched type `InboxMessage` (store or tui layer) — `store.Message` + `ChannelName`, `ChannelID`, `ThreadTitle`. Single SQL join surfaces all needed for columns + preview title.
- Preview pane reuses existing split logic `model.go:499` (`width >=80`); content contract changes, layout code does not.

## 4. Data Model & Store

**New struct:**
```go
type InboxMessage struct {
    Message
    ChannelName string `json:"channel_name"`
    ThreadTitle string `json:"thread_title"`
    ChannelID   int64  `json:"channel_id"`
}
```

**New method in `internal/store/store.go`:**
```go
func (s *Store) ListInbox(limit int) ([]InboxMessage, error)
```
SQL:
```sql
SELECT m.id, m.thread_id, m.seq, m.parent_id, m.author, m.author_type, m.role, m.content, m.created_at,
       c.name, c.id, t.title
FROM messages m
JOIN threads t ON t.id = m.thread_id
JOIN channels c ON c.id = t.channel_id
WHERE c.archived_at IS NULL AND t.archived_at IS NULL
ORDER BY m.created_at DESC, m.id DESC LIMIT ?
```
Reverse result to ASC for display (oldest top, cursor at newest bottom optional — default ASC, cursor starts at 0; polish may start cursor at end). Filter `limit<=0` defaults to 100, caps at 200.

Uses existing `scanMessages` pattern; no new indices required beyond existing `idx_messages_thread_seq`.

## 5. API

**New endpoint in `internal/apiserver/server.go`:**
```
GET /v1/inbox?limit=100
Response: []InboxMessage (JSON, Content-Type: application/json)
Errors: {"code","message"} envelope, 500 on DB error
```

Reuses daemon auth (localhost-only, `X-Fluffle-Agent` header path untouched). Existing `GET /v1/threads/:id/messages` serves preview; no change.

**Client in `internal/tui/api.go`:**
```go
func (c *apiClient) ListInbox(ctx context.Context, limit int) ([]InboxMessage, error)
```

## 6. TUI — State & Lifecycle

**Model fields (model.go:29):**
- Remove or deprecate: `view`, `channels`, `threads`, `messages`, `selectedChannel/Thread`
- Add: `inbox []InboxMessage`, `previewReplies []store.Message` (filtered), keep `previewThreadID`, `preview bool`, `cursor`
- Keep `channels []store.Channel` cache only for `n` (new thread → channel picker)

**Init:**
```go
func (m *model) fetchInbox() tea.Cmd // calls api.ListInbox
```
`Init() = fetchInbox`. On `inboxFetchedMsg` populate `m.inbox`, set `cursor=0` (or len-1 for newest), update `status = fmt.Sprintf("inbox — %d messages · %d channels · ↑↓ nav · r reply · n new thread · L preview", ...)`, then `maybeFetchPreview()`.

**Preview fetching:**
```go
func (m *model) maybeFetchPreview() tea.Cmd
```
Only if `preview && width>=80 && len(inbox)>0`. If `inbox[cursor].ThreadID != previewThreadID` → `fetchPreviewMessages(threadID)`. Dedup avoids storms on fast scroll.

## 7. TUI — Rendering

**Inbox table `renderInboxWithWidth(w int)`** (replaces `renderListWithWidth` branches):

```
TIME(8 right-aligned) | CHANNEL(12 truncate) | THREAD(16 truncate) | SENDER(12 color-coded) | CONTENT(flex truncated)
```

- Fixed widths: time 8, channel 12, thread 16, sender 12 → overhead ≈ 58 incl. separators. `contentWidth = max(minContentWidth 10, w - 48 - 10)`.
- Single-line truncation via existing `truncate(s, maxLen)` (`model.go:824`) with `ellipsisReserve=3`.
- Color: `getAuthorStyle(AuthorType)` applied to SENDER column only (`styles.go:128`).
- Header row via `lipgloss/table` with `Headers("TIME","CHANNEL","THREAD","SENDER","CONTENT")`.
- Cursor row uses `chatMsgSelectedStyle`; unselected uses alternating `treeItemStyle`/`chatMsgStyle`.

**Preview `renderPreview(w int)` (Inbox variant):**

```
Preview: #channel › thread · author
────────────────────────────────
<full message, truncated to Y lines>
... (+N lines if truncated)  // dim style
────────────────────────────────
Replies (last X):
  [time] author: one-line-content
  ...
  (no replies → "─ no replies ─")
  · time · authorType
```

Adaptive Y calculation:
```
hAvail := previewHeight - 4 // header+sep+padding+context line
replyReserve := X * 2      // 2 lines per reply worst-case
Y := hAvail - replyReserve - 2 // separator + title slack
if Y < 3 { Y = 3 }
if Y > 20 { Y = 20 } // cap so replies remain visible
lines := strings.Split(msg.Content, "\n")
if len(lines) > Y {
  lines = append(lines[:Y], fmt.Sprintf("... (+%d lines)", len(orig)-Y))
}
```

Replies: filter `previewMessages` where `Seq > highlighted.Seq` (or `ParentID == highlighted.ID` if threading adopted); take `last X` via slice. Each reply one line truncated to `w-6`.

## 8. Keybindings

| Key | Action (Inbox) |
|-----|----------------|
| `↑/k`, `↓/j` | Move cursor (±1), then `maybeFetchPreview()` |
| `r` / `c` | Reply: `compose.Open(composeModeMessage, "#ch › thread", height)` with `state.threadID = inbox[cursor].ThreadID`, `state.parentID = inbox[cursor].ID` (or 0 to append) |
| `n` | New thread: needs channel. Reuse channel cache: if cached empty, fetch channels. Open compose with channel picker (two-step modal: first select channel via inline list or prompt, then title). Simplest v1: prompt text `channelName/title` or reuse `composeModeNewThread` with channel ID selector. |
| `L`/`l` | Toggle preview bool |
| `q`, `ctrl+c` | Quit |
| `Esc` | No-op (reserved for future filter clear) |
| `Enter` | Alias for `r` (reply) to preserve muscle memory |

## 9. Error Handling

- Store/DB errors bubble as `fmt.Errorf("%s: %s", code, msg)` with envelope `{"code","message"}` already used in daemon.
- TUI: on `inboxFetchedMsg.err != nil` set `status = "error: …"` and retain prior inbox; no crash.
- Empty states: inbox loading vs loaded empty vs preview empty as described.
- Limits: `limit` capped at 200, negative defaults to 100.

## 10. Testing Plan

- `internal/store/store_test.go`: `TestListInbox` — seed 2 channels, 3 threads, 5 messages across them, assert `ListInbox(10)` returns 5 enriched rows sorted ASC by `created_at`, channel/thread names correct, archived rows excluded.
- `internal/apiserver` handler test: `TestInboxHandler` — inject store, `GET /v1/inbox` returns `Content-Type: application/json`, 200, correct count.
- `internal/tui/simple_test.go`: rewrite `TestSimpleFlow` to `TestInboxFlow` — feed `inboxFetchedMsg` with 3 `InboxMessage`, assert cursor nav moves, `maybeFetchPreview` dedup (second call with same thread returns nil), `renderInboxWithWidth` non-empty, adaptive truncation: create message with 20 lines + 10 previewMessages → rendered preview contains `... (+` and shows ≤5 replies. Also verify `handleReply` sets `state.threadID` correctly.
- Static checks: `gofmt -l` empty, `go vet ./...` clean, exit codes preserved.

## 11. File Touch List

- `internal/store/store.go` — `InboxMessage` + `ListInbox`
- `internal/apiserver/server.go` — handler + route `GET /v1/inbox`
- `internal/tui/api.go` — `ListInbox`
- `internal/tui/model.go` — inbox state, fetch, key handling, renderInbox, adaptive preview (largest diff)
- `internal/tui/simple_test.go` — rewritten flow test
- `docs/inbox-preview-design.md` — this doc

## 12. Rollout & Migration

- Behind single code path — inbox fully replaces stack; no feature flag. Old view code deleted to satisfy YAGNI and avoid dead branches.
- `n` (new thread) two-step is the only UX risk; fallback is to require `flf channel create` via CLI for first release if picker complexity delays.

## 13. Open Questions (Deferred)

- Cursor start at top vs bottom (newest). Default top (0) keeps `↑↓` natural; start-at-bottom can be added with `G` key later.
- Whether `c/r` should use `ParentID` threading or append — v1 appends, preview still shows last X regardless.

## 14. Success Criteria

- Single table shows all messages with truncated content fitting 80+ col terminals; preview shows full message + 5 replies with adaptive Y preventing overflow; j/k nav updates preview without full refetch when same thread; `gofmt -l` empty, `go vet ./...` clean, tests pass.
