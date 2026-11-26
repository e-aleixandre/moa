package antigravity

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/e-aleixandre/moa/pkg/core"
)

// The models agy advertises in session/new (configOptions, category
// "model") become moa models "agy.<value>", so the selector and an owner's
// `model:` can name them. Each model offers its own thought levels (agy
// returns them when the model is set), learned once per model. Both are
// cached in moa's config directory, never in agy's, so they are known before
// the first session of a new process.

var modelsMu sync.Mutex

func modelsPath() string { return filepath.Join(core.ConfigDir(), "antigravity-models.json") }

type cachedModels struct {
	Models []core.Model `json:"models"`
	// Levels holds each model's thought level names, by moa model id; an
	// empty list is a model with none (an orchestrator).
	Levels map[string][]string `json:"levels,omitempty"`
}

func loadModelsCache() cachedModels {
	var c cachedModels
	if b, err := os.ReadFile(modelsPath()); err == nil {
		_ = json.Unmarshal(b, &c)
	}
	return c
}

// LoadCachedModels registers the models learned in an earlier run.
func LoadCachedModels() {
	modelsMu.Lock()
	defer modelsMu.Unlock()
	c := loadModelsCache()
	core.RegisterProviderModels(ProviderName, c.Models, c.Levels)
}

// learnModels registers the models agy offers and the thought levels of
// each. A model whose levels are not known yet is set once to read them, and
// agy's current model is restored afterwards: setting options is local to
// agy and calls no model.
func (s *agySession) learnModels(ctx context.Context) {
	model := findOption(s.options, "model")
	if model == nil {
		return
	}
	modelsMu.Lock()
	defer modelsMu.Unlock()
	levels := loadModelsCache().Levels
	if levels == nil {
		levels = map[string][]string{}
	}
	current := model.CurrentValue
	levels[modelPrefix+current] = levelNames(s.options)
	levels[ProviderName] = levelNames(s.options)
	probed := false
	var models []core.Model
	for _, c := range model.Options {
		id := modelPrefix + c.Value
		models = append(models, core.Model{ID: id, Provider: ProviderName, Name: displayName(c.Name, c.Value) + " (Antigravity)", Pricing: &core.Pricing{}})
		if _, known := levels[id]; known {
			continue
		}
		if opts, err := s.setModel(ctx, model.ID, c.Value); err == nil {
			levels[id] = levelNames(opts)
			probed = true
		}
	}
	if probed {
		if opts, err := s.setModel(ctx, model.ID, current); err == nil {
			s.options = opts
		}
	}
	core.RegisterProviderModels(ProviderName, models, levels)
	b, _ := json.MarshalIndent(cachedModels{Models: models, Levels: levels}, "", "  ")
	_ = os.MkdirAll(filepath.Dir(modelsPath()), 0o700)
	tmp := modelsPath() + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, modelsPath())
	}
}

// levelNames are the names of the thought levels options offer, empty when
// they offer none.
func levelNames(options []configOption) []string {
	names := []string{}
	if o := findOption(options, "thought_level"); o != nil {
		for _, c := range o.Options {
			names = append(names, displayName(c.Name, c.Value))
		}
	}
	return names
}

func findOption(options []configOption, category string) *configOption {
	for i := range options {
		if options[i].Category == category {
			return &options[i]
		}
	}
	return nil
}

func displayName(name, value string) string {
	if name != "" {
		return name
	}
	return value
}
