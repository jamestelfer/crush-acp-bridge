# Run all checks before committing
verify: fmt build lint test

# Format all Go source files
fmt:
    gofmt -w .

# Run all tests
test *args:
    go test ./... {{args}}

# Regenerate go-snaps golden files; always review and commit the diff
snapshots *args:
    UPDATE_SNAPS=true go test ./... {{args}}

# Build the binary
build *args:
    #!/usr/bin/env bash
    set -euo pipefail
    mkdir -p dist
    env CGO_ENABLED=0 go build -trimpath -o dist/crush-acp-bridge {{args}} ./cmd/cli

# Run linter
lint:
    golangci-lint run ./...

# Cross-platform snapshot build for every release matrix target (via goreleaser)
xbuild *args:
    goreleaser build --snapshot --clean {{args}}

# Drive the built binary against a running crush server (needs CRUSH_ACP_URL and LIVE_CWD)
live: build
    go test -tags live -count=1 -v ./internal/live/
