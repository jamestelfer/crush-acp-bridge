package bridge

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/coder/acp-go-sdk"

	"github.com/jamestelfer/crush-acp-bridge/internal/crush"
)

// credential is what one of Crush's providers needs before Crush will use it.
type credential struct {
	name string   // the provider's display name
	vars []string // the environment variables it reads, all required
	note string   // a sentence on other credentials it takes or needs, when there are any
}

// credentials lists the environment each of Crush's known providers reads, by
// provider ID. It follows the provider catalogue and config loading of Crush
// 0.102.0 (internal/config/load.go): a provider without its variables is
// dropped when a workspace opens.
var credentials = map[string]credential{
	"anthropic":         {name: "Anthropic", vars: []string{"ANTHROPIC_API_KEY"}},
	"openai":            {name: "OpenAI", vars: []string{"OPENAI_API_KEY"}, note: "Signing in with `crush login openai` also works."},
	"gemini":            {name: "Google Gemini", vars: []string{"GEMINI_API_KEY"}},
	"vertexai":          {name: "Google Vertex AI", vars: []string{"VERTEXAI_PROJECT", "VERTEXAI_LOCATION"}, note: "It also needs Google Application Default Credentials on the server's machine (`gcloud auth application-default login`)."},
	"bedrock":           {name: "AWS Bedrock US", vars: []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"}, note: "AWS_PROFILE, AWS_REGION or AWS_BEARER_TOKEN_BEDROCK also work."},
	"bedrock-europe":    {name: "AWS Bedrock Europe", vars: []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"}, note: "AWS_PROFILE, AWS_REGION or AWS_BEARER_TOKEN_BEDROCK also work."},
	"azure":             {name: "Azure OpenAI", vars: []string{"AZURE_OPENAI_API_ENDPOINT", "AZURE_OPENAI_API_KEY"}},
	"copilot":           {name: "GitHub Copilot", note: "Sign in with `crush login copilot` on the server's machine."},
	"hyper":             {name: "Hyper", vars: []string{"HYPER_API_KEY"}, note: "Signing in with `crush login hyper` also works."},
	"xai":               {name: "xAI", vars: []string{"XAI_API_KEY"}, note: "Signing in with `crush login grok` also works."},
	"zai":               {name: "Z.AI", vars: []string{"ZAI_API_KEY"}},
	"kimi-coding":       {name: "Kimi Coding", vars: []string{"KIMI_CODING_API_KEY"}},
	"minimax":           {name: "MiniMax", vars: []string{"MINIMAX_API_KEY"}},
	"minimax-china":     {name: "MiniMax China", vars: []string{"MINIMAX_API_KEY"}},
	"moonshot":          {name: "Moonshot", vars: []string{"MOONSHOT_API_KEY"}},
	"synthetic":         {name: "Synthetic", vars: []string{"SYNTHETIC_API_KEY"}},
	"aihubmix":          {name: "AIHubMix", vars: []string{"AIHUBMIX_API_KEY"}},
	"alibaba-singapore": {name: "Alibaba (Singapore)", vars: []string{"ALIBABA_SINGAPORE_API_KEY"}},
	"alibaba-us":        {name: "Alibaba (US)", vars: []string{"ALIBABA_US_API_KEY"}},
	"atlascloud":        {name: "Atlas Cloud", vars: []string{"ATLASCLOUD_API_KEY"}},
	"baseten":           {name: "Baseten", vars: []string{"BASETEN_API_KEY"}},
	"avian":             {name: "Avian", vars: []string{"AVIAN_API_KEY"}},
	"cerebras":          {name: "Cerebras", vars: []string{"CEREBRAS_API_KEY"}},
	"chutes":            {name: "Chutes", vars: []string{"CHUTES_API_KEY"}},
	"coralbricks":       {name: "CoralBricks", vars: []string{"CORALBRICKS_API_KEY"}},
	"cortecs":           {name: "Cortecs", vars: []string{"CORTECS_API_KEY"}},
	"deepseek":          {name: "DeepSeek", vars: []string{"DEEPSEEK_API_KEY"}},
	"fireworks":         {name: "Fireworks", vars: []string{"FIREWORKS_API_KEY"}},
	"groq":              {name: "Groq", vars: []string{"GROQ_API_KEY"}},
	"huggingface":       {name: "Hugging Face", vars: []string{"HF_TOKEN"}},
	"ionet":             {name: "io.net", vars: []string{"IONET_API_KEY"}},
	"nebius":            {name: "Nebius Token Factory", vars: []string{"NEBIUS_API_KEY"}},
	"neuralwatt":        {name: "Neuralwatt", vars: []string{"NEURALWATT_API_KEY"}},
	"opencode-go":       {name: "OpenCode Go", vars: []string{"OPENCODE_API_KEY"}},
	"opencode-zen":      {name: "OpenCode Zen", vars: []string{"OPENCODE_API_KEY"}},
	"openrouter":        {name: "OpenRouter", vars: []string{"OPENROUTER_API_KEY"}},
	"pareto":            {name: "Pareto Inference", vars: []string{"PARETO_API_KEY"}},
	"qiniucloud":        {name: "QiniuCloud", vars: []string{"QINIUCLOUD_API_KEY"}},
	"scaleway":          {name: "Scaleway", vars: []string{"SCW_SECRET_KEY"}},
	"vercel":            {name: "Vercel", vars: []string{"VERCEL_API_KEY"}},
	"venice":            {name: "Venice AI", vars: []string{"VENICE_API_KEY"}},
	"zhipu":             {name: "Zhipu", vars: []string{"ZHIPU_API_KEY"}},
	"zhipu-coding":      {name: "Zhipu Coding", vars: []string{"ZHIPU_API_KEY"}},
}

// credentialData is the data of an error about a provider's credentials, so
// an editor can show the provider and its variables without parsing the
// message.
type credentialData struct {
	Provider  string   `json:"provider"`
	Variables []string `json:"variables,omitempty"`
}

// where says where Crush reads provider credentials from.
const where = "in the environment of the Crush server, then restart it. " +
	"A server on this machine takes the environment of the first client to open the directory; " +
	"a server on another machine uses its own"

// describe names provider, as "Google Vertex AI (vertexai)", and says how to
// give it credentials. An unknown provider, such as a custom one, reads the
// variable its api_key names.
func describe(provider string) (name, fix string) {
	c, ok := credentials[provider]
	if !ok {
		return provider, "Set the variable its api_key names in crush.json " + where + "."
	}
	var parts []string
	if len(c.vars) > 0 {
		parts = append(parts, "Set "+joinAnd(c.vars)+" "+where+".")
	}
	if c.note != "" {
		parts = append(parts, c.note)
	}
	return c.name + " (" + provider + ")", strings.Join(parts, " ")
}

func joinAnd(s []string) string {
	if len(s) < 2 {
		return strings.Join(s, "")
	}
	return strings.Join(s[:len(s)-1], ", ") + " and " + s[len(s)-1]
}

// unusableProvider reports the provider of the current model when Crush
// offers none of its models: Crush dropped it for missing credentials. An
// empty provider reported unusable means Crush has no provider at all.
//
// Crush keeps a dropped provider's model current only when no provider is
// left. While another provider has credentials, Crush switches to that
// provider's default model and saves the switch, so the bridge sees a usable
// model and cannot tell the user's choice was replaced.
func unusableProvider(models crush.Models) (provider string, unusable bool) {
	p := models.Current.Provider
	if p == "" {
		return "", len(models.Available) == 0
	}
	return p, !slices.ContainsFunc(models.Available, func(m crush.Model) bool { return m.Provider == p })
}

// noProviderError explains a prompt Crush refused because it has no agent:
// the current model's provider, or every provider, lacks credentials.
func (a *Agent) noProviderError(ctx context.Context, backend Backend) error {
	models, err := backend.Models(ctx)
	if err != nil {
		a.log.Warn("crush models not listed", "error", err)
	}
	provider, unusable := unusableProvider(models)
	switch {
	case err != nil || !unusable:
		return internalError("Crush has no usable provider. Set the provider's credentials " + where + ".")
	case provider == "":
		return internalError(fmt.Sprintf(
			"Crush has no provider with credentials. Set a provider's API key, such as ANTHROPIC_API_KEY or OPENAI_API_KEY, %s.", where))
	}
	name, fix := describe(provider)
	model := cmp.Or(models.Current.Model, "the current model")
	return credentialsError(fmt.Sprintf("Crush cannot run %s: %s has no credentials. %s", model, name, fix), provider)
}

// credentialsError is a JSON-RPC internal error carrying msg, with the
// provider and its variables as data.
func credentialsError(msg, provider string) error {
	return acp.NewRequestError(-32603, msg, credentialData{Provider: provider, Variables: credentials[provider].vars})
}

// turnFailure is the editor's error for a turn Crush ended with an error. A
// failure that reads as a credentials problem also names the provider and
// the variables it reads.
func turnFailure(provider, title, detail string) error {
	account := strings.Join(slices.DeleteFunc([]string{title, detail}, func(s string) bool { return s == "" }), ": ")
	msg := "Crush ended the turn with an error"
	if account != "" {
		msg += ": " + account
	}
	if provider == "" || !aboutCredentials(account) {
		return internalError(msg)
	}
	name, fix := describe(provider)
	return credentialsError(fmt.Sprintf("%s. Check the credentials of %s. %s", msg, name, fix), provider)
}

// credentialWords mark a provider error about credentials.
var credentialWords = []string{
	"credential", "api key", "api_key", "apikey", "unauthorized", "unauthenticated",
	"authentication", "forbidden", "permission denied", "access denied",
}

func aboutCredentials(s string) bool {
	s = strings.ToLower(s)
	return slices.ContainsFunc(credentialWords, func(w string) bool { return strings.Contains(s, w) })
}

// warnUnusableModel logs, at session/new, a directory whose current model
// Crush cannot run, before a prompt fails on it.
func (a *Agent) warnUnusableModel(sessionID string, models crush.Models) {
	provider, unusable := unusableProvider(models)
	if !unusable {
		return
	}
	if provider == "" {
		a.log.Warn("crush has no usable model", "session_id", sessionID,
			"hint", "no provider has credentials; set a provider's API key in the Crush server's environment")
		return
	}
	name, fix := describe(provider)
	a.log.Warn("crush has no usable model", "session_id", sessionID, "provider", provider,
		"model", models.Current.Model, "variables", credentials[provider].vars,
		"hint", name+" has no credentials. "+fix)
}
