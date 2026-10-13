package secretaction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/e-aleixandre/moa/pkg/core"
)

// BrowserFillToolName is the model-facing name of the browser tool.
const BrowserFillToolName = "browser_fill_secret"

const (
	mcpPrefix         = "mcp__"
	browserTypeSuffix = "__browser_type"
	browserEvalSuffix = "__browser_evaluate"
)

// SyncBrowserFillTool registers browser_fill_secret when the registry holds an
// MCP server exposing both browser_type and browser_evaluate (Playwright), and
// removes it when none does. Call it whenever MCP tools change.
func SyncBrowserFillTool(reg *core.Registry, broker *Broker) {
	if reg == nil || broker == nil {
		return
	}
	typeName, evalName := findBrowserTools(reg)
	if typeName == "" {
		reg.Unregister(BrowserFillToolName)
		return
	}
	core.RegisterOrLog(reg, newBrowserFillTool(reg, broker, typeName, evalName))
}

// findBrowserTools returns the first (by name) MCP server prefix offering
// both tools.
func findBrowserTools(reg *core.Registry) (typeName, evalName string) {
	for _, t := range reg.All() {
		if !strings.HasPrefix(t.Name, mcpPrefix) || !strings.HasSuffix(t.Name, browserTypeSuffix) {
			continue
		}
		eval := strings.TrimSuffix(t.Name, browserTypeSuffix) + browserEvalSuffix
		if _, ok := reg.Get(eval); ok {
			return t.Name, eval
		}
	}
	return "", ""
}

type browserField struct {
	target, element string
	field           Field
}

// probeFunction reads what the page really shows for a field. It runs through
// the MCP's own browser_evaluate, the same way the model would call it.
const probeFunction = `(el) => ({origin: location.origin, type: (el.getAttribute('type') || el.type || el.tagName || '').toLowerCase(), name: el.name || '', id: el.id || '', label: ((el.getAttribute('aria-label') || (el.labels && el.labels[0] ? el.labels[0].textContent : '') || el.placeholder || '') + '').trim(), autocomplete: el.getAttribute('autocomplete') || ''})`

const originFunction = `() => location.origin`

type fieldProbe struct {
	Origin       string `json:"origin"`
	Type         string `json:"type"`
	Name         string `json:"name"`
	ID           string `json:"id"`
	Label        string `json:"label"`
	Autocomplete string `json:"autocomplete"`
}

func newBrowserFillTool(reg *core.Registry, broker *Broker, typeName, evalName string) core.Tool {
	return core.Tool{
		Name:  BrowserFillToolName,
		Label: "Fill secret in browser",
		Description: "Fill a password (and optionally a username) into fields of the page open in the browser, without you ever seeing the value. " +
			"The user sees the page's real origin and each field's real label and type, types or autofills the value, and approves; " +
			"the server then types it with browser_type. Use browser_snapshot first to get each field's ref. " +
			"Never ask the user for a password in chat and never type one with browser_type yourself. " +
			"alias is a stable short name for the credential (e.g. \"apple-id\") so the user's phone can offer a saved value.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"fields": {
					"type": "array",
					"minItems": 1,
					"maxItems": 4,
					"description": "Fields to fill, in order.",
					"items": {
						"type": "object",
						"properties": {
							"target": {"type": "string", "description": "Exact target element reference from the page snapshot, or a unique selector"},
							"element": {"type": "string", "description": "Human-readable description of the field"},
							"kind": {"type": "string", "enum": ["password", "username", "text"], "description": "What the value is (default password)"},
							"label": {"type": "string", "description": "Short name of the value shown to the user, e.g. \"Apple ID password\""},
							"alias": {"type": "string", "description": "Stable short name of the credential, e.g. \"apple-id\""}
						},
						"required": ["target", "element", "label"]
					}
				},
				"submit": {"type": "boolean", "description": "Press Enter after filling the last field (default false)"}
			},
			"required": ["fields"]
		}`),
		Effect: core.EffectInteractive,
		Execute: func(ctx context.Context, params map[string]any, _ func(core.Result)) (core.Result, error) {
			typeTool, okType := reg.Get(typeName)
			evalTool, okEval := reg.Get(evalName)
			if !okType || !okEval {
				return core.ErrorResult("the browser is not available"), nil
			}
			fields, err := parseBrowserFields(params)
			if err != nil {
				return core.ErrorResult(err.Error()), nil
			}
			submit, _ := params["submit"].(bool)
			targetKey := targetParam(evalTool)

			// What the card shows comes from the page, not from the model.
			origin := ""
			for i := range fields {
				probe, err := probeField(ctx, evalTool, targetKey, fields[i])
				if err != nil {
					return core.ErrorResult(fmt.Sprintf("could not inspect field %d (%s): %v", i+1, fields[i].element, err)), nil
				}
				if i == 0 {
					origin = probe.Origin
				} else if probe.Origin != origin {
					return core.ErrorResult("the fields belong to different origins; fill them one page at a time"), nil
				}
				f := &fields[i].field
				f.RealType = probe.Type
				f.Autocomplete = probe.Autocomplete
				f.RealLabel = firstNonEmpty(probe.Label, probe.Name, probe.ID)
			}
			cardFields := make([]Field, len(fields))
			for i, f := range fields {
				cardFields[i] = f.field
			}
			req := Request{
				Requester:         Requester(ctx),
				Kind:              KindBrowser,
				Origin:            origin,
				Submit:            submit,
				Fields:            cardFields,
				ActionFingerprint: BrowserFingerprint(origin, cardFields, submit),
			}
			ans, err := broker.Request(ctx, req)
			defer ans.Wipe()
			if err != nil {
				return core.ErrorResult("cancelled"), nil
			}
			if !ans.Allow {
				return core.ErrorResult("The user denied the request; nothing was filled."), nil
			}

			// The page may have navigated while the card waited.
			now, err := evalString(ctx, evalTool, map[string]any{"function": originFunction})
			if err != nil {
				return core.ErrorResult(fmt.Sprintf("could not re-check the page origin; nothing was filled: %v", err)), nil
			}
			if now != origin {
				return core.ErrorResult("page origin changed; nothing was filled"), nil
			}

			variants := EscapedVariants(ans.Values)
			defer func() {
				for _, v := range variants {
					Wipe(v)
				}
			}()
			typeKey := targetParam(typeTool)
			var filled []string
			var last string
			for i, f := range fields {
				args := map[string]any{
					typeKey:   f.target,
					"element": f.element,
					// A Go string cannot be wiped; it lives until the MCP call
					// has serialized it and the collector reclaims it.
					"text":   string(ans.Values[i]),
					"submit": submit && i == len(fields)-1,
				}
				res, err := typeTool.Execute(ctx, args, nil)
				if err != nil {
					res = core.ErrorResult(err.Error())
				}
				last = redactedResultText(res, variants)
				if res.IsError {
					msg := fmt.Sprintf("filling %q failed", f.field.Label)
					if len(filled) > 0 {
						msg += fmt.Sprintf(" after filling %s", strings.Join(filled, ", "))
					}
					return core.ErrorResult(msg + ":\n" + last), nil
				}
				filled = append(filled, f.field.Label)
			}
			summary := "Filled " + strings.Join(filled, ", ") + " on " + origin
			if submit {
				summary += " and pressed Enter"
			}
			return core.TextResult(summary + ".\n\nLast browser response:\n" + last), nil
		},
	}
}

func parseBrowserFields(params map[string]any) ([]browserField, error) {
	raw, _ := params["fields"].([]any)
	if len(raw) == 0 {
		return nil, errors.New("fields is required")
	}
	if len(raw) > MaxFields {
		return nil, fmt.Errorf("too many fields, max %d", MaxFields)
	}
	out := make([]browserField, 0, len(raw))
	for i, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("field %d must be an object", i+1)
		}
		target, _ := m["target"].(string)
		if target == "" {
			target, _ = m["ref"].(string)
		}
		element, _ := m["element"].(string)
		label, _ := m["label"].(string)
		kind, _ := m["kind"].(string)
		alias, _ := m["alias"].(string)
		if kind == "" {
			kind = FieldPassword
		}
		if target == "" || label == "" {
			return nil, fmt.Errorf("field %d needs target and label", i+1)
		}
		if !ValidFieldKind(kind) {
			return nil, fmt.Errorf("field %d: kind must be password, username or text", i+1)
		}
		if element == "" {
			element = label
		}
		if err := validateCardText(label, alias); err != nil {
			return nil, fmt.Errorf("field %d: %w", i+1, err)
		}
		out = append(out, browserField{target: target, element: element, field: Field{Label: label, Kind: kind, Alias: alias, Element: element}})
	}
	return out, nil
}

// validateCardText bounds the model-supplied text a card shows.
func validateCardText(label, alias string) error {
	if len(label) > 200 {
		return errors.New("label is too long (max 200)")
	}
	if len(alias) > 64 {
		return errors.New("alias is too long (max 64)")
	}
	for _, c := range alias {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return errors.New("alias may only contain letters, digits, '-', '_' and '.'")
		}
	}
	return nil
}

// ValidateField checks the model-supplied card text of one field.
func ValidateField(label, kind, alias string) error {
	if label == "" {
		return errors.New("label is required")
	}
	if !ValidFieldKind(kind) {
		return errors.New("kind must be password, username or text")
	}
	return validateCardText(label, alias)
}

// targetParam is the name the MCP gives the element reference: "target" in
// current Playwright MCP, "ref" in older releases.
func targetParam(t core.Tool) string {
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if json.Unmarshal(t.Spec().Parameters, &schema) == nil {
		if _, ok := schema.Properties["target"]; !ok {
			if _, ok := schema.Properties["ref"]; ok {
				return "ref"
			}
		}
	}
	return "target"
}

func probeField(ctx context.Context, eval core.Tool, targetKey string, f browserField) (fieldProbe, error) {
	res, err := eval.Execute(ctx, map[string]any{"function": probeFunction, targetKey: f.target, "element": f.element}, nil)
	if err != nil {
		return fieldProbe{}, err
	}
	text := mcpText(res)
	if res.IsError {
		return fieldProbe{}, errors.New(strings.TrimSpace(text))
	}
	var probe fieldProbe
	if err := decodeEvaluateResult(text, &probe); err != nil {
		return fieldProbe{}, err
	}
	return probe, nil
}

func evalString(ctx context.Context, eval core.Tool, args map[string]any) (string, error) {
	res, err := eval.Execute(ctx, args, nil)
	if err != nil {
		return "", err
	}
	text := mcpText(res)
	if res.IsError {
		return "", errors.New(strings.TrimSpace(text))
	}
	var s string
	if err := decodeEvaluateResult(text, &s); err != nil {
		return "", err
	}
	return s, nil
}

// decodeEvaluateResult reads the JSON Playwright prints under "### Result".
// A result that is itself a JSON-encoded string of the value is accepted too.
func decodeEvaluateResult(text string, v any) error {
	body := text
	if i := strings.Index(body, "### Result"); i >= 0 {
		body = body[i+len("### Result"):]
		if j := strings.Index(body, "\n### "); j >= 0 {
			body = body[:j]
		}
	}
	body = strings.TrimSpace(body)
	if err := json.Unmarshal([]byte(body), v); err == nil {
		return nil
	}
	var inner string
	if err := json.Unmarshal([]byte(body), &inner); err == nil {
		if json.Unmarshal([]byte(inner), v) == nil {
			return nil
		}
	}
	return errors.New("unexpected browser_evaluate result")
}

func mcpText(r core.Result) string {
	var sb strings.Builder
	for _, c := range r.Content {
		if c.Type == "text" || c.Type == "" {
			sb.WriteString(c.Text)
		}
	}
	return sb.String()
}

// redactedResultText keeps only the text of an MCP response (an image could
// show a filled text field) and redacts every form of the values in it.
func redactedResultText(r core.Result, variants [][]byte) string {
	text := mcpText(r)
	for _, c := range r.Content {
		if c.Type != "text" && c.Type != "" {
			text += "\n[non-text content omitted]"
			break
		}
	}
	return RedactString(text, variants)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
