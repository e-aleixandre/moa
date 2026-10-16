package core

import "strings"

// Daybreak is a per-session request mode, not a model: the request carries an
// access program next to an ordinary model, the way the official Codex client
// sends it ({"access_programs":{"cyber":"daybreak_blue"}}).
const (
	// DaybreakAccessProgram is the wire value of access_programs.cyber.
	DaybreakAccessProgram = "daybreak_blue"
	// DaybreakModelID is the model the "daybreak" spec resolves to.
	DaybreakModelID = "gpt-6-sol"
	// LegacyDaybreakModelID is the retired alias sessions used before the mode
	// existed. It stays in the registry for the cost and identity of history,
	// but asking for it now means DaybreakModelID with the program.
	LegacyDaybreakModelID = "gpt-daybreak-blue-latest"
	// daybreakSpec is the short name for "GPT-6 Sol with Daybreak".
	daybreakSpec = "daybreak"
)

// DaybreakSpec reports whether spec names the Daybreak mode: "daybreak" or the
// retired alias, optionally prefixed with "openai/".
func DaybreakSpec(spec string) bool {
	spec = strings.ToLower(strings.TrimSpace(spec))
	if rest, ok := strings.CutPrefix(spec, "openai/"); ok {
		spec = strings.TrimSpace(rest)
	}
	return spec == daybreakSpec || spec == LegacyDaybreakModelID
}

// ResolveModelDaybreak resolves spec like ResolveModel, also reporting whether
// it asks for Daybreak. A Daybreak spec resolves to DaybreakModelID.
func ResolveModelDaybreak(spec string) (Model, bool, bool) {
	if DaybreakSpec(spec) {
		m, ok := knownModels[DaybreakModelID]
		return m, true, ok
	}
	m, ok := ResolveModel(spec)
	return m, false, ok
}

// DaybreakAvailability is a snapshot of what the account's model catalog says
// about Daybreak.
type DaybreakAvailability struct {
	// Transport is whether the selected OpenAI credential is the plan's OAuth,
	// the only transport that carries the program.
	Transport bool
	// Known is whether the catalog was ever read. Until then nothing is
	// rejected: the request is sent and the provider answers.
	Known bool
	// Models maps a catalog model id to whether it accepts Daybreak.
	Models map[string]bool
}

// Sendable reports whether a Daybreak request for m would carry the program.
func (a DaybreakAvailability) Sendable(m Model) bool {
	if m.Provider != "openai" || !a.Transport {
		return false
	}
	return !a.Known || a.Models[m.ID]
}

// Rejects reports whether the catalog rules m out for Daybreak. Only a known
// catalog rejects, and only OpenAI models: another provider simply does not
// apply the program.
func (a DaybreakAvailability) Rejects(m Model) bool {
	return a.Known && m.Provider == "openai" && !a.Models[m.ID]
}

// Any reports whether the switch is worth offering: the plan transport is
// selected and the catalog advertises some selectable Daybreak model.
func (a DaybreakAvailability) Any() bool {
	if !a.Transport || !a.Known {
		return false
	}
	for id, ok := range a.Models {
		if ok && id != LegacyDaybreakModelID {
			return true
		}
	}
	return false
}

// DaybreakPromptCacheKey derives the cache key of a request sent with program,
// so a Daybreak conversation never shares the standard one's cache routing.
// Deterministic: the same session and program always yield the same key.
func DaybreakPromptCacheKey(key, program string) string {
	if key == "" || program == "" {
		return key
	}
	return key + ":" + program
}

// IsModelAllowedFor is IsModelAllowed for a request that may carry Daybreak.
// The "daybreak" spec and the retired alias authorize DaybreakModelID with the
// program, never the model on its own.
func IsModelAllowedFor(model Model, daybreak bool, allowedIDs []string) bool {
	if IsModelAllowed(model, allowedIDs) {
		return true
	}
	if !daybreak || model.ID != DaybreakModelID {
		return false
	}
	for _, id := range allowedIDs {
		if DaybreakSpec(id) {
			return true
		}
	}
	return false
}

// daybreakAllowed reports whether allowedIDs (non-empty) lets the "daybreak"
// spec through.
func daybreakAllowed(allowedIDs []string) bool {
	for _, id := range allowedIDs {
		if id == DaybreakModelID || DaybreakSpec(id) {
			return true
		}
	}
	return false
}
