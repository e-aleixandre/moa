package openai

import "strings"

// modelSupportsExplicitCacheBreakpoints reports whether modelID is GPT-5.6 or
// later. Those models only place an implicit cache breakpoint at the latest
// user/tool message, so a mismatch anywhere in the prefix misses the entire
// cache; earlier GPT-5.x models use interval breakpoints and reject the
// explicit-breakpoint field.
func modelSupportsExplicitCacheBreakpoints(modelID string) bool {
	id := strings.ToLower(modelID)
	if _, rest, ok := strings.Cut(id, "/"); ok {
		id = rest
	}
	if !strings.HasPrefix(id, "gpt-") {
		return false
	}
	major, minor, ok := parseLeadingVersion(strings.TrimPrefix(id, "gpt-"))
	if !ok {
		return false
	}
	if major > 5 {
		return true
	}
	return major == 5 && minor >= 6
}

// parseLeadingVersion reads the leading major[.minor] from an OpenAI model
// suffix such as "5.6-terra" or "5.3-codex". A bare major ("5") yields minor 0.
func parseLeadingVersion(s string) (major, minor int, ok bool) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		major = major*10 + int(s[i]-'0')
		i++
		ok = true
	}
	if !ok {
		return 0, 0, false
	}
	if i < len(s) && s[i] == '.' {
		i++
		found := false
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			minor = minor*10 + int(s[i]-'0')
			i++
			found = true
		}
		if !found {
			minor = 0
		}
	}
	return major, minor, true
}
