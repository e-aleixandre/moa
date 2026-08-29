package core

import "strings"

// CanonicalModelID maps a model id to the registry's spelling for it, so a
// provider naming the same model differently per backend does not fragment
// cost attribution or identity checks. Unknown ids pass through unchanged:
// a custom model must reach its provider verbatim.
func CanonicalModelID(id string) string {
	if model, ok := ResolveModel(id); ok {
		return model.ID
	}
	return id
}

// SameModelIdentity compares model IDs after resolving known aliases. Unknown
// provider-returned IDs remain comparable without pretending they are aliases.
func SameModelIdentity(requested, effective string) bool {
	requested = strings.TrimSpace(requested)
	effective = strings.TrimSpace(effective)
	if requested == "" || effective == "" {
		return requested == effective
	}
	return strings.EqualFold(CanonicalModelID(requested), CanonicalModelID(effective))
}
