package bridge

import (
	"cmp"
	"context"
	"fmt"
	"strings"

	"github.com/coder/acp-go-sdk"

	"github.com/jamestelfer/crush-acp-bridge/internal/crush"
)

// modelConfigID identifies the model selector among a session's config
// options.
const modelConfigID acp.SessionConfigId = "model"

// modelValue is the selector value for a model: "provider/model". Provider IDs
// contain no slash, so the first one separates the parts.
func modelValue(m crush.ModelRef) acp.SessionConfigValueId {
	return acp.SessionConfigValueId(m.Provider + "/" + m.Model)
}

func parseModelValue(v acp.SessionConfigValueId) (crush.ModelRef, bool) {
	provider, model, ok := strings.Cut(string(v), "/")
	if !ok || provider == "" || model == "" {
		return crush.ModelRef{}, false
	}
	return crush.ModelRef{Provider: provider, Model: model}, true
}

// configOptions describes Crush's model choice as ACP config options. It
// returns none when Crush offers no models.
func configOptions(models crush.Models) []acp.SessionConfigOption {
	if len(models.Available) == 0 {
		return nil
	}

	choices := make(acp.SessionConfigSelectOptionsUngrouped, 0, len(models.Available)+1)
	found := false
	for _, m := range models.Available {
		ref := crush.ModelRef{Provider: m.Provider, Model: m.ID}
		choice := acp.SessionConfigSelectOption{Value: modelValue(ref), Name: cmp.Or(m.Name, m.ID)}
		if m.ProviderName != "" {
			choice.Description = &m.ProviderName
		}
		choices = append(choices, choice)
		found = found || ref == models.Current
	}
	// ACP requires the current value to be one of the options.
	current := modelValue(models.Current)
	switch {
	case models.Current.Model == "":
		current = choices[0].Value // Crush has no model set; show the first it offers
	case !found:
		choices = append(choices, acp.SessionConfigSelectOption{Value: current, Name: models.Current.Model})
	}

	opt := acp.NewSessionConfigOptionSelect(current, acp.SessionConfigSelectOptions{Ungrouped: &choices})
	category := acp.SessionConfigOptionCategoryModel
	description := "The model Crush uses in this directory. Changing it affects every Crush client of the directory."
	opt.Select.Id = modelConfigID
	opt.Select.Name = "Model"
	opt.Select.Category = &category
	opt.Select.Description = &description
	return []acp.SessionConfigOption{opt}
}

// sessionConfig reads the backend's models for a new session. A failure is
// logged and leaves the session without a model selector.
func (a *Agent) sessionConfig(ctx context.Context, backend Backend, sessionID string) []acp.SessionConfigOption {
	models, err := backend.Models(ctx)
	if err != nil {
		a.log.Warn("crush models not listed", "session_id", sessionID, "error", err)
		return nil
	}
	a.warnUnusableModel(sessionID, models)
	return configOptions(models)
}

// SetSessionConfigOption switches Crush's model. Crush keeps one model per
// directory, so the change applies to every session there, and the others
// are told.
func (a *Agent) SetSessionConfigOption(ctx context.Context, req acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	set := req.ValueId
	if set == nil || set.ConfigId != modelConfigID {
		return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(map[string]string{"error": "only the model option can be set"})
	}
	s, err := a.lookup(set.SessionId)
	if err != nil {
		return acp.SetSessionConfigOptionResponse{}, err
	}
	be := a.backend(s.cwd)

	models, err := be.Models(ctx)
	if err != nil {
		return acp.SetSessionConfigOptionResponse{}, err
	}
	ref, ok := parseModelValue(set.Value)
	if !ok || !offered(models, ref) {
		return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(map[string]string{
			"error": fmt.Sprintf("crush does not offer model %q", set.Value),
		})
	}

	if err := be.SetModel(ctx, ref); err != nil {
		a.log.Error("model change failed", "session_id", string(set.SessionId), "model", string(set.Value), "error", err)
		return acp.SetSessionConfigOptionResponse{}, err
	}
	a.log.Info("model changed", "session_id", string(set.SessionId), "model", string(set.Value))

	models, err = be.Models(ctx)
	if err != nil {
		return acp.SetSessionConfigOptionResponse{}, err
	}
	opts := configOptions(models)
	for id := range a.sessionsIn(s.cwd) {
		if id == string(set.SessionId) {
			continue
		}
		a.send(acp.SessionId(id), acp.SessionUpdate{ConfigOptionUpdate: &acp.SessionConfigOptionUpdate{
			ConfigOptions: opts, SessionUpdate: "config_option_update",
		}})
	}
	return acp.SetSessionConfigOptionResponse{ConfigOptions: opts}, nil
}

func offered(models crush.Models, ref crush.ModelRef) bool {
	for _, m := range models.Available {
		if m.Provider == ref.Provider && m.ID == ref.Model {
			return true
		}
	}
	return false
}
