#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

# Seed script for TUI manual testing.
# Creates orphan channels, repo-anchored channels, threads, messages,
# threaded replies, and reactions so the TUI has rich data to display.
# Messages span ~14 days with varied lengths (short ↔ 1000+ chars)
# and monotonically increasing timestamps.
#
# Usage:
#   export HOME_TMP=$(mktemp -d)
#   export FLUFFLE_HOME=$HOME_TMP
#   go run ./cmd/flf daemon start --background
#   sleep 1
#   ./scripts/seed-tui.sh
#   go run ./cmd/flf tui

export FLUFFLE_HOME="${FLUFFLE_HOME:?FLUFFLE_HOME not set}"
export HOME_TMP="${HOME_TMP:?HOME_TMP not set}"

# ── helpers ───────────────────────────────────────────────────────────
flf() { go run ./cmd/flf "$@"; }
flf_json() { go run ./cmd/flf "$@" --json; }
flf_json_q() { go run ./cmd/flf "$@" --json | jq .; }

# ── timestamp helpers ─────────────────────────────────────────────────
# Base: 14 days ago, UTC. Each call adds ~10-30 minutes.
NOW=$(date -u +%s)
BASE=$(( NOW - 14*86400 ))
ELAPSED=0

next_ts() {
  # Add 10-30 minutes (random-ish but deterministic)
  ELAPSED=$(( ELAPSED + 600 + (RANDOM % 1200) ))
  date -u -d "@$(( BASE + ELAPSED ))" +"%Y-%m-%dT%H:%M:%SZ" 2>/dev/null || \
  date -u -r $(( BASE + ELAPSED )) +"%Y-%m-%dT%H:%M:%SZ" 2>/dev/null
}

PORT=$(jq -r .port "$FLUFFLE_HOME/daemon.json")

# ── 1. orphan channel: general ───────────────────────────────────────
flf_json_q channel create --orphaned --name general

# ── 2. orphan channel: random ────────────────────────────────────────
flf_json_q channel create --orphaned --name random

# ── 3. orphan channel: tui-testing ───────────────────────────────────
flf_json_q channel create --orphaned --name tui-testing

# ── 4. repo-anchored channel ─────────────────────────────────────────
REPO=$(mktemp -d)
git -C "$REPO" init -q
git -C "$REPO" config user.email "t@t.com"
git -C "$REPO" config user.name "T"
touch "$REPO"/placeholder
git -C "$REPO" add .
git -C "$REPO" commit -qm init
flf_json_q channel create --name dev --repo "$REPO"

# ── 5. threads in general ────────────────────────────────────────────
flf_json_q thread new --channel general --orphaned --title "welcome"
flf_json_q thread new --channel general --orphaned --title "announcements"

# ── 6. threads in random ─────────────────────────────────────────────
flf_json_q thread new --channel random --orphaned --title "off-topic"
flf_json_q thread new --channel random --orphaned --title "memes"

# ── 7. threads in tui-testing ────────────────────────────────────────
flf_json_q thread new --channel tui-testing --orphaned --title "tui feedback"

# ── 8. threads in dev (repo) ─────────────────────────────────────────
flf_json_q thread new --channel dev --repo "$REPO" --title "pr review"
flf_json_q thread new --channel dev --repo "$REPO" --title "bug fix"

# ── 9. messages & replies ────────────────────────────────────────────
# Gather thread IDs
TID_WELCOME=$(flf_json_q thread list --channel general --orphaned | jq -r '.[] | select(.Title=="welcome") | .ID')
TID_ANNOUNCE=$(flf_json_q thread list --channel general --orphaned | jq -r '.[] | select(.Title=="announcements") | .ID')
TID_OFFTOPIC=$(flf_json_q thread list --channel random --orphaned | jq -r '.[] | select(.Title=="off-topic") | .ID')
TID_MEMES=$(flf_json_q thread list --channel random --orphaned | jq -r '.[] | select(.Title=="memes") | .ID')
TID_TUI=$(flf_json_q thread list --channel tui-testing --orphaned | jq -r '.[] | select(.Title=="tui feedback") | .ID')
TID_PR=$(flf_json_q thread list --channel dev --repo "$REPO" | jq -r '.[] | select(.Title=="pr review") | .ID')
TID_BUG=$(flf_json_q thread list --channel dev --repo "$REPO" | jq -r '.[] | select(.Title=="bug fix") | .ID')

# ── 9a. post root messages (no replies) ──────────────────────────────
# Timestamps span ~14 days, monotonically increasing. Mix of short & long.

# --- general: welcome (day 0) ---
flf_json_q message send --thread "$TID_WELCOME" --text "Welcome to Fluffle! 🎉" --as alice --created-at "$(next_ts)"
flf_json_q message send --thread "$TID_WELCOME" --text "Hey everyone!" --as bob --created-at "$(next_ts)"
flf_json_q message send --thread "$TID_WELCOME" --text "Nice to be here" --as carol --created-at "$(next_ts)"

# --- general: announcements (day 1) ---
flf_json_q message send --thread "$TID_ANNOUNCE" --text "Fluffle v0.1 is out!" --as alice --created-at "$(next_ts)"

# --- random: off-topic (day 2) ---
flf_json_q message send --thread "$TID_OFFTOPIC" --text "Anyone up for a game tonight?" --as dave --created-at "$(next_ts)"

# --- random: memes (day 3) ---
flf_json_q message send --thread "$TID_MEMES" --text "Check this out" --as grace --created-at "$(next_ts)"
flf_json_q message send --thread "$TID_MEMES" --text "😂😂😂" --as henry --created-at "$(next_ts)"

# --- tui-testing: feedback (day 4) ---
flf_json_q message send --thread "$TID_TUI" --text "The preview panel looks great!" --as ivan --created-at "$(next_ts)"
flf_json_q message send --thread "$TID_TUI" --text "Can we add dark mode?" --as judy --created-at "$(next_ts)"

# --- dev: pr review (day 5) ---
flf_json_q message send --thread "$TID_PR" --text "LGTM, r+1" --as alice --created-at "$(next_ts)"

# --- dev: bug fix (day 6) ---
flf_json_q message send --thread "$TID_BUG" --text "Found the race condition" --as carol --created-at "$(next_ts)"

# ── 9b. fetch message IDs for threaded replies ───────────────────────
MSG_ANNOUNCE_ROOT=$(curl -s "http://127.0.0.1:$PORT/v1/threads/$TID_ANNOUNCE/messages" | jq -r '.[0].ID')
MSG_OFFTOPIC_ROOT=$(curl -s "http://127.0.0.1:$PORT/v1/threads/$TID_OFFTOPIC/messages" | jq -r '.[0].ID')
MSG_TUI_JUDY=$(curl -s "http://127.0.0.1:$PORT/v1/threads/$TID_TUI/messages" | jq -r '.[1].ID')
MSG_PR_ALICE=$(curl -s "http://127.0.0.1:$PORT/v1/threads/$TID_PR/messages" | jq -r '.[0].ID')
MSG_BUG_CAROL=$(curl -s "http://127.0.0.1:$PORT/v1/threads/$TID_BUG/messages" | jq -r '.[0].ID')

# ── 9c. post threaded replies ────────────────────────────────────────
# Mix of short replies and longer detailed responses

# announcements reply (day 7) - long
flf_json_q message send --thread "$TID_ANNOUNCE" --text "When can we expect the TUI? I've been using the web interface and it's fine but having a native terminal client would be amazing for quick checks without opening a browser. Also wondering about offline support and whether there's a plan for mobile support down the road?" --as bob --reply-to "$MSG_ANNOUNCE_ROOT" --created-at "$(next_ts)"

# off-topic replies (day 8) - short
flf_json_q message send --thread "$TID_OFFTOPIC" --text "I'm in!" --as eve --reply-to "$MSG_OFFTOPIC_ROOT" --created-at "$(next_ts)"
flf_json_q message send --thread "$TID_OFFTOPIC" --text "Count me in too" --as frank --reply-to "$MSG_OFFTOPIC_ROOT" --created-at "$(next_ts)"

# tui-testing reply (day 9) - long
flf_json_q message send --thread "$TID_TUI" --text "Dark mode is a must-have. I spend most of my time in tmux with a dark background and having a bright white terminal app would cause eye strain after a few hours. Also, some thoughts on the overall UX: the channel list on the left is great, but I think the thread panel could use a collapse/expand toggle. Currently it just takes up space even when I'm not actively following a thread. Maybe a 'starred threads' feature would help?" --as ivan --reply-to "$MSG_TUI_JUDY" --created-at "$(next_ts)"

# pr review replies (day 10) - short
flf_json_q message send --thread "$TID_PR" --text "Minor nit on line 42" --as bob --reply-to "$MSG_PR_ALICE" --created-at "$(next_ts)"
flf_json_q message send --thread "$TID_PR" --text "Fixed!" --as alice --reply-to "$MSG_PR_ALICE" --created-at "$(next_ts)"

# bug fix reply (day 11) - long
flf_json_q message send --thread "$TID_BUG" --text "Nice find, I'll patch it. The issue is in the message append handler where we don't properly lock the channel map before iterating. I think we need a RWMutex here - reads can be concurrent but writes need exclusive access. I'll write a test case that reproduces the panic under load first." --as dave --reply-to "$MSG_BUG_CAROL" --created-at "$(next_ts)"

# ── 9d. more messages for variety (days 12-14) ──────────────────────
# Longer messages to fill out threads with realistic content

flf_json_q message send --thread "$TID_WELCOME" --text "Quick note on etiquette: please keep general discussions friendly and on-topic. If you have suggestions for features or want to report bugs, tui-testing and dev are the right places. Have fun!" --as alice --created-at "$(next_ts)"

flf_json_q message send --thread "$TID_OFFTOPIC" --text "So I was thinking we could set up a weekly game night. Maybe Fridays at 8pm? I was thinking we could do a mix of party games and strategy games. Something like Jackbox for the party games and maybe a round of Catan or Ticket to Ride for strategy. Let me know what works for everyone and I'll set up a recurring calendar invite." --as dave --created-at "$(next_ts)"

flf_json_q message send --thread "$TID_TUI" --text "I've been prototyping a dark mode theme for the TUI. The main challenge is balancing contrast - too dark and text becomes hard to read, too bright and it defeats the purpose. I'm leaning towards a slate-900 background with slate-100 text for body content, and using indigo-500 for links and interactive elements. The key is making sure the thread list, message area, and input bar all have distinct visual hierarchy. Also need to handle the case where the user has a light terminal theme - we should detect that and offer a light mode variant." --as ivan --created-at "$(next_ts)"

flf_json_q message send --thread "$TID_BUG" --text "After investigating the race condition further, I found it's not just in the channel map - the message append logic also has a TOCTOU issue where we check if a thread exists, then try to append, but another goroutine could have deleted it in between. The fix should use a single atomic operation with proper locking. I'll also add integration tests that run with -race and -cpu=4 to catch these in CI." --as carol --created-at "$(next_ts)"

flf_json_q message send --thread "$TID_PR" --text "One more thing - can we add a check in CI to verify that all exported functions have godoc comments? It's been a while since we enforced this and I've noticed a few new functions without documentation. Consistent documentation makes it much easier for new contributors to understand the codebase." --as alice --created-at "$(next_ts)"

# ── 10. reactions ────────────────────────────────────────────────────
# Fetch message IDs for reactions (global IDs, not seq)
MSG_WELCOME_ALICE=$(curl -s "http://127.0.0.1:$PORT/v1/threads/$TID_WELCOME/messages" | jq -r '.[0].ID')
MSG_OFFTOPIC_DAVE=$(curl -s "http://127.0.0.1:$PORT/v1/threads/$TID_OFFTOPIC/messages" | jq -r '.[0].ID')
MSG_MEMES_EVE=$(curl -s "http://127.0.0.1:$PORT/v1/threads/$TID_OFFTOPIC/messages" | jq -r '.[1].ID')
MSG_TUI_JUDY_ID=$(curl -s "http://127.0.0.1:$PORT/v1/threads/$TID_TUI/messages" | jq -r '.[1].ID')
MSG_PR_BOB=$(curl -s "http://127.0.0.1:$PORT/v1/threads/$TID_PR/messages" | jq -r '.[1].ID')
MSG_BUG_DAVE=$(curl -s "http://127.0.0.1:$PORT/v1/threads/$TID_BUG/messages" | jq -r '.[1].ID')

flf react add --message "$MSG_WELCOME_ALICE" --emoji "+1" --as bob
flf react add --message "$MSG_WELCOME_ALICE" --emoji "🎉" --as carol
flf react add --message "$MSG_OFFTOPIC_DAVE" --emoji "👀" --as dave
flf react add --message "$MSG_OFFTOPIC_DAVE" --emoji "😂" --as eve
flf react add --message "$MSG_OFFTOPIC_DAVE" --emoji "🤣" --as frank
flf react add --message "$MSG_TUI_JUDY_ID" --emoji "👍" --as ivan
flf react add --message "$MSG_TUI_JUDY_ID" --emoji "👎" --as judy
flf react add --message "$MSG_PR_BOB" --emoji "✅" --as bob
flf react add --message "$MSG_BUG_DAVE" --emoji "🔧" --as dave

# ── 11. summary ──────────────────────────────────────────────────────
echo ""
echo "=== Seed complete ==="
echo "Channels:"
flf_json_q channel list --include-orphaned
echo ""
echo "Threads:"
flf_json_q thread list --channel general --orphaned
flf_json_q thread list --channel random --orphaned
flf_json_q thread list --channel tui-testing --orphaned
flf_json_q thread list --channel dev --repo "$REPO"
echo ""
echo "Daemon port: $PORT"
echo "Start TUI: go run ./cmd/flf tui"
