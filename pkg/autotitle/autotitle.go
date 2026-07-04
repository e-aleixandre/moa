// Package autotitle generates short, human-readable session titles from a
// conversation using a cheap LLM call. It is shared by all frontends (TUI,
// serve) so the auto-titling behavior stays identical across them.
package autotitle

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

// DefaultModelSpec is the cheap, fast model used for title generation when the
// session's provider has no cheaper same-vendor option (Anthropic).
const DefaultModelSpec = "haiku"

// cheapModelSpecFor returns a cheap title-generation model on the SAME provider
// as the session, so an OpenAI session's transcript isn't shipped to Anthropic
// (a different vendor) just to make a title. Falls back to the Anthropic default.
func cheapModelSpecFor(provider string) string {
	if provider == "openai" {
		return "gpt-5.4-mini"
	}
	return DefaultModelSpec
}

// MaxTitleLen caps the generated title length (in runes).
const MaxTitleLen = 60

// generateTimeout bounds the one-shot LLM call so a slow provider never
// blocks the caller (which runs in the background anyway).
const generateTimeout = 20 * time.Second

const systemPrompt = `You write short, specific titles for coding-assistant sessions.

Given the start of a conversation, reply with a concise title (3 to 7 words)
that captures the concrete task or topic. Rules:
- No quotes, no trailing punctuation, no markdown.
- Prefer the specific subject over generic phrases ("Fix auth token refresh",
  not "Help with code").
- Ignore greetings and small talk.
- Reply with ONLY the title, nothing else.`

// ProviderFactory builds a provider for a given model. Callers pass the same
// factory they use elsewhere (it handles auth/OAuth refresh).
type ProviderFactory func(core.Model) (core.Provider, error)

// Generate makes a one-shot LLM call to produce a session title from the
// conversation messages. It uses a cheap model (DefaultModelSpec) and returns
// the cleaned title. Returns an error if the model can't be resolved, the
// provider can't be built, or the call produces no usable text.
func Generate(ctx context.Context, factory ProviderFactory, sessionModel core.Model, msgs []core.AgentMessage) (string, error) {
	if factory == nil {
		return "", fmt.Errorf("autotitle: nil provider factory")
	}
	prompt := buildPrompt(msgs)
	if prompt == "" {
		return "", fmt.Errorf("autotitle: no conversation content")
	}

	spec := cheapModelSpecFor(sessionModel.Provider)
	model, ok := core.ResolveModel(spec)
	if !ok {
		return "", fmt.Errorf("autotitle: cannot resolve model %q", spec)
	}
	prov, err := factory(model)
	if err != nil {
		return "", fmt.Errorf("autotitle: provider: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, generateTimeout)
	defer cancel()

	req := core.Request{
		Model:    model,
		System:   systemPrompt,
		Messages: []core.Message{core.NewUserMessage(prompt)},
		Options:  core.StreamOptions{ThinkingLevel: "off"},
	}
	ch, err := prov.Stream(ctx, req)
	if err != nil {
		return "", fmt.Errorf("autotitle: stream: %w", err)
	}

	var text strings.Builder
	var finalMsg *core.Message
	for event := range ch {
		switch event.Type {
		case core.ProviderEventTextDelta:
			text.WriteString(event.Delta)
		case core.ProviderEventDone:
			finalMsg = event.Message
		case core.ProviderEventError:
			return "", fmt.Errorf("autotitle: %w", event.Error)
		}
	}

	result := text.String()
	if strings.TrimSpace(result) == "" && finalMsg != nil {
		for _, c := range finalMsg.Content {
			if c.Type == "text" {
				result += c.Text
			}
		}
	}

	title := clean(result)
	if title == "" {
		return "", fmt.Errorf("autotitle: empty output")
	}
	return title, nil
}

// buildPrompt serializes the first few messages into a compact prompt. It caps
// the amount of text so the call stays cheap regardless of conversation size.
func buildPrompt(msgs []core.AgentMessage) string {
	const maxChars = 4000
	var b strings.Builder
	for _, msg := range msgs {
		var role string
		switch msg.Role {
		case "user":
			role = "User"
		case "assistant":
			role = "Assistant"
		default:
			continue
		}
		text := firstText(msg.Content)
		if strings.TrimSpace(text) == "" {
			continue
		}
		if b.Len()+len(text) > maxChars {
			text = text[:max(0, maxChars-b.Len())]
		}
		fmt.Fprintf(&b, "%s: %s\n", role, strings.TrimSpace(text))
		if b.Len() >= maxChars {
			break
		}
	}
	return strings.TrimSpace(b.String())
}

func firstText(content []core.Content) string {
	for _, c := range content {
		if c.Type == "text" && c.Text != "" {
			return c.Text
		}
	}
	return ""
}

// clean strips quotes, surrounding whitespace, trailing punctuation and caps
// the length to MaxTitleLen runes.
func clean(s string) string {
	s = strings.TrimSpace(s)
	// Take the first non-empty line only.
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"'`)
	s = strings.TrimRight(s, ".!, ")
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) > MaxTitleLen {
		s = strings.TrimSpace(string(runes[:MaxTitleLen]))
	}
	return s
}
