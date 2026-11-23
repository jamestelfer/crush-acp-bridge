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

- [ ] A bridge test asserts that `session/new` returns the model list and the current model.
- [ ] Selecting a model from the editor changes Crush's model, covered by a test.
- [ ] Paseo's agent view shows the real model name instead of `-` (live).
- [ ] `just verify` passes.

### 2. Reconnect after a dropped event stream

The event stream ends when SSE drops, which hangs a live turn.

- [ ] A test drops the SSE connection mid-turn on the fake backend; the editor receives every chunk exactly once and one `end_turn`.
- [ ] A test with the backend gone for good ends the turn with an error and logs it.
- [ ] A live check kills and restarts the server mid-session.
- [ ] `just verify` passes.

### 3. `session/list`, `session/load` and `session/resume`

- [ ] A snapshot test asserts that loading a recorded session replays the same updates a live run produced.
- [ ] `session/list` returns the workspace's Crush sessions, covered by a test.
- [ ] Capabilities are advertised only for methods that work.
- [ ] Paseo resumes an archived agent and shows its history (live).
- [ ] `just verify` passes.

### 4. Diffs for edit tools

- [ ] Snapshot tests cover an edit and a write trace, with ACP diff content (path, old text, new text).
- [ ] Permission requests for edits carry the diff.
- [ ] A live edit permission shows a diff in Paseo.
- [ ] `just verify` passes.

### 5. Isolation per Paseo agent

Two agents in one directory share a Crush database file.

- [ ] Two bridges in the same cwd run concurrent turns without cross-talk, covered by a test with two managed fake servers.
- [ ] `README.md` states the behaviour.
- [ ] `just verify` passes.

### 6. Unsupported methods

- [ ] Paseo logs show no unexpected method-not-found errors during a full session.

### Deferred

- A Paseo plugin, unless a need appears.
- Clearer failures when provider credentials are missing.

## Log

- 2026-11-24: Spike commits pushed. Plan written. Slice 1 started.
