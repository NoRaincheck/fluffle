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

usage() {
  cat <<'EOF'
Seeds a scratch Fluffle install with a rich TUI dataset and launches the TUI.

  scripts/seed-tui.sh [--no-tui]

Owns the whole lifecycle: wipes FLUFFLE_HOME, builds a fixture repo and a
fresh flf binary, starts the daemon and waits for it, imports the JSONL
fixtures in scripts/fixtures, stops the daemon to stage agent sessions
through internal/store, restarts, fires one live @mention, verifies, and
then execs `flf tui` from inside the fixture repo.

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

thread_id() {
  "$BIN" thread list --channel "$1" --orphaned --json |
    jq -r --arg t "$2" '.[] | select(.Title == $t) | .ID'
}

verify() {
  local inbox threads staged failures=0
  inbox=$("$BIN" inbox --json --limit 500 | jq 'length')
  threads=$("$BIN" channel list --include-orphaned --json | jq 'length')
  staged=$(thread_sessions "$(thread_id agents "session states")")
  printf '  channels                  %s\n' "$threads"
  printf '  inbox messages            %s\n' "$inbox"
  printf '  sessions in session states %s\n' "$staged"
  printf '  sessions in verbose run    %s\n' "$(thread_sessions "$(thread_id agents "verbose run")")"
  ((threads == 7)) || { printf '  FAIL: expected 7 channels\n' >&2; failures=1; }
  ((inbox > 40)) || { printf '  FAIL: expected a rich inbox\n' >&2; failures=1; }
  ((staged == 6)) || { printf '  FAIL: expected 6 staged sessions in session states\n' >&2; failures=1; }
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
  make_channel general orphaned
  make_channel random orphaned
  make_channel tui-testing orphaned
  make_channel agents orphaned
  make_channel release-coordination-and-handoffs orphaned
  make_channel unicode-channels orphaned
  make_channel dev repo

  note "Seeding threads"
  seed_thread general-welcome.jsonl general orphaned welcome
  seed_thread general-announcements.jsonl general orphaned announcements
  seed_thread random-off-topic.jsonl random orphaned off-topic
  seed_thread random-memes.jsonl random orphaned memes
  seed_thread tui-testing-feedback.jsonl tui-testing orphaned "tui feedback"
  seed_thread dev-pr-review.jsonl dev repo "pr review"
  seed_thread dev-bug-fix.jsonl dev repo "bug fix"
  seed_thread release-long-names.jsonl release-coordination-and-handoffs orphaned \
    cross-team-migration-notes-for-the-v1-cutover
  seed_thread unicode-wide-glyphs.jsonl unicode-channels orphaned \
    "全角・Display・Width・テスト"
  seed_thread agents-session-states.jsonl agents orphaned "session states"
  seed_thread agents-verbose-run.jsonl agents orphaned "verbose run"
  seed_thread agents-no-session-here.jsonl agents orphaned "no session here"
  seed_thread agents-live-mention.jsonl agents orphaned "live mention"

  note "Staging agent sessions (daemon must be down: no WAL, no busy_timeout)"
  stop_daemon
  (cd "$ROOT" && go run ./scripts/seed-sessions.go)

  note "Restarting daemon"
  start_daemon

  note "Firing one live @mention"
  "$BIN" message send --thread "$(thread_id agents "live mention")" \
    --text "@replacer live check: the newest inbox row should gain a session within a second" \
    --as alice >/dev/null

  note "Verifying"
  verify

  note "Ready"
  cat <<EOF
  Try these first:
    Enter            open the thread under the cursor (top row is the live mention)
    s                session pane; press it again to go back to the thread
    p  then  l       preview on, then switch to the full layout (preview turns itself off)
    v                toggle sort: latest-desc vs channel/thread + time desc
    f                filter; type zzz to see the 0/N filtered empty state
    r  n  C  e       reply, new thread, new channel, react
    C                anchors a new channel to $FIXTURE_REPO (the cwd for this TUI)

  Session coverage:
    agents / session states   failed, canceled, succeeded x4, one with "replied #N",
                              two agents on a single message, reply modes auto/stdout/cli
    agents / verbose run      41 events, exercises the "... N hidden ..." fold
    agents / live mention     a real @mention run, queued then running for ~$((AGENT_SLEEP * 2))s
    agents / no session here  press s and read the status bar

  queued and running are never staged: the daemon reconciles both to canceled on
  startup, so only a live run can show those states.

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
