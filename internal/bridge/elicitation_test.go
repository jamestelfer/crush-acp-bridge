package bridge

import (
	"testing"

	"github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jamestelfer/crush-acp-bridge/internal/crush"
	"github.com/jamestelfer/crush-acp-bridge/internal/translate"
)

var (
	langQ = translate.Question{
		ID: "lang", Type: translate.SingleChoice, Label: "Language", Text: "English or French?",
		Description: "Pick one.",
		Choices:     []translate.Choice{{ID: "en", Label: "English", Description: "the default"}, {ID: "fr", Label: "French"}},
	}
	exclaimQ = translate.Question{ID: "ex", Type: translate.YesNo, Text: "End with an exclamation mark?"}
	nameQ    = translate.Question{ID: "name", Type: translate.FreeText, Text: "Whose name?", Description: "A first name."}
	toppingQ = translate.Question{
		ID: "top", Type: translate.MultiChoice, Text: "Toppings?",
		Choices: []translate.Choice{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}, {ID: "c", Label: "C"}},
	}
)

func str(s string) acp.ElicitationContentValue { return acp.NewElicitationContentValueString(s) }

func strs(s ...string) acp.ElicitationContentValue {
	return acp.NewElicitationContentValueStringArray(s...)
}

func TestElicitationFormMessage(t *testing.T) {
	tests := map[string]struct {
		u    translate.Update
		want string
	}{
		"confirm title and description": {
			translate.Update{ConfirmTitle: "Ready?", ConfirmDescription: "We will greet.", Questions: []translate.Question{nameQ}},
			"Ready?\n\nWe will greet.",
		},
		"confirm title only":       {translate.Update{ConfirmTitle: "Ready?", Questions: []translate.Question{nameQ, langQ}}, "Ready?"},
		"confirm description only": {translate.Update{ConfirmDescription: "We will greet.", Questions: []translate.Question{nameQ, langQ}}, "We will greet."},
		"single question text":     {translate.Update{Questions: []translate.Question{nameQ}}, "Whose name?"},
		"count of questions":       {translate.Update{Questions: []translate.Question{nameQ, langQ, exclaimQ}}, "3 questions"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, elicitationForm(tc.u).Message)
		})
	}
}

func TestElicitationFormIsScopedToTheToolCall(t *testing.T) {
	form := elicitationForm(translate.Update{SessionID: "s1", ToolCallID: "call-1", Questions: []translate.Question{nameQ}})

	assert.Equal(t, "form", form.Mode)
	require.NotNil(t, form.Session)
	assert.Equal(t, acp.SessionId("s1"), form.Session.SessionId)
	require.NotNil(t, form.Session.ToolCallId)
	assert.Equal(t, acp.ToolCallId("call-1"), *form.Session.ToolCallId)
	assert.Equal(t, acp.ElicitationSchemaType("object"), form.RequestedSchema.Type)
}

func TestElicitationFormWithoutAToolCallIsScopedToTheSession(t *testing.T) {
	form := elicitationForm(translate.Update{SessionID: "s1", Questions: []translate.Question{nameQ}})
	assert.Nil(t, form.Session.ToolCallId)
}

func TestElicitationFormFields(t *testing.T) {
	u := translate.Update{SessionID: "s", ToolCallID: "c", Questions: []translate.Question{langQ, exclaimQ, nameQ, toppingQ}}
	props := elicitationForm(u).RequestedSchema.Properties

	assert.Len(t, props, 6, "q0..q3 plus q0_custom and q3_custom")

	t.Run("single_choice", func(t *testing.T) {
		f := props["q0"].String
		require.NotNil(t, f)
		assert.Equal(t, "string", f.Type)
		assert.Equal(t, "Language", *f.Title)
		assert.Equal(t, "English or French?\n\nPick one.", *f.Description, "the question is the description beside several others")
		desc := "the default"
		assert.Equal(t, []acp.EnumOption{
			{Const: "en", Title: "English", Description: &desc},
			{Const: "fr", Title: "French"},
		}, f.OneOf)
		custom := props["q0_custom"].String
		require.NotNil(t, custom)
		assert.Equal(t, "string", custom.Type)
		assert.Empty(t, custom.OneOf)
	})

	t.Run("yes_no", func(t *testing.T) {
		f := props["q1"].String
		require.NotNil(t, f)
		assert.Equal(t, "string", f.Type)
		assert.Nil(t, f.Description)
		assert.Equal(t, []acp.EnumOption{{Const: "yes", Title: "Yes"}, {Const: "no", Title: "No"}}, f.OneOf)
		assert.NotContains(t, props, "q1_custom")
	})

	t.Run("free_text", func(t *testing.T) {
		f := props["q2"].String
		require.NotNil(t, f)
		assert.Equal(t, "string", f.Type)
		assert.Empty(t, f.OneOf)
		assert.Equal(t, "A first name.", *f.Description)
		assert.NotContains(t, props, "q2_custom")
	})

	t.Run("multi_choice", func(t *testing.T) {
		f := props["q3"].Array
		require.NotNil(t, f)
		assert.Equal(t, "array", f.Type)
		require.NotNil(t, f.Items.Titled)
		assert.Equal(t, []acp.EnumOption{{Const: "a", Title: "A"}, {Const: "b", Title: "B"}, {Const: "c", Title: "C"}}, f.Items.Titled.AnyOf)
		assert.Contains(t, props, "q3_custom")
	})
}

func TestElicitationFormRequiresNoField(t *testing.T) {
	u := translate.Update{Questions: []translate.Question{langQ, exclaimQ, nameQ, toppingQ}}
	assert.Empty(t, elicitationForm(u).RequestedSchema.Required, "every question may be skipped")
}

func TestElicitationAnswers(t *testing.T) {
	yes, no := true, false
	tests := map[string]struct {
		qs      []translate.Question
		content map[string]acp.ElicitationContentValue
		want    []crush.QuestionAnswer
	}{
		"every type answered": {
			qs: []translate.Question{langQ, exclaimQ, nameQ, toppingQ},
			content: map[string]acp.ElicitationContentValue{
				"q0": str("fr"), "q1": str("yes"), "q2": str("Ada"), "q3": strs("a", "c"),
			},
			want: []crush.QuestionAnswer{
				{QuestionID: "lang", SelectedIDs: []string{"fr"}},
				{QuestionID: "ex", Yes: &yes},
				{QuestionID: "name", FillInText: "Ada"},
				{QuestionID: "top", SelectedIDs: []string{"a", "c"}},
			},
		},
		"yes_no no": {
			qs:      []translate.Question{exclaimQ},
			content: map[string]acp.ElicitationContentValue{"q0": str("no")},
			want:    []crush.QuestionAnswer{{QuestionID: "ex", Yes: &no}},
		},
		"yes_no unknown value is skipped": {
			qs:      []translate.Question{exclaimQ},
			content: map[string]acp.ElicitationContentValue{"q0": str("maybe")},
			want:    []crush.QuestionAnswer{{QuestionID: "ex"}},
		},
		"single_choice custom only": {
			qs:      []translate.Question{langQ},
			content: map[string]acp.ElicitationContentValue{"q0_custom": str("Klingon")},
			want:    []crush.QuestionAnswer{{QuestionID: "lang", FillInText: "Klingon"}},
		},
		"single_choice selection and custom keep both": {
			qs:      []translate.Question{langQ},
			content: map[string]acp.ElicitationContentValue{"q0": str("en"), "q0_custom": str("but formal")},
			want: []crush.QuestionAnswer{{
				QuestionID: "lang", SelectedIDs: []string{"en"},
				Notes: map[string]string{crush.QuestionNoteKey: "but formal"},
			}},
		},
		"single_choice unknown id with custom keeps the custom text": {
			qs:      []translate.Question{langQ},
			content: map[string]acp.ElicitationContentValue{"q0": str("de"), "q0_custom": str("German")},
			want:    []crush.QuestionAnswer{{QuestionID: "lang", FillInText: "German"}},
		},
		"single_choice unknown id alone is skipped": {
			qs:      []translate.Question{langQ},
			content: map[string]acp.ElicitationContentValue{"q0": str("de")},
			want:    []crush.QuestionAnswer{{QuestionID: "lang"}},
		},
		"multi_choice selection and custom coexist": {
			qs:      []translate.Question{toppingQ},
			content: map[string]acp.ElicitationContentValue{"q0": strs("b"), "q0_custom": str("and olives")},
			want:    []crush.QuestionAnswer{{QuestionID: "top", SelectedIDs: []string{"b"}, FillInText: "and olives"}},
		},
		"multi_choice drops unknown and repeated ids": {
			qs:      []translate.Question{toppingQ},
			content: map[string]acp.ElicitationContentValue{"q0": strs("c", "zzz", "a", "c")},
			want:    []crush.QuestionAnswer{{QuestionID: "top", SelectedIDs: []string{"a", "c"}}},
		},
		"multi_choice custom only": {
			qs:      []translate.Question{toppingQ},
			content: map[string]acp.ElicitationContentValue{"q0_custom": str("anchovies")},
			want:    []crush.QuestionAnswer{{QuestionID: "top", FillInText: "anchovies"}},
		},
		"wrong value kind is skipped": {
			qs:      []translate.Question{nameQ, toppingQ},
			content: map[string]acp.ElicitationContentValue{"q0": strs("x"), "q1": str("a")},
			want:    []crush.QuestionAnswer{{QuestionID: "name"}, {QuestionID: "top"}},
		},
		"empty and blank fields are skipped": {
			qs:      []translate.Question{langQ, nameQ},
			content: map[string]acp.ElicitationContentValue{"q0": str(""), "q0_custom": str("  "), "q1": str("   ")},
			want:    []crush.QuestionAnswer{{QuestionID: "lang"}, {QuestionID: "name"}},
		},
		"no content skips every question": {
			qs:   []translate.Question{langQ, exclaimQ, nameQ, toppingQ},
			want: []crush.QuestionAnswer{{QuestionID: "lang"}, {QuestionID: "ex"}, {QuestionID: "name"}, {QuestionID: "top"}},
		},
		"partial answers": {
			qs:      []translate.Question{langQ, exclaimQ, nameQ},
			content: map[string]acp.ElicitationContentValue{"q2": str("Ada")},
			want:    []crush.QuestionAnswer{{QuestionID: "lang"}, {QuestionID: "ex"}, {QuestionID: "name", FillInText: "Ada"}},
		},
		"unknown keys are ignored": {
			qs:      []translate.Question{nameQ},
			content: map[string]acp.ElicitationContentValue{"q9": str("x"), "q0": str("Ada")},
			want:    []crush.QuestionAnswer{{QuestionID: "name", FillInText: "Ada"}},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, elicitationAnswers(tc.qs, tc.content))
		})
	}
}

func TestCredentialGuard(t *testing.T) {
	asks := []string{
		"Enter your password",
		"What is your database password?",
		"Please paste your API key",
		"Provide the OpenAI api_key",
		"Paste your GitHub personal access token",
		"Share your private key",
		"What's your SSH key passphrase?",
		"Give me the client secret",
		"Type your recovery code",
		"Enter your 2FA code",
		"Please provide your credit card number",
		"Provide your CVV",
		"Send the bearer token",
		"Paste your token",
		"Enter your GitHub token",
		"Please enter your credentials",
		"Which auth token should the service use?",
		"What is the secret?",
		"Password?",
		"Database password",
		"OpenAI API key",
		"GitHub PAT",
		"What password should the admin account use?",
		"What API key should I use?",
		"What token should the bot use?",
		"Your GitHub personal access token?",
		"Paste your PAT",
		"Enter your PIN",
		"Paste your JWT",
		"Enter your wallet seed phrase",
		"Enter your recovery phrase",
		"Paste the session cookie",
		"Paste the database connection string",
		"Paste the contents of ~/.ssh/id_rsa",
		"Enter the key for the S3 bucket",
		"Enter your password manager master password",
		"Entrez votre mot de passe",
		"Paste your .env file",
		"What is your social security number?",
	}
	for _, text := range asks {
		t.Run("flags "+text, func(t *testing.T) {
			assert.True(t, asksForCredentials([]translate.Question{{Type: translate.FreeText, Text: text}}))
		})
	}

	fine := []string{
		"Should passwords be hashed with bcrypt or argon2?",
		"Which token limit should the model use?",
		"Use JWT access tokens or server sessions?",
		"Should the API key be read from an environment variable?",
		"Provide a password policy for the service",
		"Do you want me to add a password reset flow?",
		"Which SSH key type should the deploy script generate: ed25519 or rsa?",
		"Should we pin dependency versions?",
		"Paste the stack trace",
		"Which card layout should the dashboard use?",
		"What is your preferred test framework?",
		"Should I rotate the secrets manager keys?",
		"Whose name should be greeted?",
		"Should we pin the base image version?",
		"Should the service store the session cookie as HttpOnly?",
		"How long should password reset links stay valid?",
	}
	for _, text := range fine {
		t.Run("allows "+text, func(t *testing.T) {
			assert.False(t, asksForCredentials([]translate.Question{{Type: translate.FreeText, Text: text}}))
		})
	}
}

func TestCredentialGuardInspectsEveryTextOfTheBatch(t *testing.T) {
	tests := map[string]translate.Question{
		"label":              {Type: translate.FreeText, Text: "Value?", Label: "Enter your password"},
		"description":        {Type: translate.FreeText, Text: "Value?", Description: "Please paste your API key here."},
		"choice label":       {Type: translate.SingleChoice, Text: "How?", Choices: []translate.Choice{{ID: "a", Label: "Enter your API token below"}}},
		"choice description": {Type: translate.SingleChoice, Text: "How?", Choices: []translate.Choice{{ID: "a", Label: "A", Description: "Share your private key"}}},
	}
	for name, q := range tests {
		t.Run(name, func(t *testing.T) {
			assert.True(t, asksForCredentials([]translate.Question{nameQ, q}))
		})
	}
}

func TestElicitationFieldTitles(t *testing.T) {
	t.Run("a single labelled question leaves its text to the message", func(t *testing.T) {
		form := elicitationForm(translate.Update{Questions: []translate.Question{langQ}})
		f := form.RequestedSchema.Properties["q0"].String
		assert.Equal(t, "English or French?", form.Message)
		assert.Equal(t, "Language", *f.Title)
		assert.Equal(t, "Pick one.", *f.Description)
		assert.Equal(t, "Other answer to: Language", *form.RequestedSchema.Properties["q0_custom"].String.Title)
	})

	t.Run("a single labelled question without a description has none", func(t *testing.T) {
		q := translate.Question{ID: "n", Type: translate.FreeText, Label: "Name", Text: "Whose name?"}
		f := elicitationForm(translate.Update{Questions: []translate.Question{q}}).RequestedSchema.Properties["q0"].String
		assert.Equal(t, "Name", *f.Title)
		assert.Nil(t, f.Description)
	})

	t.Run("a question without a label is titled by its text", func(t *testing.T) {
		f := elicitationForm(translate.Update{Questions: []translate.Question{exclaimQ, nameQ}}).RequestedSchema.Properties["q0"].String
		assert.Equal(t, "End with an exclamation mark?", *f.Title)
		assert.Nil(t, f.Description)
	})
}
