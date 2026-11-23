# crush-acp-bridge

Experimenting with Crush.

An [Agent Client Protocol](https://agentclientprotocol.com) (ACP) agent for
[Crush](https://github.com/charmbracelet/crush). The bridge speaks ACP over stdio and talks to a
running `crush server`, so an ACP editor and the Crush TUI work in the same sessions.

This is a spike. It is built and tested against Crush 0.102.0.

## Installation

Pre-built binaries for Linux, macOS, and Windows (amd64 and arm64) are published to
[GitHub Releases](https://github.com/jamestelfer/crush-acp-bridge/releases). Every archive
carries a build provenance attestation.

Verify a downloaded archive with the [GitHub CLI](https://cli.github.com/):

```sh
gh attestation verify crush-acp-bridge_linux_amd64.tar.gz --repo jamestelfer/crush-acp-bridge \
  --signer-workflow chinmina/.github/.github/workflows/goreleaser-release.yml
```

Install with any of:

```sh
mise use -g github:jamestelfer/crush-acp-bridge
brew install jamestelfer/tap/crush-acp-bridge
nix profile install github:jamestelfer/nixpkgs#crush-acp-bridge
go install github.com/jamestelfer/crush-acp-bridge/cmd/cli@latest
```

## Usage

### Self-contained: let the bridge run Crush

`--managed` starts `crush server` as a child process on a free local port, connects to it, and stops it
when the editor disconnects. No separate server is needed, and the server's idle timeout does not apply.

```sh
crush-acp-bridge --managed
```

`crush` must be on `PATH`, or pass `--crush-bin`. Provider settings such as `VERTEXAI_PROJECT` and
`VERTEXAI_LOCATION` are read from the bridge's environment and passed to the server. Add
`--listen 127.0.0.1:8484` to fix the address so a Crush TUI can attach to the same server (see
[Share the session](#3-share-the-session-with-the-crush-tui)).

### Or: connect to a server you run

#### 1. Start a Crush server

```sh
crush server --host http://127.0.0.1:8484
```

The server needs a configured provider. Providers that read environment variables, such as Vertex AI
(`VERTEXAI_PROJECT`, `VERTEXAI_LOCATION`), read them from the process that starts the server.

The server exits 60 seconds after its last workspace is released, or 60 seconds after it starts if no
client opens one. Start the bridge's first session within that window, or raise the limit with
`CRUSH_SERVER_IDLE_TIMEOUT` (seconds):

```sh
CRUSH_SERVER_IDLE_TIMEOUT=86400 crush server --host http://127.0.0.1:8484
```

`0` shuts the server down immediately when it goes idle. It does not disable the shutdown.

#### 2. Point the editor at the bridge

Configure the editor to run `crush-acp-bridge` as an ACP agent over stdio. For an editor with a
`command` and `args` agent entry:

```json
{
  "command": "crush-acp-bridge",
  "args": ["--crush-url", "http://127.0.0.1:8484"]
}
```

| Flag | Environment | Description |
|---|---|---|
| `--managed` | | Run `crush server` as a child process. Conflicts with an explicit `--crush-url`. |
| `--crush-bin` | `CRUSH_ACP_CRUSH_BIN` | The `crush` executable for `--managed`. Defaults to `crush`. |
| `--listen` | | `host:port` for the `--managed` server. Defaults to a free local port. |
| `--crush-url` | `CRUSH_ACP_URL` | Base URL of the Crush server. Defaults to `http://127.0.0.1:8484`. HTTP only. |
| `--cwd` | | Open this directory in Crush for every session, ignoring the directory the editor sends. |
| `--log-level` | `CRUSH_ACP_LOG_LEVEL` | `debug`, `info`, `warn` or `error`. Defaults to `info`. |
| `--log-format` | `CRUSH_ACP_LOG_FORMAT` | `text` or `json`. Defaults to `text`. |

Logs are structured ([`log/slog`](https://pkg.go.dev/log/slog)) and go to stderr. Stdout carries ACP
messages only. Records carry `session_id` and `cwd` where they apply.

Each ACP session opens Crush in the directory the editor sends in `session/new`. The bridge fails at
startup with a clear message if the server is unreachable.

#### 3. Share the session with the Crush TUI

Set `CRUSH_CLIENT_SERVER=1` on every Crush client that connects to the server. The ACP session ID is
the Crush session ID.

```sh
CRUSH_CLIENT_SERVER=1 crush --host http://127.0.0.1:8484 --cwd /path/to/project --session <session-id>
```

Without `CRUSH_CLIENT_SERVER=1`, Crush 0.102.0 ignores `--host` and runs its own agent against the same
database. Sessions then appear in history, but turns are not shared live: the TUI and the editor do not
see each other's output until a restart. Clients also send their own environment to the server, so a
TUI that needs a provider must have the same provider variables set.

### Use with Paseo

[Paseo](https://paseo.sh) runs any ACP agent from `~/.paseo/config.json`, so no plugin is needed. Add a
provider that runs the bridge in managed mode, then run `paseo reload`:

```json
{
  "agents": {
    "providers": {
      "crush": {
        "extends": "acp",
        "label": "Crush",
        "command": ["/path/to/crush-acp-bridge", "--managed", "--crush-bin", "/path/to/crush"],
        "env": { "VERTEXAI_PROJECT": "my-project", "VERTEXAI_LOCATION": "global" }
      }
    }
  }
}
```

Paseo starts one bridge, and one Crush server, per agent, and stops both when the agent ends. Each agent
works in its own directory, including a git worktree. Permission requests appear in Paseo with the command
to be run.

### Try it without an editor

[acpx](https://github.com/openclaw/acpx) is a headless ACP client. With a server running and `LIVE_CWD`
set to a project directory:

```sh
just acpx "Run the shell command 'echo hello > hello.txt' and reply with one word"
```

## ACP support

Supported:

- `initialize`, `session/new` and `session/prompt`, with streamed text and reasoning.
- Tool calls with kind, title, input, output and file locations.
- Permission requests: allow once, allow for the session, and reject.
- `session/cancel`. The prompt returns the `cancelled` stop reason.
- User messages typed in other Crush clients, shown in the editor as they arrive.

Not supported:

- `session/load`, `session/list` and `session/resume`. Reopening a session starts blank.
- Mode and model switching.
- Editor file system and terminal delegation. The agent reads files from disk, so unsaved buffers are invisible.
- Diffs for file edits. Edit calls show a title and location only.
- Reconnecting after the event stream drops. Restart the bridge.
- Unix socket servers and Crush question prompts.
- Image and attachment prompts. Only text blocks are sent.

## Development

Run `mise install` for the pinned toolchain, then `just verify` before committing.
See `AGENTS.md` for conventions.

Tests replay SSE traces recorded from Crush 0.102.0 (`testdata/*.sse`) through the translator and the
bridge, with go-snaps snapshots of the output. `just live` drives the built binary against a running
Crush server and a real model; it needs `CRUSH_ACP_URL`, `LIVE_CWD` and a configured provider.

## Licence

Apache 2.0. See `LICENSE`.
