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

// SameResponseOrigin reports whether state recorded under the effective model
// id belongs to the model now being requested. It is SameModelIdentity plus
// the alias relation the registry declares: an alias is answered under its
// target's id, so its own history comes back spelled as the target.
//
// The relation is deliberately one-way. A request for the alias accepts the
// target's state, because that state was produced by serving this alias. A
// request for the target does NOT accept the alias's state: the two are
// different products (Daybreak carries its own safeguards) and only the alias
// knows it is being redirected. Treating them as one identity would also hide
// a genuine provider fallback.
//
// Verified live on /codex/responses: encrypted reasoning recorded under
// gpt-5.6-sol during a gpt-daybreak-blue-latest session is accepted when
// replayed under the alias.
func SameResponseOrigin(requested, effective string) bool {
	if SameModelIdentity(requested, effective) {
		return true
	}
	model, ok := ResolveModel(strings.TrimSpace(requested))
	if !ok || model.AliasOf == "" {
		return false
	}
	return SameModelIdentity(model.AliasOf, effective)
}
