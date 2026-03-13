package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ealeixandre/moa/pkg/core"
)

// NewTool creates the load_skill tool that lets the agent load skill content on demand.
func NewTool(skills []Skill) core.Tool {
	byName := make(map[string]Skill, len(skills))
	for _, s := range skills {
		byName[s.Name] = s
	}

	return core.Tool{
		Name:        "load_skill",
		Description: "Load a skill pack by name. Returns the full skill content for use in the current task.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"name": {
					"type": "string",
					"description": "Skill name to load"
				}
			},
			"required": ["name"]
		}`),
		Effect: core.EffectReadOnly,
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			name, _ := params["name"].(string)
			name = strings.TrimSpace(name)
			if name == "" {
				return core.ErrorResult("name is required"), nil
			}

			s, ok := byName[name]
			if !ok {
				var available []string
				for _, sk := range skills {
					available = append(available, sk.Name)
				}
				return core.ErrorResult(fmt.Sprintf(
					"skill %q not found. Available skills: %s",
					name, strings.Join(available, ", "),
				)), nil
			}

			content, err := Load(s)
			if err != nil {
				return core.ErrorResult(fmt.Sprintf("failed to load skill %q: %v", name, err)), nil
			}

			return core.TextResult(content), nil
		},
	}
}
