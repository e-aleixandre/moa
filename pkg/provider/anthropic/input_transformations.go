package anthropic

import "encoding/json"

func responseFields(data, envelope string) map[string]json.RawMessage {
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(data), &fields) != nil {
		return nil
	}
	if envelope != "" {
		var nested map[string]json.RawMessage
		if json.Unmarshal(fields[envelope], &nested) != nil {
			return nil
		}
		return nested
	}
	return fields
}

func usageFieldPresent(data, envelope, name string) bool {
	fields := responseFields(data, envelope)
	var usage map[string]json.RawMessage
	if json.Unmarshal(fields["usage"], &usage) != nil {
		return false
	}
	var value *int
	return json.Unmarshal(usage[name], &value) == nil && value != nil && *value >= 0
}

// Keep only known enums, not paths, signatures or arbitrary provider text.
// Absence is not a claim that reasoning was preserved across accounts.
func readInputTransformations(data, envelope string) []string {
	fields := responseFields(data, envelope)
	raw, exists := fields["input_transformations"]
	if !exists {
		return nil
	}
	var entries []struct {
		Type   string `json:"type"`
		Reason string `json:"reason"`
	}
	if json.Unmarshal(raw, &entries) != nil {
		return []string{"unknown"}
	}
	out := []string{}
	for _, entry := range entries {
		if entry.Type != "thinking_dropped" && entry.Type != "thinking_mismatch_allowed" {
			continue
		}
		switch entry.Reason {
		case "organization_binding_mismatch", "prefix_binding_mismatch", "model_binding_mismatch":
			out = append(out, entry.Type+":"+entry.Reason)
		}
	}
	return out
}
