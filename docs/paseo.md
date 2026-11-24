# Paseo

[Paseo](https://paseo.sh) runs any ACP agent from `~/.paseo/config.json`, so the bridge needs no Paseo plugin. The README covers the provider entry. This page records how Paseo 0.17.3 uses the bridge, and how to trace the traffic between them.

## Provider configuration

The `crush` provider extends `acp` and runs the bridge with `--shared`. Paseo starts one bridge per agent; all of them share one Crush server, so several agents work in one directory. Set provider credentials in the provider's `env`; see [ADR 2](adr/0002-environment-only-to-local-servers.md).

Apply a config change with `paseo daemon reload` (or `paseo reload`).

## ACP methods

A traced session (create an agent, allow a permission, switch mode and model, cancel, archive and send again, `paseo import`, `paseo provider diagnostic crush`) produced this traffic, with no error in either direction:

- Paseo called `initialize`, `session/new`, `session/prompt`, `session/set_mode`, `session/set_config_option`, `session/load` and `session/cancel`.
- The bridge called `session/update` and `session/request_permission`.

Paseo's ACP provider can call four more methods, each gated:

| Method | Called when | Bridge |
|---|---|---|
| `session/list` | `sessionCapabilities.list` is advertised | Implemented |
| `session/resume` | `loadSession` is not advertised | Implemented, but Paseo prefers `session/load` |
| `session/close` | `sessionCapabilities.close` is advertised | Not advertised; see [ADR 5](adr/0005-session-close-not-advertised.md) |
| `session/set_model` | `session/new` returns the unstable `models` field | Never sent; see [ADR 4](adr/0004-models-as-config-option.md) |

No method-not-found is reachable from Paseo.

## Behaviour

- **Probe sessions.** Paseo creates a session for each provider probe and catalogue refresh, and never prompts it. The bridge deletes these empty sessions from Crush when the probe's bridge exits ([ADR 9](adr/0009-delete-empty-sessions-on-exit.md)).
- **Catalogue cache.** Paseo caches each provider's models and modes. `paseo provider diagnostic crush` re-probes the provider and refreshes the cache; its output reports the mode count (`modes=2`). `paseo provider models crush` lists the models with the current one as default.
- **Modes.** Paseo asks for a mode (`coder` or `plan`) when it creates a Crush agent, including from another provider's agent.
- **Resume and import.** Resuming an archived agent calls `session/load`. `paseo import` of a stored Crush session shows its conversation.

## Rendering quirks

The bridge shapes some updates for Paseo; [ADR 7](adr/0007-diff-content-sources.md) has the detail.

- Paseo renders a permission request from the tool call it already holds, not from the request's own copy. The bridge sends a tool call update with the diff before the request.
- Paseo reads an edit tool call's text content as a unified diff. The bridge drops Crush's confirmation text from successful edit results, and sends a write's unified diff as text when it has nothing better.
- Paseo shows only the first diff entry of a `multiedit` until the whole-file diff arrives: [#14](https://github.com/jamestelfer/crush-acp-bridge/issues/14).
- `paseo run --cwd` was seen twice starting in the caller's workspace instead of the given directory: [#13](https://github.com/jamestelfer/crush-acp-bridge/issues/13).

## Trace ACP traffic

Paseo keeps an agent's stderr only for diagnostics. To see the ACP messages, point the provider at a wrapper that copies the bridge's stdin and stdout to files.

1. Back up the Paseo config:

   ```sh
   cp ~/.paseo/config.json ~/.paseo/config.json.bak-before-trace
   ```

2. Write a wrapper, for example `~/bin/crush-acp-trace`, and make it executable:

   ```sh
   #!/usr/bin/env bash
   set -euo pipefail
   dir="${TMPDIR:-/tmp}/crush-acp-trace"
   mkdir -p "${dir}"
   stamp="$(date +%Y%m%dT%H%M%S)-$$"
   tee "${dir}/${stamp}.in.jsonl" \
     | /path/to/crush-acp-bridge "$@" 2>"${dir}/${stamp}.stderr" \
     | tee "${dir}/${stamp}.out.jsonl"
   ```

3. Replace the bridge path in the provider's `command` with the wrapper, keeping the arguments.
4. Run `paseo daemon reload`, then exercise the agent.
5. Read the traces. Each line is one JSON-RPC message: `.in.jsonl` holds what Paseo sent, `.out.jsonl` what the bridge sent. For example, list the methods each side called:

   ```sh
   jq -r 'select(.method) | .method' "${TMPDIR:-/tmp}"/crush-acp-trace/*.in.jsonl | sort | uniq -c
   jq -c 'select(.error)' "${TMPDIR:-/tmp}"/crush-acp-trace/*.jsonl
   ```

6. Restore the config and reload:

   ```sh
   mv ~/.paseo/config.json.bak-before-trace ~/.paseo/config.json
   paseo daemon reload
   ```

The traces hold prompts, file content and the bridge's log; delete them when done.
