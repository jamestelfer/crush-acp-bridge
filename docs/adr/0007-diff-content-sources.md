# 7. Diff content sources for edit tools

- **Status**: Accepted
- **Date**: 2026-11-24

## Context

ACP diff content carries a file path with its text before and after a change. Paseo and Zed render it as a diff. Crush's `edit`, `multiedit` and `write` tools describe their changes in four places, at different times:

| Source | When | Content |
|---|---|---|
| Tool input | As the call streams | `edit`: `old_string`/`new_string`. `multiedit`: an `edits` list of them. `write`: `content` only |
| Permission request parameters | When Crush asks | `old_content` and `new_content`, the whole file, for all three tools |
| `edit`/`multiedit` result metadata | When the call finishes | `old_content` and `new_content`, the whole file |
| `write` result metadata | When the call finishes | A unified `diff` only |

Crush omits empty content, so a missing `old_content` means a new or empty file.

Paseo has two behaviours that shape the choice. It renders a permission from the tool call it already holds, not from the request's copy. It reads an edit tool call's text content as a unified diff.

## Decision

The code is in `internal/bridge/diff.go`.

- Tool input: `edit` and `multiedit` send one diff entry per replaced fragment. `write` sends none, since the old content is unknown.
- Permission request: the whole-file diff goes first in a tool call update, then in the permission request.
- Result of `edit` and `multiedit`: the whole-file diff from the result metadata, else from the permission parameters.
- Result of `write`: the permission parameters when Crush asked. Otherwise a new file, detected from a unified diff whose only hunk starts at `-0,0`, gets a diff with no old text. An overwrite Crush did not ask about, for example after "allow for this session", sends Crush's unified diff as text.
- A successful edit result drops Crush's confirmation text and sends only the diff. A failed result keeps the text output.
- Relative paths resolve against the session directory. Absolute paths pass through in the server's form.

## Consequences

- Paseo shows the whole-file diff in a pending edit permission, and the completed tool call keeps it.
- An unprompted overwrite has no structured diff until Crush records the old content: [#11](https://github.com/jamestelfer/crush-acp-bridge/issues/11).
- Paseo shows only the first fragment of a `multiedit` until the whole-file diff arrives: [#14](https://github.com/jamestelfer/crush-acp-bridge/issues/14).
- Editors other than Paseo receive a confirmation-free result for successful edits.
