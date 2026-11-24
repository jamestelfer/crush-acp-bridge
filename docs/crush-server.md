# Crush server reference

Facts about the Crush 0.102.0 `crush server` API and behaviour that the bridge relies on. They were established from Crush's source, recorded traces and live runs. Re-check them when moving to a new Crush version.

## Endpoints

All paths are under `/v1`. Workspace paths are under `/workspaces/{id}`, where `{id}` is the ID returned when the workspace was opened.

| Method and path | Body or query | Use |
|---|---|---|
| `GET /health` | | Liveness. The bridge pings it at start-up and polls it while a server starts. |
| `POST /workspaces` | `path`, `client_id`, `env` | Opens a project. Returns `id`. `client_id` must be a version 4 UUID. |
| `GET …/events` | `?client_id=` | The workspace's SSE stream. |
| `POST …/current-session` | `?client_id=`; `session_id` | Reports which session the client views. Permission requests reach only the client viewing the session. |
| `POST …/sessions` | `title` | Creates a session. Returns `id`. |
| `GET …/sessions` | | Lists sessions, including sub-agent sessions. |
| `GET …/sessions/{sid}/messages` | | A session's messages, in order, in the shape of the event stream's `message` payloads. |
| `POST …/agent` | `session_id`, `prompt` | Starts a turn. Returns at once; the turn runs on the server. |
| `POST …/agent/sessions/{sid}/cancel` | | Cancels the running turn. |
| `POST …/permissions/grant` | `action`, `permission` | Answers a permission request. `permission` is the request payload as received; `action` is `allow`, `allow_session` or `deny`. |
| `POST …/agent/main` | `agent_id` | Sets the main agent, `coder` or `plan`. |
| `GET …/config` | | The workspace's configuration: `models` (`large`, `small`) and `providers`, each with `name`, `disable` and `models`. |
| `POST …/config/model` | `scope`, `model_type`, `model` | Sets the `large` or `small` model. Scope `1` (`config.ScopeWorkspace`) writes `.crush/crush.json` in the project, as `crush run --model` does. |
| `GET …/agent/default-small-model` | `?provider_id=` | A provider's default small model. |
| `POST …/agent/update` | | Reloads the agent after a model change. |

## Status codes

- `409 Conflict`: the session is busy. Returned for a prompt while a turn runs, and for a main agent change during a turn. The client maps it to `crush.ErrBusy`.
- `404 Not Found`: no such workspace, session or resource. The client maps it to `crush.ErrNotFound`.
- `500` with `failed to connect to database: data directory already in use by another crush process`: another server holds the project's data directory.

## Events

Each SSE `data:` line is a JSON envelope:

```json
{"type": "message", "payload": {"type": "updated", "payload": { … }}}
```

The outer `type` names the stream; the inner `type` is `created` or `updated`; the inner `payload` is the object. Types seen in traces: `session`, `client_session`, `message`, `permission_request`, `permission_notification`, `agent_event`, `file` and `run_complete`. The bridge reads only `message` and `permission_request`.

- `message` payloads are whole-message snapshots, resent as the message grows. Each carries `id`, `session_id`, `role` and `parts`. Part types include `text`, `reasoning`, `tool_call`, `tool_result` and `finish`.
- An assistant `finish` part's `reason` is `end_turn`, `canceled`, `max_tokens`, `error`, `content_filter` or `unknown`, or a value for a tool round trip that does not end the turn.
- `run_complete` arrives before the final message update when a turn is cancelled, so it does not mark the end of a turn.
- `permission_request` payloads carry `id`, `session_id`, `tool_call_id`, `tool_name`, `description`, `action` and `params`. For `edit`, `multiedit` and `write`, `params` holds `file_path`, `old_content` and `new_content`.
- Tool result metadata of `edit` and `multiedit` holds `old_content` and `new_content`. A `write` result holds only a unified `diff`. Crush omits empty content, so a missing `old_content` means a new or empty file.
- A server that drops a slow client ends its stream.

## Sessions

`GET …/sessions` returns objects with `id`, `parent_session_id` (set for a sub-agent's session), `title`, `message_count`, `created_at`, `updated_at` and `is_busy`, among others. `created_at` and `updated_at` are Unix seconds; `0` means unset.

## Main agent and model

The server holds the main agent per workspace, in memory, starting on `coder`. No endpoint reads it back. The model is also per workspace, and persists through `.crush/crush.json` when set at workspace scope.

## Environment

Clients send their environment in `env` when opening a workspace. The server resolves provider credentials, such as `VERTEXAI_PROJECT` and `VERTEXAI_LOCATION`, from it. The first client to open a project sets its environment for every later client.

## Lifetime

- The server exits after `CRUSH_SERVER_IDLE_TIMEOUT` seconds (default 60) with no workspace held, counting from start-up if no client ever opens one.
- `CRUSH_SERVER_IDLE_TIMEOUT=0` shuts the server down as soon as it goes idle. It does not disable the shutdown.

## Shared server and data-directory lock

- Only servers take the data-directory lock (`db.WithDataDirLock`). One server per project data directory (`.crush/`) can run.
- The per-user shared server listens on `crush-<uid>.sock` in `$XDG_RUNTIME_DIR`, else the temporary directory, else `/tmp` when the path exceeds 104 bytes (`internal/server.DefaultHost`). On Windows it uses the named pipe `npipe:////./pipe/crush-<sid>.sock`, keyed by the user's SID, bound with an owner-and-SYSTEM-only security descriptor. Crush's client checks that the server runs as the current user before sending anything: the pipe server's SID on Windows, the socket peer's uid elsewhere (`internal/client/peer_unix.go`). The bridge makes the same checks.
- A server that finds a live one on its socket exits with "address already in use". Crush's `<socket>.lock` guards simultaneous binds and stale-socket removal.
- `crush` with `CRUSH_CLIENT_SERVER=1` connects to the shared server, or to the server named by `--host`.
- A plain `crush` without `CRUSH_CLIENT_SERVER=1` ignores `--host` and runs its own agent in-process. It takes no lock, so it runs alongside a server and writes the same SQLite file. It sees no live events from the server: the two do not see each other's turns until a restart.

## Cache directories

The server keeps a cache directory per host under `~/.cache/crush/`, named after the address, for example `server-tcp___127.0.0.1_<port>/`. `--managed` picks a random port each run, so each run leaves a new directory: [#10](https://github.com/jamestelfer/crush-acp-bridge/issues/10). `--shared` uses one fixed socket and one directory.
