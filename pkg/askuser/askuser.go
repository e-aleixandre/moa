// Package askuser provides the ask_user tool that lets the agent ask
// the user one or more questions and block until they respond.
package askuser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/e-aleixandre/moa/pkg/core"
)

// Limits on a single ask_user call. They exist because the answers come back
// through a UI or, for automation sessions, through an HTTP endpoint that caps
// the answer array: a prompt with more questions than the answer cap could
// never be answered by a machine caller. Bounding the text keeps one prompt
// from being a payload dump.
const (
	// MaxQuestions is the largest batch a single ask_user call may contain.
	// The Automation API's answer cap mirrors it.
	MaxQuestions = 32
	// MaxQuestionBytes bounds a single question's text.
	MaxQuestionBytes = 4 << 10
	// MaxOptions bounds the predefined choices offered for one question.
	MaxOptions = 32
	// MaxOptionBytes bounds a single predefined choice.
	MaxOptionBytes = 1 << 10
)

// Question describes a single question with optional predefined choices.
type Question struct {
	Text string `json:"question"`
	// Context lets the user answer without reading the conversation: what is
	// going on, why it is asked, what each option changes. Optional and
	// additive; an older client simply does not show it.
	Context string   `json:"context,omitempty"`
	Options []string `json:"options,omitempty"`
	// Multiple lets the user tick several options. The answer is still one
	// string per question: the chosen options joined by MultiSeparator, then
	// any free text, so every responder (UI, automation, owner) keeps the
	// same []string contract.
	Multiple bool `json:"multiple,omitempty"`
}

// MaxContextBytes bounds a question's context: a few lines, not a report.
const MaxContextBytes = 2000

// MultiSeparator joins the options chosen in a multiple-choice question.
const MultiSeparator = "; "

// Prompt is the full batch sent from the agent to the UI.
type Prompt struct {
	Questions []Question
	// RequiresTap is a property of the whole request: only a tap or typed
	// answer may resolve it, never the voice relay.
	RequiresTap bool
	Response    chan<- Reply // exactly once
}

// Reply answers a Prompt: one answer per question, and who gave them.
type Reply struct {
	Answers   []string
	Responder Responder
}

// Responder kinds. The zero Kind means the resolver did not say, which is
// also what every answer recorded before the field existed reads as.
const (
	ResponderUser       = "user"       // the user, from the web UI or a paired app
	ResponderOwner      = "owner"      // a project owner agent, on the user's behalf
	ResponderAutomation = "automation" // a caller of the Automation API
	ResponderVoice      = "voice"      // the voice relay, set only by the server's call bridge
)

// ErrRequiresTap is returned when a voice responder tries to resolve a
// request that must be answered by a tap or typed confirmation.
var ErrRequiresTap = errors.New("this question requires a tap or typed confirmation; voice cannot answer it")

// CustomKey is the tool_result Custom key that records the Responder. Custom
// never reaches the provider, so the record costs the model nothing.
const CustomKey = "ask_responder"

// Responder identifies who answered an ask_user. It is set by the code path
// that resolved the prompt, never parsed from the answer text.
type Responder struct {
	Kind string `json:"kind"`
	// ID and Name identify an owner.
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	// Via is the authenticated channel of a user answer (device:<id>,
	// network or token), as the server audits it.
	Via string `json:"via,omitempty"`
	// CallID and Quote are the evidence of a voice answer: the local call and
	// the owner's words as heard.
	CallID string `json:"call_id,omitempty"`
	Quote  string `json:"quote,omitempty"`
}

// ResponderFromCustom reads the recorded Responder back from a tool_result's
// Custom value: the struct itself in memory, a JSON object once persisted.
func ResponderFromCustom(v any) (Responder, bool) {
	switch r := v.(type) {
	case Responder:
		return r, r.Kind != ""
	case map[string]any:
		get := func(k string) string { s, _ := r[k].(string); return s }
		out := Responder{Kind: get("kind"), ID: get("id"), Name: get("name"), Via: get("via"), CallID: get("call_id"), Quote: get("quote")}
		return out, out.Kind != ""
	}
	return Responder{}, false
}

// Attribution is the line prepended to an answer the user did not give, so
// the model never reports it as theirs. Empty for the user and for unknown.
// The web client recognises this exact shape to label the answer card.
func (r Responder) Attribution() string {
	switch r.Kind {
	case ResponderOwner:
		// The name sits inside the line's quotes: drop what could close them
		// or break the line, so the label stays one parseable line.
		name := strings.Map(func(c rune) rune {
			if c == '"' || c == '[' || c == ']' || c == '\\' || unicode.IsControl(c) {
				return -1
			}
			return c
		}, r.Name)
		if name == "" {
			return "[Answered by the owner agent on the user's behalf, not by the user]"
		}
		return "[Answered by the owner agent \"" + name + "\" on the user's behalf, not by the user]"
	case ResponderAutomation:
		return "[Answered by an automation on the user's behalf, not by the user]"
	case ResponderVoice:
		return "[Answered by voice relay on the owner's behalf; not a tap or typed confirmation]"
	}
	return ""
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
		Name:        "ask_user",
		Description: `Ask the user one or more questions and wait for their responses. Use this when you need clarification, a decision, or information you cannot determine on your own. You can provide predefined options for each question — the user can pick one or write a custom answer. Do not use this for rhetorical questions or confirmations of actions you can take directly. Fill each question's context so the user can answer without reading the conversation: what is going on, why you ask, what each option changes. Set requires_tap to true when the question authorizes a deploy, merge, release or permission-like action: only a tap or typed answer may resolve it, never a spoken relay.`,
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"requires_tap": {
					"type": "boolean",
					"description": "Optional, default false. Set true for deploy/merge/release/permission-like confirmations: the answer must come from a tap or typed confirmation, never from the voice relay."
				},
				"questions": {
					"type": "array",
					"description": "One or more questions to ask the user (max 32)",
					"items": {
						"type": "object",
						"properties": {
							"question": {
								"type": "string",
								"description": "The question text"
							},
							"context": {
								"type": "string",
								"description": "What the user needs to answer without reading the conversation: 2-5 short lines saying what is going on, why you ask, and what changes with each option. Write it for someone who has just picked up their phone after two hours away."
							},
							"options": {
								"type": "array",
								"items": { "type": "string" },
								"description": "Optional predefined answer choices. The user can always write a custom answer instead."
							},
							"multiple": {
								"type": "boolean",
								"description": "Only with options: let the user tick several of them (default false). The answer comes back as the chosen options joined by '; ', followed by any free text the user adds."
							}
						},
						"required": ["question"]
					}
				}
			},
			"required": ["questions"]
		}`),
		Effect: core.EffectInteractive,
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			questions, err := parseQuestions(params)
			if err != nil {
				return core.ErrorResult(err.Error()), nil
			}

			requiresTap, _ := params["requires_tap"].(bool)
			return Ask(ctx, b, questions, requiresTap), nil
		},
	}
}

// Ask puts questions to the user through b and blocks until they are
// answered or ctx ends. It is ask_user's body, shared with tools that end in
// a written question (request_call when no call happens).
func Ask(ctx context.Context, b *Bridge, questions []Question, requiresTap bool) core.Result {
	respCh := make(chan Reply, 1)
	select {
	case b.ch <- Prompt{Questions: questions, RequiresTap: requiresTap, Response: respCh}:
	case <-ctx.Done():
		return core.ErrorResult("cancelled")
	}
	select {
	case reply := <-respCh:
		return formatAnswers(questions, reply)
	case <-ctx.Done():
		if ctx.Err() == context.Canceled {
			return core.ErrorResult("Question not answered: the user stopped this run. The answer will arrive in the next user message.")
		}
		return core.ErrorResult("cancelled")
	}
}

// ParseQuestion validates one question as ask_user does.
func ParseQuestion(m map[string]any) (Question, error) {
	qs, err := parseQuestions(map[string]any{"questions": []any{m}})
	if err != nil {
		return Question{}, err
	}
	return qs[0], nil
}

// ParseQuestions validates the "questions" parameter as ask_user does.
func ParseQuestions(params map[string]any) ([]Question, error) { return parseQuestions(params) }

func parseQuestions(params map[string]any) ([]Question, error) {
	raw, ok := params["questions"]
	if !ok {
		return nil, fmt.Errorf("questions is required")
	}
	arr, ok := raw.([]any)
	if !ok || len(arr) == 0 {
		return nil, fmt.Errorf("questions must be a non-empty array")
	}
	if len(arr) > MaxQuestions {
		return nil, fmt.Errorf("too many questions, max %d", MaxQuestions)
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
		if len(text) > MaxQuestionBytes {
			return nil, fmt.Errorf("question too long, max %d bytes", MaxQuestionBytes)
		}
		q := Question{Text: text}
		if c, ok := m["context"].(string); ok {
			c = strings.TrimSpace(c)
			if len(c) > MaxContextBytes {
				return nil, fmt.Errorf("question context too long, max %d bytes", MaxContextBytes)
			}
			q.Context = c
		}
		if opts, ok := m["options"].([]any); ok {
			if len(opts) > MaxOptions {
				return nil, fmt.Errorf("too many options for one question, max %d", MaxOptions)
			}
			for _, o := range opts {
				s, ok := o.(string)
				if !ok || s == "" {
					continue
				}
				if len(s) > MaxOptionBytes {
					return nil, fmt.Errorf("option too long, max %d bytes", MaxOptionBytes)
				}
				q.Options = append(q.Options, s)
			}
		}
		if multiple, ok := m["multiple"].(bool); ok && multiple && len(q.Options) > 0 {
			q.Multiple = true
		}
		questions = append(questions, q)
	}
	return questions, nil
}

func formatAnswers(questions []Question, reply Reply) core.Result {
	answers := reply.Answers
	// answers may be shorter than questions (e.g. an aborted prompt resolves
	// with an empty slice) — never index past its length.
	answerAt := func(i int) string {
		if i < len(answers) {
			return answers[i]
		}
		return ""
	}
	var sb strings.Builder
	if line := reply.Responder.Attribution(); line != "" {
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	if len(questions) == 1 {
		sb.WriteString(answerAt(0))
	} else {
		for i, q := range questions {
			if i > 0 {
				sb.WriteString("\n")
			}
			fmt.Fprintf(&sb, "Q: %s\nA: %s", q.Text, answerAt(i))
		}
	}
	result := core.TextResult(sb.String())
	if reply.Responder.Kind != "" {
		result.Custom = map[string]any{CustomKey: reply.Responder}
	}
	return result
}
