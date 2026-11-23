# 5. Do not advertise `session/close`

- **Status**: Accepted
- **Date**: 2026-11-24

## Context

ACP's `session/close` lets an agent release a session's resources. Paseo calls it when `sessionCapabilities.close` is advertised, after every probe and every import scan.

The bridge holds no per-session resource beyond an in-memory record. Crush keeps the session, and the workspace stays open while the bridge runs. The bridge process ends when the editor disconnects.

## Decision

The bridge does not advertise `sessionCapabilities.close`. `session/close` answers method-not-found.

## Consequences

- No extra round trip after each Paseo probe or import scan.
- Process exit releases everything the bridge holds.
- A client that calls `session/close` without checking the capability receives method-not-found.
