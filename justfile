# fluffle dev tasks. Run `just` to run the gate, or `just --list` to see these.
#
# The sqlc half of this file is documented in docs/backend.md under "SQL layer".

# Regenerate internal/db from internal/store/queries/ and internal/store/migrations/.
generate:
    go tool sqlc generate

# Fail unless internal/db/ matches what the generator produces right now.
diff:
    go tool sqlc diff

# sqlc's rules: append-only (no DELETE) and no-pragma.
vet:
    go tool sqlc vet

# The gate: go vet, gofmt, tests. The `e2e` tag is not optional: cmd/flf/e2e_test.go
# carries it, so a plain `go test ./...` skips the daemon end-to-end suite.
test:
    go vet ./...
    @test -z "$(gofmt -l .)" || { echo "gofmt -l reported unformatted files:"; gofmt -l .; exit 1; }
    go test ./...
    go test -tags e2e ./...

# Bare `just` runs the gate.
default: test
