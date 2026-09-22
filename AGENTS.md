# AGENTS.md

## Conventions

- **No comments** unless explicitly requested. Code should be self-documenting.
- **gofmt -l** must be empty for all changed files. **go vet ./...** must be clean.
- **TDD:** failing test → implementation → commit. Never commit without tests passing.
- **Exit codes:** 0 = success, 1 = client error, 2 = daemon error. Never deviate.
- **Error envelope:** `{"code","message"}` at every layer boundary.
- **Agent identity:** `X-Fluffle-Agent` header. The daemon re-derives `author_type` from it for anti-spoofing. Never trust `author_type` from client request bodies.
- **Agents are append-only:** messages + reactions. Never create channels, threads, or delete data.
- **No hidden agent memory:** the JSONL thread history is the only context.

## Domain Rules (Immutable)

1. **Repo-anchored by default:** every channel/thread ties to a local Git repo path.
2. **SQLite-first:** single database file, `SetMaxOpenConns(1)`.
3. **Localhost-only:** daemon binds `127.0.0.1:0`. No remote access.
4. **YAGNI:** do not add features not in the plan or spec.

## Common Pitfalls

- **`:memory:` SQLite:** always `SetMaxOpenConns(1)`, or use a file path.
- **`os.FindProcess` on Unix:** always succeeds; does not validate PID existence.
- **`net.Listen("tcp", ...)` always returns `*TCPAddr`:** safe to type-assert.
- **HTTP `Content-Type`:** set `application/json` on all responses, including success.
- **Import/append:** parse the whole file before the first POST. No partial writes.
