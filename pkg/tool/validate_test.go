package tool

import (
	"encoding/json"
	"testing"

	"github.com/ealeixandre/go-agent/pkg/core"
)

func TestValidateParams_Required(t *testing.T) {
	tool := core.Tool{
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"command": {"type": "string"},
				"path": {"type": "string"}
			},
			"required": ["command"]
		}`),
	}

	// Missing required
	err := ValidateParams(tool, map[string]any{"path": "/tmp"})
	if err == nil {
		t.Fatal("expected error for missing required field")
	}

	// Has required
	err = ValidateParams(tool, map[string]any{"command": "ls"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateParams_TypeCheck(t *testing.T) {
	tool := core.Tool{
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"name": {"type": "string"},
				"count": {"type": "integer"},
				"enabled": {"type": "boolean"}
			}
		}`),
	}

	// Correct types
	err := ValidateParams(tool, map[string]any{"name": "test", "count": float64(5), "enabled": true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Wrong type for name
	err = ValidateParams(tool, map[string]any{"name": 42})
	if err == nil {
		t.Fatal("expected error for wrong type")
	}

	// Wrong type for count
	err = ValidateParams(tool, map[string]any{"count": "five"})
	if err == nil {
		t.Fatal("expected error for wrong type")
	}
}

func TestValidateParams_Enum(t *testing.T) {
	tool := core.Tool{
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"level": {"type": "string", "enum": ["low", "medium", "high"]}
			}
		}`),
	}

	err := ValidateParams(tool, map[string]any{"level": "medium"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err = ValidateParams(tool, map[string]any{"level": "extreme"})
	if err == nil {
		t.Fatal("expected error for invalid enum value")
	}
}

func TestValidateParams_EmptySchema(t *testing.T) {
	tool := core.Tool{Parameters: nil}
	err := ValidateParams(tool, map[string]any{"anything": "goes"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSummarizeArgs(t *testing.T) {
	args := map[string]any{"command": "ls -la"}
	s := SummarizeArgs(args)
	if s == "" {
		t.Fatal("expected non-empty summary")
	}

	// Empty
	s = SummarizeArgs(nil)
	if s != "" {
		t.Fatalf("expected empty, got %q", s)
	}
}
