# 10. Re-subscribe when the event stream drops

- **Status**: Accepted
- **Date**: 2026-11-24
- **Supersedes**: [ADR 8](0008-turn-failure-on-event-stream-loss.md)

## Context

The bridge learns that a turn ended only from Crush's event stream (`GET /workspaces/{id}/events`). The stream ends when the server exits, and also when the server drops a client that fell behind. In the second case the server keeps running the turn.

[ADR 8](0008-turn-failure-on-event-stream-loss.md) failed every turn in flight on any drop and reopened the workspace with a new client ID. The editor had to resend a prompt the server was still running, and each drop left a stale client registration on the server.

Crush 0.102.0 accepts a new subscription from a client ID it has seen before, and answers `404` for a workspace it does not serve. Events sent while a client is unsubscribed are lost, including a turn's final message. A client that reports its current session again receives the permission requests still pending.

## Decision

When a directory's event stream ends while the bridge runs, the bridge waits briefly, pings the server, and subscribes again with the existing workspace and client ID.

- Turns in flight keep waiting.
- The bridge reports each session in the directory as current again.
- For each turn in flight, the bridge reads the session's stored messages from the last user message on and feeds them through the directory's translator. The editor receives only content it has not seen, and a turn that ended during the gap ends with Crush's stop reason. Earlier turns are skipped, so a resumed session's history is not replayed.
- The bridge tracks the permission requests the editor is answering, by ID. A request Crush re-delivers while the editor answers it is not asked again.
- A stream that ends again before delivering an event backs off before the next attempt: from 100 ms, doubling, capped at 5 seconds.

When the ping fails, or the subscription fails (for example `404` from a restarted server), the bridge falls back to the ADR 8 path:

- Every turn waiting in that directory fails with "lost the connection to crush during the turn; the bridge is reconnecting, so send the prompt again".
- The bridge reconnects: it ensures the server again (starting a shared one if needed), reopens the workspace with a new client ID, restores the mode ([ADR 3](0003-bridge-tracks-mode.md)), reports each session as current, and subscribes again.
- Retries back off from 100 ms, doubling, capped at 5 seconds. A permanently gone server produces a warning every 5 seconds.
- A prompt sent while the directory reconnects waits up to 30 seconds, then fails with "crush is unreachable; the bridge is still reconnecting".

The directory's translator spans re-subscribes and reconnects, so content the editor already has is not sent again.

## Consequences

- A dropped slow client no longer costs the editor its turn, and leaves no stale client on the server.
- Catch-up reads only the stored messages, so a tool call's progress or a thought that Crush has not yet stored arrives with the next snapshot, not at once.
- A server restart still fails the turn, as the server lost it.
- Tests: `internal/bridge/reconnect_test.go` covers the drop with the server up, a turn that ends during the gap, a resumed session's earlier turns, a re-delivered permission request, client ID reuse against `internal/fakecrush`, and the restart and gone-server fallbacks.
