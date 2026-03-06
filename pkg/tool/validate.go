package tool

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ealeixandre/moa/pkg/core"
)

// ValidateParams validates tool call arguments against the tool's JSON Schema.
// V0 validates: required fields, type checks, enum values.
func ValidateParams(t core.Tool, args map[string]any) error {
	if len(t.Parameters) == 0 {
		return nil
	}

	var schema schemaNode
	if err := json.Unmarshal(t.Parameters, &schema); err != nil {
		return nil // Can't parse schema — skip validation
	}

	return validateObject(&schema, args, "")
}

type schemaNode struct {
	Type       string                `json:"type"`
	Properties map[string]schemaNode `json:"properties"`
	Required   []string              `json:"required"`
	Enum       []any                 `json:"enum"`
	Items      *schemaNode           `json:"items"`
}

func validateObject(schema *schemaNode, args map[string]any, path string) error {
	// Check required fields
	for _, req := range schema.Required {
		if _, ok := args[req]; !ok {
			return fmt.Errorf("missing required parameter: %s%s", path, req)
		}
	}

	// Check each provided field against its schema
	for name, propSchema := range schema.Properties {
		val, ok := args[name]
		if !ok {
			continue
		}
		fieldPath := path + name
		if err := validateValue(&propSchema, val, fieldPath); err != nil {
			return err
		}
	}

	return nil
}

func validateValue(schema *schemaNode, val any, path string) error {
	// Enum check
	if len(schema.Enum) > 0 {
		found := false
		for _, e := range schema.Enum {
			if fmt.Sprintf("%v", e) == fmt.Sprintf("%v", val) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("parameter %s: value %v not in enum %v", path, val, schema.Enum)
		}
	}

	// Type check
	if schema.Type == "" {
		return nil
	}

	switch schema.Type {
	case "string":
		if _, ok := val.(string); !ok {
			return fmt.Errorf("parameter %s: expected string, got %T", path, val)
		}
	case "number", "integer":
		switch val.(type) {
		case float64, int, int64, float32:
			// ok
		case json.Number:
			// ok
		default:
			return fmt.Errorf("parameter %s: expected number, got %T", path, val)
		}
	case "boolean":
		if _, ok := val.(bool); !ok {
			return fmt.Errorf("parameter %s: expected boolean, got %T", path, val)
		}
	case "array":
		arr, ok := val.([]any)
		if !ok {
			return fmt.Errorf("parameter %s: expected array, got %T", path, val)
		}
		if schema.Items != nil {
			for i, item := range arr {
				if err := validateValue(schema.Items, item, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
	case "object":
		obj, ok := val.(map[string]any)
		if !ok {
			return fmt.Errorf("parameter %s: expected object, got %T", path, val)
		}
		return validateObject(schema, obj, path+".")
	}

	return nil
}

// ValidateToolCall validates a tool call's parameters against the registry.
// Returns an error string for the LLM if validation fails.
func ValidateToolCall(registry *core.Registry, toolName string, args map[string]any) error {
	t, ok := registry.Get(toolName)
	if !ok {
		return fmt.Errorf("unknown tool: %s", toolName)
	}
	return ValidateParams(t, args)
}

// summarizeArgs creates a short string summary of tool arguments for logging.
func SummarizeArgs(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	var parts []string
	for k, v := range args {
		s := fmt.Sprintf("%v", v)
		if len(s) > 80 {
			s = s[:77] + "..."
		}
		parts = append(parts, fmt.Sprintf("%s=%s", k, s))
	}
	result := strings.Join(parts, " ")
	if len(result) > 200 {
		result = result[:197] + "..."
	}
	return result
}
