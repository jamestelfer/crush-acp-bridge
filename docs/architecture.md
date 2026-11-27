# Architecture

The bridge is an ACP agent on stdio that relays each ACP session to a Crush session on a `crush server`. Crush runs the model and every tool; the bridge translates and relays.

```
editor ──ACP/stdio──▶ crush-acp-bridge ──HTTP + SSE──▶ crush server ──▶ model, tools, SQLite
```

Decisions behind this design are in [adr/](adr/README.md). The Crush API it relies on is in [crush-server.md](crush-server.md).

## Components

| Package | Role |
|---|---|
| `cmd/crush-acp-bridge` | The `crush-acp-bridge` binary. Parses flags with urfave/cli v3, picks the server (`--crush-url`, `--managed` or `--shared`), builds the `Opener`, and calls `bridge.Serve`. |
| `internal/bridge` | The ACP agent. Implements the ACP methods, tracks directories and sessions, forwards translated updates, relays permission requests, settles question batches through a strategy, and reconnects. |
| `internal/translate` | Converts Crush SSE events and stored messages into ACP-shaped `Update` values. No ACP or HTTP dependency. |
| `internal/crush` | HTTP client for the Crush server API, over TCP or a Unix socket. `*crush.Workspace` satisfies `bridge.Backend`. |
| `internal/crushserver` | Runs Crush: `Start` supervises a child server for `--managed`; `EnsureShared` finds or starts the detached shared server for `--shared`. |
| `internal/fakecrush` | A stand-in Crush server for tests, including a mode in which the test binary acts as the `crush` executable. |
| `internal/live` | Tests behind the `live` build tag that drive the built binary against a real server. |

`bridge.Backend` is the seam between the agent and Crush. Bridge tests replace it with a fake that replays recorded traces; see [testing.md](testing.md).

## Server selection

- `--crush-url` connects to a server the user runs.
- `--managed` starts `crush server --host http://<free port>` as a child, with `CRUSH_SERVER_IDLE_TIMEOUT` raised to a day unless already set, and stops it on exit. The child's output goes to the bridge's log, never to stdout.
- `--shared` runs `EnsureShared` at start-up and before every workspace open, so a reconnect replaces a server that has gone.

Every mode pings `/v1/health` at start-up and fails with a clear message when the server is unreachable.

## Directories and sessions

The bridge opens one Crush workspace per directory the editor names (or per `--cwd`, when set) and reuses it for every session there. Opening runs outside the agent lock, so a slow open blocks only callers for that directory. Concurrent callers share one open; a failed open is retried by the next caller.

Each workspace has its own client ID and event stream. Each ACP session ID is the Crush session ID. On `session/new`, `session/load` and `session/resume` the bridge reports the session as this client's current session, because Crush delivers permission requests only to the client viewing the session.

Crush keeps one model and one main agent per directory, so model and mode changes apply to the directory and the bridge notifies the directory's other sessions.

Before `Serve` returns, the bridge deletes the sessions it created and the editor never prompted, when Crush still lists them as idle with no messages. Editors such as Paseo open such sessions to probe the agent. See [ADR 9](adr/0009-delete-empty-sessions-on-exit.md).

## Event flow

1. `Workspace.Events` opens `GET /v1/workspaces/{id}/events` and sends each SSE `data:` line on a channel.
2. One `follow` goroutine per directory reads the channel and feeds each event to the directory's `translate.Translator`.
3. The translator reads the envelope `{type, payload: {payload}}` and handles `message`, `permission_request`, `question_batch_request` (a `QuestionAsked` update, with `confirm_title` and `confirm_description`) and `question_batch_notification` (a `QuestionResolved` update). Crush publishes whole-message snapshots, so the translator remembers what it emitted per message and returns only new text, new reasoning, newly seen tool calls, tool input once complete, and tool results.
4. A turn ends on an assistant `finish` part whose reason ends the turn: `end_turn`, `canceled` (ACP `cancelled`), `max_tokens`, or `error`, `content_filter` and `unknown` (ACP `error`). `run_complete` is not used: on cancel it arrives before the final message update.
5. `forward` turns each update into an ACP `session/update`: user, agent and thought chunks, tool call start and updates (kind, title, locations, raw input, diffs, output), permission requests and questions (see below).

The bridge drops the echo of the editor's own prompt and forwards user messages typed in other Crush clients.

A permission request runs in its own goroutine, so a slow answer does not stall the stream. For edit tools, a tool call update carrying the diff goes first; see [ADR 7](adr/0007-diff-content-sources.md). Allow once, allow for the session and reject map to Crush's `allow`, `allow_session` and `deny`; any other outcome, including an error, denies.

A question batch also runs in its own goroutine, after a tool call update that lists its questions. The goroutine calls a `questionHandler`, a strategy that `Initialize` selects: `elicitationQuestionHandler` when the editor advertises `elicitation.form`, else `chatFallbackQuestionHandler`. See [ADR 13](adr/0013-questions-via-elicitation-or-chat-fallback.md); it replaces [ADR 11](adr/0011-questions-as-permission-requests.md).

- **Form elicitation.** The whole batch is one `elicitation/create` (mode `form`), scoped to the session and the question tool call. Question `i` is field `q{i}`: `yes_no` and `single_choice` are a titled `oneOf` string, `multi_choice` a titled `anyOf` array, `free_text` a string; `single_choice` and `multi_choice` questions add an optional `q{i}_custom` string. A field's title is the question's `label` (the question text when the label is empty), with the question text as its description when the form has several questions and a label. No field is `required`: an omitted field means the user skipped that question. `accept` is mapped to `questions/answer` (choice IDs are checked against those offered; a single choice plus custom text sends the selection with the custom text in the `_question` note; a multi-choice sends both as `selected_ids` and `fill_in_text`). `decline` answers every question as skipped, so the turn continues. `cancel`, or an action the bridge does not know, calls `questions/cancel`, which ends the turn.
- **Chat fallback.** The batch is answered at once with one empty answer per question, each carrying a `_question` note that tells the model the client has no question form, not to call `question` again, and to ask in plain text or state its assumptions. It never calls `session/request_permission`, `elicitation/create` or `questions/cancel`.
- **Fallbacks inside the form strategy.** A batch whose text asks for a credential goes to the chat fallback without a form, since ACP forbids collecting secrets in form mode. The guard (`asksForCredentials`) flags any credential noun in a question, label, description or choice unless the sentence reads as design discussion; a miss would break the ACP rule, while a false positive only moves one batch to chat. An `elicitation/create` that fails with the context still live (for example `-32601`), or whose response the SDK cannot decode (a `null` field value fails decoding), also goes to the chat fallback.
- **Cancellation.** Each batch runs under a context that is cancelled on `session/cancel`, on `question_batch_notification` (Crush resolved it, for example in another client), when the question tool call finishes, and when the turn ends. Cancelling the context of a pending `elicitation/create` makes the SDK send `$/cancel_request` to the editor, and a cancelled batch never posts a late answer or cancel to Crush.
- **Not done: `session/request_permission` is not cancelled on `session/cancel`.** Issue #44 listed applying the same turn-scoped cancellation to permission requests as something to consider; it was left out because it needs the same registry for permissions and a decision on whether to still send Crush a denial. It is open work, not a decision.

A prompt waits on its session's `turn` channel for the end the translator reports. On `session/cancel`, the bridge asks Crush to cancel and waits up to 2 seconds for the cancelled finish, then answers `cancelled`.

## Reconnect

When a stream ends while the bridge runs and the server still serves the workspace, the bridge subscribes again as the same client. Turns in flight keep waiting; the bridge reports its sessions again and catches up on each turn from the stored messages. When the server is gone or has forgotten the workspace, the directory's turns fail and the bridge reconnects with backoff, restoring the mode and current sessions. Prompts sent after the stream ends wait until it is back. Details and limits are in [ADR 10](adr/0010-resubscribe-on-event-stream-loss.md).

## Locking and ownership

- `Agent.mu` guards the maps of directories, sessions, modes and pending reconnects, and each workspace's current backend. It is never held across network calls.
- Each workspace's `trMu` is held while an event is translated and forwarded, so the editor sees updates in order. `session/load` holds it while it replays, so the live stream waits.
- The directory's translator lives for the bridge's lifetime, across reconnects and loads.
- A session's `active` flag is set while a prompt waits, so turn ends from other clients do not complete a later prompt. The `turn` channel holds at most one pending end.
- The `follow` goroutine owns the event channel, re-subscribes, and replaces the backend on reconnect.
- `Agent.asking` holds the IDs of permission requests the editor is answering, and of question batches in progress, so a re-delivered request or batch is not asked twice. A batch stays recorded once its outcome reaches Crush, until Crush resolves it, its tool call finishes or its turn ends; one whose outcome did not reach Crush is dropped so a re-delivery is handled again. `Agent.batches` holds each active batch's cancel function, with a run number so a failed run cannot drop a later registration of the same batch ID.
- `Agent.questions` is set in `Initialize` and read by batch goroutines, both under `Agent.mu`.
