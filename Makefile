generate:
	go tool sqlc generate

diff:
	go tool sqlc diff

vet:
	go tool sqlc vet

test:
	go vet ./...
	gofmt -l .
	go test ./...
