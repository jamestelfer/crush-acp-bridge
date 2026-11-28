# ACP spec movement: subagents, session state and background work

Research for the bridge's design decision on exposing Crush subagents and background jobs over ACP.
All sources accessed **2026-11-28**. The spec repository was cloned at `main` (latest commit 2026-11-26) and every schema tag was extracted locally. Claims are marked:

- **Confirmed**: a primary source is quoted or a schema at a release tag was diffed.
- **Inferred**: my reasoning from confirmed facts.
- **Unverified**: not checked against a primary source.

Abbreviations: `REPO` = `https://github.com/agentclientprotocol/agent-client-protocol`; `DOCS` = `https://agentclientprotocol.com`.

## Summary

1. **Subagents are stable in v1 from schema 1.34.0 (2026-11-20).** The wire shapes are the ones the 1.32.0 unstable schema already had, so the bundled SDK schema matches the stable shape. v1 Clients still opt in with `clientCapabilities.subagents: {}`. **Confirmed.**
2. **The latest schema is 1.35.0 (2026-11-23).** It adds nothing relevant to subagents or state. **Confirmed.**
3. **v1 has no standard for background work.** The Background Work RFD is v2 only, and it is in Preview inside the v2 Preview (`schema-v2.0.0-beta.4`). It explicitly declines a v1 form. **Confirmed.**
4. **`session/status` is still an unstable Draft RFD.** In v1 it returns only `open` or `saved`, with no work state. **Confirmed.**
5. **`terminal/kill` is not new or unstable.** It has been stable since 2025-09 (PR #53). It is an Agent-to-Client request for a terminal the Client owns, and v2 removes it. **Confirmed.**

## 1. Corrections to the starting facts

| Starting fact | Finding | Status |
|---|---|---|
| SDK v0.20.0 bundles schema 1.32.0 | `version` file reads `1.32.0`. After `jq -S` normalisation, `schema.unstable.json` is identical to `schema/v1/schema.unstable.json` at tag `schema-v1.32.0` (0 diff lines). | Confirmed |
| `subagent_update` is typed `SessionSubagentUpdate` | No `SessionSubagentUpdate` def exists in the bundled schema (grep count 0). The def is **`SubagentUpdate`**. | Confirmed (correction) |
| These elements are unstable | That was true in 1.32.0. In **1.34.0** they moved to the stable `schema.json`: `SubagentUpdate`, `SubagentCapabilities`, `SubagentSessionCapabilities`, `SessionCancelCapabilities`, `SessionMessage`, `SessionMessageChunk`, `SessionReference`, `StateUpdate` and its variants. `session/status` and `IdleStateUpdate.usage` remain unstable. | Confirmed (correction) |
| `StateUpdate` (running / idle / requires_action / unknown) | Correct, and the enum is open (it also has an `other` branch). In v1 it is carried **only** inside `subagent_update.state`. v1 has no session-level `state_update`, and the v1 `session/status` response has no `state`. | Confirmed |
| terminal/* methods including `terminal/kill` | `KillTerminalRequest` is in the stable schema at every tag from 1.13.7 onwards. It was added by PR #53 (2025-09-01). | Confirmed |
| Repository is agentclientprotocol/agent-client-protocol, formerly zed-industries | `gh api repos/zed-industries/agent-client-protocol` resolves to `agentclientprotocol/agent-client-protocol`. | Confirmed |

## 2. RFDs, PRs, issues and discussions

### 2.1 Subagent Sessions RFD (`docs/rfds/subagents.mdx`): **Completed**

- Authors: Vadim Briliantov and Ben Brandt. The PR author is `Ololoshechkin`; the champion and lead is `benbrandt` (Zed).
- PR #1992, "feat(unstable): Intial RFD and schema for subagents": opened 2026-08-20, merged 2026-09-30, released in schema 1.24.0.
- Preview on 2026-11-13 (PR #2800). Completed and stabilised on 2026-11-20 (PR #2939). **Confirmed** from the RFD revision history and `docs/docs.json`, which lists `rfds/subagents` under "Completed".

What it proposes:

> Allow an Agent to expose subagents that it creates when delegating work. Each subagent is represented by its own ACP session ID, so its messages, thoughts, plans, tool calls, and current work can be displayed independently from the parent session.
>
> The Agent associates a reusable child session with its parent through an upsert-style `subagent_update`. Child events then flow automatically on the same connection.
>
> — REPO/blob/main/docs/rfds/subagents.mdx (accessed 2026-11-28)

The capability rule:

> In v1, an Agent **MUST NOT** send the updates defined by this RFD unless the Client advertised `subagents`. It may still use subagents internally and present their results through the parent session.
>
> There is no Agent-side capability. […] In ACP v2, subagent updates are part of the baseline session model and support is assumed rather than negotiated
>
> — same file, "Capability negotiation"

There is no lifecycle enum, and idle does not mean done:

> There is no separate subagent lifecycle enum. Work can start and stop many times within the same child session. Becoming idle, completing an operation, or cancelling current work does not permanently end the association.
>
> — same file, "Current work state"

> When the v1 Agent observes child foreground work starting, resuming, or stopping, it **MUST** report the corresponding `running` or `idle` state. It **SHOULD** report `requires_action` while observed foreground work is blocked on user action […] A v1 child has no prompt response that could carry a JSON-RPC error, so this snapshot is the only place a v1 Client learns that the child's work failed
>
> — same file

One-shot children (one delegation, never reused) conform:

> Many runtimes run a subagent once, often synchronously inside a single tool call, and discard it. Such an Agent announces a new child session for each run and never sends it another assignment. This conforms to this RFD.
>
> — same file, "Announcing and updating an association"

Stabilisation note from PR #2939:

> In v1, Clients still opt in with `clientCapabilities.subagents: {}`. The wire shapes are the ones the unstable schema already had.
>
> — REPO/pull/2939 (merged 2026-11-20)

Implementations at completion:

> The behavior below was checked on 2026-11-19 against Zed `07278eddc4a`, Claude Agent adapter v0.109.0, and Codex adapter v2.8.0.
>
> — RFD, "Agent and Client implementations"

The same table says Zed "Advertises `subagents` on v1 behind its `acp-beta` feature flag". **Confirmed.**

**Parent work outside a prompt turn (v1 stable page).** This matters for a bridge:

> The parent's `session/prompt` response does not mean that its children have stopped. The Agent keeps sending the updates and requests of a child that is still running.
>
> When such a child reports back, the parent can work on the result while no `session/prompt` request is pending. […] No `session/prompt` response reports that this work stopped. […] `session/cancel` with the parent's `sessionId` cancels this work. No response confirms the cancellation.
>
> — REPO/blob/main/docs/protocol/v1/subagents.mdx, "After the parent's prompt response"; cross-checked at DOCS/protocol/v1/subagents (accessed 2026-11-28; the page has no draft or unstable label)

Restricted methods on a child:

> `cancel` is the only capability defined for a child session. It enables `session/cancel` and nothing else.
>
> — docs/protocol/v1/subagents.mdx, "Restricted methods"

Closing the parent:

> `session/close` on a parent session cancels the work of all its descendants, including children that keep running after the parent's own work ended
>
> — docs/protocol/v1/subagents.mdx, "Closing the parent"

### 2.2 v2 Background Work RFD (`docs/rfds/v2/background-work.mdx`): **Preview (v2 only)**

- Author: `@benbrandt`. RFD PR #2452 (2026-10-21). Schema PR #2738, merged 2026-10-30 behind `unstable_protocol_v2`.
- Moved to Preview on 2026-11-20, together with the other v2 RFDs. **Confirmed.**

> **Terminals and tool calls can outlive the foreground work that started them.** A backgrounded shell is a terminal that keeps running after control returns to the model. A watcher or a scheduled wakeup is a tool call that stays `pending` or `in_progress` after the session becomes `idle`.
> **Terminals and tool calls can be cancelled individually.** […] a new `terminal/cancel` or `tool_call/cancel` request
> **State snapshots list the background work.** Every `state_update` can carry a `background` list referencing the terminals, tool calls, and subagent sessions that are running independently of foreground work.
>
> — REPO/blob/main/docs/rfds/v2/background-work.mdx, "Elevator pitch"

The status quo it cites, including Claude Code's `run_in_background` and the adapters' private extension:

> `Bash` with `run_in_background`, Ctrl+B, or a command that hits its timeout keeps running after the turn. […] Both `claude-agent-acp` and `codex-acp` implement a private extension (`async_task_spawned`, `async_task_state_update`, `_session/async_task/stop`) that only one Client negotiates, so other Clients see nothing.
>
> — same file, "Status quo"

No new task entity:

> No new entity type is added. Anything an agent runs in the background is a process, work started by a tool call, or a subagent. […] When the Agent knows that a tool call's work continues after the call returns control to the model, it **SHOULD** keep that tool call `pending`, `in_progress`, or `paused` and list it, instead of giving it a final status.
>
> — same file

Cancelling a session does not stop background work:

> **`session/cancel` cancels foreground work only.** Work in the latest `background` list is not part of the session's current work.
>
> — same file, "Cancellation, close, and resume"

There is no v1 form:

> ### Is there a v1 form?
> Not in this RFD. The `background` list belongs on the session-level `state_update`, which only v2 defines. Tool call `capabilities` and `tool_call/cancel` are additive and could be proposed for v1 separately. Terminal cancellation has no v1 equivalent, because v1 terminals are owned by the Client. Until then, v1 Clients rely on adapter-specific extensions.
>
> — same file, FAQ

Why it is called "cancel" rather than "kill":

> It also avoids reusing v1's `terminal/kill`, which goes in the other direction, from Agent to Client, for a terminal the Client owns.
>
> — same file, FAQ

Implementation status:

> **Phase 2 is partly done.** `claude-agent-acp` reports the `background` list and the `paused` status, and handles `tool_call/cancel` and `terminal/cancel`, on v2 connections. Zed reads the list and sends both requests for items that advertise `cancel`. `codex-acp` does not report the list or handle the two requests yet.
>
> — same file, "Status on 2026-11-18"

The v2 schema confirms the shape. At `schema-v2.0.0-beta.4`, `schema/v2/schema.json` has:

- `ToolCallStatus` = `pending | in_progress | paused | completed | failed | cancelled | other`
- a `BackgroundWork` union of `terminal`, `tool_call` and `subagent`
- `terminal_cancel` and `tool_call_cancel` in `meta.json` `agentMethods`
- **no** Client `terminal/*` methods

**Confirmed.**

### 2.3 Session status RFD (`docs/rfds/session-status.mdx`): **Draft**

- Author: `@chazcb`; champion: `@benbrandt`. RFD PR #986 (2026-04-15).
- Schema PR #2735, merged 2026-10-30 behind `unstable_session_status`, released in 1.29.0.
- Listed under "Draft" in `docs/docs.json`. **Confirmed.**

> Add a `session/status` method that lets a Client ask an Agent about a single session without changing anything: **Open** […] **Saved** […] **Unknown**: […] returned as a `-32002` error.
>
> — REPO/blob/main/docs/rfds/session-status.mdx

> v1 has no foreground state model, so the v1 response has no `state` field.
>
> — same file, "Foreground state (v2)"

Status values were renamed before release:

> 2026-10-30: Rename the status values from `active` / `inactive` to `open` / `saved`
>
> — same file, revision history

This is a liveness probe for routing, not a "busy or idle" signal in v1. **Confirmed.**

### 2.4 v2 Terminal Output and v2 Client Filesystem/Terminal RFDs: **Preview (v2)**

> ACP v2 will remove the standard Client filesystem and terminal execution surface. […] the Client terminal execution methods, and the inherited terminal tool-call content that refers to a Client-created terminal.
>
> — REPO/blob/main/docs/rfds/v2/client-filesystem-terminal-capabilities.mdx

> A tool call can reference an Agent-owned terminal by `terminalId` […] This is a presentation surface, not a Client execution API.
>
> — REPO/blob/main/docs/rfds/v2/terminal-output.mdx

### 2.5 Other related items

| # | Type | Title | Author | Dates | Status | Relevance |
|---|---|---|---|---|---|---|
| 53 | PR | Add `terminal/kill` method | agu-z | 2025-09-01 | merged | "stops the active terminal process without releasing the terminal, so we can still get its output" |
| 855 | PR (RFD) | Subagent Discovery, Delegation and Child Sessions | egor-baranov | 2026-03-26 → closed 2026-10-22 | closed unmerged | Competing proposal: Client-side subagent selection and an `available_subagents_update` catalogue. Superseded by #1992. In 2026-09-23 benbrandt asked: "It seems more about controlling subagents from the client? Is this still needed?" |
| 865 | PR (RFD) | Agent Status Update | anaslimem | 2026-03-27 → closed 2026-05-08 | closed by author | Proposed thinking / reading / writing / waiting / idle statuses. benbrandt pointed to the v2 `state_change` and said "reading/thinking/etc are inferrable from current tool calls" |
| 1847 | issue | Tool calls that outlive their turn have no protocol expression | ivebenfreed | 2026-08-01 → closed 2026-10-16 | closed (completed) | Asked exactly the backgrounded-shell question. Closed by benbrandt: "v2's entity based updates and lack of updates being so closely tied to a turn help here" |
| 2308 | PR | let the Agent decide whether background subagents survive a cancel | benbrandt | 2026-10-05 | merged | Cancel cascade limited to descendant work the cancelled work waits on |
| 2767 | PR | allow per-item cancels on restricted subagent sessions | benbrandt | 2026-11-06 | merged | A child's tool call or terminal can be cancelled with `tool_call/cancel` or `terminal/cancel` |
| 2896 | PR | describe parent work after the prompt response for subagents | benbrandt | 2026-11-19 | merged | Source of the v1 "outside a prompt turn" text quoted above |
| 2971 | PR (RFD) | work attribution for reactive continuations | j-asefa | 2026-11-21 | **open** | Asks how to attribute foreground work to "the accepted prompt or background entity that caused it". Cites Antigravity CLI (Google) over v1 returning `end_turn` while a command was still `in_progress`. Builds on v2. |
| 2848 | PR (RFD) | v2 session state and message scope annotations | slapoguzov | 2026-11-17 | **open** | Puts `x-state` and `x-scope` hints in the schema; "No existing message changes" |
| 2485 | PR (RFD) | per-turn, in-turn, subagent, and cumulative token usage | EugeneTheDev | 2026-10-26 | **open** | Usage attribution for subagents. `IdleStateUpdate.usage` is still unstable. |
| 2482 | PR (RFD) | report the model of each subagent | nikita-ashihmin | 2026-10-26 | closed | n/a |
| 1261 | PR (RFD) | steer and queue prompts sent while the Agent is working | kennethsinder | 2026-05-19 | **open** | The v2 Preview announcement says steering and queueing are not in Preview |
| 533 | PR (RFD) | Multi-Client Session Attach | joaommartins | 2026-02-18 | **open** | Referenced by the session/status RFD |

All rows come from `gh search issues` / `gh pr view` / `gh issue view` against REPO on 2026-11-28. **Confirmed.** The 2026-09-23 comment on #855 and the comments on #1847 were read via `gh api repos/.../issues/N/comments`. I did not search GitHub Discussions or Zulip (**Unverified** coverage).

## 3. Timeline

Dates are git tag creation dates (`git for-each-ref --sort=creatordate`), except where a PR date is given. "v1u" means `schema/v1/schema.unstable.json`, "v1s" means `schema/v1/schema.json`, and "v2" means `schema/v2/schema.json` or `schema.unstable.json`.

| Date | Version / tag | Change | Source |
|---|---|---|---|
| 2025-09-01 | (pre-1.13.7) | `terminal/kill` added; terminals stabilised (#81) | PR #53; `KillTerminalRequest` present in v1s from `schema-v1.13.7` |
| 2026-04-15 | n/a | session/status RFD drafted | PR #986 |
| 2026-07-20 | `schema-v2.0.0-alpha.1` | v2 Draft schema; `StateUpdate` exists in v2 from the first alpha | tags2 diff |
| 2026-08-19 | n/a (PR branch) | Subagents RFD initial draft, with separate `subagent_spawned` and `subagent_state_update` and a terminal lifecycle enum | RFD revision history |
| 2026-09-15 | n/a (PR branch) | Merged into one upsert `subagent_update`; Agent capability and per-child `close` removed | RFD revision history; #1992 comment |
| 2026-09-24 | n/a (PR branch) | Lifecycle enum replaced by work-state snapshots; `session_message` added | RFD revision history |
| 2026-09-30 | **1.24.0** / v2 alpha.6 | First release of `SubagentUpdate` {sessionId, title, description, capabilities, state, _meta}, `StateUpdate` (running/idle/requires_action/unknown/other), `SessionMessage`, `SessionMessageChunk` and `SubagentCapabilities` in v1u (`unstable_subagents`) | CHANGELOG 1.24.0; v1u diff |
| 2026-10-09 | 1.25.0 | `IdleStateUpdate.stopReason` reshaped: from a `$ref StopReason` to an open set that adds `error` with an optional JSON-RPC `error` object (#2281) | CHANGELOG; v1u diff |
| 2026-10-27 | 1.27.0 | **Breaking:** `senderSessionId` and `recipientSessionId` removed from `SessionMessageChunk` and kept only on `SessionMessage`, which gains `senderMessageId` and `recipientMessageId` (#2465). Semantics: `null` clears participant IDs (#2555). | CHANGELOG; v1u diff |
| 2026-10-30 | 1.28.0 / v2 alpha.11 | `cwd` and `additionalDirectories` added to `SubagentUpdate` (additive, #2593) | CHANGELOG; v1u diff |
| 2026-10-30 | v2 alpha.12 (PR #2738) | Background work: `background` list, `paused`, `tool_call/cancel`, `terminal/cancel` (v2 only) | PR #2738; tags2 diff |
| 2026-11-01 | 1.29.0 | `session/status` (unstable) added: `SessionStatus` open/saved | CHANGELOG; PR #2735 |
| 2026-11-10 | 1.31.0 | `SessionReference` and `session` `ToolCallContent` added (#2787) | CHANGELOG; v1u diff |
| 2026-11-13 | (docs) / v2 alpha.16 | Subagents RFD to Preview; v2 drops the `unstable_subagents` gate (#2817), and `SubagentUpdate` appears in v2 `schema.json` at alpha.16 | RFD history; tags2 diff |
| 2026-11-14 | **1.32.0** | Description changes only for subagent types (#2801) | CHANGELOG; v1u diff (structure unchanged) |
| 2026-11-18 | 1.33.0 | Description changes (subagent docs pages) | CHANGELOG |
| 2026-11-20 | **1.34.0** / v2 beta.1 | **Subagents stabilised in v1** (#2939); RFD Completed; ACP v2 Preview announced | CHANGELOG; announcement |
| 2026-11-21 | 1.34.1 | Docs reorder | CHANGELOG |
| 2026-11-23 | **1.35.0** (latest) / v2 beta.4 | `mcp/message` attribution (unstable, #2988). Nothing changes for subagents, state or status | CHANGELOG; `gh release list` |

### 3.1 Churn count (Confirmed by def-hash diffs across all 27 v1 schema tags, with descriptions stripped)

- **`SubagentUpdate`**: one structural change since its first release (1.28.0, additive). No rename, reshape or split in any released schema.
  - The two-notification design and the `subagentSessionId`, `name` and `task` fields existed only on the PR #1992 branch (2026-08-19 to 2026-09-30).
  - JetBrains AIR noted on #1992: "New adapters emit `task`". That shape was never in a schema tag. A scan of every schema JSON at every tag for `subagent_spawned` or `subagent_state_update` found no matches.
- **`StateUpdate`**: no structural change from 1.24.0 to 1.35.0 stable.
- **`IdleStateUpdate`**: one reshape (1.25.0, a widening of the stop reason).
- **`SessionMessageChunk`**: one breaking removal (1.27.0).
- **`SessionMessage`**: one additive change (1.27.0).
- **`SessionStatus` / `SessionStatusResponse`**: no change since 1.29.0.
- **1.32.0 unstable to 1.35.0 stable**: for `SubagentUpdate`, `StateUpdate`, `SubagentCapabilities`, `SubagentSessionCapabilities`, `SessionMessage`, `SessionMessageChunk`, `SessionReference`, `ToolCallContent` and `ClientCapabilities`, the structural diff is empty. The only difference is that `IdleStateUpdate.usage` is absent from stable, because it stays unstable.

So across released schemas there was **1 breaking reshape** (message chunk participant fields) and **1 semantic patch-rule change**, over 51 days (1.24.0 on 2026-09-30 to 1.34.0 on 2026-11-20). Counting the unreleased PR-branch period adds 2 more reshapes, and the whole period is about 3 months (2026-08-19 to 2026-11-20).

## 4. Stability process and versioning

**RFD stages** (Draft → Preview → Completed, plus "To be removed"):

> Draft means the proposal belongs in the RFD process, but it may still be waiting for design work […] Experimental implementation may begin, properly feature-gated with the RFD name
>
> Depending on what the RFD is about, "completed" is the only state that can represent a 1-way door (if there is a stability commitment involved), as changes might require a breaking change to the protocol after this point.
>
> **final decision is always made by the core team lead**
>
> — REPO/blob/main/docs/rfds/about.mdx

Governance: "a design team with the Zed team as the lead (BDFL)" (same file). **Confirmed.**

**Unstable guarantee.** Every unstable def's description in the v1 schema begins:

> **UNSTABLE** This capability is not part of the spec yet, and may be removed or changed at any point.
>
> — `schema.unstable.json` (e.g. `SubagentUpdate` at 1.32.0)

And:

> Unreleased features will be behind an `unstable` feature flag in the Rust crate.
>
> — REPO/blob/main/CONTRIBUTING.md

**Protocol version vs schema semver.** `protocolVersion` is an integer major. The schema packages use semver (`CHANGELOG.md`: "adheres to Semantic Versioning"):

> The protocol versions that appear in the `initialize` requests and responses are a single integer that identifies a **MAJOR** protocol version. This version is only incremented when breaking changes are introduced. […] The introduction of new capabilities is not considered a breaking change. Therefore, Clients and Agents **MUST** treat all capabilities omitted in the `initialize` request as **UNSUPPORTED**.
>
> — REPO/blob/main/docs/protocol/v1/initialization.mdx

Unstable fields did change shape in v1 minor releases (1.27.0). That is consistent with "may be removed or changed at any point", so semver protects the stable schema only. **Inferred.**

**Negotiating unstable features (v2 text; v1 has no equivalent section):**

> Negotiating `protocolVersion: 2` selects the baseline protocol, not all experimental features in `schema/v2/schema.unstable.json`. There is no connection-wide "unstable schema" flag. Implementations **MUST NOT** infer peer support from their own schema selection, generated types, or local feature flags.
>
> Experimental schema revisions may disagree about a feature's shape or semantics. A capability advertises the behavior defined for that feature, not compatibility with every revision of the unstable schema.
>
> — REPO/blob/main/docs/protocol/v2/extensibility.mdx

**v2 Preview status:**

> **v2 is a Preview.** We think v2 is ready to build on, and we will aim to avoid large breaking changes from here, but it is not frozen. […] Adding v2 support still should not mean dropping v1. v1-only Agents and Clients will be common for some time.
>
> — REPO/blob/main/docs/announcements/acp-v2-preview.mdx (published 2026-11-20, Ben Brandt, "Zed Industries / ACP Lead Maintainer")

The same announcement says the Rust SDK has v2 "behind the `unstable_protocol_v2` feature", and lists Rust, TypeScript, Kotlin and Python as having v2 APIs. It does not mention Go. **Confirmed** (Go SDK v2 status is out of scope here, so **Unverified**).

**SDK preservation of unstable fields** (an adoption risk the RFD names):

> SDKs that strip the draft fields force temporary out-of-band negotiation bridges such as `_meta`-scoped capability flags.
>
> — subagents RFD, "Rollout plan" step 5

JetBrains AIR used `_meta.jetbrains.air.capabilities = ["nativeSubagentSessions"]` for exactly this reason (#1992 comment by nikita-ashihmin, 2026-08-26). **Confirmed.**

## 5. Background shell commands

v1 (stable, current):

- **Terminals.** v1 terminals are created and owned by the **Client** (`terminal/create`). "The command keeps running in the background while the Agent does other work." `terminal/kill` "terminates a command without releasing the terminal", and `terminal/release` "kills the command if still running" (REPO/blob/main/docs/protocol/v1/terminals.mdx). Nothing in v1 describes an Agent-owned process outliving its tool call or its prompt turn. **Confirmed.**
- **Tool call status.** v1 `ToolCallStatus` is only `pending | in_progress | completed | failed`. It has no `paused` and no `cancelled` (stable schema 1.35.0). **Confirmed.**
- **Cancellation.** v1 cancellation expects the Agent to finish updates before the prompt response:

  > The Agent **MAY** send `session/update` notifications with content or tool call updates after receiving the `session/cancel` notification, but it **MUST** ensure that it does so before responding to the `session/prompt` request.
  >
  > — docs/protocol/v1/prompt-turn.mdx

  > The Client **SHOULD** preemptively mark all non-finished tool calls pertaining to the current turn as `cancelled` as soon as it sends the `session/cancel` notification.
  >
  > — same file
- **Late updates.** The one stable v1 permission for updates after the prompt response is the subagent rule in §2.1, and it applies only to Clients that advertise `subagents`. **Confirmed.**
- **No v1 form for background work.** The Background Work RFD says v1 Clients "rely on adapter-specific extensions" (§2.2). Issue #1847, which raised the dev-server case and the `in_progress`-forever case, was closed in favour of v2. **Confirmed.**

v2 (Preview): background shells are Agent-owned display terminals listed in `state_update.background` and cancellable with `terminal/cancel`. Watchers and schedules are tool calls kept `in_progress`, `pending` or `paused` (§2.2). **Confirmed.**

There is no "task list" proposal. The RFD rejects a task entity: "Why not a new background task entity? […] A separate task entity would duplicate that status and output". **Confirmed.**

## 6. Competing proposals and objections

- **Subagents.** The competing RFD #855 (Client-driven discovery and delegation) was closed on 2026-10-22. I found no open objection after stabilisation. The only open subagent-adjacent RFDs are additive: token usage (#2485) and session-list row metadata (#2323). **Confirmed** for PRs and issues. Zulip and Discussions were not checked (**Unverified**).
- **Background work.**
  - The public review is mostly implementation feedback. zhahaoyu (2026-10-25, #2452) raised a follow-up-turn race, which became the grace-period rule. slapoguzov made a wording review.
  - An open v2 RFD, #2971, comes from an Antigravity (Google) CLI observation. It wants attribution of foreground passes to the background entity that triggered them, which would likely add optional fields to `state_update` or events. The RFD says "exact field names […] are open for discussion". **Confirmed** that it is open; its outcome is **Unverified**.
- **Adoption.** `codex-acp` had not implemented background work as of 2026-11-18, and Zed gates v1 subagents behind `acp-beta`. **Confirmed** (RFD status text).
- **Steering and queueing (#1261) and remote transports.** These are explicitly outside the v2 Preview. They are said to "add methods and capabilities without changing existing messages". **Confirmed** (announcement).

## 7. Implications for this bridge (Inferred)

1. **Subagents.** Exposing Crush subagents as v1 child sessions is now building on a stable, Completed feature that has kept its released shape since 1.24.0, apart from additive changes. The 1.32.0 types in acp-go-sdk v0.20.0 are wire-identical to stable. The risk now lies in Clients advertising `subagents`, which Zed does only behind `acp-beta`, rather than in the spec moving.
2. **Background jobs on v1.** These have no standard representation. Each option has a cost:
   - **Leave tool calls `in_progress` across turns.** This contradicts no stable text, but issue #1847 shows Clients cannot tell a backgrounded call from a wedged one, and the cancel rules expect turn-scoped finalisation.
   - **Use a private `_meta` or `_`-prefixed extension.** This is what claude-agent-acp and codex-acp did, and v2 aims to retire it.
   - **Model the job as an exposed one-shot subagent child**, whose `state` can stay `running` after the parent's response. This bends the semantics: a shell is not a session.
3. **v2.** v2's design for background work (terminals, tool calls, `background` list) is in Preview with a stated aim to "avoid large breaking changes". It requires speaking protocol v2. Its fit depends on Go SDK v2 support, which is outside this report.

## Contradictions and open questions

- **Def name.** The starting fact names `SessionSubagentUpdate`; the schema def is `SubagentUpdate`. A Go SDK type name could differ from the schema def (**Unverified**: I did not inspect the SDK's Go source).
- **"Foreground work" vs session state in v1.** `StateUpdate` exists in v1 stable only as the child snapshot on `subagent_update`. There is no v1 way to report the parent's own work state after its prompt response; the v1 page says "No `session/prompt` response reports that this work stopped." It is an open question how a v1 Client knows that parent work started by a child's report has ended.
- **Pre-release field names.** JetBrains AIR's adapters emitted `task` (a field that never shipped in a tag). Any Client built against PR-branch shapes may disagree with the released `title` / `description` fields.
- **Merge vs RFD dates.** PR #1992 was created 2026-08-20 and the RFD's initial draft is dated 2026-08-19. Consistent, but the RFD dates are self-reported.
- **Coverage.** Zulip (where the RFD process says "Detailed discussions often happen") and GitHub Discussions were not searched. Objections raised only there would be missed.
- **Will background work reach v1?** The RFD leaves the door open ("could be proposed for v1 separately"). No such PR was found as of 2026-11-28.

## References

All accessed 2026-11-28.

- Repository: https://github.com/agentclientprotocol/agent-client-protocol (formerly zed-industries/agent-client-protocol)
- Subagents RFD: https://github.com/agentclientprotocol/agent-client-protocol/blob/main/docs/rfds/subagents.mdx · https://agentclientprotocol.com/rfds/subagents
- v1 Subagents page: https://github.com/agentclientprotocol/agent-client-protocol/blob/main/docs/protocol/v1/subagents.mdx · https://agentclientprotocol.com/protocol/v1/subagents
- v2 Background Work RFD: https://github.com/agentclientprotocol/agent-client-protocol/blob/main/docs/rfds/v2/background-work.mdx
- Session status RFD: https://github.com/agentclientprotocol/agent-client-protocol/blob/main/docs/rfds/session-status.mdx
- v2 Terminal Output RFD: https://github.com/agentclientprotocol/agent-client-protocol/blob/main/docs/rfds/v2/terminal-output.mdx
- v2 Client Filesystem and Terminal RFD: https://github.com/agentclientprotocol/agent-client-protocol/blob/main/docs/rfds/v2/client-filesystem-terminal-capabilities.mdx
- RFD process: https://github.com/agentclientprotocol/agent-client-protocol/blob/main/docs/rfds/about.mdx
- v1 initialization (protocol version): https://github.com/agentclientprotocol/agent-client-protocol/blob/main/docs/protocol/v1/initialization.mdx
- v1 terminals: https://github.com/agentclientprotocol/agent-client-protocol/blob/main/docs/protocol/v1/terminals.mdx
- v1 prompt turn: https://github.com/agentclientprotocol/agent-client-protocol/blob/main/docs/protocol/v1/prompt-turn.mdx
- v2 extensibility: https://github.com/agentclientprotocol/agent-client-protocol/blob/main/docs/protocol/v2/extensibility.mdx
- v2 Preview announcement: https://github.com/agentclientprotocol/agent-client-protocol/blob/main/docs/announcements/acp-v2-preview.mdx
- v1 schema CHANGELOG: https://github.com/agentclientprotocol/agent-client-protocol/blob/main/schema/v1/CHANGELOG.md
- Schemas at tags: https://github.com/agentclientprotocol/agent-client-protocol/tree/schema-v1.24.0/schema/v1 · …/tree/schema-v1.32.0/schema/v1 · …/tree/schema-v1.34.0/schema/v1 · …/tree/schema-v1.35.0/schema/v1 · …/tree/schema-v2.0.0-beta.4/schema/v2
- Releases: https://github.com/agentclientprotocol/agent-client-protocol/releases
- PRs: #53 https://github.com/agentclientprotocol/agent-client-protocol/pull/53 · #855 …/pull/855 · #865 …/pull/865 · #986 …/pull/986 · #1261 …/pull/1261 · #1992 …/pull/1992 · #2308 …/pull/2308 · #2452 …/pull/2452 · #2465 …/pull/2465 · #2485 …/pull/2485 · #2593 …/pull/2593 · #2735 …/pull/2735 · #2738 …/pull/2738 · #2767 …/pull/2767 · #2787 …/pull/2787 · #2800 …/pull/2800 · #2817 …/pull/2817 · #2848 …/pull/2848 · #2896 …/pull/2896 · #2939 …/pull/2939 · #2971 …/pull/2971
- Issues: #1847 https://github.com/agentclientprotocol/agent-client-protocol/issues/1847
- Local: `~/go/pkg/mod/github.com/coder/acp-go-sdk@v0.20.0/schema/` (`version`, `schema.unstable.json`, `meta.unstable.json`)
