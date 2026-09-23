# TUI Manual Test (Human)

This doc covers pure-CLI JSON testing and interactive TUI navigation, following Roborev's split-layout pattern.

## Glossary (language firm)

- **Channel** = Slack-like room. Either repo-anchored (`--repo PATH`, tied to `repo_abs_path + repo_head_branch`) or `--orphaned` (no git, stored with `is_orphaned=1`). Listed via `flf channel list [--include-orphaned]`.
- **Thread** = titled conversation inside a channel (`flf thread new --channel NAME --title T`). Integer ID.
- **Message** = chat line inside a thread (`flf message send --thread ID --text T [--reply-to ID]`). Supports threaded reply via `parent_id` (stored as `ParentID`).
- **Reaction** = emoji on a message (`flf react add --message ID --emoji E`).

## Pure CLI JSON mode (no TUI)

Every command supports `--json` and returns `{"id":...}` or `[]` (never `null`). Use for scripting/tests.

```bash
# isolated daemon (avoids ~/.fluffle)
HOME_TMP=$(mktemp -d)
FLUFFLE_HOME=$HOME_TMP go run ./cmd/flf daemon start --background
sleep 1

# 1. Create orphan channel (no git required)
FLUFFLE_HOME=$HOME_TMP go run ./cmd/flf channel create --orphaned --name demo --json
# → {"id":1}

# 2. Verify listing
FLUFFLE_HOME=$HOME_TMP go run ./cmd/flf channel list --include-orphaned --json | jq .
# → [{"ID":1,"Name":"demo","IsOrphaned":true}]

# 3. Create repo-anchored channel (requires git repo)
REPO=$(mktemp -d); git -C $REPO init -q; git -C $REPO config user.email "t@t.com"; git -C $REPO config user.name "T"; touch $REPO/f; git -C $REPO add .; git -C $REPO commit -qm init
FLUFFLE_HOME=$HOME_TMP go run ./cmd/flf channel create --name repo-demo --repo $REPO --json
FLUFFLE_HOME=$HOME_TMP go run ./cmd/flf channel list --repo $REPO --json | jq .

# 4. Thread in orphan channel
FLUFFLE_HOME=$HOME_TMP go run ./cmd/flf thread new --channel demo --orphaned --title "hello world" --json
# → {"id":1}
FLUFFLE_HOME=$HOME_TMP go run ./cmd/flf thread list --channel demo --orphaned --json | jq .

# 5. Thread in repo channel
FLUFFLE_HOME=$HOME_TMP go run ./cmd/flf thread new --channel repo-demo --repo $REPO --title "from repo" --json
FLUFFLE_HOME=$HOME_TMP go run ./cmd/flf thread list --channel repo-demo --repo $REPO --json | jq .

# 6. Post messages (any thread ID)
TID=1
FLUFFLE_HOME=$HOME_TMP go run ./cmd/flf message send --thread $TID --text "first message" --as alice --json
# → {"seq":1}
FLUFFLE_HOME=$HOME_TMP go run ./cmd/flf message send --thread $TID --text "second" --as bob --json

# 7. Reply to a message (threaded)
# Get parent message ID via HTTP (or agent read)
curl -s http://127.0.0.1:$(jq -r .port $HOME_TMP/daemon.json)/v1/threads/$TID/messages | jq .
FLUFFLE_HOME=$HOME_TMP go run ./cmd/flf message send --thread $TID --text "reply to first" --as carol --reply-to 1 --json
# → {"seq":3} with ParentID.Valid=true

# 8. Read back (JSONL per line when --json)
FLUFFLE_HOME=$HOME_TMP go run ./cmd/flf agent read --thread $TID --json
# → {"seq":1,"role":"user","author":"alice",...}
# → {"seq":2,"role":"user","author":"bob",...}
# → {"seq":3,"role":"user","author":"carol",...}

# 9. Teardown
curl -s -X POST http://127.0.0.1:$(jq -r .port $HOME_TMP/daemon.json)/api/shutdown
```

## Interactive TUI — Roborev-inspired split layout

Layout mirrors Roborev's `splitlayout` (see `cmd/roborev/tui/layout.go` + `split_render.go`): when terminal width ≥ 90 columns, left tree + right detail side-by-side; otherwise stacked (focused pane only). Focus toggles with `Tab`; detail follows selection; Esc navigates back via view stack.

### Start

```bash
HOME_TMP=$(mktemp -d)
FLUFFLE_HOME=$HOME_TMP go run ./cmd/flf daemon start --background
# create demo data first (so tree is not empty)
FLUFFLE_HOME=$HOME_TMP go run ./cmd/flf channel create --orphaned --name demo --json
FLUFFLE_HOME=$HOME_TMP go run ./cmd/flf thread new --channel demo --orphaned --title "hello" --json
# also: FLUFFLE_HOME=$HOME_TMP go run ./cmd/flf channel create --name repo-demo --repo $REPO --json

FLUFFLE_HOME=$HOME_TMP go run ./cmd/flf tui
```

### Navigation (human checklist)

- [ ] **Home (channel/repo view)**: left pane shows `🗂 Orphaned` → `🗨 demo`, and `📂 <repo> (main)` → `🗨 repo-demo`. Status line: "3 channels — ↑↓ navigate, Enter open".
- [ ] **↑↓ / k/j**: moves cursor in tree; highlight follows.
- [ ] **Enter on channel** `demo`: tree expands (🗨▾), right pane switches to thread list `— threads (1)` showing `# hello`. Status: "channel demo: 1 threads — Enter thread, n new, Esc back".
- [ ] **Enter on thread** `# hello`: right pane switches to messages `demo › hello` (empty or with messages), status "thread hello: N messages — c post, r reply, Esc back". Tree stays on thread.
- [ ] **c (post)** in thread view: modal `... › hello` opens, type `first!`, `Enter` → status "message sent", messages refresh with `[now] alice: first!` (selected).
- [ ] **r (reply)**: with a message selected (▸), press `r` → modal `Reply to: first!`, type `ack`, `Enter` → message list shows `↳` reply indicator.
- [ ] **n (new thread)** in channel view: with channel selected or in channel view, press `n` → modal "New thread in #demo", type `second topic`, Enter → thread list refreshes (now 2), tree shows second thread.
- [ ] **Esc back**: from thread messages → back to thread list; again → back to home (channels). Home chat shows empty.
- [ ] **Tab** (wide terminal): toggles focus between tree (list) and chat (detail). Help bar updates: `Tab switch`.
- [ ] **q / ctrl+c**: quit, returns to shell.

### Troubleshooting

- Empty tree: `flf channel list --include-orphaned --json` — if empty, create channel as above.
- "no thread selected — press n": you are in channel view; create thread with `n` or `Enter` thread.
- `DAEMON_DOWN`: daemon not running — `FLUFFLE_HOME=$HOME_TMP go run ./cmd/flf daemon status` should say `daemon up`.
- Narrow terminal (<90 cols): layout stacks; only focused pane visible — use Tab to switch.

### Attribution

Split logic adapts Roborev's `splitlayout.Config{ListMinWidth:50, ListMaxWidth:90, DetailReservedWidth:100}` + `resolveLayout` + `splitActive` gating + `followSelectionChange` debounce pattern (see `cmd/roborev/tui/layout.go`, `split_render.go`, `nav.go`). Fluffle simplifies to 2 panes (tree + chat) and a 3-level view stack (home→channel→thread) with `Esc` stack pop, without panels/members or queued/running job states.
