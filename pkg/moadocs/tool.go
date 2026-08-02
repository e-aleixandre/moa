package moadocs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/e-aleixandre/moa/pkg/core"
)

// Description builds the tool description. The page names live here on
// purpose: every registered tool already costs one line in the system prompt,
// so listing them inside that line adds no new block, and without them the
// model does not know an Automation API page exists — it would answer from
// memory instead of looking it up, which is the whole failure being fixed.
func Description() string {
	var sb strings.Builder
	sb.WriteString("Read moa's own documentation (this agent). Use it when the user asks how moa works, or wants to configure, extend or integrate it — do not answer such questions from memory. Pages: ")
	pages := Pages()
	names := make([]string, 0, len(pages))
	for _, p := range pages {
		names = append(names, p.Name)
	}
	sb.WriteString(strings.Join(names, ", "))
	sb.WriteString(".")
	return sb.String()
}

// NewTool returns the tool that reads an embedded documentation page.
func NewTool() core.Tool {
	return core.Tool{
		Name:        "moa_docs",
		Description: Description(),
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"page": {
					"type": "string",
					"description": "Page name, e.g. \"configuration\" or \"recipes/linear\""
				}
			},
			"required": ["page"]
		}`),
		Effect: core.EffectReadOnly,
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			page, _ := params["page"].(string)
			if strings.TrimSpace(page) == "" {
				return core.ErrorResult("page is required. Available pages: " + strings.Join(names(), ", ")), nil
			}
			content, ok := Read(page)
			if !ok {
				return core.ErrorResult(fmt.Sprintf(
					"no documentation page %q. Available pages: %s",
					page, strings.Join(names(), ", "),
				)), nil
			}
			return core.TextResult(content), nil
		},
	}
}

func names() []string {
	pages := Pages()
	out := make([]string, 0, len(pages))
	for _, p := range pages {
		out = append(out, p.Name)
	}
	return out
}
