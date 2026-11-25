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

# Drive the built binary against a running crush server (needs CRUSH_ACP_URL and LIVE_CWD; TestSmoke starts its own server when CRUSH_ACP_URL is unset)
live *args: build
    go test -tags live -count=1 -v ./internal/live/ {{args}}

acpx_version := "0.22.0"

# Send one prompt through the built bridge with acpx (needs a running crush server and LIVE_CWD)
acpx prompt: build
    #!/usr/bin/env bash
    set -euo pipefail
    url="${CRUSH_ACP_URL:-http://127.0.0.1:8484}"
    npx -y "acpx@{{acpx_version}}" \
        --cwd "${LIVE_CWD:?set LIVE_CWD to the project directory}" \
        --agent "$PWD/dist/crush-acp-bridge --crush-url ${url}" \
        --approve-all exec {{quote(prompt)}}

# Record a Crush SSE trace into testdata/ (needs a running crush server and a configured provider)
[positional-arguments]
record-trace name action +prompts:
    #!/usr/bin/env bash
    set -euo pipefail
    url="${CRUSH_ACP_URL:-http://127.0.0.1:8484}"
    name="$1" action="$2"
    shift 2
    scripts/record-trace "${url}" "${LIVE_CWD:?set LIVE_CWD to the project directory}" \
        "testdata/${name}.sse" "${action}" "$@"
