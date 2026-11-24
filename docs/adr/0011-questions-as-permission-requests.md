# 11. Ask Crush's questions as permission requests

- **Status**: Accepted
- **Date**: 2026-11-24

## Context

Crush's `question` tool sends a `question_batch_request` event: one to five questions, each `yes_no`, `single_choice`, `multi_choice` or `free_text`. The tool blocks until a client answers the batch through `POST …/questions/answer` or cancels it through `POST …/questions/cancel`. Cancelling ends the turn.

ACP has no question method. `session/request_permission` offers a list of named options and returns one, or a cancelled outcome. Every editor implements it, Paseo included. Form elicitation (`elicitation/create`) takes typed input, but an editor uses it only when it advertises `elicitation.form` at `initialize`. Paseo 0.17.3 does not.

## Decision

The code is in `internal/bridge/questions.go`.

- A tool call update lists the batch's questions first. Paseo renders a permission request from the tool call it holds.
- Each question is a separate permission request on the question tool call, in order. The title is the question, prefixed `(i/n)` in a batch of several. The content is its description and choices.
- `yes_no` offers Yes and No. `single_choice` offers one option per choice, keyed by the choice ID.
- `free_text` uses a form elicitation with one required string field when the editor offers form elicitation. Otherwise it offers only Skip, and its content says to reply in the chat.
- `multi_choice` uses a form elicitation with one multi-select array field, listing each choice ID and label, when the editor offers form elicitation. Otherwise it offers one option per choice and takes one choice.
- Once all are answered, the bridge sends the answers to Crush. A skipped or declined question sends an empty answer, which Crush reports to the model as skipped.
- A dismissed question, or a failed request, cancels the batch and asks nothing further.
- The batch ID is recorded as asked, so a batch Crush re-delivers after a resubscribe is not asked again.

## Consequences

- Questions work in every editor, with no protocol extension.
- Paseo users cannot type a free-text answer or pick several choices. They skip, then reply in the chat.
- ACP has no option kind for an answer, so Yes and every choice use `allow_once`. An editor or mode that approves permission requests without asking answers Yes, or the first choice, and the model acts on an answer the user never gave. A form elicitation is not approved this way.
- An answer or cancel that fails to reach Crush clears the batch from the asked set, so a re-delivery of the still-pending batch is asked again.
- A batch of several questions takes several prompts, with no confirmation step. `confirm_title` and `confirm_description` are ignored.
- An answer from another client, such as the Crush TUI, does not dismiss the editor's open prompt. ACP has no way to withdraw a permission request. The bridge's late answer reaches Crush as `resolved: false` and is logged.
