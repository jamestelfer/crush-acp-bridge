# ACP client support for subagents and background work

Survey of which ACP clients can show subagents, session references, terminals and plans, how each one handles updates it does not recognise, and whether it accepts `session/update` after a prompt turn has ended. Every claim was read from source at a release tag or from the installed build on 2026-11-28, unless it is marked otherwise.

Marks: **Confirmed** means read in source at the cited tag or in the installed bundle. **Inferred** means it follows from the code path that was read, but nobody has seen it happen in a run. **Unverified** means it comes from secondary docs only, or the source is closed.

## Support matrix

Features: (1) advertises `clientCapabilities.subagents` and handles `subagent_update`; (2) renders a tool call's `SessionReference` (`{type:"session"}` content); (3) calls `session/status` or uses a child's `cancel` capability; (4) `terminal` capability and `terminal/*`, including `terminal` tool-call content; (5) `plan` updates; (6) what happens to an unknown `sessionUpdate`; (7) accepts `session/update` outside a `session/prompt` turn; (8) ACP SDK and version.

| Client (version) | 1 subagents | 2 SessionReference | 3 status / cancel | 4 terminal | 5 plan | 6 unknown variant | 7 out-of-turn | 8 SDK |
|---|---|---|---|---|---|---|---|---|
| Paseo 0.17.5 (installed; 0.18.1 latest) | No [P1] | No, and the whole tool call is dropped [P2] | No [P3] | Yes [P4] | Yes [P5] | Logged by the SDK, then dropped [P6] | Yes, Inferred [P7] | TS `@agentclientprotocol/sdk` 0.17.1 [P8] |
| acpx 0.22.0 (pinned; 0.23.0 latest) | No [A1] | No; dropped item by item [A2] | No [A3] | Yes; content shown as an id only [A4] | Yes [A5] | Logged by the SDK, then dropped [A6] | No [A7] | TS SDK 1.9.0 (`^1.9.0`) [A8] |
| Zed 1.30.2 | Partial: v1 agents need the `acp-beta` flag, which is staff-only [Z1] | Yes [Z2] | Child cancel Yes; `session/status` No [Z3] | Yes [Z4] | Yes [Z5] | Logged, then dropped, Inferred [Z6] | Yes, Inferred [Z7] | Rust `agent-client-protocol` =5.3.0, schema 1.18.0 [Z8] |
| CodeCompanion.nvim v20.3.0 | Yes [C1] | Yes [C2] | Child cancel Yes; `session/status` No [C3] | No [C4] | Yes [C5] | Ignored silently [C6] | Partial [C7] | None: hand-written Lua JSON-RPC [C8] |
| agent-shell v0.99.5 / acp.el v0.20.3 | No [E1] | No [E1] | No | No [E2] | Yes [E3] | Ignored; shown as "Unhandled" when ACP logging is on [E4] | Yes [E5] | None: Emacs Lisp `acp.el` |
| avante.nvim v0.5.0 | No | No | No | No [V1] | Yes [V2] | Unknown [V3] | Partial, Inferred [V3] | Rust crate =3.1.0, schema 1.10.2 [V1] |
| agentic.nvim (HEAD 4750d6ce42, no release tag) | No | No | No | No [G1] | Yes [G2] | Warns the user with a notification [G2] | Unknown | None: hand-written Lua |
| Obsidian Agent Client 0.13.2 | No | No | No | Yes [O1] | Yes [O2] | Ignored silently [O2] | Unknown | TS SDK 0.28.1 [O3] |
| Toad v0.6.20 | No | No | No | Yes [T1] | Yes [T2] | Ignored silently [T2] | Unknown | None: own Python JSON-RPC |
| marimo 0.26.0 | No | No | No | No [M1] | Unknown | Unknown | Unknown | `@zed-industries/agent-client-protocol` ^0.4.5 through `use-acp` 0.2.6 [M1] |
| JetBrains AI Assistant / Air / Junie | Unverified [J1] | Unknown | Unknown | Unverified [J1] | Unverified [J1] | Unknown | Unknown | Closed source |

## Per-client detail

### Paseo 0.17.5

The installed app is `/Applications/Paseo.app`, version 0.17.5 (`CFBundleShortVersionString`, and `paseo --version` prints `0.17.5`). The source is public at `github.com/getpaseo/paseo`; the latest release there is v0.18.1, published 2026-11-27. The ACP client lives in `packages/server/src/server/agent/providers/acp-agent.ts`. Where this section quotes the installed bundle, it means `/Applications/Paseo.app/Contents/Resources/app.asar`, read with `grep -a` (the compiled JS keeps the source comments). The repository's `docs/paseo.md` records version 0.17.3, which is now two patch releases old.

- **[P8] SDK. Confirmed.** In the installed asar, `/node_modules/@agentclientprotocol/sdk/package.json` has `"version": "0.17.1"`, and `@getpaseo/server` declares `"@agentclientprotocol/sdk": "^0.17.1"`. A second copy, version 1.4.0, sits under `@getpaseo/plugin/node_modules`; it serves the plugin API, not the ACP provider. At v0.18.1, `packages/server/package.json` still declares `^0.17.1` (https://github.com/getpaseo/paseo/blob/v0.18.1/packages/server/package.json).
- **[P1] Subagents. Confirmed No.** `subagent_update` appears nowhere in the 125 MB asar. The capabilities Paseo advertises are, from `acp-agent.ts` at v0.17.5 (line 308), and identical in the bundle:
  ```ts
  const BASE_ACP_CLIENT_CAPABILITIES = {
      fs: { readTextFile: false, writeTextFile: false },
      terminal: true,
  };
  ```
  The `zSessionUpdate` union in SDK 0.17.1 has 11 variants, from `user_message_chunk` to `usage_update`. It has no `subagent_update` and no catch-all.
- **[P6] Unknown variant. Confirmed.** The SDK validates before Paseo's code runs (asar, SDK `ClientSideConnection`):
  ```js
  case schema.CLIENT_METHODS.session_update: {
      const validatedParams = validate.zSessionNotification.parse(params);
      return client.sessionUpdate(validatedParams);
  }
  ```
  The resulting `ZodError` becomes `invalidParams`, and the SDK logs it without disconnecting:
  ```js
  if ("error" in response) {
      console.error("Error handling notification", message, response.error);
  }
  ```
  A variant that does pass validation but that Paseo does not handle reaches `default: return pendingUserEvents;` in `translateSessionUpdate` (acp-agent.ts v0.17.5, line 3493) and is ignored. Unknown fields are kept: SDK 0.17.1 builds every object with `z.looseObject`.
- **[P2] SessionReference. Confirmed No, with a hazard.** SDK 0.17.1 defines `zToolCallContent` as a union of `content`, `diff` and `terminal` only, and declares `content: z.array(zToolCallContent).optional()`. A `tool_call` or `tool_call_update` that carries one `{type:"session"}` item therefore fails validation as a whole and is dropped under the P6 path. The bridge must not put session content in tool calls sent to Paseo.
- **[P3] Confirmed No.** The two `"/session/status"` strings in the asar belong to the bundled opencode HTTP SDK (`url: "/session/status"`), not to ACP.
- **[P4] Terminal. Confirmed.** Paseo implements `createTerminal`, `terminalOutput`, `waitForTerminalExit`, `releaseTerminal` and `killTerminal` (acp-agent.ts v0.17.5, lines 3113–3205) by spawning a sibling process. It reads `terminal` content in tool calls through `extractTerminalContent`, which returns `{ output, exitCode }` from the terminals it holds (line 4375). Provider probe sessions reject terminals: `throw new Error("ACP model probe does not support terminal execution")`.
- **[P5] Plan. Confirmed.** `case "plan": return [...pendingUserEvents, this.wrapTimeline(mapPlanToTimeline(update))];`
- **[P7] Out of turn. Inferred Yes.** `sessionUpdate()` drops a notification only when it names a different session (`if (params.sessionId !== this.sessionId) return;`). It never checks for an active turn. `wrapTimeline` tags the event with `turnId: this.activeForegroundTurnId ?? undefined`. In the agent manager, `attachManagedTurnIdentity` returns `{ event, turnId: undefined }` for a timeline item outside a run, and `handleStreamEvent` still calls `dispatchStream` (`flags.shouldDispatchEvent` defaults to `true`). No Paseo run has been watched rendering such an update.

### acpx 0.22.0

The bridge's justfile pins `acpx_version := "0.22.0"` and runs it with `npx -y "acpx@{{acpx_version}}" … --approve-all exec`. The repository is `github.com/openclaw/acpx`, and npm `latest` is 0.23.0. The tarballs were downloaded from `registry.npmjs.org` and read in `dist/`.

- **[A8] Confirmed.** `package.json` declares `"@agentclientprotocol/sdk": "^1.9.0"`. 1.9.0 is the newest 1.x release (2026-10-16); 2.12.0 is npm `latest`. acpx imports the v1 root export: `import { PROTOCOL_VERSION, RequestError, client, methods } from "@agentclientprotocol/sdk";`.
- **[A1] Confirmed No.** `resolveClientCapabilities` (`queue-owner-runtime-*.js`, line 1545) advertises `fs`, `terminal` and an optional `elicitation`, and nothing else. `subagent` does not appear anywhere in `dist` for 0.22.0 or 0.23.0. SDK 1.9.0's v1 schema does parse `subagent_update`, but acpx's renderer drops it at `default: return;`.
- **[A6] Confirmed.** SDK 1.9.0's v1 `zSessionUpdate` is a closed `z.union` with no catch-all. An unknown variant fails `parseParams` in `registerAppNotification`, and `jsonrpc.js` line 788 logs `console.error("Error handling notification", …)` and keeps the connection open. Unknown fields are stripped, because the v1 objects are `z.object`.
- **[A2] Confirmed.** In SDK 1.9.0, `zToolCallContent` covers only `content`, `diff` and `terminal`, and the tool call declares `content: defaultOnError(vecSkipError(zToolCallContent)…)`. An unknown content item is skipped and the rest of the tool call survives. This differs from Paseo's SDK 0.17.1.
- **[A3] Confirmed No.** The ACP methods acpx sends are `initialize`, `authenticate` and `session/{new,load,resume,list,close,prompt,cancel,set_mode,set_config_option}`. The `"session/status"` string in `runtime.js` is one of acpx's own embedding controls (`capabilitiesFromRecord`), not an ACP call.
- **[A4] Confirmed.** acpx registers all five `terminal/*` handlers when `capabilities.terminal` is set, which is the default. Tool-call terminal content renders as `[terminal] ${entry.terminalId}`.
- **[A5] Confirmed.** `case "plan": this.renderPlanUpdate(update.entries);`.
- **[A7] Confirmed No.** Updates reach `this.eventHandlers.onSessionUpdate?.(notification)`, and after each prompt turn the code calls `client.clearEventHandlers()` (sets `this.eventHandlers = {}`). An update between turns is counted and then dropped. In `exec` mode the process exits once the update queue has drained after the prompt response.

### Zed 1.30.2

Latest stable is v1.30.2 (2026-11-25), with v1.31.0-pre the same day. Files were fetched at the tag with `gh api`.

- **[Z8] Confirmed.** Root `Cargo.toml`: `agent-client-protocol = { version = "=5.3.0", features = ["schemars", "unstable", "unstable_protocol_v2"] }`. crates.io lists `agent-client-protocol-schema =1.18.0` as its dependency.
- **[Z1] Confirmed, with a gate.** `crates/agent_servers/src/acp/v1.rs`:
  ```rust
  /// `acp_beta` advertises the unstable subagents and plan operations RFDs, which Zed only
  /// offers v1 agents behind the `acp-beta` flag.
  …
          .subagents(acp_beta.then(acp::SubagentCapabilities::new))
  ```
  `AcpBetaFeatureFlag` (`NAME = "acp-beta"`) uses the trait defaults `enabled_for_staff() -> true` and `enabled_for_all() -> false` (`crates/feature_flags/src/feature_flags.rs`). When the capability was not advertised, `handle_session_notification` does this:
  ```rust
  log::warn!(
      "Ignoring a subagents update for {session_id:?}: Zed did not advertise the subagents capability"
  );
  return;
  ```
  When it was advertised, `apply_v1_subagent_update` creates the child thread and applies its state. `acp.rs` line 247 reads "v2 includes subagents in every session". The UI maps states in `crates/agent_ui/src/conversation_view/subagent_card.rs`: `Running | RequiresAction => Running`; `Idle` maps by stop reason to Completed, Canceled or Failed; no state, or an unrecognised one, maps to `Unknown`.
- **[Z2] Confirmed.** `thread_view.rs`: `ToolCallContent::SessionReference(reference) => self.render_tool_call_session_reference(…)` renders an "Open Session" button, or the muted label "Unavailable Session" when Zed has no thread for that session ("The reference is display-only").
- **[Z3] Confirmed for cancel.** `v2.rs`: "The subagents RFD allows no Client-initiated mutation of a subagent session except the `cancel` its parent advertises, which `AcpThread::cancel` checks." `subagents.rs` holds `pub can_cancel: bool`. **Inferred No** for `session/status`: `gh search code` found no `SessionStatusRequest` or `session_status` in the repository, although that search covers the default branch, not the tag.
- **[Z4] Confirmed.** `.terminal(true)`, plus the `_meta` keys `terminal_output` and `terminal-auth`. `terminal_info`, `terminal_output` and `terminal_exit` are also read from tool-call `_meta`.
- **[Z5] Confirmed.** v2 `PlanUpdate` items and markdown are handled. An unknown plan type "still replaces the plan with that ID" (`session_updates.rs`).
- **[Z6] Inferred.** The v1 `SessionUpdate` is a serde `tag = "sessionUpdate"` enum with no `#[serde(other)]` variant, so an unknown tag fails to deserialise. The Rust SDK's `handle_handler_error` (`src/jsonrpc/incoming_actor.rs`, 5.3.0) is documented as "Handle a message-processing error without tearing down the connection … Notification errors are logged without replying."
- **[Z7] Inferred Yes.** `handle_session_notification` checks only that the session is known and the thread still exists, and `AcpThread::handle_session_update` does not check for a running turn.

### CodeCompanion.nvim v20.3.0

- **[C8]** Lua JSON-RPC in `lua/codecompanion/acp/init.lua`; no ACP SDK.
- **[C1] Confirmed.** The config default is `adapters.acp.opts.subagents = true`, and `Connection:_initialize` adds `clientCapabilities.subagents = vim.empty_dict()`. `acp/subagents.lua` handles `subagent_update` (the comment links `https://agentclientprotocol.com/rfds/subagents`), and the chat UI can "Watch or cancel the agent's subagents".
- **[C2] Confirmed.** `ui/tool_calls.lua`: `if content.type == "session" and chat.subagents[content.sessionId] then`, which opens the subagent float.
- **[C3] Confirmed.** `Subagents:can_cancel` checks `capabilities.cancel`. A comment in `subagents.lua` adds that "session/load replays history, so its capabilities and state aren't current and can't permit a cancel". `session/status` is not used.
- **[C4] Confirmed No.** The adapters advertise only `fs`, and the `DISPATCH` table has no `terminal/*` entry. A request with no handler gets no response: `local handler = DISPATCH[notification.method]; if handler then …`.
- **[C5][C6] Confirmed.** `PromptBuilder:handle_session_update` handles `plan`, and its `if … elseif` chain has no `else` branch, so an unknown variant is ignored silently.
- **[C7] Confirmed Partial.** Outside a turn, subagent updates, `available_commands_update`, `config_option_update`, `session_info_update` and `usage_update` are applied. Everything else reaches the user only through `elseif self._active_prompt then`, so a parent session's message or tool call between turns is dropped. Permission requests from a background subagent are queued: "A background subagent keeps working, and asking, after the prompt turn has ended".

### agent-shell v0.99.5 (Emacs) with acp.el v0.20.3

- **[E1][E2] Confirmed.** `acp.el` `initialize` sends `clientCapabilities` with `fs` and optional `elicitation`; it never sends `terminal` or `subagents`.
- **[E3] Confirmed.** `agent-shell.el` handles `"plan"` with `:block-id "plan"`.
- **[E4] Confirmed.** Unmatched notifications produce an "Unhandled" fragment only `(acp-logging-enabled …)`; otherwise they are ignored.
- **[E5] Confirmed.** `agent-shell--unfinished-tool-calls` documents: "A turn can end with tool calls still running (subagents carry on past it), and their tool_call_update notifications arrive out of turn." Fragments render `:above-last-prompt (not (shell-maker-busy))`.

### avante.nvim v0.5.0

- **[V1] Confirmed.** `crates/avante-acp` uses `agent-client-protocol =3.1.0` (schema 1.10.2, per `Cargo.lock`). Capabilities: `ClientCapabilities::new().fs(…)` only, with no terminal.
- **[V2] Confirmed.** `lua/avante/llm.lua` checks `update.sessionUpdate == "plan"`.
- **[V3]** Updates for the active session go to `on_session_update` without a turn check (`acp_client.lua`), so out-of-turn updates are delivered: Inferred Partial. Unknown variants depend on the 3.1.0 crate's serde handling and were not traced: Unknown.

### agentic.nvim (default branch, commit 4750d6ce42, 2026-11-08; the repository has no release tag)

- **[G1] Confirmed.** `acp_client.lua` line 102: `terminal = false`.
- **[G2] Confirmed.** `session_manager.lua` handles `plan`, and its `else` branch calls `Logger.notify("Unknown session update type: " …, vim.log.levels.WARN, { title = "⚠️ Unknown session update" })`, which shows a warning to the user for each unknown update.

### Obsidian Agent Client 0.13.2

- **[O3] Confirmed.** `"@agentclientprotocol/sdk": "^0.28.1"`; `package-lock.json` resolves 0.28.1.
- **[O1] Confirmed.** `clientCapabilities: { fs: { readTextFile: false, writeTextFile: false }, terminal: true }`, and `terminal-handler.ts` implements the terminal methods.
- **[O2] Confirmed.** `acp-handler.ts` `switch (update.sessionUpdate)` includes `case "plan"` and has no `default`. An unknown variant that the SDK lets through is ignored; how SDK 0.28.1 validates was not checked.

### Toad v0.6.20

- **[T1] Confirmed.** `"terminal": True`, with `terminal/create`, `kill`, `output`, `release` and `wait_for_exit` exposed (`src/toad/acp/agent.py`).
- **[T2] Confirmed.** `rpc_session_update` uses a `match update:` with a `{"sessionUpdate": "plan", …}` case and no top-level `case _`, so unknown updates are ignored.

### marimo 0.26.0

- **[M1] Confirmed.** `frontend/package.json` declares `@zed-industries/agent-client-protocol ^0.4.5` and `use-acp 0.2.6`. `agent-panel.tsx` advertises only `fs` read and write. The renderer was not read.

### JetBrains AI Assistant, Air and Junie

- **[J1] Unverified.** Closed source. The JetBrains ACP docs (https://www.jetbrains.com/help/ai-assistant/acp.html) describe `~/.jetbrains/acp.json` and say nothing about capabilities. A JetBrains blog post (https://blog.jetbrains.com/idea/2026/08/how-to-use-ai-agents-in-intellij-idea-with-acp/) lists "Plans, modes, slash commands, session loading, terminal operations" as negotiated features. A third-party agent's docs (https://docs.waibee.com/code/ide/) claim "JetBrains AIR shows each subagent as a separate session", which does not say whether AIR uses `subagent_update`.

## Contradictions and open questions

- **`docs/paseo.md` is out of date.** It records Paseo 0.17.3; 0.17.5 is installed and 0.18.1 is released. The SDK pin (`^0.17.1`) is the same at all three versions, so the findings here should hold for 0.18.1. That is Inferred: only `acp-agent.ts` was compared, and only for capabilities.
- **Paseo has two SDK copies.** The ACP provider uses 0.17.1, and the 1.4.0 copy belongs to the plugin package. Upgrading Paseo's provider SDK would change both the P6 path and the P2 path: 1.x skips bad tool-call content items instead of rejecting the whole tool call.
- **Zed subagents are flag-gated for v1 agents.** The bridge speaks ACP v1, so most Zed users would not receive subagent cards today; `acp-beta` is enabled for staff only. Whether Zed v2 connections, which "include subagents in every session", are reachable from a v1 Go agent is a question for the SDK and spec reports (01 and 02).
- **`session/status` exists only in schemas.** It is `unstable_session_status` in the Rust schema and in the TS SDK 2.x `v1/unstable` export. No client surveyed calls it.
- **Out-of-turn support is mostly inferred.** Only agent-shell documents out-of-turn tool-call updates. Paseo and Zed have no turn gate in their code paths, but no run has been watched. acpx drops such updates, and CodeCompanion drops them except for subagent and metadata updates.
- **Not checked:** how SDK 0.28.1 (Obsidian) and the Rust 3.1.0 crate (avante) handle unknown variants, and how marimo renders tool calls.

## References (all accessed 2026-11-28)

- Paseo installed bundle: `/Applications/Paseo.app/Contents/Resources/app.asar` (0.17.5); the asar file list was parsed from its header.
- Paseo source: https://github.com/getpaseo/paseo/blob/v0.17.5/packages/server/src/server/agent/providers/acp-agent.ts ; https://github.com/getpaseo/paseo/blob/v0.18.1/packages/server/package.json
- acpx: https://registry.npmjs.org/acpx/-/acpx-0.22.0.tgz and `acpx-0.23.0.tgz`; repository https://github.com/openclaw/acpx
- TS SDK: https://registry.npmjs.org/@agentclientprotocol/sdk/-/sdk-1.9.0.tgz (`dist/schema/zod.gen.js`, `dist/acp.js`, `dist/jsonrpc.js`, `dist/schema-deserialize.js`); `sdk-2.12.0.tgz`
- Zed: https://github.com/zed-industries/zed/blob/v1.30.2/crates/agent_servers/src/acp/v1.rs ; `…/acp/v2.rs`; `…/acp/session_updates.rs`; `…/crates/acp_thread/src/subagents.rs`; `…/crates/agent_ui/src/conversation_view/subagent_card.rs`; `…/thread_view.rs`; `…/crates/feature_flags/src/flags.rs`; `…/Cargo.toml`
- Rust crates: https://static.crates.io/crates/agent-client-protocol/agent-client-protocol-5.3.0.crate (`src/jsonrpc/handlers.rs`, `src/jsonrpc/incoming_actor.rs`); `agent-client-protocol-schema-1.18.0.crate` (`src/v1/client.rs`, `src/v1/tool_call.rs`)
- CodeCompanion: https://github.com/olimorris/codecompanion.nvim/tree/v20.3.0 (`lua/codecompanion/acp/init.lua`, `acp/subagents.lua`, `acp/prompt_builder.lua`, `interactions/chat/ui/tool_calls.lua`, `config.lua`)
- agent-shell: https://github.com/xenodium/agent-shell/tree/v0.99.5/agent-shell.el ; acp.el: https://github.com/xenodium/acp.el/tree/v0.20.3/acp.el
- avante.nvim: https://github.com/yetone/avante.nvim/tree/v0.5.0 (`crates/avante-acp/src/worker.rs`, `lua/avante/libs/acp_client.lua`, `Cargo.lock`)
- agentic.nvim: https://github.com/carlos-algms/agentic.nvim/tree/4750d6ce42
- Obsidian Agent Client: https://github.com/RAIT-09/obsidian-agent-client/tree/0.13.2
- Toad: https://github.com/batrachianai/toad/tree/v0.6.20/src/toad/acp/agent.py
- marimo: https://github.com/marimo-team/marimo/blob/0.26.0/frontend/package.json ; `…/frontend/src/components/chat/acp/agent-panel.tsx`
- JetBrains: https://www.jetbrains.com/help/ai-assistant/acp.html ; https://blog.jetbrains.com/idea/2026/08/how-to-use-ai-agents-in-intellij-idea-with-acp/ ; https://docs.waibee.com/code/ide/
