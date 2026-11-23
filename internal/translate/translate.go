// Package translate converts Crush server-sent events into ACP-shaped updates.
//
// Crush publishes whole-message snapshots. The Translator remembers what it
// has already emitted for each message and returns only the new content.
package translate

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
)

// Kind identifies the type of an Update.
type Kind int

const (
	// MessageChunk is new assistant text.
	MessageChunk Kind = iota + 1
	// ThoughtChunk is new assistant reasoning.
	ThoughtChunk
	// UserChunk is a user message, whoever sent it.
	UserChunk
	// ToolCallStarted is the first sighting of a tool call.
	ToolCallStarted
	// ToolCallInput carries a tool call's complete input, which arrives after
	// the call is first seen.
	ToolCallInput
	// ToolCallFinished carries a tool call's result.
	ToolCallFinished
	// PermissionRequested asks the client to approve a tool call.
	PermissionRequested
	// TurnEnded closes a prompt turn; StopReason holds the ACP stop reason.
	TurnEnded
)

var kindNames = map[Kind]string{
	MessageChunk:        "message_chunk",
	ThoughtChunk:        "thought_chunk",
	UserChunk:           "user_chunk",
	ToolCallStarted:     "tool_call_started",
	ToolCallInput:       "tool_call_input",
	ToolCallFinished:    "tool_call_finished",
	PermissionRequested: "permission_requested",
	TurnEnded:           "turn_ended",
}

// String returns the kind's name, for logs and snapshots.
func (k Kind) String() string {
	if name, ok := kindNames[k]; ok {
		return name
	}
	return fmt.Sprintf("kind(%d)", int(k))
}

// Update is a single incremental change derived from a Crush event.
type Update struct {
	Kind      Kind
	SessionID string
	MessageID string
	Text      string

	// Tool call fields, set for ToolCallStarted and ToolCallFinished.
	ToolCallID string
	ToolName   string
	Output     string
	IsError    bool
	RawInput   jsontext.Value // ToolCallInput: the tool's JSON arguments

	// Permission fields, set for PermissionRequested.
	PermissionID  string
	Description   string
	RawPermission jsontext.Value // the request as sent; echoed back to grant it
	RawParams     jsontext.Value // the tool's parameters, as shown in the request

	// StopReason is set for TurnEnded: end_turn, cancelled, max_tokens or error.
	StopReason string
}

// Translator turns a stream of Crush events into Updates. It is not safe for
// concurrent use.
type Translator struct {
	sentText    map[string]int
	sentThought map[string]int
	sentUser    map[string]int
	startedTool map[string]bool
	inputTool   map[string]bool
	doneTool    map[string]bool
	ended       map[string]bool
}

// New returns a Translator with no history.
func New() *Translator {
	return &Translator{
		sentText:    map[string]int{},
		sentThought: map[string]int{},
		sentUser:    map[string]int{},
		startedTool: map[string]bool{},
		inputTool:   map[string]bool{},
		doneTool:    map[string]bool{},
		ended:       map[string]bool{},
	}
}

type envelope struct {
	Type    string `json:"type"`
	Payload struct {
		Payload jsontext.Value `json:"payload"`
	} `json:"payload"`
}

type message struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	Role      string `json:"role"`
	Parts     []part `json:"parts"`
}

type part struct {
	Type string         `json:"type"`
	Data jsontext.Value `json:"data"`
}

// Feed consumes the data field of one SSE event and returns any updates.
func (t *Translator) Feed(data []byte) ([]Update, error) {
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("decode event envelope: %w", err)
	}

	switch env.Type {
	case "message":
		return t.message(env.Payload.Payload)
	case "permission_request":
		return t.permission(env.Payload.Payload)
	default:
		return nil, nil
	}
}

// FeedMessage consumes one stored message, as the server's message list
// returns it, and returns any updates. It replays a session's history.
func (t *Translator) FeedMessage(raw jsontext.Value) ([]Update, error) {
	return t.message(raw)
}

func (t *Translator) message(raw jsontext.Value) ([]Update, error) {
	var msg message
	if err := json.Unmarshal(raw, &msg); err != nil {
		return nil, fmt.Errorf("decode message: %w", err)
	}

	var updates []Update
	for _, p := range msg.Parts {
		u, err := t.part(msg, p)
		if err != nil {
			return nil, fmt.Errorf("decode %s part: %w", p.Type, err)
		}
		updates = append(updates, u...)
	}
	for i := range updates {
		updates[i].SessionID = msg.SessionID
	}
	return updates, nil
}

func (t *Translator) part(msg message, p part) ([]Update, error) {
	assistant := msg.Role == "assistant"

	if p.Type == "text" && msg.Role == "user" {
		var d struct {
			Text   string `json:"text"`
			Hidden bool   `json:"hidden"`
		}
		if err := json.Unmarshal(p.Data, &d); err != nil {
			return nil, err
		}
		if d.Hidden {
			return nil, nil
		}
		return delta(UserChunk, msg.ID, d.Text, t.sentUser), nil
	}

	switch p.Type {
	case "tool_call":
		return t.toolCall(p.Data)
	case "tool_result":
		return t.toolResult(p.Data)
	case "finish":
		if assistant {
			return t.finish(msg.ID, p.Data)
		}
	case "text":
		if assistant {
			var d struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(p.Data, &d); err != nil {
				return nil, err
			}
			return delta(MessageChunk, msg.ID, d.Text, t.sentText), nil
		}
	case "reasoning":
		if assistant {
			var d struct {
				Thinking string `json:"thinking"`
			}
			if err := json.Unmarshal(p.Data, &d); err != nil {
				return nil, err
			}
			return delta(ThoughtChunk, msg.ID, d.Thinking, t.sentThought), nil
		}
	}
	return nil, nil
}

func (t *Translator) toolCall(data jsontext.Value) ([]Update, error) {
	var d struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Input    string `json:"input"`
		Finished bool   `json:"finished"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, err
	}

	var updates []Update
	if !t.startedTool[d.ID] {
		t.startedTool[d.ID] = true
		updates = append(updates, Update{Kind: ToolCallStarted, ToolCallID: d.ID, ToolName: d.Name})
	}
	if d.Finished && d.Input != "" && !t.inputTool[d.ID] {
		t.inputTool[d.ID] = true
		updates = append(updates, Update{
			Kind: ToolCallInput, ToolCallID: d.ID, ToolName: d.Name, RawInput: jsontext.Value(d.Input),
		})
	}
	return updates, nil
}

func (t *Translator) toolResult(data jsontext.Value) ([]Update, error) {
	var d struct {
		ToolCallID string `json:"tool_call_id"`
		Name       string `json:"name"`
		Content    string `json:"content"`
		IsError    bool   `json:"is_error"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, err
	}
	if t.doneTool[d.ToolCallID] {
		return nil, nil
	}
	t.doneTool[d.ToolCallID] = true
	return []Update{{
		Kind: ToolCallFinished, ToolCallID: d.ToolCallID, ToolName: d.Name,
		Output: d.Content, IsError: d.IsError,
	}}, nil
}

func (t *Translator) finish(msgID string, data jsontext.Value) ([]Update, error) {
	if t.ended[msgID] {
		return nil, nil
	}
	var d struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, err
	}
	reason, ok := stopReason(d.Reason)
	if !ok {
		return nil, nil
	}
	t.ended[msgID] = true
	return []Update{{Kind: TurnEnded, MessageID: msgID, StopReason: reason}}, nil
}

func (t *Translator) permission(raw jsontext.Value) ([]Update, error) {
	var d struct {
		ID          string         `json:"id"`
		SessionID   string         `json:"session_id"`
		ToolCallID  string         `json:"tool_call_id"`
		ToolName    string         `json:"tool_name"`
		Description string         `json:"description"`
		Params      jsontext.Value `json:"params"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("decode permission request: %w", err)
	}
	return []Update{{
		Kind: PermissionRequested, SessionID: d.SessionID, PermissionID: d.ID, ToolCallID: d.ToolCallID,
		ToolName: d.ToolName, Description: d.Description, RawPermission: raw, RawParams: d.Params,
	}}, nil
}

// delta returns the part of full not yet emitted for id, and records it as sent.
func delta(kind Kind, id, full string, sent map[string]int) []Update {
	n := sent[id]
	if n >= len(full) {
		return nil
	}
	sent[id] = len(full)
	return []Update{{Kind: kind, MessageID: id, Text: full[n:]}}
}

// stopReason maps a Crush finish reason to an ACP stop reason. The second
// result is false when the finish does not end the turn (a tool round trip).
func stopReason(finish string) (string, bool) {
	switch finish {
	case "end_turn":
		return "end_turn", true
	case "canceled":
		return "cancelled", true
	case "max_tokens":
		return "max_tokens", true
	case "error", "content_filter", "unknown":
		return "error", true
	default:
		return "", false
	}
}
