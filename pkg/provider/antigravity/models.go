package antigravity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/e-aleixandre/moa/pkg/core"
)

// The models agy advertises in session/new (configOptions, category
// "model") become moa models "agy.<value>", so the selector and an owner's
// `model:` can name them. They are cached in moa's config directory, never in
// agy's, so they are known before the first session of a new process.

var modelsMu sync.Mutex

func modelsPath() string { return filepath.Join(core.ConfigDir(), "antigravity-models.json") }

type cachedModels struct {
	Models  []core.Model `json:"models"`
	Efforts []string     `json:"thought_levels,omitempty"`
}

// LoadCachedModels registers the models learned in an earlier run.
func LoadCachedModels() {
	modelsMu.Lock()
	defer modelsMu.Unlock()
	b, err := os.ReadFile(modelsPath())
	if err != nil {
		return
	}
	var c cachedModels
	if json.Unmarshal(b, &c) == nil {
		core.RegisterProviderModels(ProviderName, c.Models, c.Efforts)
	}
}

func learnModels(options []configOption) {
	var efforts []string
	for _, o := range options {
		if o.Category == "thought_level" {
			for _, c := range o.Options {
				efforts = append(efforts, displayName(c.Name, c.Value))
			}
		}
	}
	if len(efforts) > len(core.ThinkingLevels) {
		efforts = efforts[:len(core.ThinkingLevels)]
	}
	var models []core.Model
	for _, o := range options {
		if o.Category != "model" {
			continue
		}
		for _, c := range o.Options {
			models = append(models, core.Model{
				ID:       modelPrefix + c.Value,
				Provider: ProviderName,
				Name:     displayName(c.Name, c.Value) + " (Antigravity)",
				Pricing:  &core.Pricing{},
			})
		}
	}
	if len(models) == 0 {
		return
	}
	modelsMu.Lock()
	defer modelsMu.Unlock()
	core.RegisterProviderModels(ProviderName, models, efforts)
	b, _ := json.MarshalIndent(cachedModels{Models: models, Efforts: efforts}, "", "  ")
	tmp := modelsPath() + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, modelsPath())
	}
}

func displayName(name, value string) string {
	if name != "" {
		return name
	}
	return value
}
