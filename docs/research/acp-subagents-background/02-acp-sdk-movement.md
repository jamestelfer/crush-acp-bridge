# ACP SDK movement: subagents, state and session status

Research note for an architecture decision on subagent support in `crush-acp-bridge`, which is pinned to `github.com/coder/acp-go-sdk` v0.20.0 (`go.mod`). Accessed 2026-11-28 unless stated otherwise. Every claim carries a status:

- **Confirmed**: read in a primary source (code at a tag, release record, lockfile).
- **Inferred**: follows from confirmed facts, not stated directly anywhere.
- **Unverified**: stated by a secondary source or not checked.

## Summary

1. **Confirmed.** acp-go-sdk v0.20.0 bundles ACP schema 1.32.0. In that schema the subagent surface (`subagent_update`, `session_message`, `session_message_chunk`, `StateUpdate`, `SubagentCapabilities`, `SessionCancelCapabilities`, `session` tool-call content) exists only in `schema.unstable.json`. Upstream made it stable in schema 1.34.0 on 2026-11-20, two days after v0.20.0 shipped.
2. **Confirmed.** The shapes did not change on the way to stable. With descriptions stripped, the JSON schema definitions of `SubagentUpdate`, `SessionMessage`, `SessionMessageChunk`, `SubagentCapabilities`, `SubagentSessionCapabilities`, `SessionCancelCapabilities`, `StateUpdate`, `SessionReference` and `ToolCallContent` in v0.20.0's `schema.unstable.json` are identical to those in `schema-v1.34.1` and `schema-v1.35.0` `schema.json`. Only `IdleStateUpdate` differs: its `usage` member stays unstable.
3. **Confirmed.** v0.20.0 has every type and wire method needed to emit subagent updates. It has no builder helpers and no example that uses them.
4. **Confirmed.** In all three SDKs (Go, TypeScript, Rust) a `session/update` whose `sessionUpdate` discriminator is unknown fails to decode. The SDK logs it and drops that one notification. The connection stays up and no error goes back to the agent.
5. **Confirmed.** Paseo's server package locks `@agentclientprotocol/sdk` 0.17.1 (schema 0.11.3), which cannot decode `subagent_update`. acpx 0.23.0 depends on `^1.9.0` (schema 1.26.0), and that version's default import does decode it.
6. **Confirmed.** Eight of the nine acp-go-sdk releases in the last six months broke unstable types. Seven of those nine were cut within about 13 hours on 2026-11-18.

## 1. acp-go-sdk release history

Source: `gh release list -R coder/acp-go-sdk` and `gh release view <tag>`. Tag commit dates come from `git for-each-ref` on a clone. The schema column is the version each release names in its notes.

| Release | Published (UTC) | Schema | Subagent / state / session-status / terminal changes | Breaks unstable API? |
|---|---|---|---|---|
| v0.4.3 | 2025-09-26 | 0.4.3 | Initial release; terminal methods incl. `KillTerminalCommand` | n/a |
| v0.4.4 | 2025-10-01 | 0.4.4 | none | no |
| v0.4.5 | 2025-10-10 | 0.4.5 | none (constant rename for `session/set_model`) | yes (rename) |
| v0.4.9 | 2025-10-13 | 0.4.9 | none | no |
| v0.6.3 | 2025-11-03 | 0.6.3 | none | yes |
| v0.10.8 | 2026-04-16 (tag commit 2026-02-27) | 0.10.8 | Unstable surface first generated (`Unstable*` prefix, `AgentExperimental`) | n/a |
| v0.11.7 | 2026-04-16 | 0.11.7 | `KillTerminalCommand` → `KillTerminal`; terminal auth (unstable) | yes |
| v0.12.0 | 2026-04-20 | (unchanged) | none | no |
| v0.12.2 | 2026-04-28 | 0.12.2 | `session/close`, `session/resume` stable | yes |
| v0.13.0 | 2026-05-13 | 0.13.0 | MCP-over-ACP (unstable) | yes |
| v0.13.4 | 2026-06-01 | 0.13.4 | unstable `session/delete`, plan updates | **yes** |
| v0.13.5 | 2026-06-02 | 0.13.5 | unstable `session/set_model` removed | **yes** |
| v0.14.0 | 2026-11-18 01:04 | 1.13.7 | SDK semver decoupled from schema | **yes** |
| v0.15.0 | 2026-11-18 01:40 | 1.20.0 | typed `TerminalId`; plan `id` → `planId` on the wire (unstable) | **yes** |
| v0.16.0 | 2026-11-18 02:41 | 1.21.0 | terminal auth stable; unstable elicitation removed | **yes** |
| v0.17.0 | 2026-11-18 03:33 | 1.25.0 | **subagents added (unstable)**: `ClientCapabilities.Subagents`, `SessionUpdate.SubagentUpdate`, `SessionMessage`/`SessionMessageChunk`, `StateUpdate`, `SessionCancelCapabilities`, `ErrorStopReason` | **yes** |
| v0.18.0 | 2026-11-18 04:35 | 1.32.0 | **unstable `session/status`** (and archive, set_title, options); subagent working directories; `ToolCallContent.Session`; **removed `SenderSessionId`/`RecipientSessionId` from `SessionMessageChunk`** (wire change, schema 1.27.0) | **yes** |
| v0.19.0 | 2026-11-18 09:11 | 1.32.0 | none ("No API break") | no |
| v0.20.0 | 2026-11-18 14:16 | 1.32.0 | `SubagentUpdate`/`SessionMessage` patch fields become `acp.Patch[T]`; `Unstable` copies of shared types removed | **yes** |
| (main, unreleased) | main at 2026-11-24 | 1.34.1 | subagents stable upstream; Go names unchanged; `cursor_update` added; stricter decoding (`MIGRATION_v0.21.md`) | yes (38 incompatible changes per PR #161) |

**Confirmed: the v0.17.0 subagent entry** (release notes):

> Unstable: subagents (`ClientCapabilities.Subagents`, `SessionUpdate.SubagentUpdate`, `SessionMessage` / `SessionMessageChunk` updates, `StateUpdate`, `SessionCancelCapabilities`) and `ErrorStopReason` ([#105](https://github.com/coder/acp-go-sdk/pull/105)).

— https://github.com/coder/acp-go-sdk/releases/tag/v0.17.0

**Confirmed: the v0.18.0 wire change to a subagent type:**

> `SenderSessionId` and `RecipientSessionId` on `SessionMessageChunk` / `SessionUpdateSessionMessageChunk` (unstable, schema 1.27.0) — read them from the `session_message` update via `MessageId` ([#108](https://github.com/coder/acp-go-sdk/pull/108)).

— https://github.com/coder/acp-go-sdk/releases/tag/v0.18.0

**Confirmed: the v0.20.0 Go API change to subagent types:**

> Patch fields on `ToolCallUpdate`, `SessionInfoUpdate`, `CompactionUpdate`, and the unstable `SubagentUpdate`/`SessionMessage` are now `acp.Patch[T]`.

— https://github.com/coder/acp-go-sdk/releases/tag/v0.20.0

**Six-month count (2026-05-28 to 2026-11-28).** **Confirmed.** Nine releases fall in the window: v0.13.4, v0.13.5 and v0.14.0 to v0.20.0. Eight broke unstable types (all except v0.19.0). Three releases touched the subagent types after they were added: v0.17.0 added them, v0.18.0 changed the wire shape of the chunk, and v0.20.0 changed the Go types.

**Release cadence. Confirmed.** v0.14.0 to v0.20.0 were tagged between 01:04 and 14:16 UTC on 2026-11-18. The PRs were opened and merged in the same window: #98 was opened at 2026-11-17T23:20Z, and #124 (the v0.20.0 release) was merged at 2026-11-18T14:15Z (`gh pr view`). The schema walk 0.13.5 → 1.32.0 had gone five and a half months without a release (v0.13.5 shipped 2026-06-02) and then landed in one day. **Inferred:** the 8-of-9 figure comes mostly from one catch-up burst, not a steady rate. It still sets the expected cost of following `main`: v0.21.0 is already staged with 38 incompatible changes.

**Lag behind the spec. Confirmed.** Subagents first appeared upstream in crate v1.10.0 / `schema-v1.24.0` on 2026-09-30 ("Initial RFD and schema for subagents ([#1992])", spec `CHANGELOG.md`). The Go SDK picked them up in v0.17.0 on 2026-11-18, seven weeks later. `session/status` arrived upstream in crate v1.15.0 (#2735, `schema-v1.29.0`, 2026-11-01) and reached Go in v0.18.0 on 2026-11-18.

## 2. How the Go SDK exposes unstable features

### Same package, no build tag, mixed naming

**Confirmed.** There is no build tag. Unstable types sit in package `acp` next to the stable ones. Naming has followed three schemes over time:

- v0.10.8 introduced an `Unstable*` prefix and `AgentExperimental`/`ClientExperimental` interfaces (release notes, #17).
- v0.18.0 replaced those umbrella interfaces with one optional interface per method, such as `AgentUnstableSessionStatus`.
- v0.20.0 merged shared shapes into one type. A type keeps the `Unstable` prefix only when unstable-only methods reach it. Unstable additions to shared types are marked only in doc comments.

The v0.20.0 README:

> Unstable types: one Go type per shape. The Request, Response, and Notification types of unstable-only methods, and the definitions that only they reach, get the `Unstable` prefix, such as `UnstableForkSessionRequest` and `UnstableProviderInfo`. A type that stable and unstable methods share keeps its name and carries the unstable additions, each with a doc comment that starts with `**UNSTABLE**`, as the Rust SDK gates them behind a feature […] Definitions that exist only in `schema/schema.unstable.json` and that stable methods reach keep their names too, for example `SubagentUpdate` and `ErrorStopReason`

— https://github.com/coder/acp-go-sdk/blob/v0.20.0/README.md (line 202)

This explains the observed split. `UnstableSessionStatusRequest` is the request of an unstable-only method, so it keeps the prefix. `SubagentUpdate`, `SessionSubagentUpdate`, `SubagentCapabilities` and `StateUpdate` are reached from the stable `session/update` and `initialize`, so they are unprefixed.

**Confirmed.** In v0.20.0 the bridge uses no `Unstable*` symbol and no type whose doc comment carries an `**UNSTABLE**` marker (grep of `*.go`). Its only nearby reference is `acp.SessionConfigOptionCategoryModel`, which has been stable since v0.13.5. ADR 0004 records the earlier move away from unstable `session/set_model`.

### Compatibility promise

**Confirmed.** The SDK gives no compatibility guarantee for anything, unstable or not. It stays on v0, and the schema itself disclaims unstable items:

> The SDK stays on v0. Raise the minor version for a schema bump or an API break, and the patch version for fixes only.

— https://github.com/coder/acp-go-sdk/blob/v0.20.0/RELEASING.md

> The SDK has its own semver, separate from the ACP schema version. `acp.SchemaVersion` names the ACP schema release that an SDK version targets.

— https://github.com/coder/acp-go-sdk/blob/v0.20.0/README.md (lines 20–21)

Every unstable doc comment, from the generated code:

```go
// **UNSTABLE**
//
// This capability is not part of the spec yet, and may be removed or changed at any point.
```

— https://github.com/coder/acp-go-sdk/blob/v0.20.0/types_gen.go (for example lines 1025–1027, `ClientCapabilities.Subagents`)

`AGENTS.md` at v0.20.0 states only the version-file mechanics: "The top-level `version` file is the SDK's own semver: bump it only in a release PR". It makes no stability promise (https://github.com/coder/acp-go-sdk/blob/v0.20.0/AGENTS.md, line 30). `MIGRATION_v0.20.md` documents breaks but promises nothing about later releases.

One mitigating guarantee covers interfaces:

> A schema release never adds a method to `Agent` or `Client`: a method that it adds or makes stable gets its own optional interface

— README line 194. **Inferred:** this lowers the cost of a bump for an implementer, but it does nothing for type shapes.

### Decoding of unknown variants and fields

**Confirmed. `SessionUpdate` has no catch-all variant.** An unknown discriminator falls through the switch and returns an error:

```go
case "subagent_update":
	var v SessionSubagentUpdate
	...
	}
	...
	return errors.New("no matching variant for union")
```

— https://github.com/coder/acp-go-sdk/blob/v0.20.0/types_gen.go#L11081-L11112

A test pins this behaviour:

```go
{"session update", new(SessionUpdate), `{"sessionUpdate":"bogus_update"}`},
{"session notification", new(SessionNotification), `{"sessionId":"s","update":{"sessionUpdate":"bogus_update"}}`},
```

— https://github.com/coder/acp-go-sdk/blob/v0.20.0/union_decode_test.go#L44-L55 (`TestUnion_UnmarshalJSON_RejectsAnUnknownDiscriminator`)

The v0.14.0 notes give the policy:

> Unions dispatch on their discriminator value: unknown discriminators are errors (not a silent first-variant match), `ToolKind` reads unknown kinds as `other`, and catch-all structs preserve unknown keys in `AdditionalProperties` ([#97])

— https://github.com/coder/acp-go-sdk/releases/tag/v0.14.0

**Confirmed: some unions are open.** Unions whose schema defines an "other" variant decode unknown values into `Other`. Examples in v0.20.0 are `StateUpdate.Other` (`StateUpdateOther{State, AdditionalProperties}`), `IdleStateUpdate.Other`, `ErrorCode.Other`, `NoticeSeverity` and `SystemPromptMode`. `StateUpdate` also has an explicit `Unknown` variant (`"The Agent cannot currently determine foreground activity."`). See https://github.com/coder/acp-go-sdk/blob/v0.20.0/types_gen.go#L12230-L12300. So a new *state* inside a known `subagent_update` decodes, but a new *session-update kind* does not.

**Confirmed: the connection drops a notification that fails to decode and keeps running.** `session/update` decoding returns `-32602`:

```go
case ClientMethodSessionUpdate:
	var p SessionNotification
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, NewInvalidParams(map[string]any{"error": err.Error()})
	}
```

— https://github.com/coder/acp-go-sdk/blob/v0.20.0/client_gen.go#L109-L120

For a notification, no reply is sent. The error is logged at Error, unless it is `-32601`, which is logged at Debug:

```go
c.loggerOrDefault().Error("failed to handle notification", "method", req.Method, "err", err)
```

— https://github.com/coder/acp-go-sdk/blob/v0.20.0/connection.go#L669-L686

The unreleased `main` states the consequence for a new kind explicitly:

> **Handlers see `cursor_update`.** v0.20.0 failed to decode it (`no matching variant for union`), so the connection dropped it.

— https://github.com/coder/acp-go-sdk/pull/161 (merged 2026-11-23)

**Confirmed: unknown object fields are ignored.** The generated `UnmarshalJSON` methods use `encoding/json` (v1) with no `DisallowUnknownFields`. The only use of `DisallowUnknownFields` in the module is in a verification tool, `.agents/skills/verify-acp-go-sdk/scripts/acpwire/main.go`. Properties marked `x-deserialize-default-on-error` decode leniently through `decodeLenient` (`types_gen.go#L16270`). Since v0.19.0 a value that does not match its schema shape reads as the default.

**Inferred consequences for the bridge, which is the agent side:**

- A newer editor sending extra capability fields or unknown notifications to the bridge on v0.20.0 does no harm. Unknown fields are ignored, unknown notifications are ignored at Debug, and unknown requests get `-32601`.
- If the bridge sends `subagent_update` to an editor whose SDK predates that variant, the editor drops each such notification. It keeps processing the others. The spec gates sending on the client's `subagents` capability (next section), so a conforming bridge never reaches this case.

## 3. Subagent support in v0.20.0

**Confirmed.** Inventory from `types_gen.go`, `client_gen.go`, `agent_gen.go` and `constants_gen.go` at v0.20.0:

| Item | Kind | Notes |
|---|---|---|
| `ClientCapabilities.Subagents *SubagentCapabilities` | client capability | `{}` "means the client understands child associations, work-state snapshots, session-directed messages, and restricted-session semantics" (L1025–1035) |
| `SubagentCapabilities` | type | "The client must advertise this capability before the agent sends subagent updates or session-directed messages." (L12594) |
| `SubagentUpdate` | schema definition type | Standalone body; no message carries it on its own (the unreleased `MIGRATION_v0.21.md` table says "no message carries it") (L12675) |
| `SessionSubagentUpdate` / `SessionUpdate.SubagentUpdate` | `session/update` variant `subagent_update` | Same fields plus the `SessionUpdate` discriminator: `SessionId` (child), `Title`, `Description`, `Cwd`, `AdditionalDirectories`, `Capabilities`, `State`, `Meta`, all `Patch[T]` except `SessionId` (L10642–10693) |
| `SubagentSessionCapabilities{Cancel *SessionCancelCapabilities}` | per-child mutation grant | "A mutation requires an explicit per-child capability. Support for the method on ordinary sessions does not grant support on a child." (L12623). This is the restricted-session rule |
| `SessionCancelCapabilities` | type | "Capability to cancel work in a subagent session without ending that session." (L8572) |
| `StateUpdate` (`Running`, `Idle`, `RequiresAction`, `Unknown`, `Other`) and `StateUpdateIdle` (stop-reason variants incl. `Error`, `Other`, `None`) | union | Child work-state snapshot (L11697–12300) |
| `SessionMessage`, `SessionUpdate.SessionMessage` (`session_message`) | upsert of an inter-session message | `MessageId`, `Content`, `SenderSessionId`, `RecipientSessionId`, `SenderMessageId`, `RecipientMessageId` as `Patch` (L9393) |
| `SessionMessageChunk`, `SessionUpdate.SessionMessageChunk` (`session_message_chunk`) | streamed block | `MessageId`, `Content`, `Meta` only (L9481) |
| `ToolCallContentSession`, `SessionReference` | tool-call content referencing a session | (L13259, L9663) |
| `ErrorStopReason`, `IdleStateUpdate*` | types | Used inside state snapshots |
| `UnstableSessionStatusRequest/Response`, `UnstableSessionStatus` (`open`, `saved`), `SessionStatusCapabilities`, `AgentUnstableSessionStatus`, `(*ClientSideConnection).UnstableSessionStatus`, `AgentMethodSessionStatus = "session/status"` | unstable method | **Not subagent work state.** It reports whether the agent has a session in memory: "Whether the Agent has a session in memory, as reported by 'session/status'." (L14765). Unknown values: "Clients MUST treat a value they do not recognize as 'saved'." |

**Confirmed: no subagent-specific methods.** There are no child-cancel or child-message methods. Child cancellation reuses `session/cancel` (`Agent.Cancel(ctx, CancelNotification)`), gated by `SubagentSessionCapabilities.Cancel`. **Inferred:** the bridge would receive a child cancel as an ordinary `Cancel` carrying the child's `SessionId`.

**Confirmed: no agent-side helpers or example.** `helpers.go` and `helpers_gen.go` contain no subagent builders. The README notes "`cmd/generate` emits no constructors for `ContentBlock`, `ToolCallContent`, and `SessionUpdate`" (line 246). `grep -ril subagent example/ cmd/` returns nothing. An agent emits an update by building the union by hand and calling `(*AgentSideConnection).SessionUpdate`. For example: `acp.SessionNotification{SessionId: parent, Update: acp.SessionUpdate{SubagentUpdate: &acp.SessionSubagentUpdate{SessionId: child, Title: acp.PatchSet("…")}}}`. **Inferred:** this compiles against v0.20.0 types; it has not been tested.

**Confirmed: complete for schema 1.32.0, and the schema is now stable.** v0.20.0 generates every definition in the 1.32.0 unstable subagent surface. Section 6 shows those definitions are byte-equal (descriptions aside) to stable 1.34.1. The README's description of `SubagentUpdate` as unstable-only was out of date by 2026-11-20; PR #161 corrected it on `main`.

## 4. TypeScript and Rust SDKs

### TypeScript `@agentclientprotocol/sdk`

The package `@zed-industries/agent-client-protocol` was renamed; the repository is now `agentclientprotocol/typescript-sdk`. npm `latest` is 2.12.0 (registry, 2026-11-28).

| Release | Date | v1 schema | Subagent decode in default import | Notes |
|---|---|---|---|---|
| 0.17.1 | 2026-03-27 | 0.11.3 (from 0.17.0) | no | Locked by Paseo server |
| 0.23.0 | 2026-06-01 | 0.13.4 | no | |
| 0.24.0 | 2026-06-02 | 0.13.5 | no | "Add resilient schema deserialization (#167)" |
| 0.26.0 | 2026-06-16 | 1.13.7 | no | |
| 0.28.0 | 2026-06-18 | 1.14.0 | no | |
| 1.0.0 | 2026-06-24 | 1.16.0 | no | major |
| 1.2.0 | 2026-07-06 | 1.17.0, 1.19.0 | no | |
| 1.3.0 | 2026-07-21 | 1.20.0 | no | experimental v2 entry |
| 1.4.0 | 2026-08-20 | (1.20.0) | no | Locked by Paseo plugin; stable elicitation |
| 1.5.0 | 2026-09-21 | 1.22.0, 1.23.0 | no | |
| 1.6.0 | 2026-10-01 | 1.24.1 | Unverified | |
| 1.8.0 | 2026-10-09 | 1.25.0 | Unverified | |
| 1.9.0 | 2026-10-16 | 1.26.0 | **yes** (`src/schema/zod.gen.ts` contains `"subagent_update"`) | Default import included unstable features before 2.0; acpx `^1.9.0` |
| 2.0.0 | 2026-10-19 | (1.26.0) | **no** (stable only); yes via `/unstable` | **BREAKING:** stable-by-default entrypoints |
| 2.1.0 – 2.7.0 | 2026-10-28 – 11-14 | 1.27.0 – 1.32.0 | no; `/unstable` yes | |
| 2.8.0 | 2026-11-18 | 1.33.0 | no; `/unstable` yes | |
| 2.9.0 | 2026-11-21 | 1.34.0 | **yes** (stable) | subagents stable |
| 2.10.0 – 2.12.0 | 2026-11-24 | 1.35.0 (`scripts/generate.js`: `CURRENT_V1_SCHEMA_RELEASE = "schema-v1.35.0"`) | yes | |

Sources: `CHANGELOG.md` at v2.12.0, `git show <tag>:package.json`, and `grep -c '"subagent_update"'` in each tag's v1 `zod.gen.ts`. "Unverified" rows were not grepped.

**Confirmed: entrypoint split and stability statement.**

> Each `/unstable` entrypoint includes the stable features for its protocol version, plus additions that may change or be removed in any SDK release.

— https://github.com/agentclientprotocol/typescript-sdk/blob/v2.12.0/README.md (lines 26–28)

> Subagent sessions and plan operations are stable in both protocol versions and need no `/unstable` import: […] V1 also has the `subagents` client capability, and agents send these updates only to a client that advertises it.

> Passing unstable fields through untyped code is not a substitute for opting in: stable-feature request parsing can strip those fields, and stable built-in handlers do not include unstable methods.

— https://github.com/agentclientprotocol/typescript-sdk/blob/v2.12.0/MIGRATION_STABLE_UNSTABLE.md

### Rust `agent-client-protocol` (repo `agentclientprotocol/rust-sdk`)

The SDK crate pins the schema crate `agent-client-protocol-schema` exactly. Schema-crate versions map to `schema-v*` tags by release timestamp in `agentclientprotocol/agent-client-protocol`; for example crate v1.20.0 and `schema-v1.34.0` were both released at 2026-11-20T21:51Z. PR #161 confirms that crate 1.21.0 corresponds to `schema-v1.34.1`. **Inferred** for the other rows.

| SDK release | Date | Schema crate | Schema (inferred) | `unstable_subagents` feature | `unstable_session_status` |
|---|---|---|---|---|---|
| v1.0.0 | 2026-06-24 | =1.1.0 | — | no | no |
| v2.0.0 | 2026-07-23 | =1.5.0 | — | no | no |
| v2.2.0 | 2026-09-18 | =1.9.1 | — | no | no |
| v3.0.0 – v3.2.0 | 2026-10-06 – 10-08 | =1.10.2 | 1.24.1 | **yes** | no |
| v3.3.0 | 2026-10-09 | =1.11.0 | 1.25.0 | yes | no |
| v3.4.0 – v3.5.0 | 2026-10-16 – 10-22 | =1.12.0 | 1.26.0 | yes | no |
| v3.6.0 | 2026-10-28 | =1.13.0 | 1.27.0 | yes | no |
| v5.0.0 | 2026-11-02 | =1.15.1 | 1.29.1 | yes | **yes** |
| v5.1.0 | 2026-11-09 | =1.16.0 | 1.30.0 | yes | yes |
| v5.2.0 | 2026-11-10 | =1.17.0 | 1.31.0 | yes | yes |
| v5.3.0 | 2026-11-14 | =1.18.0 | 1.32.0 | yes | yes |
| v5.4.0 | 2026-11-18 | =1.19.0 | 1.33.0 | yes | yes |
| v5.5.0 / v5.5.1 | 2026-11-21 | =1.21.1 / =1.21.2 | 1.34.1 | **removed (stable)** | yes |
| v5.6.0 / v5.6.1 | 2026-11-24 / 11-25 | =1.22.0 | 1.35.0 | stable | yes |

Source: `git show <tag>:Cargo.toml` and `src/agent-client-protocol/Cargo.toml`. There is no v4 tag.

**Confirmed: Rust gates unstable features behind cargo features.**

```toml
# Forward unstable features from agent-client-protocol-schema.
# Enable these to get support for the corresponding unstable ACP surfaces.
unstable = [
    ...
    "unstable_session_status",
    ...
]
```

— https://github.com/agentclientprotocol/rust-sdk/blob/v5.6.1/src/agent-client-protocol/Cargo.toml#L52

The Go README says the same thing about Rust: "as the Rust SDK gates them behind a feature".

**Confirmed: of the three SDKs, Go is the furthest behind on schema.** As of 2026-11-28, TypeScript 2.12.0 and Rust 5.6.1 bundle schema 1.35.0, where subagents are stable. The released Go v0.20.0 bundles 1.32.0, where they are unstable. Go `main` bundles 1.34.1.

## 5. Unknown `sessionUpdate` on the wire

| SDK (version) | Decoder | Result | Connection |
|---|---|---|---|
| Go acp-go-sdk v0.20.0 | `SessionUpdate.UnmarshalJSON` → `no matching variant for union` → `-32602` | Notification dropped, logged at Error, handler not called | Stays up |
| TS 2.12.0 | zod `zSessionUpdate = z.union([...])` without catch-all → `ZodError` | Notification dropped, `console.error("Error handling notification", {method, code, message})` | Stays up |
| TS 0.17.1 (Paseo server) | `validate.zSessionNotification.parse(params)` throws | `console.error("Error handling notification", message, response.error)` | Stays up |
| Rust 5.6.1 / schema crate 1.22.0 | serde `#[serde(tag = "sessionUpdate")]`, `#[non_exhaustive]`, no `#[serde(other)]` → deserialize error | Logged, no reply | Stays up |

**Confirmed: TypeScript, current.** The stable v1 union has no catch-all:

```ts
export const zSessionUpdate = z.union([
  zContentChunk.and(z.object({ sessionUpdate: z.literal("user_message_chunk") })),
  ...
  zSubagentUpdate.and(z.object({ sessionUpdate: z.literal("subagent_update") })),
  ...
]);
```

— https://github.com/agentclientprotocol/typescript-sdk/blob/v2.12.0/src/v1/schema/zod.gen.ts#L2337

The notification-error path:

```ts
    } catch (error) {
      ...
      } else {
        const response = errorToResult(error);
        if ("error" in response) {
          // Params and error data can carry credentials (such as MCP server
          // `env` and `headers` values), so log only identifying metadata.
          console.error("Error handling notification", {
            method: message.method,
            code: response.error.code,
            message: response.error.message,
          });
```

— https://github.com/agentclientprotocol/typescript-sdk/blob/v2.12.0/src/core/jsonrpc.ts#L1304-L1322

**Confirmed: TypeScript 0.17.1.**

```ts
case schema.CLIENT_METHODS.session_update: {
  const validatedParams = validate.zSessionNotification.parse(params);
  return client.sessionUpdate(validatedParams);
}
```

— https://github.com/agentclientprotocol/typescript-sdk/blob/v0.17.1/src/acp.ts#L538-L541. Line 1049 has `console.error("Error handling notification", message, response.error);`.

**Confirmed: Rust.**

```rust
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(tag = "sessionUpdate", rename_all = "snake_case")]
...
#[non_exhaustive]
pub enum SessionUpdate {
```

— https://github.com/agentclientprotocol/agent-client-protocol/blob/v1.22.0/agent-client-protocol-schema/src/v1/client.rs#L95-L100

> JSON-RPC notifications are one-way and cannot receive success or error replies. The SDK logs notification parse and handler errors without answering them.

— https://github.com/agentclientprotocol/rust-sdk/blob/v5.6.1/src/agent-client-protocol/src/concepts/error_handling.rs#L41-L42

**Inferred: none of the SDKs is lenient here.** All three drop the notification. None of them errors back to the agent or tears down the connection. The failure is quiet: the agent gets no signal that the editor discarded its updates. The only safe trigger for sending `subagent_update` is the client's `subagents` capability, which the Go, TS and Rust docs all require.

**Confirmed: whether clients can decode subagent updates.**

- Paseo `main` (2026-11-28) `package-lock.json` resolves `node_modules/@agentclientprotocol/sdk` to **0.17.1** (server) and `packages/plugin/node_modules/@agentclientprotocol/sdk` to **1.4.0**. Neither decodes `subagent_update`. Paseo's base ACP client capabilities are `{fs: {readTextFile: false, writeTextFile: false}, terminal: true}` with no `subagents` (`packages/server/src/server/agent/providers/acp-agent.ts`, lines 311–317 on `main`).
- acpx 0.23.0 (npm `latest`) declares `"@agentclientprotocol/sdk": "^1.9.0"`, which resolves to 1.9.0, the newest 1.x. That version's default import decodes `subagent_update`. **Unverified:** whether acpx advertises `subagents`, and which version its lockfile or installs actually resolve.

## 6. Versioning compatibility

**Confirmed: three independent version numbers.**

- SDK semver: Go v0.x, TS 2.x, Rust 5.x.
- Schema semver: `acp.SchemaVersion`, for example `"1.32.0"`.
- Wire `protocolVersion`: `acp.ProtocolVersionNumber = 1`. The v0.13.4 notes say "The wire `ProtocolVersionNumber` stays `1`".

The schema moved from 0.x to 1.13.7 between Go v0.13.5 and v0.14.0 without any change to `protocolVersion`. **Inferred:** `protocolVersion` negotiation tells a peer nothing about unstable support. Only capabilities do.

**Confirmed: unstable fields are not wire-compatible across Go minors in general.** Two examples:

- v0.15.0: "Plan `id` → `planId` […] (unstable, schema 1.18.0 rename, on the wire too)".
- v0.18.0 removed the participant IDs from `session_message_chunk`. On the subagent types this is benign in practice: an older receiver sees the fields as absent, and a newer one ignores extras. **Inferred.**

The v0.20.0 Patch change is Go-API-only and does not change the wire. MIGRATION_v0.20 says of the shared-type merge: "The compiler reports each break, and the wire does not change."

**Confirmed: from 1.32.0 onward the subagent wire shapes are frozen.** This comes from a jq comparison of definitions with `description` stripped, run on 2026-11-28:

- Identical in v0.20.0 `schema/schema.unstable.json` (1.32.0), `schema-v1.34.1` `schema/v1/schema.json` and `schema-v1.35.0` `schema/v1/schema.json`: `SubagentUpdate`, `SessionMessage`, `SessionMessageChunk`, `SubagentCapabilities`, `SubagentSessionCapabilities`, `SessionCancelCapabilities`, `StateUpdate`, `RunningStateUpdate`, `RequiresActionStateUpdate`, `UnknownStateUpdate`, `SessionReference`, `ToolCallContent`, `ClientCapabilities`.
- `IdleStateUpdate` differs because of its unstable `usage` member. `SessionUpdate` differs because `prompt_suggestions_update` is still unstable.

PR #161 says the same: "The SDK already generated them from the unstable schema, so their Go names and decoding stay the same." **Inferred:** a bridge on v0.20.0 that emits these updates is wire-compatible with clients on TS ≥2.9.0 and Rust ≥5.5.0. It should leave `usage` out of idle states.

**Confirmed: the next Go upgrade is stricter.** `MIGRATION_v0.21.md` on `main`:

> A request that breaks a rule below gets `-32602` (invalid params) and never reaches your method. A notification that breaks one is dropped, and the connection logs it at Error.

It also lists "`AgentSideConnection` answers a request before a successful `initialize` with `-32600`". **Inferred:** updating the bridge to v0.21.0 can change behaviour toward lax editors even if no subagent code changes.

## Contradictions and open questions

- **The brief's `SubagentUpdate` vs `SessionSubagentUpdate`.** These are not two competing APIs. `SubagentUpdate` is the schema definition. `SessionSubagentUpdate` is the generated `session/update` variant wrapper, which adds the `sessionUpdate` discriminator. Only the second appears on the wire. **Confirmed.**
- **"Session status".** In v0.20.0, `session/status` (`UnstableSessionStatus*`) answers "is this session in memory (`open`/`saved`)". It does not report subagent activity, which travels as `StateUpdate` inside `subagent_update`. Treating them as one feature would be wrong. `session/status` remains unstable in Rust 5.6.1 (`unstable_session_status`). **Confirmed.**
- **Stability of Go docs vs spec.** The v0.20.0 README and doc comments call the subagent types unstable. The spec made them stable on 2026-11-20. The marker in v0.20.0 is now stale. The code is not. **Confirmed.**
- **Release-note provenance.** `RELEASING.md` says the release-notes workflow needs `ANTHROPIC_API_KEY`. **Inferred:** the Go release notes are machine-written, so this note checks the key claims against code. The v0.17.0 note that "Neither Claude Code nor Codex send these yet" was not checked (**Unverified**).
- **Burst releases.** The 8-of-9 figure for unstable breaks is accurate, but seven of the releases came out of a single day. Whether the cadence stays this high after v0.21.0 is open.
- **Schema-crate to schema-tag mapping.** For Rust rows other than crate 1.21.0, the mapping rests on matching release timestamps, not on an explicit statement. **Inferred.**
- **TS 1.6.0 and 1.8.0** were not grepped for `subagent_update` (**Unverified**). Subagents first entered the schema at 1.24.0, so 1.6.0 (schema 1.24.1) probably decodes them. **Inferred.**
- **acpx capability advertisement** was not checked (**Unverified**).
- **Open question for the decision:** whether to emit subagent updates on v0.20.0 now (wire-stable, Go doc marked unstable) or wait for v0.21.0. The upgrade costs 38 incompatible API changes and stricter inbound validation.

## References

All accessed 2026-11-28.

Go SDK (`coder/acp-go-sdk`):
- https://github.com/coder/acp-go-sdk/releases (and `/tag/v0.4.3`, `v0.4.4`, `v0.4.5`, `v0.4.9`, `v0.6.3`, `v0.10.8`, `v0.11.7`, `v0.12.0`, `v0.12.2`, `v0.13.0`, `v0.13.4`, `v0.13.5`, `v0.14.0`, `v0.15.0`, `v0.16.0`, `v0.17.0`, `v0.18.0`, `v0.19.0`, `v0.20.0`)
- https://github.com/coder/acp-go-sdk/blob/v0.20.0/README.md
- https://github.com/coder/acp-go-sdk/blob/v0.20.0/AGENTS.md
- https://github.com/coder/acp-go-sdk/blob/v0.20.0/RELEASING.md
- https://github.com/coder/acp-go-sdk/blob/v0.20.0/MIGRATION_v0.20.md
- https://github.com/coder/acp-go-sdk/blob/v0.20.0/types_gen.go
- https://github.com/coder/acp-go-sdk/blob/v0.20.0/client_gen.go#L109-L120
- https://github.com/coder/acp-go-sdk/blob/v0.20.0/connection.go#L654-L700
- https://github.com/coder/acp-go-sdk/blob/v0.20.0/union_decode_test.go#L44-L55
- https://github.com/coder/acp-go-sdk/blob/v0.20.0/schema/schema.unstable.json
- https://github.com/coder/acp-go-sdk/blob/main/MIGRATION_v0.21.md
- PRs: https://github.com/coder/acp-go-sdk/pull/90, /pull/97, /pull/98, /pull/101, /pull/103, /pull/105, /pull/107, /pull/108, /pull/113, /pull/117, /pull/119, /pull/122, /pull/124, /pull/161
- Module cache copy: `~/go/pkg/mod/github.com/coder/acp-go-sdk@v0.20.0/`

Spec and schema (`agentclientprotocol/agent-client-protocol`):
- https://github.com/agentclientprotocol/agent-client-protocol/releases
- https://github.com/agentclientprotocol/agent-client-protocol/releases/tag/schema-v1.34.0
- https://github.com/agentclientprotocol/agent-client-protocol/releases/tag/v1.22.0
- https://github.com/agentclientprotocol/agent-client-protocol/blob/v1.22.0/CHANGELOG.md
- https://github.com/agentclientprotocol/agent-client-protocol/blob/v1.22.0/agent-client-protocol-schema/src/v1/client.rs#L95-L100
- https://github.com/agentclientprotocol/agent-client-protocol/blob/schema-v1.34.1/schema/v1/schema.json
- https://github.com/agentclientprotocol/agent-client-protocol/blob/schema-v1.35.0/schema/v1/schema.json
- https://github.com/agentclientprotocol/agent-client-protocol/pull/1992, /pull/2735, /pull/2939

TypeScript SDK (`agentclientprotocol/typescript-sdk`):
- https://github.com/agentclientprotocol/typescript-sdk/releases
- https://github.com/agentclientprotocol/typescript-sdk/blob/v2.12.0/CHANGELOG.md
- https://github.com/agentclientprotocol/typescript-sdk/blob/v2.12.0/README.md
- https://github.com/agentclientprotocol/typescript-sdk/blob/v2.12.0/MIGRATION_STABLE_UNSTABLE.md
- https://github.com/agentclientprotocol/typescript-sdk/blob/v2.12.0/package.json
- https://github.com/agentclientprotocol/typescript-sdk/blob/v2.12.0/scripts/generate.js
- https://github.com/agentclientprotocol/typescript-sdk/blob/v2.12.0/src/v1/schema/zod.gen.ts#L2337
- https://github.com/agentclientprotocol/typescript-sdk/blob/v2.12.0/src/core/jsonrpc.ts#L1304-L1322
- https://github.com/agentclientprotocol/typescript-sdk/blob/v0.17.1/src/acp.ts#L538-L541
- https://github.com/agentclientprotocol/typescript-sdk/blob/v1.9.0/src/schema/zod.gen.ts
- https://registry.npmjs.org/@agentclientprotocol%2fsdk

Rust SDK (`agentclientprotocol/rust-sdk`):
- https://github.com/agentclientprotocol/rust-sdk/releases
- https://github.com/agentclientprotocol/rust-sdk/blob/v5.6.1/Cargo.toml
- https://github.com/agentclientprotocol/rust-sdk/blob/v5.6.1/src/agent-client-protocol/Cargo.toml#L52
- https://github.com/agentclientprotocol/rust-sdk/blob/v5.4.0/src/agent-client-protocol/Cargo.toml
- https://github.com/agentclientprotocol/rust-sdk/blob/v5.6.1/src/agent-client-protocol/src/concepts/error_handling.rs#L41-L42

Clients:
- https://github.com/getpaseo/paseo/blob/main/package-lock.json
- https://github.com/getpaseo/paseo/blob/main/packages/server/package.json
- https://github.com/getpaseo/paseo/blob/main/packages/plugin/package.json
- https://github.com/getpaseo/paseo/blob/main/packages/server/src/server/agent/providers/acp-agent.ts
- https://registry.npmjs.org/acpx/latest

Bridge (this repository): `go.mod`, `docs/adr/0004-models-as-config-option.md`, `internal/bridge/models.go`.
