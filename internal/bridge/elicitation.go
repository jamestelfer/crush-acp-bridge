package bridge

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/coder/acp-go-sdk"

	"github.com/jamestelfer/crush-acp-bridge/internal/crush"
	"github.com/jamestelfer/crush-acp-bridge/internal/translate"
)

// Values of a yes_no field, whose titled options stand in for a checkbox that
// cannot tell "no" from "not answered".
const (
	yesValue = "yes"
	noValue  = "no"
)

func questionKey(i int) string { return fmt.Sprintf("q%d", i) }

func customKey(i int) string { return questionKey(i) + "_custom" }

// elicitationForm turns a whole Crush question batch into one form scoped to
// the question tool call. Choice fields are never required, so an editor that
// preselects the first option cannot force it on a user who typed their own
// answer or skipped.
func elicitationForm(u translate.Update) *acp.CreateElicitationForm {
	props := make(map[string]acp.ElicitationPropertySchema, len(u.Questions))
	for i, q := range u.Questions {
		switch q.Type {
		case translate.YesNo:
			props[questionKey(i)] = choiceField(q, []acp.EnumOption{
				{Const: yesValue, Title: "Yes"},
				{Const: noValue, Title: "No"},
			})
		case translate.SingleChoice:
			props[questionKey(i)] = choiceField(q, enumOptions(q.Choices))
			props[customKey(i)] = customField(q)
		case translate.MultiChoice:
			props[questionKey(i)] = multiChoiceField(q)
			props[customKey(i)] = customField(q)
		default:
			props[questionKey(i)] = textField(q)
		}
	}

	form := &acp.CreateElicitationForm{
		Mode:            "form",
		Message:         formMessage(u),
		RequestedSchema: acp.ElicitationSchema{Type: "object", Properties: props},
		Session:         &acp.ElicitationSessionScope{SessionId: acp.SessionId(u.SessionID)},
	}
	if u.ToolCallID != "" {
		id := acp.ToolCallId(u.ToolCallID)
		form.Session.ToolCallId = &id
	}
	return form
}

// formMessage prefers Crush's own confirm text, then the question itself when
// there is only one.
func formMessage(u translate.Update) string {
	switch {
	case u.ConfirmTitle != "" && u.ConfirmDescription != "":
		return u.ConfirmTitle + "\n\n" + u.ConfirmDescription
	case u.ConfirmTitle != "":
		return u.ConfirmTitle
	case u.ConfirmDescription != "":
		return u.ConfirmDescription
	case len(u.Questions) == 1:
		return u.Questions[0].Text
	default:
		return fmt.Sprintf("%d questions", len(u.Questions))
	}
}

func enumOptions(choices []translate.Choice) []acp.EnumOption {
	opts := make([]acp.EnumOption, len(choices))
	for i, c := range choices {
		opts[i] = acp.EnumOption{Const: c.ID, Title: c.Label}
		if c.Description != "" {
			opts[i].Description = &c.Description
		}
	}
	return opts
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func choiceField(q translate.Question, opts []acp.EnumOption) acp.ElicitationPropertySchema {
	return acp.ElicitationPropertySchema{String: &acp.ElicitationPropertySchemaString{
		Type: "string", Title: &q.Text, Description: optional(q.Description), OneOf: opts,
	}}
}

func multiChoiceField(q translate.Question) acp.ElicitationPropertySchema {
	return acp.ElicitationPropertySchema{Array: &acp.ElicitationPropertySchemaArray{
		Type: "array", Title: &q.Text, Description: optional(q.Description),
		Items: acp.MultiSelectItems{Titled: &acp.TitledMultiSelectItems{AnyOf: enumOptions(q.Choices)}},
	}}
}

func customField(q translate.Question) acp.ElicitationPropertySchema {
	title := "Other answer to: " + q.Text
	return acp.ElicitationPropertySchema{String: &acp.ElicitationPropertySchemaString{Type: "string", Title: &title}}
}

func textField(q translate.Question) acp.ElicitationPropertySchema {
	return acp.ElicitationPropertySchema{String: &acp.ElicitationPropertySchemaString{
		Type: "string", Title: &q.Text, Description: optional(q.Description),
	}}
}

// elicitationAnswers decodes an accepted form into one answer per question.
// An answer the form leaves empty, or gives a value that was not offered, is
// an empty answer, which tells Crush the user skipped it. Custom text is kept
// beside a selection: as FillInText for multi_choice, and as the batch note
// for single_choice, whose answer has no room for both.
func elicitationAnswers(qs []translate.Question, content map[string]acp.ElicitationContentValue) []crush.QuestionAnswer {
	answers := make([]crush.QuestionAnswer, len(qs))
	for i, q := range qs {
		ans := crush.QuestionAnswer{QuestionID: q.ID}
		text := contentString(content[questionKey(i)])
		custom := contentString(content[customKey(i)])
		switch q.Type {
		case translate.YesNo:
			switch text {
			case yesValue, noValue:
				yes := text == yesValue
				ans.Yes = &yes
			}
		case translate.SingleChoice:
			if isOffered(q.Choices, text) {
				ans.SelectedIDs = []string{text}
			}
			switch {
			case custom == "":
			case len(ans.SelectedIDs) == 0:
				ans.FillInText = custom
			default:
				ans.Notes = map[string]string{crush.QuestionNoteKey: custom}
			}
		case translate.MultiChoice:
			ans.SelectedIDs = offeredIDs(q.Choices, contentStrings(content[questionKey(i)]))
			ans.FillInText = custom
		default:
			ans.FillInText = text
		}
		answers[i] = ans
	}
	return answers
}

func contentString(v acp.ElicitationContentValue) string {
	if v.String == nil {
		return ""
	}
	return strings.TrimSpace(string(*v.String))
}

func contentStrings(v acp.ElicitationContentValue) []string {
	if v.StringArray == nil {
		return nil
	}
	return *v.StringArray
}

func isOffered(choices []translate.Choice, id string) bool {
	for _, c := range choices {
		if c.ID == id {
			return true
		}
	}
	return false
}

// offeredIDs keeps the ids that were offered, once each, in offered order.
func offeredIDs(choices []translate.Choice, ids []string) []string {
	var out []string
	for _, c := range choices {
		for _, id := range ids {
			if id == c.ID {
				out = append(out, id)
				break
			}
		}
	}
	return out
}

// The ACP elicitation spec forbids asking a form for passwords, API keys,
// tokens, private keys, recovery codes or payment credentials. A question
// asks for one when it asks the user to hand it over, not whenever it names
// one, so "Should passwords be hashed with bcrypt?" is let through.
var (
	credentialNoun = `pass(?:word|words|phrase|code)s?|api[ _-]?keys?|(?:secret|access|private|ssh|signing) keys?` +
		`|tokens?|secrets?|credentials?` +
		`|recovery codes?|backup codes?|security codes?|verification codes?|one[- ]time (?:code|password)s?|2fa codes?|mfa codes?` +
		`|cvv|cvc|card numbers?|credit cards?|card details|bank account numbers?|social security numbers?|ssn`
	credentialAsked = regexp.MustCompile(
		`\b(?:(?:enter|provide|paste|share|type|give|supply|send|input|submit|tell me|what is|what's|which|need)\b[^.?!\n]{0,60}?|(?:your|my|the user's)\s+(?:\w+\s+)?)` +
			`(` + credentialNoun + `)\b`)
	// discussedNotAsked follows a credential noun that is the subject of a
	// design question rather than something to hand over.
	discussedNotAsked = regexp.MustCompile(`^\s+(?:policy|policies|hash|hashing|requirements?|rules?|length|strength|validation|manager|rotation|storage|store|field|format|limits?|type|size|algorithm|expir\w*|lifetime)\b`)
)

// asksForCredentials reports whether any text of the batch asks the user for
// a credential or secret. It inspects each question's text, label and
// description, and each choice's label and description.
func asksForCredentials(qs []translate.Question) bool {
	for _, q := range qs {
		texts := []string{q.Text, q.Label, q.Description}
		for _, c := range q.Choices {
			texts = append(texts, c.Label, c.Description)
		}
		if slices.ContainsFunc(texts, asksForCredential) {
			return true
		}
	}
	return false
}

func asksForCredential(text string) bool {
	lower := strings.ToLower(text)
	for _, m := range credentialAsked.FindAllStringSubmatchIndex(lower, -1) {
		if !discussedNotAsked.MatchString(lower[m[1]:]) {
			return true
		}
	}
	return false
}
