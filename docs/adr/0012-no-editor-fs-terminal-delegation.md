# 12. Do not delegate file system or terminal access to the editor

- **Status**: Accepted
- **Date**: 2026-11-24

## Context

An ACP editor advertises `fs.readTextFile`, `fs.writeTextFile` and `terminal` client capabilities at `initialize`. An agent that uses them reads the editor's buffers, unsaved edits included, writes through the editor, and runs commands in terminals the editor shows.

Crush 0.102.0 runs every tool inside the server process:

- `view`, `edit`, `multiedit` and `write` call `os.Open`, `os.ReadFile` and `os.WriteFile` directly (`internal/agent/tools/view.go`, `edit.go`, `write.go`). No file system interface sits between a tool and the disk.
- `bash` and the background job tools run commands in Crush's in-process shell (`internal/shell`).
- The HTTP API has no endpoint or event that hands a tool's read, write or command to a client. Crush's `main` branch adds none.

The nearest features do not fit:

- `POST …/host-context` takes the editor's open files and cursor. It tells the agent what the user sees, not what a buffer holds.
- `POST …/agent/sessions/{sid}/shell` runs a command in Crush on a client's behalf. It is the opposite direction.
- A `PostToolUse` hook can replace a tool's output. Hooks are shell commands in the user's Crush configuration, run by the shared server for every client. Using one would mean the bridge rewriting user configuration and serving a side channel that other clients' tools also hit.

## Decision

The bridge does not read `clientCapabilities.fs` or `clientCapabilities.terminal`, and never calls `fs/*` or `terminal/*`.

Delegation waits on Crush routing tool file access and commands to a client. The request to Crush is tracked with the `upstream:crush` label on issue #7.

## Consequences

- Crush reads files from disk. The agent does not see unsaved editor buffers. Save before prompting.
- Crush writes files to disk. The editor sees the change only when it reloads the file.
- Commands run inside the Crush server and do not appear in the editor's terminals. Their output reaches the editor as tool call content.
- When Crush gains client-side tool execution, a new record supersedes this one. The mapping is `fs/read_text_file` for `view`, `fs/write_text_file` for `write`, `edit` and `multiedit`, and `terminal/*` for `bash`, each gated on the capability the editor advertises.
