package core

import "strings"

// SameModelIdentity compares model IDs after resolving known aliases. Unknown
// provider-returned IDs remain comparable without pretending they are aliases.
func SameModelIdentity(requested, effective string) bool {
	requested = strings.TrimSpace(requested)
	effective = strings.TrimSpace(effective)
	if requested == "" || effective == "" {
		return requested == effective
	}
	if model, ok := ResolveModel(requested); ok {
		requested = model.ID
	}
	if model, ok := ResolveModel(effective); ok {
		effective = model.ID
	}
	return strings.EqualFold(requested, effective)
}
