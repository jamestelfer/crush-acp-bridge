# 13. Ask Crush's questions via form elicitation, with a chat fallback

- **Status**: Accepted
- **Date**: 2026-11-27
- **Supersedes**: [ADR 11](0011-questions-as-permission-requests.md)

## Context

Crush's `question` tool raises a `question_batch_request` event carrying one to five questions (`yes_no`, `single_choice`, `multi_choice` or `free_text`), with optional `confirm_title` and `confirm_description`, and blocks until a client answers through `POST …/questions/answer` or cancels through `POST …/questions/cancel`. When the batch is resolved, Crush publishes `question_batch_notification`.

[ADR 11](0011-questions-as-permission-requests.md) sent `yes_no` and `single_choice` questions as `session/request_permission` requests (even when the editor advertised form elicitation) and used `elicitation/create` only for `free_text` and `multi_choice`. That approach has four structural flaws:

- **Silent answer fabrication.** `session/request_permission` has no neutral option kind, so choices use `allow_once`. Any editor or mode that approves permissions without asking (such as `acpx --approve-all` or auto-approve modes) silently answers `Yes` or the first choice on the user's behalf.
- **Degraded UX on elicitation-capable editors.** Mixing permissions and elicitations splits one batch across different UI cards, clips long labels in single-line permission buttons, drops option descriptions, ignores `confirm_title` and `confirm_description`, and cannot carry free-text fill-in alongside a choice question.
- **Protocol mismatch.** ACP stabilized `elicitation/create` in July 2026 specifically for structured user input, leaving `session/request_permission` for tool authorization. OpenCode v2, `claude-agent-acp`, `codex-acp` and `oh-my-pi` all route question tools exclusively through form elicitation and never overload permissions.
- **Stale prompts and late-answer risk.** ADR 11 assumed ACP could not withdraw an open request. In fact, ACP defines `$/cancel_request`, and `acp-go-sdk` sends it automatically when the outbound request's `context.Context` is cancelled. Moreover, Crush's `POST …/questions/answer` ignores `batch_request_id` and resolves whichever batch is currently pending in the workspace, so a late answer after external resolution can answer an unrelated later batch.

  **Amended:** Crush v0.102.0 routes `questions/answer` and `questions/cancel` by `batch_request_id` (`internal/backend/question.go`; `questionService.Answer` in `internal/question/question.go` returns false when that batch is not pending; the Crush change is "route question answers and cancels by batch id"). The late-answer hazard remains for older Crush, and the decision is unchanged.

Client adoption of `elicitation.form` is still uneven: Zed, Qt Creator 21, Obsidian Copilot and `acpx` advertise it, whereas Paseo 0.17.x and `avante.nvim` do not yet. Unlike agents that own the model's tool list, the bridge cannot hide Crush's `question` tool. Calling `POST …/questions/cancel` also does not serve as a silent fallback: Crush treats cancellation as `StopTurn = true` (`User cancelled this question`) and ends the turn immediately before the model can ask in chat.

However, Crush's `POST …/questions/answer` accepts empty `QuestionResponse` items carrying a `notes` map (`"_question": "…"`). Crush's `formatAnswer` formats these as a normal tool result (`StopTurn = false`), allowing the model to read the note, ask the user in plain chat text (or state assumptions and proceed), and finish the turn normally with `end_turn`.

## Decision

The bridge stops using `session/request_permission` for Crush's `question` tool. Permission requests are reserved strictly for tool authorization. Implementation is tracked in [#40](https://github.com/jamestelfer/crush-acp-bridge/issues/40).

Question handling is structured as a strategy selected at `initialize` from `clientCapabilities.elicitation.form` (present and non-null):

1. **Chat fallback strategy (default when `elicitation.form` is absent).**
   - Updates the question tool call with the batch summary so the editor sees what the model tried to ask.
   - Immediately resolves the batch through `POST …/questions/answer` with one empty `QuestionAnswer` per question and a `_question` note stating that this client cannot display interactive question forms and instructing the model not to call `question` again, and instead to ask the user in plain text in its reply or state assumptions and proceed.
   - Never calls `session/request_permission`, `elicitation/create` or `POST …/questions/cancel`.

2. **Form elicitation strategy (when `elicitation.form` is advertised).**
   - Sends the entire batch (1 to 5 questions) in a single `elicitation/create` (`mode: "form"`) request scoped to `sessionId` and `toolCallId`. The message carries `confirm_title` / `confirm_description` (or the single question text / batch summary).
   - Maps every Crush question type to flat restricted JSON Schema properties keyed `q0`, `q1`, …:
     - `yes_no`: `type: "string"` with titled `oneOf` (`"yes"` / `"Yes"`, `"no"` / `"No"`), avoiding unchecked-checkbox ambiguity between `false` and skipped.
     - `single_choice`: `type: "string"` with titled `oneOf` (`const`, `title`, optional `description`) plus an optional `qN_custom` string property for free-text fill-in.
     - `multi_choice`: `type: "array"` with titled `items.anyOf` (`const`, `title`, optional `description`) plus an optional `qN_custom` string property.
     - `free_text`: `type: "string"`.
   - Leaves choice fields optional (not in `required`) so clients such as Zed do not lock the first radio option when the user enters custom text or skips a question.
   - **Secret-collection guard.** Following the ACP elicitation spec, form mode must not collect secrets or credentials. If a question's text, label, description or choice labels match credential patterns, the strategy delegates the batch to the chat fallback strategy instead of sending `elicitation/create`.
   - **Response handling:**
     - `accept`: decodes answered fields back to `crush.QuestionAnswer` (validating choice IDs against offered choices; carrying both a `single_choice` pick and `qN_custom` via `Notes["_question"]`, and both `SelectedIDs` and `FillInText` for `multi_choice`; omitted fields become skipped answers) and posts to `POST …/questions/answer`.
     - `decline`: posts all-skipped `QuestionAnswer` entries to `POST …/questions/answer` so the turn continues.
     - `cancel` (or any unrecognised action): calls `POST …/questions/cancel`, ending Crush's turn.
     - Transport or protocol failure (such as `-32601` while the request context is still live): delegates to the chat fallback strategy.

3. **Cancellation and external resolution.**
   - Each in-flight question batch runs with a derived `context.Context`, not `serveCtx`.
   - On `session/cancel` or when Crush publishes `question_batch_notification` (for example, when a Crush TUI answers the batch first), the bridge cancels that context. `acp-go-sdk` sends `$/cancel_request` to dismiss the editor's open form, and the bridge skips posting any late `questions/answer` or `questions/cancel` call to Crush.
   - Batch deduplication across SSE resubscribes stays in the shared dispatch wrapper so a re-delivered pending batch is never asked twice.

## Consequences

- Auto-approving editors and permission modes can never fabricate answers to Crush's questions.
- Elicitation-capable editors render a whole multi-question batch in a single native form, with option descriptions, multi-select, custom fill-in answers and review before submission.
- Editors without `elicitation.form` (such as Paseo 0.17.x) no longer show clickable buttons for `yes_no` or `single_choice` questions; instead, the model recovers seamlessly in the same turn and asks the user in chat. Once an editor adds `elicitation.form`, the bridge upgrades automatically with no configuration change.
- Crush prefixes empty answers with `"User skipped this question"` before the `_question` note, so the note wording must clearly attribute the skip to client capability rather than user refusal.
- In `--shared` mode, if an elicitation-less ACP client and a Crush TUI both view the same session concurrently, the chat fallback resolves the batch immediately before the TUI user can answer it.
- Cancelling a turn or answering a batch in another Crush client withdraws the editor's open elicitation via `$/cancel_request` and avoids resolving a later batch by mistake.
- The existing permission-based question code and its tests are removed cleanly before rebuilding around the strategy interface ([#40](https://github.com/jamestelfer/crush-acp-bridge/issues/40)).
