# Testing

`just verify` runs format, build, lint and the unit tests. Run it before every commit. Test conventions (Testify, go-snaps) are in `AGENTS.md`.

## Layers

| Layer | Where | Needs |
|---|---|---|
| Translator replay | `internal/translate` | Nothing |
| Bridge replay | `internal/bridge` | Nothing |
| Crush client | `internal/crush` | Nothing; tests use `httptest` servers |
| Server lifecycle and CLI | `internal/crushserver`, `cmd/crush-acp-bridge` | Nothing; the fake Crush stands in for the binary |
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
| `http-bash-allow.sse` | A `bash` call allowed, recorded by `just record-trace` over `http://` |
| `unix-bash-deny.sse` | A denied `bash` call, then a second prompt, recorded over `unix://` |
| `question-batch.sse` | A `question` call asking a `single_choice`, a `yes_no` and a `free_text` question, recorded by `just record-trace` over `unix://` |

`TestEveryTraceReplays` replays every `testdata/*.sse` through the translator, so a new trace is checked as soon as it is added.

Translator tests feed each trace's `data:` lines to a `Translator` and snapshot the updates. Bridge tests serve a trace from a fake `bridge.Backend`, drive the agent over an in-memory ACP connection, and snapshot what the editor received. A trace can be cut to its first turn (`firstTurn`) or split across prompts.

## Snapshots

Every golden test uses go-snaps. Snapshots live in `__snapshots__` next to the tests.

- Regenerate with `just snapshots`, then review the diff before committing.
- Never edit a snapshot by hand.
- `snaps.Clean` in each package's `TestMain` fails the run on obsolete snapshots.

## The fake Crush

`internal/fakecrush` serves the part of the Crush HTTP API that start-up and lifecycle code uses: health, workspace open, and a test-only `/_fake/env` that reports the server's environment.

Packages that start a `crush` child (`internal/crushserver`, `cmd/crush-acp-bridge`) call `fakecrush.MaybeRunAsServer()` first in `TestMain`. Tests then spawn `os.Args[0]` as the `crush` executable with `CRUSH_FAKE_SERVER` set:

- `serve` (the default when set): listen on the `--host` address, or on Crush's default socket without one, and serve. Like Crush, it exits when the address is taken. `/_fake/pid` and `/_fake/exit` let tests identify and stop it.
- `exit`: fail at once.
- `silent`: run, but never listen.

No external binary is needed, so these tests run on every CI platform.

## Live tests

`just live` builds the binary and runs `go test -tags live ./internal/live/`. Extra `go test` arguments can be passed through, for example `just live -run TestSmoke`.

- `TestLiveSession` and `TestLiveModelSwitch` need `CRUSH_ACP_URL` (the server's base URL), `LIVE_CWD` (an empty project directory the agent may write to), and a provider configured in Crush with its credentials in the environment.
- `TestSmoke` (`internal/live/smoke_test.go`) is an expandable end-to-end smoke suite covering session lifecycle and probe clean-up, modes and edit diffs, attachments, questions (`questions_chat_fallback`, `questions_form_elicitation` and `questions_form_decline`), SSE drop resubscription, `--managed` cache clean-up and missing-credential errors, and `--shared` isolation. It uses `CRUSH_ACP_URL` when set, or starts and tears down its own temporary `crush server` on a free loopback port, using fresh temporary project directories for every subtest.

Start a server for `TestLiveSession` and `TestLiveModelSwitch` with a long idle timeout:

```sh
CRUSH_SERVER_IDLE_TIMEOUT=86400 crush server --host http://127.0.0.1:8484
```

`TestHold` keeps a session open so a person can attach the Crush TUI to it. Set `LIVE_HOLD` to a directory: the test writes `session-id` and `editor.log` there, sends the contents of `prompt.txt` whenever it appears, and exits when a file named `stop` appears.

## acpx

`just acpx "<prompt>"` sends one prompt through the built bridge with [acpx](https://github.com/openclaw/acpx), approving every permission. It reads `CRUSH_ACP_URL` (default `http://127.0.0.1:8484`) and `LIVE_CWD`.

## Record a trace

`scripts/record-trace` records a new trace against a real server. It opens the project, subscribes to its events, creates a session titled `trace`, sends each prompt and waits for the turn to end. It answers every permission with one action, answers every question batch (yes, the first choice, or the text `Recorded answer`), and writes every event line to the output file. Because it subscribes first, the trace opens with the session's `created` event, as the bridge sees it. `edit-permission.sse` and `write-permission.sse` predate this and start at `client_session`.

Start a server with a provider configured, then record:

```sh
CRUSH_SERVER_IDLE_TIMEOUT=86400 crush server --host http://127.0.0.1:8484
```

```sh
mkdir -p /tmp/crushtrace/edit
export CRUSH_ACP_URL=http://127.0.0.1:8484 LIVE_CWD=/tmp/crushtrace/edit
just record-trace edit-permission allow "Edit hello.txt to say goodbye"
```

The recipe writes `testdata/<name>.sse`. The action is `allow`, `allow_session` or `deny`. Pass several prompts for a multi-turn trace.

`CRUSH_ACP_URL` also takes a `unix://` URL:

```sh
CRUSH_SERVER_IDLE_TIMEOUT=86400 crush server --host unix:///tmp/crushtrace/crush.sock
```

```sh
mkdir -p /tmp/crushtrace/unix-bash-deny
CRUSH_ACP_URL=unix:///tmp/crushtrace/crush.sock LIVE_CWD=/tmp/crushtrace/unix-bash-deny \
  just record-trace unix-bash-deny deny "Run 'echo hello > out.txt'" "Now say bye."
```

Keep the socket path short. Unix socket paths are limited to about 100 bytes, and Crush fails with `bind: invalid argument` on a longer one, such as a path inside a deep temporary directory.

Run the script directly for an output outside `testdata/`:

```sh
scripts/record-trace unix:///tmp/crushtrace/crush.sock /tmp/crushtrace/proj /tmp/out.sse allow "Say hi"
```

The script sends its environment to the server, as Crush clients do, so run it from a shell that holds the provider credentials (for Vertex AI, `VERTEXAI_PROJECT` and `VERTEXAI_LOCATION`). The project path appears in permission requests and tool output, so use a short, disposable directory such as `/tmp/crushtrace/<name>`. Review a new trace for secrets and personal paths before committing it.

A denied permission ends the turn at once: Crush reports the tool call as failed and finishes with `end_turn`, without further assistant text. Send a second prompt to record what follows a denial.

Add a replay or snapshot test for each committed trace, then run `just snapshots` and review the diff.

## Race detector

CI runs `go test -race ./...` on Linux. The race detector is not supported on the Raspberry Pi used for development, so run it in CI or on another machine.
