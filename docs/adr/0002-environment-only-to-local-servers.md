# 2. Send the environment only to local servers

- **Status**: Accepted
- **Date**: 2026-11-24

## Context

Crush clients send their environment when they open a workspace (`POST /v1/workspaces`, field `env`). The server resolves provider credentials, such as `VERTEXAI_PROJECT` and `VERTEXAI_LOCATION`, from it.

With a shared server ([ADR 1](0001-join-crush-shared-server.md)), the server may have been started by another client. Its own process environment says nothing about the bridge's.

The first client to open a directory sets that directory's environment for every later client.

The environment holds secrets.

## Decision

The bridge sends `os.Environ()` on workspace open only when the server runs on the same machine: a `unix://` URL, or an HTTP host that is `localhost` or a loopback address (`crush.isLocal`). A server on another machine receives no environment.

A local server must also run as the current user. Before any request on a Unix socket, the bridge reads the peer's uid (`SO_PEERCRED` on Linux, `LOCAL_PEERCRED` on macOS and FreeBSD, the socket file's owner elsewhere). Before any request on a named pipe, it reads the pipe server's SID. It refuses a server run by another user. Crush's own client makes the same checks (Crush 0.102.0, `internal/client/peer_unix.go` and `dial_windows.go`). An HTTP URL is not verified, as in Crush.

## Consequences

- Provider variables set on the bridge, for example in Paseo's provider `env`, reach a shared server another client started.
- A remote server must have its credentials configured on its own host.
- Another user who binds the shared socket path first, for example in a shared temporary directory without `$XDG_RUNTIME_DIR`, receives no environment. The bridge fails with "owned by another user" (`TestClientRefusesAUnixSocketOwnedByAnotherUser`).
- A loopback HTTP server run by another user still receives the environment, including the `--managed` server's port if another user binds it first. On a multi-user machine, `--shared` over a socket is the verified path.
- The first client to open a directory wins. A later bridge with different variables does not change them until the server restarts.
