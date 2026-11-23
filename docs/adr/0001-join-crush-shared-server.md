# 1. Join Crush's per-user shared server

- **Status**: Accepted
- **Date**: 2026-11-24

## Context

Crush allows one server process per project data directory (`.crush/`). A second server opening the same project fails with `500 failed to connect to database: data directory already in use by another crush process`.

The first design ran one `crush server` per bridge (`--managed`). That broke a second Crush agent in the same Paseo workspace, Paseo's catalogue refresh while an agent was live there, and a Crush TUI in that directory.

Crush already has a per-user shared server. A TUI run with `CRUSH_CLIENT_SERVER=1` connects to it over a Unix socket.

## Decision

`--shared` joins Crush's per-user shared server, and starts it when none answers.

- The default socket path is the only rule copied from Crush 0.102.0 (`internal/server.DefaultHost`): `crush-<uid>.sock` in `$XDG_RUNTIME_DIR`, else in the temporary directory, else in `/tmp` when the path exceeds 104 bytes. `crushserver.DefaultSocket` implements it, a test pins it, and `--crush-socket` overrides it.
- A started server is detached: its own session, its context detached from the bridge, its working directory outside the project, and output appended to `crush-acp-bridge/shared-server.log` in the user cache directory. It outlives the bridge.
- There is no start-up lock. Bridges started together may each start a server. A server that finds a live one on its socket exits with "address already in use", and Crush's own `<socket>.lock` guards simultaneous binds and stale-socket removal. The bridge polls the socket, not its own child, so its server exiting is normal.
- The idle timeout stays at Crush's default. Each connected bridge holds its workspace open, and the server exits 60 seconds after the last client leaves.
- `--managed` remains as the self-contained fallback.

## Consequences

- Any number of bridges and Crush TUIs share one server, so several agents work in one directory.
- The bridge depends on Crush's socket rule. A change in Crush needs a matching change in `DefaultSocket`.
- A server's lifetime is no longer tied to a bridge. Its log lives in the user cache directory.
- Crush uses a named pipe on Windows, so `DefaultSocket` returns no path there and `--shared` fails with `ErrSharedUnsupported`. Windows is covered by `--managed` until named-pipe support lands separately.
- The bridge sends its environment to the shared server; see [ADR 2](0002-environment-only-to-local-servers.md).
