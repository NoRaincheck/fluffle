# fluffle (`flf`)

A local-first, TUI-first communication hub for developer teams and local AI agents.

```bash
go build ./cmd/flf
./flf daemon start --background
./flf channel create --name "refactor" --repo .
./flf channel create --name "general" --orphaned
./flf message send --thread 1 --text "Reviewing now."
./flf daemon stop
```

See `VISION.md` for the full spec.
