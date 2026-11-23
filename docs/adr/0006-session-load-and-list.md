# 6. Session load, resume and list semantics

- **Status**: Accepted
- **Date**: 2026-11-24

## Context

Crush stores sessions and their messages (`GET /sessions`, `GET /sessions/{id}/messages`). Stored messages have the same shape as the event stream's `message` payloads. The translator ([architecture](../architecture.md)) emits only what it has not emitted for each message.

Paseo calls `session/load` when `loadSession` is advertised, and `session/resume` only otherwise.

## Decision

- The bridge advertises `loadSession`, `sessionCapabilities.list` and `sessionCapabilities.resume`.
- `session/load` and `session/resume` first check that Crush holds the session in that directory; otherwise they return invalid params.
- `session/load` replays stored messages through a fresh translator, so the editor receives the whole conversation even if this bridge showed it before. The directory's live translator is fed the same messages, so a turn still in progress sends only content after the replay. The live stream waits while the replay runs.
- `session/resume` attaches without replay.
- `session/list` returns one page, newest first, and leaves out sub-agent sessions (`parent_session_id` set) and sessions with no messages.
- `session/list` without `cwd` covers only directories the bridge already has open. It opens no new Crush workspace.

## Consequences

- Paseo resumes an archived agent and shows its history. `paseo import` shows a stored conversation.
- Empty sessions, such as those Paseo probes create, stay out of the list but remain in Crush: [#5](https://github.com/jamestelfer/crush-acp-bridge/issues/5).
- A list without `cwd` from a fresh bridge is empty.
