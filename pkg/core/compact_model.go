package core

import "strings"

// CompactModelFallback says why compaction is not using the configured
// summarizer. It exists so the reason can be shown in the conversation instead
// of silently billing the session's (usually pricier) model.
type CompactModelFallback string

const (
	// CompactModelFallbackNone: the configured model is being used, or none is
	// configured and the session's model is the intended one.
	CompactModelFallbackNone CompactModelFallback = ""
	// CompactModelFallbackUnknown: the spec names no model in the catalog,
	// typically a hand-edited config or a model dropped from a release.
	CompactModelFallbackUnknown CompactModelFallback = "unknown_model"
	// CompactModelFallbackNoCredential: the model exists but its provider has
	// no usable credential right now — an expired OAuth token, a deleted key.
	CompactModelFallbackNoCredential CompactModelFallback = "no_credential"
)

// ResolveCompactModel picks the model that writes compaction summaries.
//
// It returns the session's own model unchanged when no summarizer is
// configured, and never fails: a compaction that refuses to run leaves the
// session growing until it hits the window, which is a worse outcome than
// summarizing with a more expensive model. When the configured choice cannot be
// honoured the session's model is returned together with the reason, so the
// caller can say so in the conversation rather than swallow it.
func ResolveCompactModel(spec string, sessionModel Model, available AuxiliaryModelAvailable) (Model, CompactModelFallback) {
	spec = strings.TrimSpace(spec)
	if spec == "" || strings.EqualFold(spec, CompactModelSession) {
		return sessionModel, CompactModelFallbackNone
	}

	model, ok := ResolveModel(spec)
	if !ok || model.ID == "" {
		return sessionModel, CompactModelFallbackUnknown
	}

	// Same rule the auxiliary models follow: choosing a provider explicitly
	// does not waive the credential it needs. A transcription-only key must not
	// pass for a completion credential here either.
	if model.Provider != sessionModel.Provider && (available == nil || !available(model.Provider)) {
		return sessionModel, CompactModelFallbackNoCredential
	}
	return model, CompactModelFallbackNone
}

// CompactModelFallbackNotice renders the fallback for the conversation. It
// returns "" when nothing was overridden, so callers can emit unconditionally.
//
// The ordinary compaction says nothing: the compaction card already tells that
// story. This line exists only for the case the transcript could not otherwise
// explain — the configured summarizer was not the one that wrote the summary.
// It leads with what happened, and gives the reason second.
func CompactModelFallbackNotice(reason CompactModelFallback, spec string, used Model) string {
	name := used.Name
	if name == "" {
		name = used.ID
	}
	switch reason {
	case CompactModelFallbackUnknown:
		return "✂ Summarized with " + name + " — " + strings.TrimSpace(spec) + " is not a known model"
	case CompactModelFallbackNoCredential:
		return "✂ Summarized with " + name + " — no usable credential for " + strings.TrimSpace(spec)
	default:
		return ""
	}
}
