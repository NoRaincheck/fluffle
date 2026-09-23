#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

# Seed script for TUI manual testing.
# Creates orphan channels, repo-anchored channels, threads, messages,
# threaded replies, and reactions so the TUI has rich data to display.
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
flf_json_q message send --thread "$TID_WELCOME" --text "Welcome to Fluffle! 🎉" --as alice
flf_json_q message send --thread "$TID_WELCOME" --text "Hey everyone!" --as bob
flf_json_q message send --thread "$TID_WELCOME" --text "Nice to be here" --as carol
flf_json_q message send --thread "$TID_ANNOUNCE" --text "Fluffle v0.1 is out!" --as alice
flf_json_q message send --thread "$TID_OFFTOPIC" --text "Anyone up for a game tonight?" --as dave
flf_json_q message send --thread "$TID_MEMES" --text "Check this out" --as grace
flf_json_q message send --thread "$TID_MEMES" --text "😂😂😂" --as henry
flf_json_q message send --thread "$TID_TUI" --text "The preview panel looks great!" --as ivan
flf_json_q message send --thread "$TID_TUI" --text "Can we add dark mode?" --as judy
flf_json_q message send --thread "$TID_PR" --text "LGTM, r+1" --as alice
flf_json_q message send --thread "$TID_BUG" --text "Found the race condition" --as carol

# ── 9b. fetch message IDs for threaded replies ───────────────────────
MSG_ANNOUNCE_ROOT=$(curl -s "http://127.0.0.1:$PORT/v1/threads/$TID_ANNOUNCE/messages" | jq -r '.[0].ID')
MSG_OFFTOPIC_ROOT=$(curl -s "http://127.0.0.1:$PORT/v1/threads/$TID_OFFTOPIC/messages" | jq -r '.[0].ID')
MSG_TUI_JUDY=$(curl -s "http://127.0.0.1:$PORT/v1/threads/$TID_TUI/messages" | jq -r '.[1].ID')
MSG_PR_ALICE=$(curl -s "http://127.0.0.1:$PORT/v1/threads/$TID_PR/messages" | jq -r '.[0].ID')
MSG_BUG_CAROL=$(curl -s "http://127.0.0.1:$PORT/v1/threads/$TID_BUG/messages" | jq -r '.[0].ID')

# ── 9c. post threaded replies ────────────────────────────────────────
flf_json_q message send --thread "$TID_ANNOUNCE" --text "When can we expect the TUI?" --as bob --reply-to "$MSG_ANNOUNCE_ROOT"
flf_json_q message send --thread "$TID_OFFTOPIC" --text "I'm in!" --as eve --reply-to "$MSG_OFFTOPIC_ROOT"
flf_json_q message send --thread "$TID_OFFTOPIC" --text "Count me in too" --as frank --reply-to "$MSG_OFFTOPIC_ROOT"
flf_json_q message send --thread "$TID_TUI" --text "Dark mode is a must-have" --as ivan --reply-to "$MSG_TUI_JUDY"
flf_json_q message send --thread "$TID_PR" --text "Minor nit on line 42" --as bob --reply-to "$MSG_PR_ALICE"
flf_json_q message send --thread "$TID_PR" --text "Fixed!" --as alice --reply-to "$MSG_PR_ALICE"
flf_json_q message send --thread "$TID_BUG" --text "Nice find, I'll patch it" --as dave --reply-to "$MSG_BUG_CAROL"

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
