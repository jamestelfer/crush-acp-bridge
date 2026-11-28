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
- `TestSmoke` (`internal/live/smoke_test.go`) is an expandable end-to-end smoke suite in two parts. Bridge behaviour runs once: session lifecycle and probe clean-up, SSE drop resubscription, `--managed` cache clean-up and missing-credential errors, and `--shared` isolation. `TestSmoke/models` then runs the checks whose outcome depends on the model, once per model and in parallel:
  - replies: an exact multi-byte reply, 300 numbered lines streamed in order, a needle in a 40 KB prompt, multi-turn memory;
  - tools: an edit with its diff, several files in one turn, a shell command behind a permission prompt, recovery from a failed tool call, a 1,500-line tool output, tool output full of quoting and Unicode reaching the editor byte for byte, four reads issued at once;
  - permissions: a denial and the turn after it, "allow for this session" covering a second command;
  - prompts and modes: plan mode not editing, attachments with image recognition, a resource link;
  - questions: the chat fallback, a form, a declined form;
  - cancellation: mid-stream, while a permission request is open, and while a question form is open, each followed by a working turn;
  - sessions: two sessions prompted at once on one bridge without crossed output, `session/load` replaying a tool call and keeping history, and switching model mid-conversation to the second model in `SMOKE_MODELS` (the first model switches to the second; every other model switches to the first).

  Every check also fails when the bridge breaks one of ACP's rules: turn content or a permission request outside a prompt or load, an update for a tool call never announced, a tool call without a title, a finished tool call changing status, or a tool call still pending when a turn ends normally. It fails, too, when the text the editor was streamed for a session differs from the assistant text Crush stored for it, with whitespace collapsed; sessions with a cancelled or failed turn are not compared. Each check logs its update and message-chunk counts, thought chunks, tool calls, the most tool calls open at once and permission requests, so `go test -v` output can be compared across models. A check whose model ignored the prompt is skipped with the reason, not failed, so a failure points at the bridge, Crush or the provider: a model that sends its whole reply in one chunk skips the mid-stream cancel, and one that makes fewer tool calls than a check needs (no write for an edit check, no question tool call for a question check) skips that check. Reasoning markup that some models put in their reply text (`<reasoning>`, `<think>`) is ignored when the numbered reply is parsed. It uses `CRUSH_ACP_URL` when set, or starts and tears down its own temporary `crush server --debug` on a free loopback port; when a run fails, that server's logs are copied to `dist/smoke-logs/<time>/`.
- Crush relays messages, permission requests and question batches to its event stream on separate goroutines (`setupEvents` in Crush's `internal/app/app.go`), so a question batch can reach the bridge before the message that announces its tool call. The bridge then updates a tool call the editor has not been told about, which the unannounced-tool-call rule catches; across three 22-model runs on 2026-11-28 it caught this 6 times, for Claude Haiku 4.5, Gemini 4 Hydrogen and Neon, Kimi K2.5 and MiniMax M2.5, always on a question batch.
- `SMOKE_MODELS` picks the models, as `provider/model` IDs separated by commas or whitespace; the first also runs the bridge-level checks, and the first two are each other's partner in the model-switch check. Unset, `TestSmoke` tests the one model Crush selects for a new session, which is your own configured default. A model Crush does not offer, usually because its provider has no credentials on the machine, is skipped, and one that cannot finish a one-line turn fails once instead of in every check. Run a single model with, for example, `just live -run 'TestSmoke/models/bedrock:us.anthropic.claude-haiku-5-5'` (the `/` in a model ID becomes `:` in the subtest name).

Start a server for `TestLiveSession` and `TestLiveModelSwitch` with a long idle timeout:

```sh
CRUSH_SERVER_IDLE_TIMEOUT=86400 crush server --host http://127.0.0.1:8484
```

`TestHold` keeps a session open so a person can attach the Crush TUI to it. Set `LIVE_HOLD` to a directory: the test writes `session-id` and `editor.log` there, sends the contents of `prompt.txt` whenever it appears, and exits when a file named `stop` appears.

### Running TestSmoke with your own providers

`TestSmoke` works with whatever providers your Crush is configured for; nothing in it names a provider. The temporary server reads your global Crush config, and three settings, kept in `.envrc.private` (gitignored; start from `.envrc.private.example`), point it at your own details:

- the credentials your providers read from the environment, such as `VERTEXAI_PROJECT` and `VERTEXAI_LOCATION` or `AWS_PROFILE`;
- `SMOKE_MODELS`, the models to test;
- `SMOKE_CRUSH_CONFIG_DIR`, a directory (relative to the repository root, or absolute) whose `crush.json` and `crushrc` are copied into every test project. Use it to register models or providers your global config lacks without editing that config. `testdata/smoke/local/` is gitignored for this purpose; copy the examples from `testdata/smoke/` there and edit them.

Crush logs a warning about finding both a JSON config and a crushrc in a project, and merges them.

`testdata/smoke/crushrc.example` registers models missing from Crush's built-in catalogue: the Gemini 4 models on Vertex AI and the global Claude Opus 5.5 inference profile on Bedrock.

`testdata/smoke/crush.example.json` defines OpenAI-compatible Bedrock providers in `us-east-1`; change the region in each `base_url` to yours. Their `api_key` is `$BEDROCK_API_KEY`. To mint a key whenever the config loads instead, use a command, such as `"$(my-token-command --region us-east-1)"`. Crush runs `$(…)` in a `crush.json` value, with a 5-minute timeout and again after a 401, but stores a crushrc `--api-key` literally, which is why these providers are in JSON. A short-term Bedrock key works only in the region it was signed for. When the key comes back empty, Crush drops the provider and its models are skipped as not offered. A command that waits for interactive sign-in stalls every workspace open, so start a sign-in session before the run.

What the example providers taught on 2026-11-28:

- `bedrock-runtime-responses` (type `openai`, which Crush sends to the Responses API) serves the GPT-6.2 Astra and Sol, GPT-6.1 Terra and GPT-5.6 Luna inference profiles. On `bedrock-runtime`'s Chat Completions API these models reject function tools (`Function tools with reasoning_effort are not supported … use /v1/responses`), for Astra and Sol at every effort, so no Crush turn can succeed there.
- `bedrock-runtime-openai` (type `openai-compat`) serves the gpt-oss, Qwen, DeepSeek, Kimi, Devstral, GLM and MiniMax models through `bedrock-runtime`'s Chat Completions API; all seven answered. Qwen3 Coder Next has no vision, so the bridge refuses the image in `attachments_and_vision` and the check sends the note alone. DeepSeek V3.2 sends its whole reply in one chunk, and gpt-oss puts its reasoning in the reply text.
- `bedrock-mantle-openai` and `bedrock-mantle-v1` serve models through `bedrock-mantle`, under its `/openai/v1` and `/v1` prefixes; each model's card says which. Inference there needs `bedrock-mantle:CreateInference`, which the role used lacked, so these models were not run.
- Crush's built-in `bedrock` provider is the Anthropic client in Bedrock mode, so OpenAI models under it fail on every turn (`Invalid 'tool_choice'`).

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

A turn ends at the trace session's own finish, not a subagent's. A dispatched subagent works after the parent's turn has ended, and its report starts another parent turn. To record that, set `RECORD_SETTLE=1`: recording then continues until every session descended from the trace session is idle and no event has arrived for `RECORD_QUIET` seconds (default 10).

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
