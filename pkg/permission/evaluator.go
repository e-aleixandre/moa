package permission

import (
	"context"
	"fmt"
	"strings"

	"github.com/ealeixandre/moa/pkg/core"
)

// Decision is the AI evaluator's verdict.
type Decision int

const (
	DecisionApprove Decision = iota
	DecisionDeny
	DecisionAsk // escalate to user
)

// Evaluator uses a lightweight LLM to decide whether a tool call is safe.
type Evaluator struct {
	provider core.Provider
	model    core.Model
}

// NewEvaluator creates an evaluator with the given provider and model.
func NewEvaluator(provider core.Provider, model core.Model) *Evaluator {
	return &Evaluator{provider: provider, model: model}
}

// Evaluate asks the LLM whether the tool call should be approved, denied,
// or escalated to the user. Rules are natural language instructions that
// guide the decision.
func (e *Evaluator) Evaluate(ctx context.Context, toolName string, args map[string]any, rules []string) Decision {
	prompt := buildEvalPrompt(toolName, args, rules)

	req := core.Request{
		Model:    e.model,
		Messages: []core.Message{core.NewUserMessage(prompt)},
		Options:  core.StreamOptions{ThinkingLevel: "off"},
	}

	stream, err := e.provider.Stream(ctx, req)
	if err != nil {
		return DecisionAsk // on error, escalate to user
	}

	var response strings.Builder
	for event := range stream {
		if event.Type == core.ProviderEventTextDelta {
			response.WriteString(event.Delta)
		}
	}

	return parseDecision(response.String())
}

func buildEvalPrompt(toolName string, args map[string]any, rules []string) string {
	var sb strings.Builder

	sb.WriteString(`You are a security gate for a coding agent running on the user's machine.
Decide whether this tool call is safe to execute WITHOUT asking the user.

DEFAULT POLICY (apply when no user rules say otherwise):
- APPROVE read-only operations that don't leak sensitive data: reading
  source code, docs, configs. Running safe commands: ls, cat, grep, find,
  go build, go test, npm test, git status, git log, git diff, etc.
- ASK for anything that modifies state: writing/deleting files, installing
  packages, running commands with side effects, git commits/push, network
  requests, reading potentially sensitive files (.env, keys, credentials,
  ~/.ssh/*, ~/.aws/*), or anything you're unsure about.
- Never DENY on your own. Only DENY when a user rule explicitly forbids it.

`)

	if len(rules) > 0 {
		sb.WriteString("USER RULES (override the defaults above — may APPROVE or DENY):\n")
		for _, r := range rules {
			sb.WriteString(fmt.Sprintf("- %s\n", r))
		}
		sb.WriteString("\n")
	}

	sb.WriteString(fmt.Sprintf("Tool: %s\n", toolName))

	if len(args) > 0 {
		sb.WriteString("Arguments:\n")
		for k, v := range args {
			val := fmt.Sprintf("%v", v)
			if len(val) > 500 {
				val = val[:500] + "..."
			}
			sb.WriteString(fmt.Sprintf("  %s: %s\n", k, val))
		}
	}

	sb.WriteString(`
Respond with exactly one word: APPROVE, DENY, or ASK.
- APPROVE: clearly safe, or explicitly allowed by a user rule.
- DENY: clearly dangerous, sensitive, or explicitly forbidden by a user rule.
- ASK: uncertain — let the user decide.
`)

	return sb.String()
}

func parseDecision(response string) Decision {
	s := strings.TrimSpace(strings.ToUpper(response))

	// Handle responses that might have extra text
	if strings.Contains(s, "APPROVE") {
		return DecisionApprove
	}
	if strings.Contains(s, "DENY") {
		return DecisionDeny
	}
	// Default to ask when uncertain
	return DecisionAsk
}
