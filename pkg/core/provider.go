package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Provider streams LLM responses. Each provider (Anthropic, OpenAI, etc.)
// implements this interface, emitting normalized AssistantEvents.
//
// Error contract:
//   - Returns error immediately for pre-stream failures (auth, invalid model, network).
//   - If channel is returned, it ALWAYS receives exactly one terminal event
//     ("done" or "error") before being closed.
//   - The caller must drain the channel to avoid goroutine leaks.
//   - Context cancellation causes an "error" event with ctx.Err().
type Provider interface {
	Stream(ctx context.Context, req Request) (<-chan AssistantEvent, error)
}

// DocumentCapableProvider is an optional interface a Provider may implement to
// declare whether it accepts native "document" content blocks (e.g. PDFs).
// Providers that don't implement it are treated as NOT document-capable.
type DocumentCapableProvider interface {
	SupportsDocuments() bool
}

// ProviderUnwrapper is optionally implemented by Provider decorators to expose
// the provider they wrap. Capability helpers follow this chain so decorators
// do not hide optional provider interfaces.
//
// Unwrap must return nil when there is no wrapped provider.
type ProviderUnwrapper interface {
	Unwrap() Provider
}

// ProviderSupportsDocuments reports whether p accepts native document blocks.
// Conservative: an unknown provider (not implementing DocumentCapableProvider)
// returns false, so callers fall back to disk rather than silently dropping a
// PDF the provider can't handle.
func ProviderSupportsDocuments(p Provider) bool {
	for p != nil {
		if dc, ok := p.(DocumentCapableProvider); ok {
			return dc.SupportsDocuments()
		}
		unwrapper, ok := p.(ProviderUnwrapper)
		if !ok {
			return false
		}
		p = unwrapper.Unwrap()
	}
	return false
}

// Request contains everything needed for an LLM call.
type Request struct {
	Model    Model
	System   string     // System prompt
	Messages []Message  // Conversation history (user, assistant, tool_result)
	Tools    []ToolSpec // Available tools for tool_use
	Options  StreamOptions
}

// ToolSpec is a tool definition sent to the LLM (name + description + JSON schema).
// Separate from the executable Tool to keep the provider layer dependency-free.
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// Model identifies an LLM model.
type Model struct {
	ID        string   `json:"id"`
	Provider  string   `json:"provider"`
	API       string   `json:"api"`
	Name      string   `json:"name"`
	MaxInput  int      `json:"max_input"`
	MaxOutput int      `json:"max_output"`
	Pricing   *Pricing `json:"pricing,omitempty"`
}

// DefaultMaxOutputTokens bounds a single model response when the caller has
// not selected a smaller cap. It leaves room for reasoning without allowing
// one request to consume a model's entire output allowance.
const DefaultMaxOutputTokens = 32_000

// ResolveMaxOutputTokens returns the effective output cap for a request. An
// explicit caller value wins, subject to the model's advertised capability;
// otherwise the shared operational default is used.
func ResolveMaxOutputTokens(model Model, requested *int) int {
	maxTokens := DefaultMaxOutputTokens
	if requested != nil && *requested > 0 {
		maxTokens = *requested
	}
	if model.MaxOutput > 0 && maxTokens > model.MaxOutput {
		return model.MaxOutput
	}
	return maxTokens
}

// Pricing holds per-token costs in USD per million tokens.
//
// Some providers (e.g. OpenAI's long-context GPT models) charge a different
// flat rate once the prompt exceeds a context-length threshold. Tiers lists
// those higher-context rates in ascending Threshold order; the base
// Input/Output/CacheRead/CacheWrite fields are the tier that applies below
// the first threshold ("short context"). Cost picks the tier by the
// request's total input context (Input+CacheRead tokens count toward the
// prompt length the provider bills against) and applies it to the *whole*
// request, matching how these providers actually bill — not a blended rate.
type Pricing struct {
	Input      float64 `json:"input"`       // $/M input tokens
	Output     float64 `json:"output"`      // $/M output tokens
	CacheRead  float64 `json:"cache_read"`  // $/M cached input tokens
	CacheWrite float64 `json:"cache_write"` // $/M cache write tokens (5-minute window)
	// FastMultiplier is the premium-tier price multiplier. Zero means this
	// model does not support a premium tier.
	FastMultiplier float64 `json:"fast_multiplier,omitempty"`
	// CacheWrite1h is the $/M rate for writes into the extended 1-hour cache
	// (Anthropic bills these at 2x input, versus 1.25x for the 5-minute
	// window). Zero means the model has no extended window, and Usage's
	// CacheWrite1h tokens — which will also be zero — fall back to CacheWrite.
	CacheWrite1h float64 `json:"cache_write_1h,omitempty"`

	// Tiers holds additional pricing tiers keyed by a context-length
	// threshold, for providers that charge more once the prompt exceeds a
	// given size. Must be sorted ascending by Threshold.
	Tiers []PricingTier `json:"tiers,omitempty"`
}

// PricingTier is a pricing tier that applies once the request's context
// (input + cache-read tokens) reaches Threshold tokens.
type PricingTier struct {
	Threshold    int     `json:"threshold"`                // tier applies when Input+CacheRead >= this
	Input        float64 `json:"input"`                    // $/M input tokens
	Output       float64 `json:"output"`                   // $/M output tokens
	CacheRead    float64 `json:"cache_read"`               // $/M cached input tokens
	CacheWrite   float64 `json:"cache_write"`              // $/M cache write tokens (5-minute window)
	CacheWrite1h float64 `json:"cache_write_1h,omitempty"` // $/M extended-window cache write tokens
}

// Cost calculates the USD cost for a given Usage, selecting the pricing
// tier based on the request's total context (Input+CacheRead tokens) and
// applying that tier's rates to the entire request.
//
// Cache writes are split: Usage.CacheWrite1h is the portion written into the
// extended 1-hour window and is billed at the higher CacheWrite1h rate; the
// remainder is billed at the 5-minute CacheWrite rate. When either the usage
// split or the extended rate is absent, the whole write falls back to
// CacheWrite, which is the pre-split behavior.
func (p *Pricing) Cost(u Usage) float64 {
	if p == nil {
		return 0
	}
	rate := struct {
		Input, Output, CacheRead, CacheWrite, CacheWrite1h float64
	}{p.Input, p.Output, p.CacheRead, p.CacheWrite, p.CacheWrite1h}

	context := u.Input + u.CacheRead
	for _, t := range p.Tiers {
		if context >= t.Threshold {
			rate.Input, rate.Output, rate.CacheRead, rate.CacheWrite = t.Input, t.Output, t.CacheRead, t.CacheWrite
			rate.CacheWrite1h = t.CacheWrite1h
		}
	}

	const m = 1_000_000.0
	cost := float64(u.Input)*rate.Input/m + float64(u.Output)*rate.Output/m
	if rate.CacheRead > 0 {
		cost += float64(u.CacheRead) * rate.CacheRead / m
	}
	// Split the write between the extended and base windows. Guard against a
	// CacheWrite1h larger than the reported total so a provider inconsistency
	// can never produce a negative base-rate charge.
	write1h := u.CacheWrite1h
	if write1h > u.CacheWrite {
		write1h = u.CacheWrite
	}
	if rate.CacheWrite1h <= 0 {
		write1h = 0 // no extended rate known: bill everything at the base rate
	}
	if rate.CacheWrite1h > 0 && write1h > 0 {
		cost += float64(write1h) * rate.CacheWrite1h / m
	}
	if rate.CacheWrite > 0 {
		cost += float64(u.CacheWrite-write1h) * rate.CacheWrite / m
	}
	if u.Fast && p.FastMultiplier > 0 {
		cost *= p.FastMultiplier
	}
	return cost
}

// StreamOptions configures an LLM request.
type StreamOptions struct {
	Temperature   *float64 `json:"temperature,omitempty"`
	MaxTokens     *int     `json:"max_tokens,omitempty"`
	APIKey        string   `json:"-"`
	ThinkingLevel string   `json:"thinking_level,omitempty"`
	// CacheRetention selects the prompt-cache TTL: "" is the provider default
	// (5 minutes), "1h" opts into the long TTL, and CacheOff suppresses cache
	// writes entirely.
	CacheRetention string `json:"cache_retention,omitempty"`
	// PromptCacheKey identifies the conversation this request belongs to, so
	// requests sharing a prefix are routed to the machine holding its cache.
	// Consumed by the Responses providers (OpenAI, xAI); Anthropic has no
	// equivalent and ignores it.
	PromptCacheKey string `json:"prompt_cache_key,omitempty"`
	// Fast asks the provider to serve this request at a premium speed and
	// price. Each provider spells it differently — Anthropic takes a beta
	// header plus `speed`, the Responses providers a `service_tier` — and a
	// provider that has no such notion, or a model that doesn't support it,
	// ignores the flag rather than failing.
	Fast bool `json:"fast,omitempty"`
	// OnFastUnavailable is called when a provider rejects a fast request
	// because the account cannot use the premium tier. It is process-local
	// state coordination, never part of a provider payload.
	OnFastUnavailable func() `json:"-"`
}

// CacheOff is the CacheRetention value for a request whose prefix will never
// be reused. A cache write is billed at a premium over plain input, so paying
// it for an entry no later request can hit is pure cost: conversation turns
// share a prefix and benefit, one-shot requests built from a different system
// prompt and a flattened transcript do not.
const CacheOff = "off"

// ThinkingLevels is the canonical list of valid thinking levels.
// All validation and UI should reference this slice — not hardcoded strings.
var ThinkingLevels = []string{"off", "low", "medium", "high", "xhigh"}

// IsValidThinkingLevel reports whether level is a recognized thinking level.
func IsValidThinkingLevel(level string) bool {
	for _, l := range ThinkingLevels {
		if level == l {
			return true
		}
	}
	return false
}

// ThinkingLevelOptions returns a human-readable list for error messages.
func ThinkingLevelOptions() string {
	s := ""
	for i, l := range ThinkingLevels {
		if i > 0 {
			s += ", "
		}
		s += l
	}
	return s
}

// ThinkingAlwaysOn reports whether a model thinks on every turn. Off is not a
// real setting there: the API still thinks, at its default effort (high).
func ThinkingAlwaysOn(model Model) bool {
	id := strings.ToLower(model.ID)
	if resolved, ok := ResolveModel(model.ID); ok {
		id = strings.ToLower(resolved.ID)
	}
	return strings.Contains(id, "fable-5-1") || strings.Contains(id, "fable-5.1")
}

// EffectiveThinkingLevel resolves a persisted level for a model. xAI requires
// reasoning and only accepts low/medium/high. Meta also requires it; "off"
// becomes "low" so a session never persists a level the selector can't show,
// while internal callers may ask for "minimal" explicitly. Models whose
// thinking cannot be turned off promote "off" to the API default ("high").
// Other providers keep the value.
func EffectiveThinkingLevel(model Model, level string) (string, error) {
	if model.Provider == "xai" {
		switch level {
		case "off", "minimal", "low":
			return "low", nil
		case "medium":
			return "medium", nil
		case "high", "xhigh":
			return "high", nil
		default:
			return "", fmt.Errorf("invalid xAI thinking level %q (choose: low, medium, high)", level)
		}
	}
	if model.Provider == "meta" {
		switch level {
		case "off", "low":
			return "low", nil
		case "minimal", "medium", "high", "xhigh":
			return level, nil
		default:
			return "", fmt.Errorf("invalid Meta thinking level %q (choose: low, medium, high, xhigh)", level)
		}
	}
	if !IsValidThinkingLevel(level) {
		return "", fmt.Errorf("invalid thinking level %q", level)
	}
	if ThinkingAlwaysOn(model) && level == "off" {
		return "high", nil
	}
	return level, nil
}

// AssistantEvent is emitted by providers during streaming.
//
// Terminal events: "done" (success) or "error" (failure).
// Every stream ends with exactly one terminal event, then channel close.
type AssistantEvent struct {
	Type         string   `json:"type"`
	ContentIndex int      `json:"content_index,omitempty"`
	Delta        string   `json:"delta,omitempty"`
	Partial      *Message `json:"partial,omitempty"`
	Message      *Message `json:"message,omitempty"`
	Error        error    `json:"-"`

	// Tool call metadata — populated for toolcall_start, toolcall_delta, toolcall_end events.
	ToolCallID  string         `json:"tool_call_id,omitempty"`
	ToolName    string         `json:"tool_name,omitempty"`
	PartialArgs map[string]any `json:"partial_args,omitempty"`

	// RateLimit — populated for the "ratelimit" event, emitted once at stream
	// start from the response headers (independent of message success).
	RateLimit *RateLimit `json:"rate_limit,omitempty"`
}

// IsTerminal returns true for "done" or "error" events.
func (e AssistantEvent) IsTerminal() bool {
	return e.Type == ProviderEventDone || e.Type == ProviderEventError
}

// Provider event type constants.
const (
	ProviderEventStart         = "start"
	ProviderEventTextStart     = "text_start"
	ProviderEventTextDelta     = "text_delta"
	ProviderEventTextEnd       = "text_end"
	ProviderEventThinkingStart = "thinking_start"
	ProviderEventThinkingDelta = "thinking_delta"
	ProviderEventThinkingEnd   = "thinking_end"
	ProviderEventToolCallStart = "toolcall_start"
	ProviderEventToolCallDelta = "toolcall_delta"
	ProviderEventToolCallEnd   = "toolcall_end"
	ProviderEventRateLimit     = "ratelimit"
	ProviderEventDone          = "done"
	ProviderEventError         = "error"
)
