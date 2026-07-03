package goal

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

// DefaultVerifierSpec is the cheap, fast model used to judge the objective.
const DefaultVerifierSpec = "haiku"

// verifyTimeout bounds the one-shot verifier call.
const verifyTimeout = 60 * time.Second

// Verdict is the verifier's decision.
type Verdict struct {
	Satisfied bool   `json:"satisfied"`
	Feedback  string `json:"feedback"`
}

// ProviderFactory builds a provider for a given model. Callers pass the same
// factory they use elsewhere (it handles auth/OAuth refresh).
type ProviderFactory func(core.Model) (core.Provider, error)

const verifierSystemPrompt = `You are a strict verifier in an autonomous coding loop. You did NOT write the work — your only job is to judge whether the stated OBJECTIVE has been met, using ONLY the EVIDENCE provided.

Be skeptical. If the evidence does not clearly demonstrate the objective is complete, it is NOT satisfied — do not give the benefit of the doubt.

Reply with ONLY a JSON object, no prose and no markdown fences:
{"satisfied": <true|false>, "feedback": "<if not satisfied: what concretely is missing or wrong, so the worker can fix it next; if satisfied: a brief confirmation>"}`

// Verify makes a one-shot call to a cheap, separate model to judge whether the
// objective is satisfied given the evidence (typically the maker's final text
// plus a git diff). The verifier gets minimal context — objective + evidence,
// no history and no tools — which keeps it cheap and unbiased.
func Verify(ctx context.Context, factory ProviderFactory, verifierSpec, objective, evidence string) (Verdict, error) {
	if factory == nil {
		return Verdict{}, fmt.Errorf("goal verify: nil provider factory")
	}
	spec := verifierSpec
	if spec == "" {
		spec = DefaultVerifierSpec
	}
	model, ok := core.ResolveModel(spec)
	if !ok {
		return Verdict{}, fmt.Errorf("goal verify: cannot resolve model %q", spec)
	}
	prov, err := factory(model)
	if err != nil {
		return Verdict{}, fmt.Errorf("goal verify: provider: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()

	user := fmt.Sprintf("OBJECTIVE:\n%s\n\nEVIDENCE:\n%s", strings.TrimSpace(objective), strings.TrimSpace(evidence))
	req := core.Request{
		Model:    model,
		System:   verifierSystemPrompt,
		Messages: []core.Message{core.NewUserMessage(user)},
		Options:  core.StreamOptions{ThinkingLevel: "off"},
	}
	ch, err := prov.Stream(ctx, req)
	if err != nil {
		return Verdict{}, fmt.Errorf("goal verify: stream: %w", err)
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
			return Verdict{}, fmt.Errorf("goal verify: %w", event.Error)
		}
	}

	out := text.String()
	if strings.TrimSpace(out) == "" && finalMsg != nil {
		for _, c := range finalMsg.Content {
			if c.Type == "text" {
				out += c.Text
			}
		}
	}
	return parseVerdict(out), nil
}

// parseVerdict extracts the JSON verdict. On any parse failure it conservatively
// returns not-satisfied, keeping the raw text as feedback so the maker still
// gets a signal.
func parseVerdict(s string) Verdict {
	if raw := extractJSONObject(s); raw != "" {
		var v Verdict
		if err := json.Unmarshal([]byte(raw), &v); err == nil {
			return v
		}
	}
	return Verdict{Satisfied: false, Feedback: strings.TrimSpace(s)}
}

// extractJSONObject returns the substring from the first '{' to the last '}',
// tolerating markdown fences or surrounding prose. Returns "" if none found.
func extractJSONObject(s string) string {
	i := strings.IndexByte(s, '{')
	j := strings.LastIndexByte(s, '}')
	if i < 0 || j < 0 || j < i {
		return ""
	}
	return s[i : j+1]
}
