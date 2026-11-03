.PHONY: generate test test-race lint web build cross fixtures

VERSION ?= dev
LDFLAGS := -s -w -X main.version=$(VERSION)

generate:
	sqlc generate

test:
	go test ./...

# -race necesita CGO y un compilador de C, aunque el servidor no use CGO.
test-race:
	CGO_ENABLED=1 go test -race ./...

lint:
	golangci-lint run

# Compila el panel en web/dist, que el servidor incrusta con go:embed. Sin este
# paso el binario arranca igual, pero sin panel.
web:
	cd web && npm ci && npm run build

build: web
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/ ./cmd/...

cross:
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o dist/windows-amd64/ ./cmd/...
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o dist/linux-amd64/ ./cmd/...
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o dist/linux-arm64/ ./cmd/...
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -o dist/darwin-amd64/ ./cmd/...
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -o dist/darwin-arm64/ ./cmd/...

fixtures:
	go run ./scripts/gen-fixtures
