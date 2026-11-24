# 8. Fail the turn when the event stream drops

- **Status**: Accepted
- **Date**: 2026-11-24

## Context

The bridge learns that a turn ended only from Crush's event stream (`GET /workspaces/{id}/events`). The stream ends when the server exits, and also when the server drops a slow client.

After a drop, the bridge cannot tell whether a turn's end was lost. A prompt sent during the gap would reach a dead server.

## Decision

When a directory's event stream ends while the bridge runs:

- Every turn waiting in that directory fails with "lost the connection to crush during the turn; the bridge is reconnecting, so send the prompt again".
- The bridge reconnects: it ensures the server again (starting a shared one if needed), reopens the workspace, restores the mode ([ADR 3](0003-bridge-tracks-mode.md)), reports each session as current, and subscribes again. Each reconnect opens the workspace with a new client ID.
- Retries back off from 100 ms, doubling, capped at 5 seconds. A permanently gone server produces a warning every 5 seconds.
- A prompt sent while the directory reconnects waits up to 30 seconds, then fails with "crush is unreachable; the bridge is still reconnecting".
- The directory's translator spans reconnects, so content the editor already has is not sent again.

## Consequences

- A turn fails even when the server is still running and finishes the turn. The editor must send the prompt again.
- Each reconnect leaves a stale client registration on the server.
- This is expected to change: [#4](https://github.com/jamestelfer/crush-acp-bridge/issues/4) keeps the turn alive and re-subscribes with the existing client ID when the server is still up.
