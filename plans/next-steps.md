# Next steps

Progress tracker for the work after the spike. Update the checkboxes and the log as each slice lands.

## Rules

- Work test-first in vertical slices.
- Each slice ends with `just verify` passing and at least one commit.
- Push only when asked.
- Verify behaviour against a real Crush server (`just live`, `just acpx`, or Paseo) before ticking a live criterion.

## Slices

### 0. Housekeeping

- [x] The five spike commits are pushed (`0c4f40e`).

### 1. Paseo shows models and modes

Paseo shows model `-` today. The bridge advertises Crush's models.

- [x] A bridge test asserts that `session/new` returns the model list and the current model.
- [x] Selecting a model from the editor changes Crush's model, covered by a test.
- [x] Live: `TestLiveModelSwitch` switches the model and Crush answers with it.
- [x] Live: `paseo provider models crush` lists the Vertex models, with the current one as default.
- [x] Paseo's agent view shows the real model name instead of `-` (live): agent runtime reports `vertexai/gemini-3.1-pro-preview`.
- [x] `just verify` passes.
- [x] Modes: Crush's `coder`/`plan` agents as ACP modes (`1a27139`). The bridge tracks the mode it set, since Crush cannot report it. Live: plan mode refused a file write; coder mode wrote it after one permission. Paseo's diagnostic reports `modes=2`.

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
- The bridge sends its environment when it opens a workspace, as Crush clients do, so provider credentials reach a server another client started. The first client to open a path sets its environment.
- `--managed` stays as the fallback, and is the only self-contained mode on Windows until named pipes are supported (`go-winio`).
- Reconnect: when the event stream ends, the bridge ensures the server again, reopens its workspaces, reports current sessions and resubscribes. A turn in flight ends with an error.

**Limits.** A plain `crush` TUI without `CRUSH_CLIENT_SERVER=1` runs in-process and still locks the project.

**Acceptance.**

- [ ] The client talks HTTP over a Unix socket (`unix://` URL), covered by a test.
- [ ] Opening a workspace sends the bridge's environment, covered by a test.
- [ ] A test pins the default socket path rule.
- [ ] `--shared` connects to a running server, or starts a detached one that outlives the bridge, covered by tests using the fake Crush.
- [ ] Two bridges started together in `--shared` mode end up on one server, covered by a test.
- [ ] Live: two Crush agents in one directory run turns concurrently.
- [ ] Live: a TUI with `CRUSH_CLIENT_SERVER=1` joins the bridges' server.
- [ ] A test drops the event stream mid-turn; the bridge reconnects, the turn ends with an error, and the next prompt works.
- [ ] A test with the server gone for good ends the turn with an error and logs it.
- [ ] Live: kill the shared server mid-session; the next prompt works.
- [ ] The Paseo `crush` provider uses `--shared`.
- [ ] `README.md` documents `--shared` and the limits.
- [ ] `just verify` passes.

### 3. `session/list`, `session/load` and `session/resume`

- [ ] A snapshot test asserts that loading a recorded session replays the same updates a live run produced.
- [ ] `session/list` returns the workspace's Crush sessions, covered by a test.
- [ ] Capabilities are advertised only for methods that work.
- [ ] Paseo resumes an archived agent and shows its history (live).
- [ ] `just verify` passes.

Note: Paseo already marks Crush agents as supporting session persistence and listing; today those calls return method-not-found.

### 4. Diffs for edit tools

- [ ] Snapshot tests cover an edit and a write trace, with ACP diff content (path, old text, new text).
- [ ] Permission requests for edits carry the diff.
- [ ] A live edit permission shows a diff in Paseo.
- [ ] `just verify` passes.

### 6. Unsupported methods

- [ ] Paseo logs show no unexpected method-not-found errors during a full session.

### Deferred

- A Paseo plugin, unless a need appears.
- Clearer failures when provider credentials are missing.

## Log

- 2026-11-24: Spike commits pushed. Plan written. Slice 1 started.
- 2026-11-24: Slice 1 model selector done (`7cca523`), verified with `TestLiveModelSwitch` and a Paseo agent.
- 2026-11-24: Slice 1 modes done (`1a27139`). Slice 1 complete. Found the data-directory lock behind slice 5.
- 2026-11-24: Chose to join Crush's shared server. Merged reconnect and isolation into slice 2, ahead of slice 3. Slice 2 started.
