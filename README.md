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

### Recommended: share Crush's own server

`--shared` connects to Crush's per-user shared server, the one `crush` uses when run with
`CRUSH_CLIENT_SERVER=1`. If no server answers, the bridge starts one in the background.

```sh
crush-acp-bridge --shared
```

- The server outlives the bridge that started it. It exits on Crush's idle timeout, 60 seconds after its
  last client leaves.
- Any number of bridges, and Crush TUIs, use the one server. Several agents can work in the same directory.
- The server listens on a Unix socket: `crush-<uid>.sock` in `$XDG_RUNTIME_DIR`, or in the temporary
  directory. `--crush-socket` overrides it.
- On Windows the server listens on a named pipe instead: `\\.\pipe\crush-<sid>.sock`, where `<sid>` is the
  user's SID. `--crush-socket` takes a pipe name there, such as `\\.\pipe\my-crush`. The bridge refuses a pipe
  whose server runs as another user, as Crush does.
- A server the bridge starts logs to `crush-acp-bridge/shared-server.log` in the user cache directory.
- If the server goes away, the bridge reconnects and starts a new one. A turn in flight ends with an error;
  prompts sent while it reconnects wait for it.
- `crush` must be on `PATH`, or pass `--crush-bin`.
- Windows support is not verified live on Windows. It is cross-compiled, and its tests run against a fake
  Crush in the Windows CI job. `--managed` remains the fallback there.

Set provider credentials on the bridge. See [Provider credentials](#provider-credentials).

### Self-contained: one server per bridge

`--managed` starts `crush server` as a child process on a free local port, connects to it, and stops it
when the editor disconnects. The server's idle timeout does not apply.

```sh
crush-acp-bridge --managed
```

Crush allows one server per project. A second `--managed` bridge in the same directory fails to open
it: "data directory already in use by another crush process". Use `--shared` to run several agents in one
directory.

### Or: connect to a server you run

#### 1. Start a Crush server

```sh
crush server --host http://127.0.0.1:8484
```

The server needs a provider with credentials. See [Provider credentials](#provider-credentials).

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

`--crush-url` also accepts a Unix socket: `unix:///path/to/crush.sock`.

### Provider credentials

Crush reads provider credentials from environment variables, such as `ANTHROPIC_API_KEY`, or
`VERTEXAI_PROJECT` and `VERTEXAI_LOCATION` for Vertex AI. It resolves them when a client first opens a
directory, and drops every provider whose variables are missing. A directory with no usable provider
refuses every prompt. When another provider still has credentials, Crush replaces a model whose provider
it dropped with that provider's default model and saves the replacement as your preferred model. The
bridge cannot detect this case: check the model the editor shows after changing credentials.

The bridge sends its environment when it opens a directory on a local server, as Crush clients do: a Unix
socket, a named pipe, or a `--crush-url` whose host is `localhost` or a loopback address. A server on another
machine receives no environment. The bridge refuses a socket or pipe whose server runs as another user.

The first client to open a directory sets its environment for every later client. A later bridge with
different variables changes nothing until the server restarts.

| Mode | Where to set the credentials |
|---|---|
| `--shared` | The bridge's environment, such as Paseo's provider `env`. The bridge sends it to the shared server, whichever client started it. Every Crush TUI on the same server needs the same variables: if a TUI opens the directory first, its environment wins. To change them, stop every client of the server and wait for its idle timeout. |
| `--managed` | The bridge's environment. The child `crush server` inherits it, and the bridge also sends it on open. Restart the bridge to change them. |
| External server on this machine | The bridge's environment, as for `--shared`, or the shell that starts `crush server`. Restart the server to change them. |
| External server on another machine | The environment of the process that starts `crush server` on that machine. The bridge sends nothing. |

Credentials that are not environment variables live on the machine that runs the server: an `api_key` in
Crush's `crush.json`, Google Application Default Credentials for Vertex AI
(`gcloud auth application-default login`), AWS profiles for Bedrock, or a `crush login` sign-in.

When a prompt fails for want of credentials, the editor receives an error naming the provider of the current
model and the variables it reads. The error's `data` carries the same as `provider` and `variables`. A turn
that a provider rejects, for example for an invalid key, fails with Crush's account of the error. At
`session/new`, the bridge logs a warning when Crush cannot run the current model.

### Flags

| Flag | Environment | Description |
|---|---|---|
| `--shared` | | Use Crush's per-user shared server, starting it if needed. Conflicts with `--managed` and an explicit `--crush-url`. |
| `--crush-socket` | `CRUSH_ACP_CRUSH_SOCKET` | The `--shared` server's Unix socket, or named pipe on Windows. Defaults to Crush's own. Rejected without `--shared`. |
| `--managed` | | Run `crush server` as a child process. Conflicts with an explicit `--crush-url`. |
| `--crush-bin` | `CRUSH_ACP_CRUSH_BIN` | The `crush` executable for `--shared` and `--managed`. Defaults to `crush`. |
| `--listen` | | `host:port` for the `--managed` server. Defaults to a free local port. |
| `--crush-url` | `CRUSH_ACP_URL` | Base URL of the Crush server: `http://`, `unix://`, or `npipe://` on Windows. Defaults to `http://127.0.0.1:8484`. |
| `--cwd` | | Open this directory in Crush for every session, ignoring the directory the editor sends. |
| `--log-level` | `CRUSH_ACP_LOG_LEVEL` | `debug`, `info`, `warn` or `error`. Defaults to `info`. |
| `--log-format` | `CRUSH_ACP_LOG_FORMAT` | `text` or `json`. Defaults to `text`. |

Logs are structured ([`log/slog`](https://pkg.go.dev/log/slog)) and go to stderr. Stdout carries ACP
messages only. Records carry `session_id` and `cwd` where they apply.

Each ACP session opens Crush in the directory the editor sends in `session/new`. The bridge fails at
startup with a clear message if the server is unreachable.

### Share the session with the Crush TUI

Run Crush with `CRUSH_CLIENT_SERVER=1`. With `--shared`, no address is needed. Otherwise pass the server's
`--host`. The ACP session ID is the Crush session ID: `session/new` creates the Crush session at once and
returns its ID, and `session/load` and `session/resume` take any Crush session ID from that directory.

```sh
CRUSH_CLIENT_SERVER=1 crush --cwd /path/to/project --session <session-id>
```

When the bridge exits, it deletes the sessions it created that nobody prompted, so editor probes leave no
empty `ACP` sessions in Crush's history. A session that received a prompt from any client is kept. Clean-up
also runs on `SIGINT` and `SIGTERM`, and a `--managed` server stays up until it finishes. A bridge that is
killed outright (`SIGKILL`) cannot clean up, and its empty sessions remain. So can a `--managed` bridge
stopped with Ctrl-C in a terminal, which interrupts its `crush server` child too.

Without `CRUSH_CLIENT_SERVER=1`, Crush 0.102.0 ignores `--host` and runs its own agent against the same
database. Sessions then appear in history, but turns are not shared live: the TUI and the editor do not
see each other's output until a restart. Clients also send their own environment to the server, so a
TUI that needs a provider must have the same provider variables set.

### Models and modes

Each session offers Crush's models, from every enabled provider, and two modes: `coder` and `plan`. Plan
analyses and prepares plans without modifying files.

Crush keeps one model and one mode per directory, not per session. Changing either affects every Crush
client in that directory. A model change is written to the project's `.crush/crush.json`, as
`crush run --model` does. A mode change is refused while a turn runs.

### Use with Paseo

[Paseo](https://paseo.sh) runs any ACP agent from `~/.paseo/config.json`, so no plugin is needed. Add a
provider that runs the bridge in shared mode, then run `paseo reload`:

```json
{
  "agents": {
    "providers": {
      "crush": {
        "extends": "acp",
        "label": "Crush",
        "command": ["/path/to/crush-acp-bridge", "--shared", "--crush-bin", "/path/to/crush"],
        "env": { "VERTEXAI_PROJECT": "my-project", "VERTEXAI_LOCATION": "global" }
      }
    }
  }
}
```

Paseo starts one bridge per agent. The bridges share one Crush server, so several agents can work in
the same directory. Each agent works in its own directory, including a git worktree. Paseo lists Crush's
models and modes, and asks for a mode when it creates a Crush agent. Permission requests appear in Paseo
with the command to be run, or the diff of a file edit. Crush's questions appear as permission requests
with one option per answer.

Paseo caches each provider's model list. Run `paseo provider diagnostic crush` to refresh it after
changing Crush's providers.

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
- Diffs for Crush's `edit`, `multiedit` and `write` tools, in tool calls and in permission requests. The
  diff covers the whole file, from Crush's permission request or edit result. Until one arrives, an edit
  shows each replaced fragment. A write Crush does not ask about, such as one already allowed for the
  session, records only a unified diff, which is shown as text.
- Permission requests: allow once, allow for the session, and reject.
- Questions from Crush's `question` tool, asked one at a time as permission requests: Yes and No, or one
  option per choice. A free-text or multi-choice question uses a form when the editor offers form
  elicitation. Elsewhere a free-text question can only be skipped, and a multi-choice question takes one
  choice. Dismissing a question cancels the batch and ends Crush's turn. An editor that approves
  permission requests without asking answers each question with Yes or its first choice.
- `session/cancel`. The prompt returns the `cancelled` stop reason.
- Turn failures. A turn Crush ends with an error fails the prompt with Crush's account of it. A prompt that
  fails for want of provider credentials names the provider and its variables.
- Image and embedded-resource prompts, sent to Crush as attachments. Text resources are inlined into the
  prompt; an image is refused when Crush's current model cannot see it. Resource links appear in the prompt
  text for Crush to read itself. A prompt needs text alongside its attachments.
- User messages typed in other Crush clients, shown in the editor as they arrive.
- Model selection (`session/set_config_option`) and the `coder` and `plan` modes (`session/set_mode`).
- Reconnecting after the event stream drops.
- `session/load`, `session/resume` and `session/list`. Loading replays the stored conversation: user messages, reasoning, tool calls and replies. The list covers one directory's conversations, newest first, and leaves out sub-agent and empty sessions. Without a `cwd`, it covers only the directories the bridge already has open.

Not supported:

- Editor file system and terminal delegation. Crush reads files from disk, so unsaved buffers are invisible,
  and commands do not appear in the editor's terminals. Crush has no way to hand a tool to a client
  ([ADR 12](docs/adr/0012-no-editor-fs-terminal-delegation.md)).
- Audio prompts, and embedded binary resources other than images and text.
- `authenticate`, `logout`, `session/close`, `session/delete` and the unstable `session/set_model`. The bridge
  advertises none of them, and offers models as a config option instead.

## Development

Run `mise install` for the pinned toolchain, then `just verify` before committing.
See `AGENTS.md` for conventions, and `docs/` for design and reference:

- [Architecture](docs/architecture.md)
- [Architecture decision records](docs/adr/README.md)
- [Crush server reference](docs/crush-server.md)
- [Paseo](docs/paseo.md)
- [Testing](docs/testing.md)

Tests replay SSE traces recorded from Crush 0.102.0 (`testdata/*.sse`) through the translator and the
bridge, with go-snaps snapshots of the output. `just live` drives the built binary against a running
Crush server and a real model; it needs `CRUSH_ACP_URL`, `LIVE_CWD` and a configured provider.

## Licence

Apache 2.0. See `LICENSE`.
