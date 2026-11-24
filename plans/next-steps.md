# Next steps

Progress tracker for the work after the spike. Update the checkboxes and the log as each slice lands.

## Rules

- Work test-first in vertical slices.
- Each slice ends with `just verify` passing and at least one commit.
- Push only when asked.
- Verify behaviour against a real Crush server (`just live`, `just acpx`, or Paseo) before ticking a live criterion.

## Slices

### 0. Housekeeping

- [x] The five spike commits are pushed.

### 1. Paseo shows models and modes

Paseo shows model `-` today. The bridge advertises Crush's models.

- [x] A bridge test asserts that `session/new` returns the model list and the current model.
- [x] Selecting a model from the editor changes Crush's model, covered by a test.
- [x] Live: `TestLiveModelSwitch` switches the model and Crush answers with it.
- [x] Live: `paseo provider models crush` lists the Vertex models, with the current one as default.
- [x] Paseo's agent view shows the real model name instead of `-` (live): agent runtime reports `vertexai/gemini-3.1-pro-preview`.
- [x] `just verify` passes.
- [x] Modes: Crush's `coder`/`plan` agents as ACP modes (ADR 0003; `TestReconnectRestoresTheModeOnTheServer`, `TestModeChangeDuringATurnFailsAndKeepsTheMode`). The bridge tracks the mode it set, since Crush cannot report it. Live: plan mode refused a file write; coder mode wrote it after one permission. Paseo's diagnostic reports `modes=2`.

Notes:

- Crush keeps one model per directory. Switching writes `.crush/crush.json` in the project, as `crush run --model` does, and affects every client there.
- Paseo caches the provider catalogue. `paseo provider diagnostic crush` re-probes it.

### 2. Shared Crush server, and reconnect (was slices 2 and 5)

Ordered ahead of slice 3: without it, a second Crush agent in a directory fails.

**Problem.** Crush allows one process per project data directory (`.crush/`). A second server opening the same project gets `500 failed to connect to database: data directory already in use by another crush process`. Each `--managed` bridge runs its own server, so this breaks a second Crush agent in the same Paseo workspace, Paseo's catalogue refresh while an agent is live there, and a Crush TUI in that directory.

**Design (chosen: join Crush's own shared server).**

- A new `--shared` mode connects to Crush's default per-user socket, the one a TUI with `CRUSH_CLIENT_SERVER=1` uses: `$XDG_RUNTIME_DIR` (else the temp dir) plus `crush-<uid>.sock`, falling back to `/tmp` past 104 bytes. This rule is the only part copied from Crush; `--crush-socket` overrides it and a test pins it.
- When nothing answers, the bridge starts a detached `crush server` (new session, output to a log file under the user cache directory, no `--host` for the default socket) and polls `/v1/health`.
- No start-up lock is needed: a server that finds a live one on its socket exits with "address already in use", and Crush's own `<socket>.lock` guards simultaneous binds and stale-socket removal. The bridge treats its own server exiting as normal while it keeps polling.
- The idle timeout is left at Crush's default. Each connected bridge holds its workspace open; the server exits 60s after the last client leaves.
- The bridge sends its environment when it opens a workspace on a local server (Unix socket or loopback host), as Crush clients do, so provider credentials reach a server another client started. The first client to open a path sets its environment.
- `--managed` stays as the fallback. On Windows `--shared` uses Crush's named pipe through `go-winio` (see the notes).
- Reconnect: when the event stream ends, the bridge ensures the server again, reopens its workspaces, reports current sessions and resubscribes. A turn in flight ends with an error.

**Limits.** Only servers take the data-directory lock (`db.WithDataDirLock`). A plain `crush` without `CRUSH_CLIENT_SERVER=1` runs in-process alongside the shared server without conflict, writing the same SQLite file, but sees no live events from it.

**Aside.** Each `--managed` run leaves `~/.cache/crush/server-tcp___127.0.0.1_<port>/` behind, one per random port. `--shared` uses one fixed directory.

**Acceptance.**

- [x] The client talks HTTP over a Unix socket (`unix://` URL), covered by a test.
- [x] Opening a workspace sends the bridge's environment, covered by a test.
- [x] A test pins the default socket path rule.
- [x] `--shared` connects to a running server, or starts a detached one that outlives the bridge, covered by tests using the fake Crush.
- [x] Two bridges started together in `--shared` mode end up on one server, covered by a test.
- [x] Live: two Crush agents in one directory run turns concurrently (two `--shared` bridges in `/tmp/crushlive/proj`; each got its own reply; both started a server and one survived).
- [x] Live: a Crush client with `CRUSH_CLIENT_SERVER=1` joins the bridges' server (`crush run` held Unix-socket connections and opened no `crush.db` of its own). The TUI uses the same connection path.
- [x] A test drops the event stream mid-turn; the bridge reconnects, the turn ends with an error, and the next prompt works. A prompt sent during the reconnect waits for it, up to 30 seconds.
- [x] A test with the server gone for good ends the turn with an error and logs it. The bridge keeps retrying, with backoff capped at 5 seconds.
- [x] Live: kill the shared server mid-session; the next prompt works (turn failed with the reconnect message; new server and reconnect in about 2 seconds; next prompt answered).
- [x] The Paseo `crush` provider uses `--shared` (backup at `~/.paseo/config.json.bak-before-shared`). Live: two Crush agents in one Paseo workspace answered concurrently through one server.
- [x] `README.md` documents `--shared` and the limits.
- [x] `just verify` passes.

Notes:

- Paseo now requires an explicit mode (`coder` or `plan`) when it creates a Crush agent from another provider's agent.
- A permanently gone server makes the bridge retry every 5 seconds, logging a warning each time.
- Deferred: a dropped event stream to a server that is still running fails the turn, and reconnect registers a new client ID. Keeping the turn alive and re-subscribing with the existing client is deferred.
- Windows: `--shared` uses Crush's named pipe `//./pipe/crush-<sid>.sock` (Crush 0.102.0 `internal/server/server.go` `DefaultHost`), dialled as `npipe://` with go-winio v0.6.4 after the same pipe-owner check Crush's client makes. A detached server starts with `CREATE_NEW_PROCESS_GROUP | CREATE_NO_WINDOW`, breaking away from the job object where allowed, as Crush's `root_windows.go` does. go-winio creates a pipe's first instance only when the name is free, so the start-up race needs no lock. Cross-compiled and vetted; the shared tests run on Windows in CI against pipes of their own. Not verified live on Windows.

### 3. `session/list`, `session/load` and `session/resume`

- [x] A snapshot test asserts that loading a recorded session replays the same updates a live run produced (`TestLoadReplaysWhatTheLiveRunShowed`: equal after merging streamed chunks, plus the editor's own prompt).
- [x] `session/list` returns the workspace's Crush sessions, covered by a test.
- [x] Capabilities are advertised only for methods that work: `loadSession`, `sessionCapabilities.list` and `.resume`; not `close`.
- [x] Paseo resumes an archived agent and shows its history (live): an archived agent answered a follow-up from the earlier turn's context in the same Crush session; `paseo import` of a stored session showed its conversation.
- [x] `just verify` passes.

Notes:

- Paseo calls `session/load` when `loadSession` is advertised, and uses `session/resume` only otherwise.
- Every Paseo probe leaves an empty `ACP` session in Crush. The list hides sessions with no messages.
- `session/list` without a `cwd` covers only directories the bridge already has open, so it opens no new Crush workspace.

### 4. Diffs for edit tools

- [x] Snapshot tests cover an edit and a write trace, with ACP diff content (path, old text, new text). Traces `edit-permission.sse` (edit, multiedit) and `write-permission.sse` (new file asked, overwrite not asked).
- [x] Permission requests for edits carry the diff, and a tool call update with it goes first: Paseo renders the tool call it holds, not the request's copy.
- [x] A live edit permission shows a diff in Paseo: the pending permission's detail was `edit` with the whole file before and after; the completed tool call kept it.
- [x] `just verify` passes.

Notes:

- Old and new text are the whole file, from Crush's permission parameters (`old_content`, `new_content`) or the edit and multiedit result metadata. Until then, edit and multiedit show each `old_string`/`new_string` fragment.
- A write's result metadata holds only a unified diff. Without a permission request, a new file gets a diff with no old text; an overwrite shows Crush's unified diff as text, which Paseo renders as a diff.
- Successful edit results drop Crush's confirmation text: Paseo reads an edit's text content as a unified diff.

### 5. Unsupported methods

- [x] Paseo logs show no unexpected method-not-found errors during a full session. A traced Paseo 0.17.3 session (create, permission allowed, mode and model switch, cancel, archive and send again, `paseo import`, `paseo provider diagnostic crush`) called `initialize`, `session/new`, `session/prompt`, `session/set_mode`, `session/set_config_option`, `session/load` and `session/cancel`. The bridge called `session/update` and `session/request_permission`. No error crossed the wire in either direction.

Notes:

- Paseo's ACP provider can also call `session/list`, `session/resume`, `session/close` and `session/set_model`. Each is gated: `session/list` on `sessionCapabilities.list` (implemented), `session/resume` only without `loadSession`, `session/close` on `sessionCapabilities.close` (not advertised), and `session/set_model` only when `session/new` returns the unstable `models` field, which the bridge leaves out in favour of the model config option. No method-not-found is reachable from Paseo.
- `session/close` stays unimplemented. Paseo would call it after every probe and import scan, and the bridge holds nothing a closed process does not release.
- The trace came from a wrapper that teed the bridge's stdio; Paseo keeps an agent's stderr only for diagnostics.

### Deferred

- A Paseo plugin, unless a need appears.
- Clearer failures when provider credentials are missing.

## Log

- 2026-11-24: Spike commits pushed. Plan written. Slice 1 started.
- 2026-11-24: Slice 1 model selector done (`TestSelectingAModelSwitchesCrush`), verified with `TestLiveModelSwitch` and a Paseo agent.
- 2026-11-24: Slice 1 modes done (ADR 0003). Slice 1 complete. Found the data-directory lock behind slice 5.
- 2026-11-24: Chose to join Crush's shared server. Merged reconnect and isolation into slice 2, ahead of slice 3. Slice 2 started.
- 2026-11-24: `--shared` done (ADR 0001; `TestBridgesStartingTogetherShareOneServer`), verified live with two concurrent bridges and a `CRUSH_CLIENT_SERVER=1` client. Next: reconnect, Paseo provider, README.
- 2026-11-24: Reconnect done (ADR 0008; `TestLostEventStreamFailsTheTurnAndTheBridgeReconnects`), Paseo switched to `--shared`, README updated. Slice 2 complete. Next: slice 3.
- 2026-11-24: Slice 3 done (ADR 0006; `TestLoadReplaysWhatTheLiveRunShowed`), verified live through Paseo resume, `paseo import` and a direct `session/list`. Next: slice 4.
- 2026-11-24: Review fixes applied (ADR 0002; `TestReconnectRestoresTheModeOnTheServer`, `TestConcurrentSessionsInANewDirectoryOpenItOnce`).
- 2026-11-24: Slice 4 done (ADR 0007; `TestEditPermissionCarriesTheWholeFileDiff`), verified live with a Paseo Crush agent: the pending edit permission carried the whole-file diff. Next: slice 5.
- 2026-11-24: Slice 5 done (`docs/paseo.md`), verified with a traced Paseo session: no method-not-found in either direction, and every unimplemented method Paseo can call is gated on a capability or field the bridge does not send.
- 2026-11-24: Windows named pipe support for `--shared` (ADR 0001; `TestDefaultPipeFollowsCrushsRule`), cross-compiled; not verified on Windows.
