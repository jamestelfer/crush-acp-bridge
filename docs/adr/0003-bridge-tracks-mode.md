# 3. The bridge tracks the mode it set

- **Status**: Accepted
- **Date**: 2026-11-24

## Context

Crush has two main agents, `coder` and `plan`, offered as ACP modes. Crush 0.102.0 sets the agent with `POST /workspaces/{id}/agent/main` and offers no way to read it back. The server holds the choice in memory and starts every workspace on `coder`.

Crush keeps one model and one main agent per directory, not per session.

## Decision

- The bridge records the mode it set, per directory, and reports it in `session/new`, `session/load` and `session/resume`. A directory it has not changed reports `coder`.
- A mode change applies to the directory. Other ACP sessions in that directory receive `current_mode_update`.
- A model change, through the model config option ([ADR 4](0004-models-as-config-option.md)), applies to the directory and writes the project's `.crush/crush.json` (workspace scope), as `crush run --model` does. When the provider changes, the small model moves to the new provider's default. Other sessions receive `config_option_update`.
- After a reconnect, the bridge sets the recorded mode again, because a restarted server is back on `coder`.
- A mode change while a turn runs is refused: Crush answers 409, and the bridge returns "crush is busy; change mode when the turn ends".

## Consequences

- A mode change made by another client, such as the Crush TUI, is invisible to the bridge. Its sessions keep reporting the old mode.
- Two bridges in one directory each hold their own record of the mode.
- A model change persists in the project and affects every Crush client there.
- Reading the mode needs a Crush change: [#12](https://github.com/jamestelfer/crush-acp-bridge/issues/12).
