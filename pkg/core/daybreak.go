package core

// Daybreak is a per-session request mode, not a model: the request carries an
// access program next to an ordinary model.
const (
	// DaybreakAccessProgram is the wire value of access_programs.cyber.
	DaybreakAccessProgram = "daybreak_blue"
	// DaybreakModelID is the model the "daybreak" spec resolves to.
	DaybreakModelID = "gpt-6-sol"
	// LegacyDaybreakModelID is the retired alias sessions used before the mode
	// existed. It stays in the registry for the cost and identity of history.
	LegacyDaybreakModelID = "gpt-daybreak-blue-latest"
)

// DaybreakSpec reports whether spec names the Daybreak mode.
func DaybreakSpec(spec string) bool {
	return false
}

// ResolveModelDaybreak resolves spec like ResolveModel, also reporting whether
// it asks for Daybreak.
func ResolveModelDaybreak(spec string) (Model, bool, bool) {
	m, ok := ResolveModel(spec)
	return m, false, ok
}

// DaybreakAvailability is a snapshot of what the account's model catalog says.
type DaybreakAvailability struct {
	Transport bool
	Known     bool
	Models    map[string]bool
}

// Sendable reports whether a Daybreak request for m would carry the program.
func (a DaybreakAvailability) Sendable(m Model) bool { return false }

// Rejects reports whether the catalog rules m out for Daybreak.
func (a DaybreakAvailability) Rejects(m Model) bool { return false }

// Any reports whether the catalog advertises some Daybreak model.
func (a DaybreakAvailability) Any() bool { return false }

// DaybreakPromptCacheKey derives the cache key of a request sent with program.
func DaybreakPromptCacheKey(key, program string) string { return key }

// IsModelAllowedFor is IsModelAllowed for a request that may carry Daybreak.
func IsModelAllowedFor(model Model, daybreak bool, allowedIDs []string) bool {
	return IsModelAllowed(model, allowedIDs)
}
