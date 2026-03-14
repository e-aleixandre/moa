// Package askuser provides the ask_user tool that lets the agent ask
// the user one or more questions and block until they respond.
package askuser

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ealeixandre/moa/pkg/core"
)

// Question describes a single question with optional predefined choices.
type Question struct {
	Text    string   `json:"question"`
	Options []string `json:"options,omitempty"`
}

// Prompt is the full batch sent from the agent to the UI.
type Prompt struct {
	Questions []Question
	Response  chan<- []string // one answer per question (exactly once)
}

// Bridge connects the ask_user tool to the UI.
type Bridge struct {
	ch chan Prompt
}

// NewBridge creates a bridge.
func NewBridge() *Bridge {
	return &Bridge{ch: make(chan Prompt)}
}

// Prompts returns the channel the UI listens on.
func (b *Bridge) Prompts() <-chan Prompt {
	return b.ch
}

// NewTool creates the ask_user tool wired to the given bridge.
func NewTool(b *Bridge) core.Tool {
	return core.Tool{
		Name: "ask_user",
		Description: `Ask the user one or more questions and wait for their responses. Use this when you need clarification, a decision, or information you cannot determine on your own. You can provide predefined options for each question — the user can pick one or write a custom answer. Do not use this for rhetorical questions or confirmations of actions you can take directly.`,
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"questions": {
					"type": "array",
					"description": "One or more questions to ask the user",
					"items": {
						"type": "object",
						"properties": {
							"question": {
								"type": "string",
								"description": "The question text"
							},
							"options": {
								"type": "array",
								"items": { "type": "string" },
								"description": "Optional predefined answer choices. The user can always write a custom answer instead."
							}
						},
						"required": ["question"]
					}
				}
			},
			"required": ["questions"]
		}`),
		Effect: core.EffectReadOnly,
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			questions, err := parseQuestions(params)
			if err != nil {
				return core.ErrorResult(err.Error()), nil
			}

			respCh := make(chan []string, 1)

			select {
			case b.ch <- Prompt{Questions: questions, Response: respCh}:
			case <-ctx.Done():
				return core.ErrorResult("cancelled"), nil
			}

			select {
			case answers := <-respCh:
				return formatAnswers(questions, answers), nil
			case <-ctx.Done():
				return core.ErrorResult("cancelled"), nil
			}
		},
	}
}

func parseQuestions(params map[string]any) ([]Question, error) {
	raw, ok := params["questions"]
	if !ok {
		return nil, fmt.Errorf("questions is required")
	}
	arr, ok := raw.([]any)
	if !ok || len(arr) == 0 {
		return nil, fmt.Errorf("questions must be a non-empty array")
	}

	questions := make([]Question, 0, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("each question must be an object")
		}
		text, _ := m["question"].(string)
		if text == "" {
			return nil, fmt.Errorf("each question must have a non-empty 'question' field")
		}
		q := Question{Text: text}
		if opts, ok := m["options"].([]any); ok {
			for _, o := range opts {
				if s, ok := o.(string); ok && s != "" {
					q.Options = append(q.Options, s)
				}
			}
		}
		questions = append(questions, q)
	}
	return questions, nil
}

func formatAnswers(questions []Question, answers []string) core.Result {
	if len(questions) == 1 {
		return core.TextResult(answers[0])
	}
	var sb strings.Builder
	for i, q := range questions {
		if i > 0 {
			sb.WriteString("\n")
		}
		fmt.Fprintf(&sb, "Q: %s\nA: %s", q.Text, answers[i])
	}
	return core.TextResult(sb.String())
}
