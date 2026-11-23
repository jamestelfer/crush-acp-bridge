package crush

import (
	"cmp"
	"context"
	"encoding/json/jsontext"
	"fmt"
	"net/http"
	"net/url"
	"slices"
)

// ModelRef names a model by provider and model ID, as Crush's config does.
type ModelRef struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// Model is a model offered by a configured provider.
type Model struct {
	Provider     string
	ProviderName string
	ID           string
	Name         string
}

// Models is the workspace's current large model and the models it may switch
// to.
type Models struct {
	Current   ModelRef
	Available []Model
}

// workspaceConfig is the part of GET /config the bridge reads. The full
// document carries credentials, so nothing else is decoded.
type workspaceConfig struct {
	Models    map[string]ModelRef `json:"models"`
	Providers map[string]struct {
		Name    string `json:"name"`
		Disable bool   `json:"disable"`
		Models  []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"models"`
	} `json:"providers"`
}

func (w *Workspace) config(ctx context.Context) (workspaceConfig, error) {
	var cfg workspaceConfig
	err := w.c.do(ctx, http.MethodGet, w.path("/config"), nil, &cfg)
	return cfg, err
}

// Models returns the models of every enabled provider, sorted by provider ID
// with each provider's own model order kept, and the current large model.
func (w *Workspace) Models(ctx context.Context) (Models, error) {
	cfg, err := w.config(ctx)
	if err != nil {
		return Models{}, fmt.Errorf("models: %w", err)
	}

	ids := make([]string, 0, len(cfg.Providers))
	for id, p := range cfg.Providers {
		if !p.Disable {
			ids = append(ids, id)
		}
	}
	slices.SortFunc(ids, cmp.Compare)

	out := Models{Current: cfg.Models["large"]}
	for _, id := range ids {
		p := cfg.Providers[id]
		for _, m := range p.Models {
			out.Available = append(out.Available, Model{Provider: id, ProviderName: p.Name, ID: m.ID, Name: m.Name})
		}
	}
	return out, nil
}

// SetModel makes m the workspace's large model and reloads the agent. Like
// `crush run --model`, it writes the workspace config (.crush/crush.json), so
// every client of the workspace uses the new model. When the provider changes,
// the small model moves to the new provider's default, since the old one may
// not be usable there.
func (w *Workspace) SetModel(ctx context.Context, m ModelRef) error {
	cfg, err := w.config(ctx)
	if err != nil {
		return fmt.Errorf("set model: %w", err)
	}
	if err := w.setPreferred(ctx, "large", m); err != nil {
		return fmt.Errorf("set model: %w", err)
	}

	if cfg.Models["small"].Provider != m.Provider {
		var small jsontext.Value
		p := w.path("/agent/default-small-model") + "?provider_id=" + url.QueryEscape(m.Provider)
		if err := w.c.do(ctx, http.MethodGet, p, nil, &small); err != nil {
			return fmt.Errorf("set model: default small model: %w", err)
		}
		if err := w.setPreferred(ctx, "small", small); err != nil {
			return fmt.Errorf("set model: small model: %w", err)
		}
	}

	if err := w.c.do(ctx, http.MethodPost, w.path("/agent/update"), nil, nil); err != nil {
		return fmt.Errorf("set model: reload agent: %w", err)
	}
	return nil
}

// workspaceScope is config.ScopeWorkspace in Crush.
const workspaceScope = 1

func (w *Workspace) setPreferred(ctx context.Context, modelType string, model any) error {
	body := struct {
		Scope     int    `json:"scope"`
		ModelType string `json:"model_type"`
		Model     any    `json:"model"`
	}{workspaceScope, modelType, model}
	return w.c.do(ctx, http.MethodPost, w.path("/config/model"), body, nil)
}
