package planmode

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ealeixandre/moa/pkg/core"
)

// submitPlanTool returns a tool that signals the plan is ready for review.
func submitPlanTool(pm *PlanMode) core.Tool {
	return core.Tool{
		Name:        "submit_plan",
		Label:       "Submit Plan",
		Description: "Signal that your plan is complete and ready for user review. Call this after writing the plan file.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"title": {
					"type": "string",
					"description": "Short title for the plan (optional)"
				}
			}
		}`),
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			pm.mu.Lock()
			defer pm.mu.Unlock()

			if pm.state.Mode != ModePlanning {
				return core.ErrorResult("submit_plan only works in planning mode"), nil
			}
			pm.state.PlanSubmitted = true
			title, _ := params["title"].(string)
			msg := "Plan submitted for review."
			if title != "" {
				msg = fmt.Sprintf("Plan submitted: %s", title)
			}
			return core.TextResult(msg + " The user will choose the next action."), nil
		},
	}
}

// requestReviewTool returns a tool that triggers an in-process code review.
func requestReviewTool(pm *PlanMode) core.Tool {
	return core.Tool{
		Name:        "request_review",
		Label:       "Request Review",
		Description: "Request a code review from a senior reviewer. Use after completing substantial work, not after every small task.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"summary": {
					"type": "string",
					"description": "Summary of what was done since last review"
				},
				"files_changed": {
					"type": "array",
					"items": { "type": "string" },
					"description": "List of files that were modified"
				}
			},
			"required": ["summary", "files_changed"]
		}`),
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			pm.mu.Lock()
			mode := pm.state.Mode
			cfg := pm.codeReviewCfg
			pm.mu.Unlock()

			if mode != ModeExecuting {
				return core.ErrorResult("request_review only works during plan execution"), nil
			}

			// Validate summary.
			summary, _ := params["summary"].(string)
			if strings.TrimSpace(summary) == "" {
				return core.ErrorResult("summary is required"), nil
			}

			// Validate files_changed: deduplicate and normalize first, then cap.
			rawFiles, _ := params["files_changed"].([]any)
			if len(rawFiles) == 0 {
				return core.ErrorResult("files_changed is required and must not be empty"), nil
			}
			seen := make(map[string]bool, len(rawFiles))
			var files []string
			for _, raw := range rawFiles {
				f, ok := raw.(string)
				if !ok || strings.TrimSpace(f) == "" {
					continue
				}
				clean := filepath.Clean(f)
				if !seen[clean] {
					seen[clean] = true
					files = append(files, clean)
				}
			}
			if len(files) == 0 {
				return core.ErrorResult("files_changed contains no valid file paths"), nil
			}
			if len(files) > 50 {
				return core.ErrorResult("too many files (max 50 after deduplication)"), nil
			}

			result, err := ReviewCode(ctx, cfg, summary, files)
			if err != nil {
				return core.ErrorResult(fmt.Sprintf("review failed: %v", err)), nil
			}

			header := "VERDICT: APPROVED"
			if !result.Approved {
				header = "VERDICT: CHANGES_REQUESTED"
			}
			return core.TextResult(header + "\n\n" + result.Feedback), nil
		},
	}
}
