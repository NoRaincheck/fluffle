#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
SCRATCH=${SCRATCH:-$ROOT/.tui-seed}
export FLUFFLE_HOME=${FLUFFLE_HOME:-$SCRATCH/home}
FIXTURES=$ROOT/scripts/fixtures
RENDERED=$SCRATCH/rendered
FIXTURE_REPO=$SCRATCH/repo
BIN=$SCRATCH/bin/flf
NOW=$(date -u +%s)
LAUNCH_TUI=1
AGENT_SLEEP=${SEED_AGENT_SLEEP:-12}

# What the script creates, declared once. verify() counts these rather than a
# literal, so a fixture added here is verified without also editing a number
# that nothing would then keep in step.
CHANNELS=(
  "general orphaned"
  "random orphaned"
  "tui-testing orphaned"
  "agents orphaned"
  "cutover-plan orphaned"
  "glyph-check orphaned"
  "dev repo"
)

# fixture, channel, anchor, thread title
THREADS=(
  "general-welcome.jsonl general orphaned welcome"
  "general-announcements.jsonl general orphaned announce"
  "random-off-topic.jsonl random orphaned off-topic"
  "random-memes.jsonl random orphaned memes"
  "tui-testing-feedback.jsonl tui-testing orphaned tui-feedback"
  "dev-pr-review.jsonl dev repo pr-review"
  "dev-bug-fix.jsonl dev repo bug-fix"
  "release-long-names.jsonl cutover-plan orphaned migration-v1"
  "unicode-wide-glyphs.jsonl glyph-check orphaned wide-content"
  "agents-many-replies.jsonl agents orphaned many-replies"
  "agents-verbose-run.jsonl agents orphaned verbose-run"
  "agents-quiet-thread.jsonl agents orphaned quiet-thread"
  "agents-live-mention.jsonl agents orphaned live-mention"
)

usage() {
  cat <<'EOF'
Seeds a scratch Fluffle install with a rich TUI dataset and launches the TUI.

  scripts/seed-tui.sh [--no-tui]

Owns the whole lifecycle: wipes FLUFFLE_HOME, builds a fixture repo and a
fresh flf binary, starts the daemon and waits for it, imports the JSONL
fixtures in scripts/fixtures, stops the daemon to stage one agent session
through internal/store, restarts, fires one live @mention, verifies, and
then execs `flf tui` from inside the fixture repo.

Every channel and thread title below is a legal slug — at most 12 bytes,
a leading letter, then letters, digits, and dashes — because the store
refuses anything else and `set -e` would kill the run on the first one.
Every author name in the fixtures is a legal name: letters and single
dots, leading and trailing letter, at most 12 bytes. The TUI draws all
three in fixed 12-cell columns, so a name that the rules reject is a
column that cannot be drawn.

Environment:
  FLUFFLE_HOME   scratch home            (default: <repo>/.tui-seed/home)
  SCRATCH        scratch root            (default: <repo>/.tui-seed)
EOF
}

die() {
  printf 'seed-tui: %s\n' "$*" >&2
  exit 1
}

note() {
  printf '\n%s\n' "$*"
}

iso() {
  date -u -r "$1" +"%Y-%m-%dT%H:%M:%SZ" 2>/dev/null ||
    date -u -d "@$1" +"%Y-%m-%dT%H:%M:%SZ"
}

token_offset() {
  local spec=${1#@T} sign=1 total=0 num unit
  case $spec in
    -*) sign=-1; spec=${spec#-} ;;
    +*) spec=${spec#+} ;;
  esac
  while [[ $spec =~ ^([0-9]+)([dhms])(.*)$ ]]; do
    num=${BASH_REMATCH[1]}
    unit=${BASH_REMATCH[2]}
    spec=${BASH_REMATCH[3]}
    case $unit in
      d) total=$((total + num * 86400)) ;;
      h) total=$((total + num * 3600)) ;;
      m) total=$((total + num * 60)) ;;
      s) total=$((total + num)) ;;
    esac
  done
  printf '%d' $((sign * total))
}

render() {
  local src=$1 dst=$RENDERED/$(basename "$1") content tok
  content=$(cat "$src")
  for tok in $(grep -oE '@T[-+0-9dhms]*' "$src" | sort -u); do
    content=${content//$tok/$(iso $((NOW - $(token_offset "$tok"))))}
  done
  printf '%s\n' "$content" >"$dst"
}

build() {
  note "Building flf and seeding scripts"
  mkdir -p "$SCRATCH/bin"
  (cd "$ROOT" && go build -o "$BIN" ./cmd/flf)
}

fixture_repo() {
  if [[ -d $FIXTURE_REPO/.git ]]; then
    return
  fi
  rm -rf "$FIXTURE_REPO"
  mkdir -p "$FIXTURE_REPO"
  git -C "$FIXTURE_REPO" init -q
  git -C "$FIXTURE_REPO" config user.email seed@fluffle.local
  git -C "$FIXTURE_REPO" config user.name "Fluffle Seed"
  printf 'fluffle tui manual-qa fixture\n' >"$FIXTURE_REPO/README.md"
  git -C "$FIXTURE_REPO" add README.md
  git -C "$FIXTURE_REPO" commit -qm "seed fixture"
}

write_agent_config() {
  cat >"$FLUFFLE_HOME/config.toml" <<EOF
[[agents]]
name = "replacer"
description = "Seeded manual-QA agent. Sleeps so the live session is observable."
command = "/bin/sh"
args = ["-c", "echo 'reading config.toml'; sleep $AGENT_SLEEP; echo 'checked 2 agents'; sleep $AGENT_SLEEP; echo 'replacer: nothing to change'"]
reply = "stdout"
timeout_secs = $((AGENT_SLEEP * 4))
EOF
}

daemon_up() {
  "$BIN" daemon status --json >/dev/null 2>&1
}

start_daemon() {
  "$BIN" daemon start --background >/dev/null
  local waited=0
  until daemon_up; do
    sleep 0.2
    waited=$((waited + 1))
    ((waited > 100)) && die "daemon did not come up"
  done
}

stop_daemon() {
  daemon_up || return 0
  "$BIN" daemon stop >/dev/null
}

channel_id() {
  local name=$1 anchor=$2 out
  if [[ $anchor == repo ]]; then
    out=$("$BIN" channel list --repo "$FIXTURE_REPO" --json)
  else
    out=$("$BIN" channel list --include-orphaned --json)
  fi
  printf '%s' "$out" | jq -r --arg n "$name" '.[] | select(.Name == $n) | .ID' | head -1
}

make_channel() {
  local name=$1 anchor=$2
  if [[ $anchor == repo ]]; then
    "$BIN" channel create --name "$name" --repo "$FIXTURE_REPO" >/dev/null
  else
    "$BIN" channel create --orphaned --name "$name" >/dev/null
  fi
  printf '  %-38s %s\n' "$name" "$anchor"
}

seed_thread() {
  local fixture=$1 channel=$2 anchor=$3 title=$4 ch id
  ch=$(channel_id "$channel" "$anchor")
  [[ -n $ch && $ch != null ]] || die "channel $channel not found"
  if [[ $anchor == repo ]]; then
    id=$("$BIN" thread new --channel "$channel" --repo "$FIXTURE_REPO" --title "$title" --json | jq -r .id)
  else
    id=$("$BIN" thread new --channel "$channel" --orphaned --title "$title" --json | jq -r .id)
  fi
  "$BIN" thread import --thread "$id" --file "$RENDERED/$fixture" >/dev/null
  printf '  %-38s %s\n' "$title" "$fixture"
}

thread_sessions() {
  local port
  port=$(jq -r .port "$FLUFFLE_HOME/daemon.json")
  curl -s "http://127.0.0.1:$port/v1/threads/$1/sessions" | jq 'length'
}

# thread_id finds a thread the way it was created: an orphaned channel and a
# repo-anchored one are listed through different flags, and asking for the
# wrong one returns nothing rather than an error.
thread_id() {
  local channel=$1 anchor=$2 title=$3
  if [[ $anchor == repo ]]; then
    "$BIN" thread list --channel "$channel" --repo "$FIXTURE_REPO" --json
  else
    "$BIN" thread list --channel "$channel" --orphaned --json
  fi | jq -r --arg t "$title" '.[] | select(.Title == $t) | .ID'
}

# imported_messages is how many messages a fixture put in the database. A
# reaction is not one, so it does not count toward the inbox. `|| true` because
# grep exits 1 on a count of zero and this script runs under set -e.
imported_messages() {
  grep -c -v '"type":"reaction"' "$RENDERED/$1" || true
}

verify() {
  local inbox channels spec fixture channel anchor title id found=0
  local want_messages=0 failures=0 verbose

  inbox=$("$BIN" inbox --json --limit 500 | jq 'length')
  channels=$("$BIN" channel list --include-orphaned --json | jq 'length')

  for spec in "${THREADS[@]}"; do
    read -r fixture channel anchor title <<<"$spec"
    id=$(thread_id "$channel" "$anchor" "$title")
    if [[ -z $id || $id == null ]]; then
      printf '  FAIL: %s / %s was not created\n' "$channel" "$title" >&2
      failures=$((failures + 1))
      continue
    fi
    found=$((found + 1))
    want_messages=$((want_messages + $(imported_messages "$fixture")))
  done
  verbose=$(thread_sessions "$(thread_id agents orphaned verbose-run)")

  printf '  channels declared / seeded  %d / %s\n' "${#CHANNELS[@]}" "$channels"
  printf '  threads declared / found    %d / %d\n' "${#THREADS[@]}" "$found"
  printf '  inbox messages              %s (at least %d from the fixtures)\n' "$inbox" "$want_messages"
  printf '  staged sessions in verbose  %s\n' "$verbose"

  ((channels == ${#CHANNELS[@]})) ||
    { printf '  FAIL: seeded %s channels, this script declared %d\n' "$channels" "${#CHANNELS[@]}" >&2; failures=$((failures + 1)); }
  ((found == ${#THREADS[@]})) ||
    { printf '  FAIL: found %d threads, this script declared %d\n' "$found" "${#THREADS[@]}" >&2; failures=$((failures + 1)); }
  # A floor, not an equality: the live @mention and whichever of the agent's
  # replies have landed are on top of the fixtures, at a moment this script
  # does not control.
  ((inbox >= want_messages)) ||
    { printf '  FAIL: inbox holds %s messages, the fixtures alone import %d\n' "$inbox" "$want_messages" >&2; failures=$((failures + 1)); }
  ((verbose == 1)) ||
    { printf '  FAIL: expected the 1 staged session in verbose-run, found %s\n' "$verbose" >&2; failures=$((failures + 1)); }
  ((failures == 0)) || die "seed verification failed"
}

main() {
  case ${1:-} in
    '') ;;
    --no-tui) LAUNCH_TUI=0 ;;
    -h | --help)
      usage
      return 0
      ;;
    *) usage >&2; die "unknown argument: $1" ;;
  esac

  command -v jq >/dev/null || die "jq is required"
  command -v go >/dev/null || die "go is required"

  note "Resetting $FLUFFLE_HOME"
  (cd "$ROOT" && go run ./cmd/flf daemon stop >/dev/null 2>&1) || true
  rm -rf "$FLUFFLE_HOME" "$RENDERED"
  mkdir -p "$FLUFFLE_HOME" "$RENDERED"

  build
  fixture_repo

  note "Rendering fixtures"
  for f in "$FIXTURES"/*.jsonl; do
    render "$f"
  done

  write_agent_config

  note "Starting daemon"
  start_daemon

  note "Creating channels"
  for spec in "${CHANNELS[@]}"; do
    read -r name anchor <<<"$spec"
    make_channel "$name" "$anchor"
  done

  note "Seeding threads"
  for spec in "${THREADS[@]}"; do
    read -r fixture channel anchor title <<<"$spec"
    seed_thread "$fixture" "$channel" "$anchor" "$title"
  done

  note "Staging agent sessions (daemon must be down: no WAL, no busy_timeout)"
  stop_daemon
  (cd "$ROOT" && go run ./scripts/seed-sessions.go)

  note "Restarting daemon"
  start_daemon

  note "Firing one live @mention"
  "$BIN" message send --thread "$(thread_id agents orphaned live-mention)" \
    --text "@replacer live check: the newest inbox row should gain a reply within a few seconds" \
    --as alice >/dev/null

  note "Verifying"
  verify

  note "Ready"
  cat <<EOF
  Try these first:
    Enter            read the thread under the cursor (top row is the live mention)
    Esc              back to the list
    g                group: one row per message, then per thread, then per channel
    v                reverse the order: newest first vs oldest first
    r                reply to the row's thread
    ↑↓ / j / k       move the cursor; q quits

  Granularities to press g through:
    message    one row per message, so the NAME column changes down the list
               and the wide-glyph content lands in the CONTENT column
    thread     one row per thread, so the COUNT column is non-blank
    channel    one row per channel, and Enter and r say "no thread on this
               row — press g", which is the only place a row and a thread
               disagree

  Threads worth opening:
    agents / live-mention     a real @mention run; its reply lands with no
                              keypress, which is the tick earning its name
    agents / quiet-thread     the negative control for that: nothing ever
                              runs here, so the tick must change nothing
    agents / many-replies     six messages from five authors plus an agent
                              reply — the widest thread in the seed
    cutover-plan / migration-v1   names exactly at the 12-byte ceiling, so
                              they fill their cells with nothing to truncate
    glyph-check / wide-content    fullwidth text, ZWJ emoji, and combining
                              marks, which a fixed-width column has to count
                              in cells and not in bytes

  An agent run is not on screen — there is no session pane and no key that
  opens one. Read a transcript with:
      $BIN agent session --id <id> --json

  Keep poking at this install afterwards:
    export FLUFFLE_HOME=$FLUFFLE_HOME
    $BIN inbox --json --limit 20
    $BIN thread list --channel agents --orphaned

  Re-seed from scratch: scripts/seed-tui.sh
EOF

  if ((LAUNCH_TUI == 0)); then
    return 0
  fi
  cd "$FIXTURE_REPO"
  exec "$BIN" tui
}

main "$@"
