package events

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
)

// ParsedHook is the title, body and idempotency key extracted from an
// arbitrary webhook payload.
type ParsedHook struct {
	Title string
	Body  string
	Key   string
}

// ParseHookBody extracts a title, a body and a dedupe key from a hook payload.
// JSON objects look at title|subject|summary|event|message for a title and at
// id|event_id|key for a provider id; anything else is pretty-printed (or kept
// as text) and keyed by the SHA-256 of the raw bytes.
func ParseHookBody(source string, raw []byte) ParsedHook {
	raw = []byte(strings.TrimSpace(string(raw)))
	title := strings.TrimSpace(source) + " event"
	body := string(raw)
	key := ""

	if obj, ok := asJSONObject(raw); ok {
		if extracted := firstJSONString(obj, "title", "subject", "summary", "event", "message"); extracted != "" {
			title = extracted
		}
		if pretty, err := json.MarshalIndent(obj, "", "  "); err == nil {
			body = string(pretty)
		}
		key = firstJSONKey(obj, "id", "event_id", "key")
	} else if trimmed := strings.TrimSpace(string(raw)); len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
		var v any
		if err := json.Unmarshal(raw, &v); err == nil {
			if pretty, err := json.MarshalIndent(v, "", "  "); err == nil {
				body = string(pretty)
			}
		}
	}

	if key == "" {
		sum := sha256.Sum256(raw)
		key = hex.EncodeToString(sum[:])
	}
	return ParsedHook{
		Title: clip(title, MaxTitleBytes),
		Body:  clip(body, MaxBodyBytes),
		Key:   clip(key, MaxKeyBytes),
	}
}

func asJSONObject(raw []byte) (map[string]any, bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed[0] != '{' {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		return nil, false
	}
	return obj, true
}

func firstJSONString(obj map[string]any, keys ...string) string {
	for _, k := range keys {
		s, ok := obj[k].(string)
		if !ok {
			continue
		}
		s = strings.TrimSpace(s)
		if s != "" {
			return s
		}
	}
	return ""
}

func firstJSONKey(obj map[string]any, keys ...string) string {
	for _, k := range keys {
		if s := scalarJSON(obj[k]); s != "" {
			return s
		}
	}
	return ""
}

func scalarJSON(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case json.Number:
		return t.String()
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		return ""
	}
}

func clip(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max]
}
