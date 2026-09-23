# TUI Manual Test (Human)

## Glossary (language firm)

- **Channel** = Slack-like room. Either repo-anchored (`--repo PATH`, tied to `repo_abs_path + repo_head_branch`) or `--orphaned` (no git, stored with `is_orphaned=1`). Listed via `flf channel list [--include-orphaned]`.
- **Thread** = titled conversation inside a channel (`flf thread new --channel NAME --title T`). Integer ID.
- **Message** = chat line inside a thread (`flf message send --thread ID --text T [--reply-to ID]`). Supports threaded reply via `parent_id` (stored as `ParentID`).
- **Reaction** = emoji on a message (`flf react add --message ID --emoji E`).

## Pure CLI JSON mode (no TUI)

Every command supports `--json` and returns rich objects (never `null`). Use for scripting/tests.

```bash
set -e
export HOME_TMP=$(mktemp -d)
export FLUFFLE_HOME=$HOME_TMP
export REPO=$(mktemp -d)
git -C $REPO init -q; git -C $REPO config user.email "t@t.com"; git -C $REPO config user.name "T"; touch $REPO/f; git -C $REPO add .; git -C $REPO commit -qm init

go run ./cmd/flf daemon start --background
sleep 1
go run ./cmd/flf daemon status

# 1. Create orphan channel (no git required) — returns full channel object
go run ./cmd/flf channel create --orphaned --name demo --json | jq .
# → {"ID":1,"Name":"demo","IsOrphaned":true,"RepoAbsPath":"","CreatedAt":"..."}

# 2. Verify listing (also full objects)
go run ./cmd/flf channel list --include-orphaned --json | jq .
# → [{"ID":1,"Name":"demo","IsOrphaned":true}, {"ID":2,...}]

# 3. Create repo-anchored channel (requires git repo)
go run ./cmd/flf channel create --name repo-demo --repo $REPO --json | jq .
go run ./cmd/flf channel list --repo $REPO --json | jq .

# 4. Thread in orphan channel — returns id + title + channel context
go run ./cmd/flf thread new --channel demo --orphaned --title "hello world" --json | jq .
# → {"id":1,"title":"hello world","channel":"demo","channel_id":1}
go run ./cmd/flf thread list --channel demo --orphaned --json | jq .
# → [{"ID":1,"ChannelID":1,"Title":"hello world",...}]

# 5. Thread in repo channel
go run ./cmd/flf thread new --channel repo-demo --repo $REPO --title "from repo" --json | jq .
go run ./cmd/flf thread list --channel repo-demo --repo $REPO --json | jq .

# 6. Post messages (any thread ID) — returns seq + author/content/parent
TID=$(go run ./cmd/flf thread list --channel demo --orphaned --json | jq -r '.[0].ID')
go run ./cmd/flf message send --thread $TID --text "first message" --as alice --json | jq .
# → {"seq":1,"thread_id":1,"author":"alice","content":"first message","parent_id":0}
go run ./cmd/flf message send --thread $TID --text "second" --as bob --json | jq .

# 7. Reply to a message (threaded) — use message ID from HTTP
PORT=$(jq -r .port $HOME_TMP/daemon.json)
curl -s http://127.0.0.1:$PORT/v1/threads/$TID/messages | jq .
# ParentID.Valid indicates reply
go run ./cmd/flf message send --thread $TID --text "reply to first" --as carol --reply-to 1 --json | jq .
# → {"seq":3,"parent_id":1,...}

# 8. Read back (JSONL per line when --json)
go run ./cmd/flf agent read --thread $TID --json
# → {"seq":1,"role":"user","author":"alice",...}
# → {"seq":2,"role":"user","author":"bob",...}
# → {"seq":3,"role":"user","author":"carol",...}
go run ./cmd/flf agent read --thread $TID --json | wc -l  # → 3

# 9. Teardown
curl -s -X POST http://127.0.0.1:$PORT/api/shutdown | jq .
```

**Why rich JSON?** `channel create --json` used to return `{"id":1}` only — now returns the full `Channel` (with `ID`, `Name`, `IsOrphaned`, `RepoAbsPath`, `CreatedAt`) so scripts can avoid a second fetch. Same for `thread new` (`id` + `title` + `channel`) and `message send` (`seq` + `thread_id` + `author` + `content` + `parent_id`). List commands already returned full arrays.

## Interactive TUI — Simple 3-view stack (Channels → Threads → Messages)

Design is intentionally minimal (no split panes). One list at a time, `Esc` backs up the stack. Panic on width 0 fixed via `max(0, width)` guards.

### Start (isolated, copy-pasteable)

```bash
set -e
export HOME_TMP=$(mktemp -d)
export FLUFFLE_HOME=$HOME_TMP
go run ./cmd/flf daemon start --background
sleep 1
# create demo data first (so list is not empty)
go run ./cmd/flf channel create --orphaned --name demo --json | jq .
go run ./cmd/flf thread new --channel demo --orphaned --title "hello" --json | jq .
# optional repo channel:
# export REPO=$(mktemp -d); git -C $REPO init -q; git -C $REPO config user.email "t@t.com"; git -C $REPO config user.name "T"; touch $REPO/f; git -C $REPO add .; git -C $REPO commit -qm init
# go run ./cmd/flf channel create --name repo-demo --repo $REPO --json | jq .

go run ./cmd/flf tui
```

> Use `export` so `HOME_TMP`/`FLUFFLE_HOME` persist across lines. Each `FLUFFLE_HOME=$HOME_TMP go run ...` also works if you keep the same shell.

### Navigation (human checklist)

- [ ] **Channels view** (first screen): title `Channels`, list shows `demo  (orphaned)` and `repo-demo  [main]  repo`. Cursor `▸` follows `↑↓` / `j/k`. Status: "1 channels — ↑↓ nav · Enter open · n new thread · q quit". If empty, status hints `flf channel create --orphaned --name demo`.
- [ ] **Enter on channel** `demo`: switches to `Threads in #demo`, list shows `# hello` (or "(no threads — press n)"). Status: "#demo — 1 threads · ↑↓ nav · Enter open · n new · Esc back".
- [ ] **Enter on thread** `# hello`: switches to `#demo › hello`, messages show `[now] alice: hi` or "(no messages — press c)". Status: "#demo › hello — 1 messages · ↑↓ nav · c post · r reply · Esc back".
- [ ] **c (post)** in messages view: modal `... › hello` opens, type `first!`, `Enter` → status "sent", list refreshes with new line (selected `▸`).
- [ ] **r (reply)**: with a message selected (`▸`), press `r` → modal `Reply to: first!`, type `ack`, `Enter` → list shows ` ↳` indicator on reply.
- [ ] **n (new thread)** in channels or threads view: with channel selected, press `n` → modal "New thread in #demo", type `second topic`, `Enter` → thread list refreshes (now 2).
- [ ] **Esc back**: from messages → threads; again → channels. Each Esc resets cursor to 0 and clears selection.
- [ ] **q / ctrl+c**: quit, returns to shell.

### Troubleshooting

- Empty list: `go run ./cmd/flf channel list --include-orphaned --json | jq .` — if empty, create channel as above.
- "select a channel first": you are in channels view with no selection — use `↑↓` to pick one then `n`.
- "no thread — n to create": you are in threads view with no threads — press `n`.
- "open a thread first": you pressed `c` in threads view without a thread selected — `Enter` a thread first or `n`.
- `DAEMON_DOWN`: daemon not running — `go run ./cmd/flf daemon status` should say `daemon up at http://127.0.0.1:PORT` and `cat $HOME_TMP/daemon.json` shows port.
- Panic `strings: negative Repeat count`: fixed in `internal/tui/model.go:507` via `max(0, width-4)` — was `min(width-4,60)` with width 0 on first render before `WindowSizeMsg`. If you see it, `go vet` and update.
- Narrow terminal: works at any width (minimum 20 columns via `max(20, width-4)`), no hard 90-col split requirement.
