# fluffle (`flf`)

A local-first, TUI-first communication hub for developer teams and local AI agents. Split layout inspired by [roborev](https://github.com/kenn-io/roborev) (see `docs/tui-manual-test.md`).

**Glossary:** Channel = repo-anchored or `--orphaned` room · Thread = titled conversation in a channel · Message = chat line in a thread (reply via `--reply-to`).

```bash
go build ./cmd/flf
./flf daemon start --background

# orphan channel (no git needed) + thread + messages — all --json testable
./flf channel create --orphaned --name general --json
./flf thread new --channel general --orphaned --title "hello" --json   # → {"id":1}
./flf message send --thread 1 --text "first!" --as alice --json       # → {"seq":1}
./flf message send --thread 1 --text "reply" --reply-to 1 --as bob --json
./flf agent read --thread 1 --json   # JSONL per line

# repo-anchored channel/thread
./flf channel create --name refactor --repo . --json
./flf thread new --channel refactor --repo . --title "review" --json
./flf thread list --channel refactor --repo . --json

# TUI (Roborev-style split: tree left, detail right, Tab focus, Esc back)
./flf tui
#  ↑↓ navigate · Enter open channel/thread · n new thread · c post · r reply · Esc back · q quit · Tab switch

./flf daemon stop
```

See `VISION.md` and `docs/tui-manual-test.md` for manual QA + Roborev attribution.
