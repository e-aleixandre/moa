package dreaming

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// ProviderFactory builds a provider for a model, with the user's auth.
type ProviderFactory func(core.Model) (core.Provider, error)

// transportOverheadBytes bounds what a transport adds to a prompt on its own:
// Anthropic's OAuth identity preamble, message framing, the thinking
// configuration. Measured well under 1 KiB today; the margin keeps the
// reservation an upper bound if it grows.
const transportOverheadBytes = 4096

// callTimeout bounds one model call.
const callTimeout = 15 * time.Minute

// ErrBudget reports that a call could exceed the night's cap.
var ErrBudget = errors.New("the night's budget would be exceeded")

// Budget is the night's hard cap (I10). Every call reserves its maximum cost
// before it is sent: the prompt counted as one token per byte (a token is at
// least one byte) at the dearest input rate the model has, plus the output cap
// at the dearest output rate. What a call really cost replaces its
// reservation; a call that reports no usage keeps its whole reservation.
type Budget struct {
	Limit float64
	Spent float64
}

func maxRates(p *core.Pricing) (in, out float64) {
	in, out = p.Input, p.Output
	for _, w := range []float64{p.CacheWrite, p.CacheWrite1h} {
		in = max(in, w)
	}
	for _, t := range p.Tiers {
		in = max(in, t.Input, t.CacheWrite, t.CacheWrite1h)
		out = max(out, t.Output)
	}
	if p.FastMultiplier > 1 {
		in *= p.FastMultiplier
		out *= p.FastMultiplier
	}
	return in, out
}

// outputCap is the output (thinking included) a request may produce: the cap
// sent when the transport applies it, otherwise the model's maximum.
func outputCap(model core.Model, requested int) int {
	if model.Provider == "anthropic" && requested > 0 {
		return core.ResolveMaxOutputTokens(model, &requested)
	}
	if model.MaxOutput > 0 {
		return model.MaxOutput
	}
	return core.DefaultMaxOutputTokens
}

// Reservation is the most a request can cost.
func Reservation(model core.Model, system, prompt string, requestedOut int) (float64, error) {
	if model.Pricing == nil || model.Pricing.Input <= 0 || model.Pricing.Output <= 0 {
		return 0, fmt.Errorf("model %s has no known price; the dreaming does not use it", model.ID)
	}
	in, out := maxRates(model.Pricing)
	inTokens := len(system) + len(prompt) + transportOverheadBytes
	return float64(inTokens)*in/1e6 + float64(outputCap(model, requestedOut))*out/1e6, nil
}

// callResult is one model answer and what it cost.
type callResult struct {
	Text string
	Cost float64
}

// call sends one tool-less request (I6): the model can only answer.
func (b *Budget) call(ctx context.Context, factory ProviderFactory, model core.Model, system, prompt string, maxOut int) (callResult, error) {
	res, err := Reservation(model, system, prompt, maxOut)
	if err != nil {
		return callResult{}, err
	}
	if b.Spent+res > b.Limit {
		return callResult{}, ErrBudget
	}
	thinking, err := core.EffectiveThinkingLevel(model, "medium")
	if err != nil {
		return callResult{}, err
	}
	prov, err := factory(model)
	if err != nil {
		return callResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	req := core.Request{
		Model:    model,
		System:   system,
		Messages: []core.Message{core.NewUserMessage(prompt)},
		Options: core.StreamOptions{
			ThinkingLevel:  thinking,
			MaxTokens:      &maxOut,
			CacheRetention: core.CacheOff,
		},
	}
	// From here on the request may be billed: whatever happens, the night
	// pays at least the reservation unless the provider says otherwise.
	spent := res
	defer func() { b.Spent += spent }()
	ch, err := prov.Stream(ctx, req)
	if err != nil {
		return callResult{}, err
	}
	var text strings.Builder
	var final *core.Message
	var streamErr error
	for ev := range ch {
		switch ev.Type {
		case core.ProviderEventTextDelta:
			text.WriteString(ev.Delta)
		case core.ProviderEventDone:
			final = ev.Message
		case core.ProviderEventError:
			streamErr = ev.Error
		}
	}
	if final != nil && final.Usage != nil {
		spent = model.Pricing.Cost(*final.Usage)
	}
	if streamErr != nil {
		return callResult{Cost: spent}, streamErr
	}
	out := text.String()
	if strings.TrimSpace(out) == "" && final != nil {
		for _, c := range final.Content {
			if c.Type == "text" {
				out += c.Text
			}
		}
	}
	return callResult{Text: out, Cost: spent}, nil
}
