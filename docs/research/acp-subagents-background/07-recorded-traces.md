# Recorded traces: subagent dispatch and background bash

Recorded 2026-11-28 against `crush server` v0.103.0 on loopback HTTP, model `vertexai/gemini-4-neon` (Crush's default for these projects). Projects were disposable directories under `/tmp/crushtrace/`. These traces test claims that 03 had marked **Inferred**.

Files in [`traces/`](traces/):

| File | How it was produced |
|---|---|
| `dispatch-write.sse` | `RECORD_SETTLE=1 RECORD_QUIET=20 scripts/record-trace http://127.0.0.1:8484 /tmp/crushtrace/dispatch … allow "Use the agent_dispatch tool to start one subagent whose task is to create the file notes.txt containing the single word hi. Do not wait for it: end your turn immediately after dispatching, saying only 'dispatched'."` |
| `background-bash.sse` | Same settings; prompts "Run the shell command 'sleep 25; echo finished' with the bash tool in the background (run_in_background true). Do not wait for it; reply only 'started'." then "Now use job_output with wait true to read that background job's output, then tell me exactly what it printed." |
| `bridge-stay-acp.log` | Wire log of `stay_client.py`, a minimal ACP v1 client. It runs `dist/crush-acp-bridge` against the same server, sends the dispatch prompt above, stays connected 120 s after the prompt returns, and allows every `session/request_permission` it receives |
| `stay_client.py` | That client |

`scripts/record-trace` was changed for these recordings:

- A turn now ends at the trace session's own `finish`, not at any session's.
- With `RECORD_SETTLE=1`, recording continues until the session tree is idle and the stream has been quiet for `RECORD_QUIET` seconds.

The traces are **not** in `testdata/`, so `TestEveryTraceReplays` does not run them yet.

## Findings

Event numbers are 1-based `data:` lines in the trace file.

### Subagents (`dispatch-write.sse`)

1. **The parent's turn ends before the child works.**
   - The parent ends with `finish reason=end_turn` (event 20) and `run_complete` text `dispatched` (22).
   - The child's `write` tool call happens later, at events 24–25.
   - **Confirmed.**
2. **Child session ID format is `<messageID>$$<toolCallID>`.** For example `9f5eb832-…$$call_9637016987247353631`, where the tool call ID is the parent's `agent_dispatch` call. **Confirmed**, matching 03.
3. **The child's permission request goes to the client viewing the parent.**
   - The recorder had made only the parent its current session, yet it received `permission_request` with `session_id` = the child (event 27).
   - It granted the request and the write proceeded (28–31).
   - **Confirmed.**
4. **The child's report starts a parent turn with no user prompt.**
   - Event 39 is a `role: "user"` message on the parent whose only content part is `{"type":"sub_agent_report","data":{"label":"noemie-create-notes","output":"Created `…/notes.txt` with content `hi`.","model":"vertexai/gemini-4-neon"}}`.
   - It is followed by a parent assistant `end_turn` (42), `agent_finished` (43) and `run_complete` "Done. Created …" (44).
   - **Confirmed.** An ACP client would see this second turn after `session/prompt` had already returned.
5. **`is_busy` on `session` events was `false` in every event,** for parent and child, including while each was working. Running and idle state cannot be read from `session` events. Usable turn-boundary signals: assistant `finish` parts, `agent_event` `agent_finished` (which carries `session_id`), and `run_complete` (which carries `session_id`). **Confirmed** for this trace. Whether `GET …/sessions` reports `is_busy: true` mid-turn was not checked.
6. **`GET …/sessions` does not list `agent_dispatch` children.**
   - After the runs, the listing for each workspace returned only the parent session, including `/tmp/crushtrace/dispatch`, whose child finished.
   - Fetching the child's messages directly by ID (`GET …/sessions/<msgID>$$<toolCallID>/messages`) works.
   - **Confirmed.** This contradicts `docs/crush-server.md` L16 ("Lists sessions, including sub-agent sessions") for dispatch children. That line may hold for other kinds of sub-agent; not checked.

### The bridge and child permissions (`bridge-stay-acp.log`)

7. **The bridge drops the child's permission request, and the child stalls.**
   - The client received the parent's `agent_dispatch` tool call (`kind: "other"`), the text `dispatched`, and `stopReason: "end_turn"` 21.5 s in.
   - In the following 120 s it received **no further messages**: no `session/request_permission`, and no updates from the child or from a report turn.
   - `notes.txt` was never created.
   - The child session's stored messages end with an assistant `write` tool call (`finished: true` input), with no tool result and no finish. The write is waiting on a permission that nobody answered.
   - **Confirmed.** This was 03's open question 7 and was Inferred there.
8. **acpx ends the same scenario at the turn.**
   - `just acpx` with the same prompt printed `[done] end_turn` and exited, which closes the bridge.
   - `notes.txt` was not created after 120 s.
   - Because the bridge was gone, this run does not isolate the bridge from the client. Finding 7 does.

### Background bash (`background-bash.sse`)

9. **A backgrounded `bash` is an ordinary, completed tool call.**
   - `run_in_background: true` → permission request (event 12) → `tool_result` "Background shell started with ID: 001\n\nUse job_output tool to view output or job_kill to terminate." (14) → parent `end_turn` (19).
   - **Confirmed.**
10. **`job_output` with `wait: true` blocks inside the next turn and returns the final state:** `tool_result` "Status: completed\n\nfinished\n" (27). **Confirmed.**
11. **No shell-lifecycle event appeared in the stream:** no `tool_progress` (0 occurrences), and no job or shell event of any type.
    - **Partly confirmed.** The job finished while `job_output` was waiting, so this trace does not test whether Crush emits anything when a shell exits during idle time.
    - The 0.103.0 source reading in 03 says it does not.

## Consequences for the v1 plan

- **Child permission routing is a real defect.** Any write-access subagent stalls under the bridge. Fixing it needs:
  - recognising child sessions (`parent_session_id`, or the `$$` ID form) on the shared SSE stream;
  - forwarding their `permission_request` to the ACP session of the root parent.
- **Report-driven turns are normal behaviour,** not an edge case. Every non-waiting dispatch produces one after the prompt has returned (finding 4). The bridge needs a stated policy for them.
- **Turn state must come from `finish`, `agent_finished` and `run_complete`,** not `is_busy` (finding 5). That matters for any future `subagent_update` state reporting.
- **Session listing cannot discover dispatch children** (finding 6). The bridge must learn of them from the event stream.
- **Background shells need nothing new to be correct;** they are already plain tool calls (findings 9–10).
