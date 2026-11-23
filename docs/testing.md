# Testing

`just verify` runs format, build, lint and the unit tests. Run it before every commit. Test conventions (Testify, go-snaps) are in `AGENTS.md`.

## Layers

| Layer | Where | Needs |
|---|---|---|
| Translator replay | `internal/translate` | Nothing |
| Bridge replay | `internal/bridge` | Nothing |
| Crush client | `internal/crush` | Nothing; tests use `httptest` servers |
| Server lifecycle and CLI | `internal/crushserver`, `cmd/cli` | Nothing; the fake Crush stands in for the binary |
| Live | `internal/live`, build tag `live` | A running `crush server`, a configured provider, a real model |
| Manual | `just acpx`, Paseo | As live |

## SSE trace replay

`testdata/*.sse` are event streams recorded from Crush 0.102.0 on Vertex AI:

| Trace | Covers |
|---|---|
| `bash-permission.sse` | A `bash` call that asks permission, then a reply |
| `long-text-cancel.sse` | A streamed reply, then a cancelled turn |
| `edit-permission.sse` | `edit` and `multiedit` calls that ask permission |
| `write-permission.sse` | A `write` that creates a file (asked) and one that overwrites it (not asked) |

Translator tests feed each trace's `data:` lines to a `Translator` and snapshot the updates. Bridge tests serve a trace from a fake `bridge.Backend`, drive the agent over an in-memory ACP connection, and snapshot what the editor received. A trace can be cut to its first turn (`firstTurn`) or split across prompts.

## Snapshots

Every golden test uses go-snaps. Snapshots live in `__snapshots__` next to the tests.

- Regenerate with `just snapshots`, then review the diff before committing.
- Never edit a snapshot by hand.
- `snaps.Clean` in each package's `TestMain` fails the run on obsolete snapshots.

## The fake Crush

`internal/fakecrush` serves the part of the Crush HTTP API that start-up and lifecycle code uses: health, workspace open, and a test-only `/_fake/env` that reports the server's environment.

Packages that start a `crush` child (`internal/crushserver`, `cmd/cli`) call `fakecrush.MaybeRunAsServer()` first in `TestMain`. Tests then spawn `os.Args[0]` as the `crush` executable with `CRUSH_FAKE_SERVER` set:

- `serve` (the default when set): listen on the `--host` address, or on Crush's default socket without one, and serve. Like Crush, it exits when the address is taken. `/_fake/pid` and `/_fake/exit` let tests identify and stop it.
- `exit`: fail at once.
- `silent`: run, but never listen.

No external binary is needed, so these tests run on every CI platform.

## Live tests

`just live` builds the binary and runs `go test -tags live ./internal/live/`. It needs:

- `CRUSH_ACP_URL`: the server's base URL.
- `LIVE_CWD`: an empty project directory the agent may write to.
- A provider configured in Crush, with its credentials in the environment.

Start a server for it with a long idle timeout:

```sh
CRUSH_SERVER_IDLE_TIMEOUT=86400 crush server --host http://127.0.0.1:8484
```

`TestHold` keeps a session open so a person can attach the Crush TUI to it. Set `LIVE_HOLD` to a directory: the test writes `session-id` and `editor.log` there, sends the contents of `prompt.txt` whenever it appears, and exits when a file named `stop` appears.

## acpx

`just acpx "<prompt>"` sends one prompt through the built bridge with [acpx](https://github.com/openclaw/acpx), approving every permission. It reads `CRUSH_ACP_URL` (default `http://127.0.0.1:8484`) and `LIVE_CWD`.

## Record a trace

`scripts/record-trace` records a new trace against a real server. It opens the project, creates a session titled `trace`, sends each prompt and waits for the turn to end, answers every permission with one action, and writes every event line to the output file.

```sh
export CRUSH_ACP_URL=http://127.0.0.1:8484 LIVE_CWD=/tmp/crushlive/proj
just record-trace edit-permission allow "Edit hello.txt to say goodbye"
```

The recipe writes `testdata/<name>.sse`. The action is `allow`, `allow_session` or `deny`. Pass several prompts for a multi-turn trace. The script also takes a `unix://` base URL; run it directly for that, or for an output outside `testdata/`:

```sh
scripts/record-trace unix:///run/user/1000/crush-1000.sock "$PWD" /tmp/out.sse allow "Say hi"
```

The script sends its environment to the server, as Crush clients do. Use a disposable project directory, and review a new trace for secrets and personal paths before committing it.

## Race detector

CI runs `go test -race ./...` on Linux. The race detector is not supported on the Raspberry Pi used for development, so run it in CI or on another machine.
