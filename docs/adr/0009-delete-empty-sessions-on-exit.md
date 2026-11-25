# 9. Delete empty sessions when the bridge exits

- **Status**: Accepted
- **Date**: 2026-11-24

## Context

Editors open ACP sessions they never prompt. Paseo opens one for each provider probe and catalogue refresh. `session/new` creates a Crush session at once, so each probe left an empty `ACP` session in Crush's database and the TUI's history ([#5](https://github.com/jamestelfer/crush-acp-bridge/issues/5)).

The ACP session ID is the Crush session ID. The README relies on it: a user opens the editor's session in the TUI with `crush --session <id>`.

Two fixes were open:

- **Lazy creation.** `session/new` returns a bridge-generated ID and creates the Crush session at the first prompt. The ACP and Crush IDs then differ, and the bridge would have to expose the mapping and persist it for `session/load`.
- **Clean-up.** The bridge deletes the empty sessions it created. Crush 0.102.0 serves `DELETE /v1/workspaces/{id}/sessions/{sid}`.

## Decision

The bridge cleans up. Before `Serve` returns, on editor disconnect or on a signal, it deletes each session that:

- this bridge created with `session/new`;
- the editor never prompted through this bridge; and
- Crush lists as idle (`is_busy` false) with a `message_count` of 0.

A session Crush does not list, or a directory whose list fails, is left alone. Clean-up has 5 seconds in total. A `session/new` that fails after Crush created the session deletes it at once, since the editor never learns its ID.

After a signal the editor is still connected while clean-up runs. Once clean-up starts, the bridge refuses new prompts and new sessions, so it never deletes a session a prompt reached. Clean-up first waits for any `session/new` already in flight, then deletes that session too if it is still empty.

A `--managed` server outlives the signal. Only the bridge stops it, after clean-up, so the deletes reach it. A Ctrl-C in a terminal also sends `SIGINT` to the child, which shares the terminal's process group; such a run can leave empty sessions behind. The child is not moved to its own process group: a supervisor that kills the bridge's group would then orphan it.

The bridge does not sweep empty `ACP` sessions when it next opens a directory. Another bridge on the same shared server may have just created one and not yet prompted it.

## Consequences

- The ACP session ID stays the Crush session ID.
- A session prompted from any client is kept, as Crush counts its messages. A session the editor prompted is kept even before Crush counts the prompt.
- A bridge killed without a chance to run clean-up (`SIGKILL`, a crash, a lost machine) leaves its empty sessions behind. `session/list` still hides them ([ADR 6](0006-session-load-and-list.md)).
- An editor that loads or resumes an empty session through a later bridge gets invalid params, as Crush no longer holds it. It creates a new session instead.
