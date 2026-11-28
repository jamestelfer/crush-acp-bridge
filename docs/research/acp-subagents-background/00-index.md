# Research: subagents and background work over ACP

Status: research notes for a decision; nothing here is decided. Accessed 2026-11-28. Bridge state: acp-go-sdk v0.20.0 (schema 1.32.0), Crush v0.103.0.

## Documents

| File | Scope |
|---|---|
| [01-acp-spec-movement.md](01-acp-spec-movement.md) | RFDs, PRs, issues; schema timeline across 27 v1 tags; stability and versioning policy; v2 |
| [02-acp-sdk-movement.md](02-acp-sdk-movement.md) | acp-go-sdk, TypeScript and Rust SDK release history; unstable-API exposure; decoding of unknown variants |
| [03-background-shells-and-subagents-mapping.md](03-background-shells-and-subagents-mapping.md) | Crush's background shells and subagents in source; how claude-agent-acp, codex-acp, Gemini CLI, opencode and goose map theirs |
| [04-client-support.md](04-client-support.md) | Support matrix for Paseo, acpx, Zed, CodeCompanion, agent-shell and six others |
| [05-verification-log.md](05-verification-log.md) | Independent re-checks (V1–V15) of the claims below |
| [06-v2-go-sdks-and-clients.md](06-v2-go-sdks-and-clients.md) | Which Go SDKs implement ACP v2; which clients speak v2 |

Each document marks claims **Confirmed** / **Inferred** / **Unverified** and ends with its own contradictions and references. A claim below cites the document and, where re-checked, the verification row.

## Correction to the earlier answer in this session

The first answer said subagent support is "only in the unstable part of the spec". That was true of the schema bundled in acp-go-sdk v0.20.0 (1.32.0) and is **wrong for the spec today**. Subagent sessions became stable v1 in schema 1.34.0 on 2026-11-20 (PR #2939; V1, V3). The Go SDK's `**UNSTABLE**` doc comments are stale relative to the spec, not to the wire shape (V2).

## Findings

### 1. Spec movement

- **Subagents: stable in v1 since schema 1.34.0.** RFD `docs/rfds/subagents.mdx` is Completed (01 §2.1). The stable v1 wire shapes are identical to the 1.32.0 unstable shapes the bridge already compiles against, except `IdleStateUpdate.usage`, which remains unstable (V2).
- **Churn before stabilising was low once released.** One breaking reshape (`SessionMessageChunk` participant fields, 1.27.0) and one patch-rule change over 51 days (1.24.0 → 1.34.0). The larger reshapes happened only on the unreleased PR branch (01 §3.1).
- **Background work has no v1 standard.** The only proposal is `docs/rfds/v2/background-work.mdx`, in Preview and v2-only (V4). Issue #1847, "Tool calls that outlive their turn have no protocol expression", closed 2026-10-16 in favour of v2 (V4). The RFD says v1 clients "rely on adapter-specific extensions" (01 §2.2).
- **`session/status` is a Draft RFD** and reports only whether a session is in memory (`open`/`saved`), not work state (01 §2.3, 02). No surveyed client calls it (04).
- **v2 is in Preview:** latest `schema-v2.0.0-beta.4` (2026-11-23) alongside v1 `schema-v1.35.0` (V5). The announcement says it "is not frozen" and that "v1-only Agents and Clients will be common for some time" (01 §4).
- **Policy for anything still unstable:** "may be removed or changed at any point" (schema text). `protocolVersion` is a major integer, and "The introduction of new capabilities is not considered a breaking change", so omitted capabilities MUST be treated as unsupported (01 §4).

### 2. SDK movement

- acp-go-sdk stays on v0 with no compatibility promise for anything (02 §2): "The SDK stays on v0. Raise the minor version for a schema bump or an API break".
- 8 of the last 9 Go releases broke unstable types, but 7 of those came in one 13-hour catch-up on 2026-11-18 (02 §1).
- Unreleased Go `main` (staged v0.21.0, schema 1.34.1) carries 38 incompatible API changes and stricter inbound validation, including answering any request before `initialize` with `-32600`. Go type names for subagents are unchanged there (02 §1, §6).
- **The Go SDK has no v2 support, even on `main`** (V15). The TS SDK (2.12.0) and Rust crate (5.6.1) are on schema 1.35.0 and ship v2 (02 §4).
- **Unknown `sessionUpdate` variants** are logged and dropped by the Go, TS and Rust SDKs; the connection stays up (02 §5; for TS 0.17.1, V13).

### 3. How background shells map to ACP

Crush 0.103.0 (03 §Crush; V7, V8):

- `bash` with `run_in_background`, or any command still running after 60 s, returns a shell ID. `job_output` (`shell_id`, `wait`) and `job_kill` read and stop the shell. Each is an ordinary, separate tool call.
- One process-wide manager (`sync.Once`), shell IDs `001`, `002`…, completed output kept for 8 h. **Nothing signals when a shell exits.**
- The bridge today maps `bash` to `kind: execute` and the job tools to `kind: other`. It doesn't consume `tool_progress`, so output arrives only with the tool result (03).

The ACP v1 options, with prior art (03 comparison table):

| Approach | Who does it | Fit for the bridge |
|---|---|---|
| Plain tool calls: `bash` completes with a shell ID; `job_output`/`job_kill` are their own tool calls | Gemini CLI, and the bridge today | Works with every client, needs no new surface, and matches Crush's own model. The client cannot see a "running" process between calls |
| Keep the `bash` tool call `in_progress` until the shell exits, streaming output through Zed's `_meta` terminal convention (`terminal_info`, `terminal_output[_delta]`, `terminal_exit`) with `content: [{type:"terminal"}]` | claude-agent-acp (opt-in by client `_meta`), codex-acp v1 | Needs exit detection, which Crush doesn't emit: the bridge would have to poll, and `job_output` is a model tool with no HTTP endpoint found (03, open question 5). It also needs updates outside the prompt turn |
| Client-hosted `terminal/*` | None of the surveyed agents | Ruled out by ADR 0012: Crush runs commands in-process |
| v2 `background` list, `paused`, `terminal/cancel`, `tool_call/cancel` | codex-acp on v2 | Not reachable: v2 is Preview and the Go SDK has no v2 (V15) |

### 4. How subagents map to ACP

Crush 0.103.0 in server (interactive) mode (03; V7):

- `agent_dispatch` returns immediately; `agent_send`, `agent_status` and `agent_stop` complete the set.
- A child is a Crush session with `parent_session_id`. Its finished report **starts a new parent turn with no user prompt**.
- Cancelling the parent leaves its children running.
- Every session's events share one SSE stream.

The bridge today drops every child-session event, **including permission requests**. A child that needs approval therefore waits indefinitely (Inferred, 03). This is a defect whatever the decision.

Spec v1 (V6, V10):

- With `clientCapabilities.subagents: {}`, the agent announces a child with `subagent_update`, reports its `state` (`running` / `requires_action` / `idle` / `unknown`), and may send parent updates and requests after `session/prompt` returns.
- Advertising the capability brings in session-directed messages and the "restricted methods" allowlist: a client cannot prompt or change a child unless the child's `capabilities` allow it.
- Without the capability, the agent must not send these.

Prior art (03):

- claude-agent-acp and codex-acp emit `subagent_update` only to clients that advertise the capability, and send plain tool calls otherwise (V9).
- claude-agent-acp holds `session/prompt` open until background subagents settle, for clients that stop listening at the response.
- opencode answers the prompt first and keeps streaming afterwards.

### 5. Client support (04; V11–V14)

| Client | `subagents` | SessionReference content | Unknown variant | Updates outside a turn | SDK |
|---|---|---|---|---|---|
| Paseo 0.17.5 (0.18.1 latest) | No | **Drops the whole tool call** (V13) | Logged, dropped | Yes (Inferred) | TS 0.17.1 at 0.17.5, 0.18.1 and `main` (V12) |
| acpx 0.22.0 (0.23.0 latest) | No | Item skipped | Logged, dropped | **No**: handlers cleared after each prompt | TS `^1.9.0` (V11) |
| Zed 1.30.2 | Only behind `acp-beta`, staff by default (V14) | Yes | Logged, dropped | Yes (Inferred) | Rust =5.3.0 |
| CodeCompanion v20.3.0 | Yes | Yes | Ignored | Partial | Hand-written |

No surveyed client calls `session/status`.

## Impact if the bridge exposes the "unstable" API

Separate the wire from the Go API:

1. **Wire.** The surface the bridge would emit (`subagent_update`, `state`, `SessionReference`, `session_message`) is now **stable v1**, unchanged from 1.32.0 (V2). Later v1 minors may only add to it; a breaking change needs a new `protocolVersion` (01 §4). The one field to avoid is `IdleStateUpdate.usage`, which is still unstable. The wire risk is therefore low, but only for the stabilised subset. `session/status` remains a Draft and could change.
2. **Go API.** acp-go-sdk promises nothing. Using these types adds more call sites for each upgrade to touch. The staged v0.21.0 already has 38 incompatible changes, though none rename the subagent types (02). The cost is code churn on upgrade, not protocol breakage.
3. **Clients, if gated on `clientCapabilities.subagents`.**
   - No effect today on Paseo or acpx (neither advertises it), or on Zed outside `acp-beta` (V14).
   - Only CodeCompanion and flagged Zed would see subagent cards.
   - If gating were wrong, Paseo would log and drop each `subagent_update` harmlessly (V13). A `session` content item would cost Paseo the whole tool call (V13), so references must be gated too.
4. **Obligations taken on by advertising.**
   - The capability carries normative rules (MUST report `unknown` when observation is lost, restricted methods, and so on; V6, V10). The bridge would have to enforce them against Crush's model.
   - Crush children outlive a parent cancel and wake the parent without a prompt. The bridge would need to send parent turns outside a prompt, and resolve its ACP turn ending on the first `finish` (03, open question 3).
   - This is the larger cost: semantics, not types.
5. **v2.** Background work exists only in v2, and v2 restructures state into per-session `state_update` (01 §2.2). A v1 subagent mapping stays valid in v1, but moving to v2 would be a separate mapping. It is blocked on the Go SDK, which has no v2 (V15).
6. **Process.** `AGENTS.md` requires an owner decision and an ADR before changing the advertised ACP surface. A new ADR would sit alongside ADR 0006 (which filters sub-agent sessions from `session/list`) and ADR 0012.

## Options for the decision (not ranked)

- **A. Status quo plus fixes.** Keep subagents and job tools as plain tool calls, and fix the dropped child permission requests, for example by projecting them onto the parent session the way opencode does. Works for every client, with no new surface.
- **B. A plus gated `subagent_update`.** Only when the client advertises `subagents`, on v0.20.0 types: announce children, report `state`, and allow out-of-turn parent updates. Benefits two clients today and takes on the obligations in item 4.
- **C. A plus Zed `_meta` terminal display for background `bash`.** Needs exit detection Crush doesn't provide, and only helps clients that recognise the `_meta` convention.
- **D. Wait for v2** before modelling background work. The Go SDK the bridge uses has no v2 and no known date. Three community Go SDKs have v2 code, but only caelis-labs/acp-go-sdk tracks the current beta.4. The only shipping v2 client is Zed behind `acp-beta` (06).

## Open questions

Each is carried from the source documents; the references are there.

- Turn collision: what the bridge should do when a report-driven Crush run overlaps a user's ACP prompt on one parent session. Needs a recorded trace (03).
- Whether Crush offers any HTTP endpoint for background-shell state (03, Unverified).
- Whether background-shell lookups filter by `owner` across sessions in a shared server (V8, partly confirmed).
- Paseo out-of-turn rendering and Zed unknown-variant handling are inferred from code, not observed (04).
- Zulip and GitHub Discussions were not searched for objections (01).
- `docs/paseo.md` records Paseo 0.17.3; 0.17.5 is installed and 0.18.1 released (04).
