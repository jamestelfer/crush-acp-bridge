# Background shells and subagents: Crush's model and how other ACP agents map theirs

Research note feeding an architecture decision for the bridge. Accessed 2026-11-28 unless marked otherwise.

Marks: **Confirmed** means read in source or spec at the cited tag. **Inferred** means it follows from confirmed code but nothing ran to prove it. **Unverified** means the claim was not checked against a primary source.

Versions read:

| Project | Ref read | ACP layer |
|---|---|---|
| Crush | `v0.103.0` (pinned in `mise.toml`); `main` compared, see [Changes on Crush `main`](#changes-on-crush-main-since-v01030) | none (HTTP + SSE server) |
| Bridge | working tree on `verify-issue-40-acceptance-criteria` | `github.com/coder/acp-go-sdk v0.20.0` |
| claude-agent-acp | `v0.113.0` | `@agentclientprotocol/sdk 2.12.0`, `@anthropic-ai/claude-agent-sdk 0.3.331` |
| codex-acp | `v2.12.1` (TypeScript over the Codex app server) | `@agentclientprotocol/sdk ~2.9.1`, `@openai/codex ^0.172.0` |
| Gemini CLI | `v0.71.1` | `@agentclientprotocol/sdk 0.16.1` |
| opencode | `v2.4.2` (Rust crate `packages/cli-rs/crates/acp`) | own Rust types |
| goose | `v1.61.1` | `agent-client-protocol 2.2.0`, schema `=1.9.1` |
| ACP spec | release `schema-v1.35.0` (2026-11-23) | — |

## Comparison

| Agent | Background shell mapping | Subagent mapping | Uses `subagent_update`? | Client terminals (`terminal/*`)? | Updates after `session/prompt` returns? |
|---|---|---|---|---|---|
| **Crush 0.103.0** (what the bridge must map) | `bash` with `run_in_background` or auto-background after 60 s returns a shell ID; `job_output` / `job_kill` read and stop it. No wake when a shell ends. | Interactive server: `agent_dispatch` (returns at once), `agent_send`, `agent_status`, `agent_stop`. `agentic_fetch` moves to the background after 60 s. Child is a Crush session with `parent_session_id`. The child's report **starts a new parent turn** with no client prompt. | n/a | n/a: shells run in the server process (ADR 12) | Yes, Crush starts turns on its own: report wake-ups, plus child-session events at any time. |
| **Bridge today** | `bash` → `kind: execute`. `job_output` and `job_kill` → `kind: other`. Output arrives only with the tool result, because `tool_progress` is not consumed. | `agent_dispatch` and the others → `kind: other`. Every child-session event, permission requests included, is dropped. | No | No (ADR 12) | It forwards parent-session updates whenever they arrive. It never sends `subagent_update`. |
| **claude-agent-acp v0.113.0** | Bash goes through the `_meta` terminal extension (`terminal_info`, `terminal_output[_delta]`, `terminal_exit`) with `content: [{type:"terminal"}]`, and stays "running" until the task ends. AIR gets its own `async_task_*` updates. | Default: `Agent`/`Task` tool call, `kind: "think"`, child calls tagged `_meta.claudeCode.parentToolUseId`. With `clientCapabilities.subagents`: `subagent_update` + `session_message` (the RFD form). AIR gets `subagent_spawned` and `subagent_state_update`. | Yes, when the client opts in | No. Terminals are display-only, through `_meta` | Default: no. **It holds `session/prompt` open** until the turn's background subagents settle. With the RFD form or AIR's `backgroundSubagents`: yes, out of turn. |
| **codex-acp v2.12.1** | ACP v1: Zed `_meta` terminal convention, plus the AIR `asyncTasks` extension. ACP v2: `terminal_update` with a `background` list and `terminal/cancel`. | Without a contract: `collabAgentToolCall` → plain tool call, `kind: "other"`. With v1 `clientCapabilities.subagents: {}` or v2: `subagent_update`, `session_message`, `session` content. | Yes, when the client opts in (v1) and always on v2 | No (Unverified that no path calls `terminal/create`) | v2: yes ("A shell process that outlives its Codex turn is listed"). v1 without a contract: Unverified |
| **Gemini CLI v0.71.1** | `run_shell_command` with `is_background`, plus `list_background_processes` and `read_background_output` (by PID). No ACP-specific mapping. | `Kind.Agent` → `kind: "think"`. No child sessions. | No | No (`acp/` has no terminal calls) | Unverified |
| **opencode v2.4.2** | A background shell can wake a parent turn. Its updates go to the client after the prompt response. | Child updates are projected into the parent: tool-call IDs prefixed `childID:`, titles prefixed with the child title, `_meta["opencode/child-session"]`. Opt-in `_meta["opencode/child-session-updates"]` sends `opencode/session/child_update` instead. | No (own extension) | No (Unverified) | **Yes**: "Children and background shells outlive a cancelled turn, so their asks and the turns they wake still reach the client." |
| **goose v1.61.1** | No background-shell mapping found. Live shell output is `_meta.toolNotification` of type `live_output` on an `in_progress` `tool_call_update`. | Subagent tool activity is `_meta.toolNotification` (an MCP logging notification, logger `subagent:<id>`) on the parent's tool call. | No | No | Unverified |

## Crush 0.103.0

### Background shells

**Confirmed.** There are three tools: `bash`, `job_output` and `job_kill`. Parameters ([bash.go](https://github.com/charmbracelet/crush/blob/v0.103.0/internal/agent/tools/bash.go) L27–33):

```go
type BashParams struct {
	Description         string `json:"description" description:"A brief description of what the command does, try to keep it under 30 characters or so"`
	Command             string `json:"command" description:"The command to execute"`
	WorkingDir          string `json:"working_dir,omitempty" description:"The working directory to execute the command in (defaults to current directory)"`
	RunInBackground     bool   `json:"run_in_background,omitempty" description:"Set to true (boolean) to run this command in the background. Use job_output to read the output later."`
	AutoBackgroundAfter int    `json:"auto_background_after,omitempty" description:"Seconds to wait before automatically moving the command to a background job (default: 60)"`
}
```

Result metadata carries `background` and `shell_id` (L47–55):

```go
type BashResponseMetadata struct {
	StartTime        int64  `json:"start_time"`
	EndTime          int64  `json:"end_time"`
	Output           string `json:"output"`
	Description      string `json:"description"`
	WorkingDirectory string `json:"working_directory"`
	Background       bool   `json:"background,omitempty"`
	ShellID          string `json:"shell_id,omitempty"`
}
```

[job_output.go](https://github.com/charmbracelet/crush/blob/v0.103.0/internal/agent/tools/job_output.go) and [job_kill.go](https://github.com/charmbracelet/crush/blob/v0.103.0/internal/agent/tools/job_kill.go):

```go
type JobOutputParams struct {
	ShellID string `json:"shell_id" description:"The ID of the background shell to retrieve output from"`
	Wait    bool   `json:"wait" description:"If true, block until the background shell completes before returning output"`
}
type JobOutputResponseMetadata struct {
	ShellID          string `json:"shell_id"`
	Command          string `json:"command"`
	Description      string `json:"description"`
	Done             bool   `json:"done"`
	WorkingDirectory string `json:"working_directory"`
}
type JobKillParams struct {
	ShellID string `json:"shell_id" description:"The ID of the background shell to terminate"`
}
```

Tool descriptions, verbatim ([job_output.md](https://github.com/charmbracelet/crush/blob/v0.103.0/internal/agent/tools/job_output.md), [job_kill.md](https://github.com/charmbracelet/crush/blob/v0.103.0/internal/agent/tools/job_kill.md), and [bash.md.tpl](https://github.com/charmbracelet/crush/blob/v0.103.0/internal/agent/tools/bash.md.tpl) `<background_execution>`):

> Get stdout/stderr from a background shell by ID; returns whatever the job has printed so far. Call it when you need the job's result, not right after starting the job, and set wait=true only when blocking until the job finishes is what you want.

> Terminate a background shell process. … The process is terminated immediately (similar to SIGTERM) … After killing, the shell ID becomes invalid

> 4. Auto-Background: Commands exceeding 1 minute (default, configurable via `auto_background_after`) automatically move to background and return shell ID
>
> - Set run_in_background=true to run commands in a separate background shell
> - Returns a shell ID for managing the background process
> - Do NOT call job_output right after starting a job. It returns the output so far, and a job that just started has printed nothing
> - job_output with wait=true blocks until the job finishes, giving back the time you just bought; only wait when waiting is the useful thing to do
> - Use job_kill tool to terminate a background shell

**Lifecycle (Confirmed, `bash.go` `executeBashCommand` L353–481, [shell/background.go](https://github.com/charmbracelet/crush/blob/v0.103.0/internal/shell/background.go)):**

- Every command, foreground or background, starts through `BackgroundShellManager.Start(context.Background(), …)`, so it survives the tool call's context: "Use background context so it continues after tool returns".
- With `run_in_background`, the tool returns `Background shell started with ID: %s\n\nUse job_output tool to view output or job_kill to terminate.` after a short fast-failure check.
- A foreground command waits for completion, the auto-background timer (default 60 s) or cancellation. Cancellation before the hand-off kills the shell. After the timer, the tool returns `Command is taking longer than expected and has been moved to background.\n\nBackground shell ID: %s …` with `background: true` and `shell_id` in the metadata.
- The manager is a **process-wide singleton**. IDs are a global counter formatted `%03X` (`001`, `002`, …). `Get(id)` does no session check, so any session in the server can read or kill any shell by ID. Shells are killed only by `job_kill`, by natural exit, or by `KillOwned(app)` at app shutdown ([app.go](https://github.com/charmbracelet/crush/blob/v0.103.0/internal/app/app.go) L1056). Cancelling a turn or deleting a session does not stop a detached shell.
- Limits:
  - `max_background_jobs` defaults to 50 running ([config.go](https://github.com/charmbracelet/crush/blob/v0.103.0/internal/config/config.go) L482–537); finished jobs do not count.
  - At most 250 finished jobs stay readable (`MaxFinishedJobs`), for 8 hours (`CompletedJobRetention`).
  - Each of stdout and stderr is capped at 1 MiB (`maxKeptOutput = 1 << 20`, `capped_buffer.go`).
  - Tool output is truncated at 30 000 characters, with the rest spilled to a file.
- **No completion signal.** Nothing in 0.103.0 wakes the agent or emits an event when a background shell exits. The only consumers of the manager are `bash`, `job_output`, `job_kill` and shutdown. The model learns of the exit only by calling `job_output`.
- **Live output.** While a foreground `bash` call or a `job_output wait=true` call runs, Crush publishes `tool_progress` SSE events ([progress.go](https://github.com/charmbracelet/crush/blob/v0.103.0/internal/agent/tools/progress.go)). They are polled every 200 ms, carry a tail of at most 64 KiB, and are display-only. They are not emitted for a detached shell that no tool call is waiting on.

```go
type ToolProgress struct {
	SessionID  string `json:"session_id"`
	ToolCallID string `json:"tool_call_id"`
	ShellID    string `json:"shell_id,omitempty"`
	Output     string `json:"output"`
}
```

### Subagents

**Which tools exist depends on the coordinator's interactivity (Confirmed, [coordinator.go](https://github.com/charmbracelet/crush/blob/v0.103.0/internal/agent/coordinator.go) L1070–1088):**

```go
		case AgentToolName:
			return isSubAgent || c.interactive
		case AgentDispatchToolName, AgentSendToolName, AgentStatusToolName, AgentStopToolName:
			return isSubAgent || !c.interactive
```

The predicate deletes a tool when it returns true. An interactive coordinator gets `agent_dispatch`, `agent_send`, `agent_status` and `agent_stop`, and **not** the blocking `agent`. A headless one (`crush run`) gets only `agent`. Subagents get neither.

**The bridge talks to an interactive coordinator (Inferred).**

- `app.New` calls `app.InitCoderAgent(ctx)` (interactive) whenever the config is set up ([app.go](https://github.com/charmbracelet/crush/blob/v0.103.0/internal/app/app.go) L266).
- `POST …/agent/init` with `interactive:false` cannot downgrade an existing coordinator ([backend/agent.go](https://github.com/charmbracelet/crush/blob/v0.103.0/internal/backend/agent.go) `initAgent`).
- The bridge never calls `/agent/init` (`internal/crush/client.go`).
- Exception: a workspace whose agent the channel path initialised headlessly first ([backend/channels.go](https://github.com/charmbracelet/crush/blob/v0.103.0/internal/backend/channels.go) L80) would offer `agent` instead.

Tool descriptions, verbatim ([templates/](https://github.com/charmbracelet/crush/tree/v0.103.0/internal/agent/templates)):

> **agent_dispatch**: Start a subagent that runs on its own and reports back later.
>
> The call returns as soon as the subagent starts. It returns the subagent's name, which is your `label` with a first name in front (`date-impl` becomes something like `betty-date-impl`). Its result arrives as a `<subagent-report>` message under that name, once it finishes. … When nothing is left that does not, end your turn: the report wakes you with a new turn, so ending one here is not abandoning the task. …
>
> Subagents keep running if the user interrupts your turn. After it reports, a subagent can still take follow-ups through `agent_send`.

> **agent_send**: … If the subagent is still running, the message reaches it at its next step and the answer folds into its report. If it has finished, it picks the message up as a follow-up and sends a new report. This call returns only a delivery confirmation; the answer arrives later as a `<subagent-report>` under the same name.

> **agent_status**: With no `label`, it lists every subagent in this session: state (running, finished, failed, stopped, or interrupted by a restart) …

> **agent_stop**: … A stopped subagent sends no report. Its session is kept, so `agent_send` can resume it later with its work in context.

> **agent** (headless only): … Emit several agent calls in one message and they run concurrently (up to 5 at a time). … The call does not return until the subagent finishes.

> **agentic_fetch**: … If a call runs long it moves to the background and returns a notice with a label; its result arrives later as a report under that label and wakes you …

Parameters ([agent_dispatch.go](https://github.com/charmbracelet/crush/blob/v0.103.0/internal/agent/agent_dispatch.go) L44–92, [agent_tool.go](https://github.com/charmbracelet/crush/blob/v0.103.0/internal/agent/agent_tool.go) L20–23):

```go
type AgentDispatchParams struct {
	Prompt    string `json:"prompt" …`
	Label     string `json:"label" …`
	Access    string `json:"access,omitempty" …`   // "read" (default) | "write"
	Model     string `json:"model,omitempty" …`
	Provider  string `json:"provider,omitempty" …`
	Reasoning string `json:"reasoning,omitempty" …`
}
type AgentSendParams struct { Label, Message, Model, Provider, Reasoning string }
type AgentParams struct { Prompt string; Access string }
```

`agent_dispatch` returns `Started subagent %q.` with metadata `{label, model?, provider?, reasoning?, run_model?}`. An auto-backgrounded `agentic_fetch` (or a headless `agent`) returns `AgentBackgroundMetadata{label, backgrounded: true, access, task}` and the notice text "Still working after %s, so this subagent moved to the background as %q …" ([agent_background.go](https://github.com/charmbracelet/crush/blob/v0.103.0/internal/agent/agent_background.go)).

**Child session creation (Confirmed).** `runSubAgent` ([coordinator.go](https://github.com/charmbracelet/crush/blob/v0.103.0/internal/agent/coordinator.go) L1991–2033) calls `CreateTaskSession(ctx, "<assistantMessageID>$$<toolCallID>", parentSessionID, title)` ([session.go](https://github.com/charmbracelet/crush/blob/v0.103.0/internal/session/session.go) L119–131, L460):

- The child's ID is derived from the tool call that launched it. The code comment says this lets a UI "walk back from a child-session event to this tool's chat item and nest the subagent's tool calls under it".
- Titles: `"Subagent: <label>"` for a dispatch, `"New Agent Session"` for `agent`, `"Fetch Analysis"` for `agentic_fetch`.
- The child inherits the parent's auto-approval (`InheritAutoApproval`). Without it, the child's permission prompts carry the **child's** session ID.

**Blocking and concurrency (Confirmed).**

- `agent_dispatch` never blocks.
- `agentic_fetch` blocks up to `subAgentBackgroundAfter = 60 * time.Second`, then detaches.
- Both are registered with `fantasy.NewParallelAgentTool`, so several run concurrently.
- Labels are unique per parent session. Dispatching to a running label is refused.
- No dispatch cap was found in 0.103.0. The "up to 5" figure appears only in the headless `agent` description (Unverified as an enforced limit).

**Report delivery: Crush starts parent turns by itself (Confirmed, `agent_dispatch.go` L482–675, `coordinator.go` L394).**

- When a dispatched child finishes, `reportDispatchResult` calls `c.runReport(ctx, parentSessionID, report)`, which runs `c.run(…, report.PromptText())`: "it folds into a turn in progress, or starts one".
- The report is a user message whose content part is `sub_agent_report` (`{label, output, failed?, model?}`), rendered to the model as `<subagent-report label="…">…</subagent-report>` ([message/content.go](https://github.com/charmbracelet/crush/blob/v0.103.0/internal/message/content.go) L194–210, [proto/message.go](https://github.com/charmbracelet/crush/blob/v0.103.0/internal/proto/message.go) L523).
- A stopped subagent's outcome is recorded as `Stopped: <reason>.` without starting a turn.

**Cancellation (Confirmed, `coordinator.go` L1670–1681):**

```go
	// Detached subagents outlive the turn that started them, so cancelling
	// the parent's turn leaves them running; they report back when they
	// finish, like a background job. Cancelling a subagent's own session
	// stops that subagent.
```

So `POST …/agent/sessions/{childID}/cancel` stops one child. That has the same shape as ACP `session/cancel` on a child session.

**Busy state (Confirmed).**

- `GET …/sessions` and `GET …/sessions/{id}` compute `is_busy` per session through `IsSessionBusy` ([server/events.go](https://github.com/charmbracelet/crush/blob/v0.103.0/internal/server/events.go) `isSessionBusy`; [coordinator.go](https://github.com/charmbracelet/crush/blob/v0.103.0/internal/agent/coordinator.go) L1708). That is true while an agent has pending work for the ID, or while the ID is a tracked live sub-session.
- A running child therefore reads `is_busy: true` under its own ID.
- The parent reads busy only while its own turn runs (Inferred: `subSessionBusy` is keyed by the child's ID).
- `GET …/agent` `is_busy` is workspace-wide.

**Event streaming (Confirmed, `server/events.go` `wrapEvent` and `deliverToClient`).**

- Each workspace has one SSE stream (`GET …/events`). Message, session, file and agent events for **every** session, children included, go to every client unfiltered.
- A `session` event carries `parent_session_id`.
- Only permission, question and `tool_progress` events are session-scoped:

> Session-scoped prompt events are only delivered to clients currently viewing the session that raised them … Prompts raised by subagent sessions (agent tool, agentic fetch) resolve to the top-level session so the viewer of the parent session can answer them.

**Bridge recorded traces (Confirmed).** `testdata/*.sse` contain no `agent_dispatch`, `agentic_fetch`, `job_output`, `job_kill`, `tool_progress` or `sub_agent_report` events. The three bash traces have `"run_in_background":false`. A design built on this note needs new traces, recorded with `just record-trace`.

### What the bridge does with these today (Confirmed from `internal/bridge`, `internal/translate`)

- `toolKind` maps `bash` → `execute` and `agentic_fetch` → `fetch`. Everything else here, including `job_output`, `job_kill` and the four `agent_*` tools, maps to `other` (`internal/bridge/tool.go`).
- `forward` drops any update whose Crush session ID is not an ACP session the bridge holds (`a.session(u.SessionID) == nil`). Child-session messages are dropped. So are **child-session permission requests**. Crush routes those to the parent's viewer and waits for an answer, so a `write` subagent without inherited auto-approval would stall (Inferred).
- `translate` has no case for `tool_progress` or `sub_agent_report` parts.
- `session.end` ignores a `TurnEnded` when no ACP turn is active, so a report-driven Crush turn streams its chunks and tool calls to the editor as out-of-turn `session/update`s (Inferred). If such a turn is in flight when the user prompts, its `finish` may end the user's ACP turn early (Inferred, untested).

### Changes on Crush `main` since v0.103.0

`main` (2026-11-28) is 20 commits ahead of `v0.103.0`. `v0.104.0` (2026-11-27) changes none of the files above. Two unreleased changes touch background shells:

- [#4386](https://github.com/charmbracelet/crush/pull/4386) "stop job_output waits after two minutes". `wait` becomes "If true, block until the background shell completes, for at most 2 minutes, before returning output". A timed-out wait appends "Stopped waiting: the job is still running …".
- [#4440](https://github.com/charmbracelet/crush/pull/4440) "describe how a command ended in one place": the exit description goes through `shell.Describe(err)`.

No change to `agent_dispatch`, the report wake-up, the server's event routing or `is_busy`.

## Prior art detail

### claude-agent-acp v0.113.0

- **Subagent forms (Confirmed, [src/acp-subagents.ts](https://github.com/agentclientprotocol/claude-agent-acp/blob/v0.113.0/src/acp-subagents.ts)):**

  > - `rfd`: the subagents RFD (agentclientprotocol/agent-client-protocol#1992, as revised): `subagent_update` and `session_message`, which the SDK types. A client that declares `clientCapabilities.subagents` and is not AIR gets it.
  > - `air`: the earlier draft of that RFD, which AIR implements: `subagent_spawned` and `subagent_state_update` …

  The RFD form announces the child with `{sessionUpdate:"subagent_update", sessionId, title, description, cwd, capabilities:{cancel:{}}, state:{state:"running"}}` and sends the prompt as the child's first `session_message` ([src/native-subagents.ts](https://github.com/agentclientprotocol/claude-agent-acp/blob/v0.113.0/src/native-subagents.ts) `announceNativeSubagent`).
- **Fallback (Confirmed, [src/tool-calls/reporters/agent.ts](https://github.com/agentclientprotocol/claude-agent-acp/blob/v0.113.0/src/tool-calls/reporters/agent.ts)):** `Agent`/`Task` is a tool call with `kind: "think"`, titled with the description, and the prompt as display content. `SendMessage`, `TaskStop`, `ListAgents` and `Monitor` get an "agent control" reporter (`reporters/index.ts` L46). A subagent's commands carry `_meta.claudeCode.parentToolUseId`.
- **Background Bash (Confirmed, [src/v1/background-work.ts](https://github.com/agentclientprotocol/claude-agent-acp/blob/v0.113.0/src/v1/background-work.ts)):** "A command that went to the background: its terminal runs from its Bash tool result until its task ends." Output and exit arrive as `tool_call_update` with `_meta.terminal_output`/`terminal_output_delta` and `_meta.terminal_exit {exit_code:null}`. The opt-in is client `_meta.terminal_output` or `_meta.terminal_output_delta` ([src/tool-calls/client-capabilities.ts](https://github.com/agentclientprotocol/claude-agent-acp/blob/v0.113.0/src/tool-calls/client-capabilities.ts)). No `terminal/create` call exists. `content:[{type:"terminal", terminalId}]` refers to the display terminal named by the tool call ID.
- **Turn ownership (Confirmed, [src/v1/acp-agent.ts](https://github.com/agentclientprotocol/claude-agent-acp/blob/v0.113.0/src/v1/acp-agent.ts) L759–775):**

  > Set instead of settling when the turn's terminal result arrives while subagents it spawned are still live … The turn is held open — its `session/prompt` stays pending — so the subagents' streamed output, their permission requests (which would otherwise block on an RPC a client that stops consuming at the prompt response never answers — issue #866), and the model's task-notification followup summary all land inside the turn.
  >
  > An ACP v1 AIR client that declares `backgroundSubagents`, and an ACP v1 client in the `rfd` form of subagent sessions, get no hold … Such a client keeps listening after the prompt response, so the subagent updates, their permission requests and elicitations, and the followup summary go out after it, with the same routing. … a followup result is an out-of-turn autonomous result; a steer with no turn in flight starts a new turn …

### codex-acp v2.12.1

- **Confirmed** ([docs/subagent-sessions.md](https://github.com/agentclientprotocol/codex-acp/blob/v2.12.1/docs/subagent-sessions.md)):

  > A v1 Client opts in with `clientCapabilities.subagents: {}`. Without it, the adapter sends none of the updates below. … There is no Agent-side capability.

  > `state` reports `running`, `requires_action`, `idle` with a `stopReason`, or `unknown` when observability is lost. … There is no terminal child state.

  > Without either contract, the subagent lifecycle stays an ordinary ACP tool call. Child permission and elicitation requests stay on the root session.

- **Fallback (Confirmed, [src/tool-calls/reporters/CollabAgentReporter.ts](https://github.com/agentclientprotocol/codex-acp/blob/v2.12.1/src/tool-calls/reporters/CollabAgentReporter.ts)):** `kind: "other"`, title = Codex tool name, `rawInput` = `{prompt, senderThreadId, receiverThreadIds, agentsStates, model, reasoningEffort, status}`.
- **Background (Confirmed, [docs/background-work.md](https://github.com/agentclientprotocol/codex-acp/blob/v2.12.1/docs/background-work.md)):**

  > A shell process that outlives its Codex turn is listed by the existing display `terminalId`, not by a process handle or a duplicate tool call. … `session/cancel` leaves listed work and its pending approvals running. … ACP v1 does not have these updates or this method. The AIR async task extension remains available on ACP v1.

### Gemini CLI v0.71.1

**Confirmed.**

- [packages/cli/src/acp/acpUtils.ts](https://github.com/google-gemini/gemini-cli/blob/v0.71.1/packages/cli/src/acp/acpUtils.ts) L246–265 maps `Kind.Agent` → `'think'`.
- Background shells are ordinary tools: `run_shell_command` with `is_background`, plus `list_background_processes` (no params) and `read_background_output {pid, lines?, delay_ms?}` ([packages/core/src/tools/shellBackgroundTools.ts](https://github.com/google-gemini/gemini-cli/blob/v0.71.1/packages/core/src/tools/shellBackgroundTools.ts)). Both are `Kind.Read`.
- `packages/cli/src/acp/` contains no subagent session, terminal or out-of-turn code.

### opencode v2.4.2

**Confirmed** ([packages/cli-rs/crates/acp/src/turn.rs](https://github.com/anomalyco/opencode/blob/v2.4.2/packages/cli-rs/crates/acp/src/turn.rs) L1–2, L485–510; [child.rs](https://github.com/anomalyco/opencode/blob/v2.4.2/packages/cli-rs/crates/acp/src/child.rs); [translate.rs](https://github.com/anomalyco/opencode/blob/v2.4.2/packages/cli-rs/crates/acp/src/translate.rs) L51, L276–279):

> Each prompt runs in its own task so it can settle and respond after cancellation. A background follower tracks unfinished children and the parent turns their reports start. The next prompt inherits those children.

> // Children and background shells outlive a cancelled turn, so their asks and the turns they wake still reach the client.
> … // The prompt's response goes first.

> /// None while following children after the prompt responded: any delivery then starts a parent turn.

> /// `projectChild`: without the child capability, only the prompt turn that started a child shows its work.

opencode's runtime model is the closest to Crush's: children report back and wake parent turns, and background shells wake turns too. Its answer is to answer `session/prompt` first and keep streaming parent-session updates afterwards. Child work is folded into the parent with prefixed tool-call IDs (`format!("{}:{id}", child.id)`) unless the client opts in to its private `opencode/session/child_update` notification.

### goose v1.61.1

**Confirmed** ([crates/goose/src/acp/server/tool_notifications.rs](https://github.com/aaif-goose/goose/blob/v1.61.1/crates/goose/src/acp/server/tool_notifications.rs)): MCP notifications from a running tool become `tool_call_update {status: in_progress, _meta.toolNotification: {type: message|progress|platform_event|live_output, params}}`. Subagent tool requests arrive as `message` with `logger: "subagent:<id>"`. No `subagent_update`, no background-shell lifecycle.

### Other `subagent_update` emitters

`gh search code 'subagent_update'` (2026-11-28) also finds the following. None was read beyond the search hit (**Unverified**):

- Agents: `Ranvier-Technologies/pixir` (`lib/pixir/acp/translate.ex`), `sudoprivacy/sudocode` (`engine-acp`), `atrium-cloud/acp-stack` (placebo agent), `kirodotdev/KiroCrew`.
- Clients: `zed-industries/zed` (`crates/agent_servers/src/acp.rs`), `olimorris/codecompanion.nvim`, `open-pencil/open-pencil`.

## The spec on updates after `session/prompt` returns

**Confirmed** at `schema-v1.35.0`.

- Subagent sessions became stable v1 in schema 1.34.0 (2026-11-20, [#2939](https://github.com/agentclientprotocol/agent-client-protocol/pull/2939) "stabilize subagent sessions").
- "describe parent work after the prompt response for subagents" is [#2896](https://github.com/agentclientprotocol/agent-client-protocol/pull/2896).

[docs/protocol/v1/subagents.mdx](https://github.com/agentclientprotocol/agent-client-protocol/blob/schema-v1.35.0/docs/protocol/v1/subagents.mdx) L404–411:

> ### After the parent's prompt response
>
> The parent's `session/prompt` response does not mean that its children have stopped. The Agent keeps sending the updates and requests of a child that is still running.
>
> When such a child reports back, the parent can work on the result while no `session/prompt` request is pending. The Agent then sends the parent's updates and requests with the parent's `sessionId`, such as `agent_message_chunk`, `tool_call`, and `session/request_permission`. A Client that advertises `subagents` handles them as it does during a prompt turn.
>
> - No `session/prompt` response reports that this work stopped. The `state` field reports the state of child sessions only.
> - `session/cancel` with the parent's `sessionId` cancels this work. No response confirms the cancellation.

Same file, opening paragraphs:

> Exposing subagents is optional for Agents. An Agent can use subagents without exposing them and report their results through the parent session.

[docs/protocol/v1/prompt-turn.mdx](https://github.com/agentclientprotocol/agent-client-protocol/blob/schema-v1.35.0/docs/protocol/v1/prompt-turn.mdx) L306–308 and L663:

> When the Client advertises support, the Agent **MAY** send a live user-facing notice at any point while a session exists, including outside prompt processing

> The Agent **MAY** send `session/update` notifications with content or tool call updates after receiving the `session/cancel` notification, but it **MUST** ensure that it does so before responding to the `session/prompt` request.

So: out-of-turn parent updates are sanctioned for a client that advertises `subagents`, and `notice` updates for one that advertises `session.notices`. The base prompt-turn text has no general rule either way for any other client. The cancel rule only requires a cancelled turn's updates to arrive before its response.

## Contradictions and open questions

1. **Go SDK lags the spec.** acp-go-sdk v0.20.0 still labels `SessionSubagentUpdate` and `ClientCapabilities.Subagents` "**UNSTABLE** … not part of the spec yet". The v1 schema made them stable in 1.34.0. The types exist (`types_gen.go` L1035, L10642), so the bridge can emit `subagent_update` without a new dependency. Whether v0.20.0 matches the stabilised shape was not checked.
2. **Crush's `agent` description contradicts its code.** `agent_tool.md` says "The call does not return until the subagent finishes", but in interactive mode `runSubAgentMaybeBackground` would background it after 60 s. Interactive mode strips `agent`, so in practice the text is accurate for the only mode that offers it. It does not apply to `agentic_fetch`, which shares the path.
3. **Turn collision (open).** A report-driven Crush run and a user's ACP prompt can overlap on one parent session. Crush "folds into a turn in progress", but the bridge ends the ACP turn on the first `finish` it sees. Needs a recorded trace.
4. **Held turn or out-of-turn (decision).**
   - claude-agent-acp holds `session/prompt` open for clients without `subagents`, citing clients that stop listening at the response (its issue #866).
   - opencode and the spec's subagent section answer first and continue.
   - Crush's `agent_dispatch` invites the model to end its turn ("ending one here is not abandoning the task"), so holding would keep an ACP turn open for an unbounded time.
5. **Shell completion is silent.** Crush 0.103.0 emits nothing when a detached shell exits. A bridge that wants terminal-style "running until exit" display would have to poll `job_output`, which is a model tool, not an HTTP endpoint. No HTTP endpoint for background shells was found; this is Unverified, from `server/proto.go` routes only.
6. **Shell IDs are global and unauthenticated by session.** In a shared server, two editors' sessions share the ID space (`001`, `002`, …).
7. **Child permissions are dropped by the bridge today.** Crush routes them to the parent's viewer; the bridge ignores them because the child session is unknown. An unanswered request leaves the child waiting. How long, and whether a turn-end cancels it, is Unverified.
8. **"up to 5 at a time"** appears only in the headless `agent` description. Its enforcement inside `fantasy` was not traced (Unverified).
9. **codex-acp v1 without a contract** and **Gemini CLI**: whether either sends any `session/update` after the prompt response is Unverified.

## References

All accessed 2026-11-28.

Crush `v0.103.0`:
- `internal/agent/tools/bash.go`, `bash.md.tpl`, `job_output.go`, `job_output.md`, `job_kill.go`, `job_kill.md`, `progress.go`: https://github.com/charmbracelet/crush/tree/v0.103.0/internal/agent/tools
- `internal/shell/background.go`, `capped_buffer.go`: https://github.com/charmbracelet/crush/tree/v0.103.0/internal/shell
- `internal/agent/agent_tool.go`, `agent_dispatch.go`, `agent_background.go`, `agentic_fetch_tool.go`, `coordinator.go`, `templates/*.md`: https://github.com/charmbracelet/crush/tree/v0.103.0/internal/agent
- `internal/session/session.go`, `internal/message/content.go`, `internal/proto/message.go`, `internal/server/events.go`, `internal/server/proto.go`, `internal/backend/agent.go`, `internal/app/app.go`, `internal/config/config.go`
- Changes after the tag: [#4386](https://github.com/charmbracelet/crush/pull/4386), [#4440](https://github.com/charmbracelet/crush/pull/4440)

Bridge: `internal/bridge/bridge.go` (`forward`, `session.end`), `internal/bridge/tool.go`, `internal/translate/translate.go`, `internal/crush/sessions.go`, `testdata/*.sse`, [ADR 12](../../adr/0012-no-editor-fs-terminal-delegation.md), [ADR 6](../../adr/0006-session-load-and-list.md).

acp-go-sdk v0.20.0: `types_gen.go` (`SessionSubagentUpdate`, `SubagentCapabilities`, `SessionReference`), `MIGRATION_v0.20.md`: https://github.com/coder/acp-go-sdk/tree/v0.20.0

claude-agent-acp `v0.113.0`: `src/acp-subagents.ts`, `src/native-subagents.ts`, `src/v1/background-work.ts`, `src/v1/acp-agent.ts`, `src/tool-calls/renderer.ts`, `src/tool-calls/client-capabilities.ts`, `src/tool-calls/reporters/agent.ts`, `src/tool-calls/reporters/index.ts`: https://github.com/agentclientprotocol/claude-agent-acp/tree/v0.113.0

codex-acp `v2.12.1`: `README.md`, `docs/subagent-sessions.md`, `docs/background-work.md`, `docs/air-extensions.md`, `src/ACPSessionConnection.ts`, `src/tool-calls/reporters/CollabAgentReporter.ts`: https://github.com/agentclientprotocol/codex-acp/tree/v2.12.1

Gemini CLI `v0.71.1`: `packages/cli/src/acp/acpUtils.ts`, `packages/cli/src/acp/acpSession.ts`, `packages/core/src/tools/shellBackgroundTools.ts`, `packages/core/src/tools/definitions/base-declarations.ts`: https://github.com/google-gemini/gemini-cli/tree/v0.71.1

opencode `v2.4.2`: `packages/cli-rs/crates/acp/src/turn.rs`, `child.rs`, `translate.rs`, `capabilities.rs`, `tools.rs`: https://github.com/anomalyco/opencode/tree/v2.4.2/packages/cli-rs/crates/acp

goose `v1.61.1`: `crates/goose/src/acp/server/tool_notifications.rs`: https://github.com/aaif-goose/goose/tree/v1.61.1/crates/goose/src/acp

ACP spec `schema-v1.35.0`: `docs/protocol/v1/subagents.mdx`, `docs/protocol/v1/prompt-turn.mdx`, `schema/v1/CHANGELOG.md`; PRs [#2939](https://github.com/agentclientprotocol/agent-client-protocol/pull/2939), [#2896](https://github.com/agentclientprotocol/agent-client-protocol/pull/2896), [#1992](https://github.com/agentclientprotocol/agent-client-protocol/pull/1992): https://github.com/agentclientprotocol/agent-client-protocol/tree/schema-v1.35.0
