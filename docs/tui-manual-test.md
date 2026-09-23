# TUI Manual Test (Human)

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

## Interactive TUI — Simple 3-view stack (Channels → Threads → Messages)

Design is intentionally minimal (no split panes, no Tab focus). One list at a time, `Esc` backs up the stack. Inspired by Roborev's view-stack idea but stripped to basics.

### Start

```bash
HOME_TMP=$(mktemp -d)
FLUFFLE_HOME=$HOME_TMP go run ./cmd/flf daemon start --background
# create demo data first (so list is not empty)
FLUFFLE_HOME=$HOME_TMP go run ./cmd/flf channel create --orphaned --name demo --json
FLUFFLE_HOME=$HOME_TMP go run ./cmd/flf thread new --channel demo --orphaned --title "hello" --json
# also: FLUFFLE_HOME=$HOME_TMP go run ./cmd/flf channel create --name repo-demo --repo $REPO --json

FLUFFLE_HOME=$HOME_TMP go run ./cmd/flf tui
```

### Navigation (human checklist)

- [ ] **Channels view** (first screen): title `Channels`, list shows `demo  (orphaned)` and `repo-demo  [main]  repo`. Cursor `▸` follows `↑↓` / `j/k`. Status: "2 channels — ↑↓ nav · Enter open · n new thread · q quit".
- [ ] **Enter on channel** `demo`: switches to `Threads in #demo`, list shows `# hello` (or "(no threads — press n)"). Status: "#demo — 1 threads · ↑↓ nav · Enter open · n new · Esc back".
- [ ] **Enter on thread** `# hello`: switches to `#demo › hello`, messages show `[now] alice: hi` or "(no messages — press c)". Status: "#demo › hello — 1 messages · ↑↓ nav · c post · r reply · Esc back".
- [ ] **c (post)** in messages view: modal `... › hello` opens, type `first!`, `Enter` → status "sent", list refreshes with new line.
- [ ] **r (reply)**: with a message selected (`▸`), press `r` → modal `Reply to: first!`, type `ack`, `Enter` → list shows ` ↳` indicator on reply.
- [ ] **n (new thread)** in channels or threads view: with channel selected, press `n` → modal "New thread in #demo", type `second topic`, `Enter` → thread list refreshes (now 2).
- [ ] **Esc back**: from messages → threads; again → channels. Each Esc resets cursor to 0.
- [ ] **q / ctrl+c**: quit, returns to shell.

### Troubleshooting

- Empty list: `flf channel list --include-orphaned --json` — if empty, create channel as above.
- "select a channel first": you are in channels view with no selection — use `↑↓` to pick one then `n`.
- "no thread — n to create": you are in threads view with no threads — press `n`.
- "open a thread first": you pressed `c` in threads view without a thread selected — `Enter` a thread first or `n`.
- `DAEMON_DOWN`: daemon not running — `FLUFFLE_HOME=$HOME_TMP go run ./cmd/flf daemon status` should say `daemon up`.
