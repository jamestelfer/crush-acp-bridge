package translate_test

import (
	"bufio"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jamestelfer/crush-acp-bridge/internal/translate"
)

// replay reads a recorded SSE trace and returns each event's data payload.
func replay(t *testing.T, name string) [][]byte {
	t.Helper()

	f, err := os.Open(filepath.Join("..", "..", "testdata", name))
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })

	var events [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for sc.Scan() {
		if data, ok := strings.CutPrefix(sc.Text(), "data:"); ok {
			events = append(events, []byte(strings.TrimSpace(data)))
		}
	}
	require.NoError(t, sc.Err())
	return events
}

// finalAssistantParts returns, per assistant message ID, the final text and
// reasoning found in the trace. It reads the trace independently of the
// translator so the two can be compared.
func finalAssistantParts(t *testing.T, events [][]byte) (text, thought map[string]string) {
	t.Helper()

	text = map[string]string{}
	thought = map[string]string{}
	for _, data := range events {
		var env struct {
			Type    string `json:"type"`
			Payload struct {
				Payload struct {
					ID    string `json:"id"`
					Role  string `json:"role"`
					Parts []struct {
						Type string `json:"type"`
						Data struct {
							Text     string `json:"text"`
							Thinking string `json:"thinking"`
						} `json:"data"`
					} `json:"parts"`
				} `json:"payload"`
			} `json:"payload"`
		}
		require.NoError(t, json.Unmarshal(data, &env))
		msg := env.Payload.Payload
		if env.Type != "message" || msg.Role != "assistant" {
			continue
		}
		for _, p := range msg.Parts {
			switch p.Type {
			case "text":
				text[msg.ID] = p.Data.Text
			case "reasoning":
				thought[msg.ID] = p.Data.Thinking
			}
		}
	}
	return text, thought
}

func TestStreamedTextReassemblesIntoFinalMessage(t *testing.T) {
	events := replay(t, "long-text-cancel.sse")
	wantText, wantThought := finalAssistantParts(t, events)
	require.NotEmpty(t, wantText)

	tr := translate.New()
	gotText := map[string]string{}
	gotThought := map[string]string{}
	for _, data := range events {
		updates, err := tr.Feed(data)
		require.NoError(t, err)
		for _, u := range updates {
			switch u.Kind {
			case translate.MessageChunk:
				gotText[u.MessageID] += u.Text
			case translate.ThoughtChunk:
				gotThought[u.MessageID] += u.Text
			}
		}
	}

	require.Equal(t, wantText, gotText)
	require.Equal(t, wantThought, gotThought)
}

func TestToolCallLifecycle(t *testing.T) {
	events := replay(t, "bash-permission.sse")

	tr := translate.New()
	var got []translate.Update
	for _, data := range events {
		updates, err := tr.Feed(data)
		require.NoError(t, err)
		for _, u := range updates {
			if u.Kind == translate.ToolCallStarted || u.Kind == translate.ToolCallFinished {
				got = append(got, u)
			}
		}
	}

	// One start and one finish for the single bash call, however many
	// snapshots carried the call.
	require.Len(t, got, 2)

	start, finish := got[0], got[1]
	require.Equal(t, translate.ToolCallStarted, start.Kind)
	require.Equal(t, "bash", start.ToolName)
	require.NotEmpty(t, start.ToolCallID)

	require.Equal(t, translate.ToolCallFinished, finish.Kind)
	require.Equal(t, start.ToolCallID, finish.ToolCallID)
	require.False(t, finish.IsError)
}

func TestPermissionRequestIsSurfacedWithToolCallLink(t *testing.T) {
	events := replay(t, "bash-permission.sse")

	tr := translate.New()
	var started, perm []translate.Update
	for _, data := range events {
		updates, err := tr.Feed(data)
		require.NoError(t, err)
		for _, u := range updates {
			switch u.Kind {
			case translate.ToolCallStarted:
				started = append(started, u)
			case translate.PermissionRequested:
				perm = append(perm, u)
			}
		}
	}

	require.Len(t, started, 1)
	require.Len(t, perm, 1, "notifications for the same call must not duplicate the request")

	p := perm[0]
	require.Equal(t, started[0].ToolCallID, p.ToolCallID)
	require.Equal(t, "bash", p.ToolName)
	require.Equal(t, "Execute command: echo hello > out.txt", p.Description)
	require.NotEmpty(t, p.PermissionID)

	// The raw request is what the grant endpoint expects back verbatim.
	var raw map[string]any
	require.NoError(t, json.Unmarshal(p.RawPermission, &raw))
	require.Equal(t, p.PermissionID, raw["id"])
}

func stopReasons(t *testing.T, name string) []string {
	t.Helper()

	tr := translate.New()
	var reasons []string
	for _, data := range replay(t, name) {
		updates, err := tr.Feed(data)
		require.NoError(t, err)
		for _, u := range updates {
			if u.Kind == translate.TurnEnded {
				reasons = append(reasons, u.StopReason)
			}
		}
	}
	return reasons
}

func TestTurnEndsOnceAfterToolUse(t *testing.T) {
	// The intermediate "tool_use" finish must not end the turn.
	require.Equal(t, []string{"end_turn"}, stopReasons(t, "bash-permission.sse"))
}

func TestCancelledTurnReportsCancelled(t *testing.T) {
	require.Equal(t,
		[]string{"end_turn", "cancelled", "end_turn"},
		stopReasons(t, "long-text-cancel.sse"))
}

func TestUpdatesCarryTheirSessionID(t *testing.T) {
	for _, name := range []string{"bash-permission.sse", "long-text-cancel.sse"} {
		t.Run(name, func(t *testing.T) {
			tr := translate.New()
			var n int
			for _, data := range replay(t, name) {
				updates, err := tr.Feed(data)
				require.NoError(t, err)
				for _, u := range updates {
					n++
					require.NotEmpty(t, u.SessionID, "update kind %d has no session", u.Kind)
				}
			}
			require.NotZero(t, n)
		})
	}
}

func TestUserMessagesAreEmittedOncePerMessage(t *testing.T) {
	tr := translate.New()
	var got []string
	for _, data := range replay(t, "long-text-cancel.sse") {
		updates, err := tr.Feed(data)
		require.NoError(t, err)
		for _, u := range updates {
			if u.Kind == translate.UserChunk {
				got = append(got, u.Text)
			}
		}
	}

	// The prompts sent while recording the trace, in order.
	require.Equal(t, []string{
		"Write a 300 word explanation of how TCP congestion control works. No tools.",
		"Now write a 1500 word essay on the history of the internet. No tools.",
		"Say ok.",
	}, got)
}

func TestToolInputIsEmittedOnceWhenComplete(t *testing.T) {
	tr := translate.New()
	var order []translate.Kind
	var inputs []translate.Update
	for _, data := range replay(t, "bash-permission.sse") {
		updates, err := tr.Feed(data)
		require.NoError(t, err)
		for _, u := range updates {
			switch u.Kind {
			case translate.ToolCallStarted, translate.ToolCallInput:
				order = append(order, u.Kind)
			}
			if u.Kind == translate.ToolCallInput {
				inputs = append(inputs, u)
			}
		}
	}

	require.Equal(t, []translate.Kind{translate.ToolCallStarted, translate.ToolCallInput}, order,
		"the empty first snapshot starts the call; the finished one supplies input")
	require.Len(t, inputs, 1)
	require.Equal(t, "bash", inputs[0].ToolName)
	require.NotEmpty(t, inputs[0].ToolCallID)
	require.JSONEq(t,
		`{"command":"echo hello > out.txt","description":"Write hello to out.txt"}`,
		string(inputs[0].RawInput))
}

func TestPermissionRequestCarriesTheToolsParameters(t *testing.T) {
	tr := translate.New()
	var perm translate.Update
	for _, data := range replay(t, "bash-permission.sse") {
		updates, err := tr.Feed(data)
		require.NoError(t, err)
		for _, u := range updates {
			if u.Kind == translate.PermissionRequested {
				perm = u
			}
		}
	}

	require.JSONEq(t, `{
		"description": "Write hello to out.txt", "command": "echo hello > out.txt",
		"working_dir": "", "run_in_background": false, "auto_background_after": 0
	}`, string(perm.RawParams))
}

// finished returns the ToolCallFinished updates of a trace, by tool call ID.
func finished(t *testing.T, name string) map[string]translate.Update {
	t.Helper()

	tr := translate.New()
	out := map[string]translate.Update{}
	for _, data := range replay(t, name) {
		updates, err := tr.Feed(data)
		require.NoError(t, err)
		for _, u := range updates {
			if u.Kind == translate.ToolCallFinished {
				out[u.ToolCallID] = u
			}
		}
	}
	return out
}

func TestFinishedToolCallCarriesItsInputAndResultMetadata(t *testing.T) {
	edit := finished(t, "edit-permission.sse")["call_16713861530277631741"]

	require.Equal(t, "edit", edit.ToolName)
	require.JSONEq(t, `{
		"file_path": "/tmp/crushlive/diffproj/greek.txt",
		"old_string": "alpha\nbeta\ngamma", "new_string": "alpha\nBETA\ngamma"
	}`, string(edit.RawInput))
	require.JSONEq(t, `{
		"additions": 1, "removals": 1,
		"old_content": "alpha\nbeta\ngamma\ndelta\n", "new_content": "alpha\nBETA\ngamma\ndelta\n"
	}`, string(edit.Metadata))
}

func TestFinishedToolCallCarriesItsPermissionParameters(t *testing.T) {
	calls := finished(t, "write-permission.sse")

	asked := calls["call_8171991326521080277"]
	require.JSONEq(t,
		`{"file_path": "/tmp/crushlive/diffproj/notes.txt", "new_content": "one\ntwo\n"}`,
		string(asked.RawParams))
	unasked := calls["call_9574960797699994523"]
	require.Empty(t, unasked.RawParams, "Crush did not ask about the second write")
}

// questionsIn returns the QuestionAsked updates a trace yields.
func questionsIn(t *testing.T, name string) []translate.Update {
	t.Helper()
	tr := translate.New()
	var out []translate.Update
	for _, data := range replay(t, name) {
		updates, err := tr.Feed(data)
		require.NoError(t, err)
		for _, u := range updates {
			if u.Kind == translate.QuestionAsked {
				out = append(out, u)
			}
		}
	}
	return out
}

func TestQuestionBatchIsSurfacedWithItsToolCall(t *testing.T) {
	asked := questionsIn(t, "question-batch.sse")
	require.Len(t, asked, 1)

	u := asked[0]
	require.Equal(t, "ccacbfd4-687f-4a4b-a1fa-f1f46d38bac2", u.BatchID)
	require.Equal(t, "a8cd137c-4786-4409-b86d-c64711797633", u.SessionID)
	require.Equal(t, "call_5373993239042528834", u.ToolCallID)
	require.Equal(t, []translate.Question{
		{
			ID: "655fcadf-67d9-43c3-a9aa-dfa415d0d710", Type: translate.SingleChoice, Label: "Language",
			Text:        "Should the greeting be in English or French?",
			Description: "Select the language you want to use for the greeting.",
			Choices:     []translate.Choice{{ID: "en", Label: "English"}, {ID: "fr", Label: "French"}},
		},
		{
			ID: "d075d1c2-760e-41f3-bded-5413c53b1100", Type: translate.YesNo, Label: "Exclamation",
			Text:        "Should we end it with an exclamation mark?",
			Description: "Adds an enthusiastic exclamation mark at the end of the greeting.",
		},
		{
			ID: "1af14935-2e23-4757-a924-026a19dbdab6", Type: translate.FreeText, Label: "Name",
			Text:        "Whose name should be greeted?",
			Description: "Provide the name of the person to be greeted.",
		},
	}, u.Questions)
}

func TestQuestionToolCallFinishesWithTheAnswers(t *testing.T) {
	tr := translate.New()
	var finished []translate.Update
	for _, data := range replay(t, "question-batch.sse") {
		updates, err := tr.Feed(data)
		require.NoError(t, err)
		for _, u := range updates {
			if u.Kind == translate.ToolCallFinished {
				finished = append(finished, u)
			}
		}
	}
	require.Len(t, finished, 1)
	require.Equal(t, "question", finished[0].ToolName)
	require.Contains(t, finished[0].Output, "User provided: Recorded answer")
}

func TestFailedTurnCarriesCrushsErrorAndProvider(t *testing.T) {
	// Crush 0.102.0 ends a turn the provider refused with an error finish
	// whose message is a title and whose details explain it.
	event := `{"type":"message","payload":{"type":"updated","payload":{"id":"m-1","role":"assistant",` +
		`"session_id":"s-1","provider":"vertexai","model":"gemini-4-hydrogen","parts":[{"type":"finish","data":` +
		`{"reason":"error","time":1,"message":"Provider Error","details":"credentials: could not find default credentials"}}]}}}`

	updates, err := translate.New().Feed([]byte(event))
	require.NoError(t, err)

	require.Equal(t, []translate.Update{{
		Kind: translate.TurnEnded, SessionID: "s-1", MessageID: "m-1", StopReason: "error",
		Provider: "vertexai", ErrorTitle: "Provider Error", ErrorDetail: "credentials: could not find default credentials",
	}}, updates)
}
