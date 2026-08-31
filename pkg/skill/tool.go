package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/e-aleixandre/moa/pkg/core"
)

// NewTool creates the load_skill tool that lets the agent load skill content on
// demand, discovering skills from cwd on every call.
//
// Reading disk per call rather than capturing a set keeps the tool honest about
// a workspace that changes while the session is open: a skill written minutes
// ago is loadable, and one deleted is gone.
//
// Skills the model may not invoke are excluded, not just hidden from the prompt
// index: honouring a name it was not offered would leave "only the user invokes
// this" as a suggestion rather than a rule.
func NewTool(cwd string) core.Tool {
	modelInvocable := func() map[string]Skill {
		byName := map[string]Skill{}
		for _, s := range Discover(cwd) {
			if s.ModelInvocable() {
				byName[s.Name] = s
			}
		}
		return byName
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

			byName := modelInvocable()
			s, ok := byName[name]
			if !ok {
				available := make([]string, 0, len(byName))
				for n := range byName {
					available = append(available, n)
				}
				sort.Strings(available)
				if len(available) == 0 {
					return core.ErrorResult(fmt.Sprintf("skill %q not found: this workspace has no skills", name)), nil
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
