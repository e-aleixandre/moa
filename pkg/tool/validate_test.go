package tool

import (
	"encoding/json"
	"testing"

	"github.com/ealeixandre/moa/pkg/core"
)

func makeTool(schema string) core.Tool {
	return core.Tool{
		Name:       "test",
		Parameters: json.RawMessage(schema),
	}
}

func TestValidateInteger_WholeFloat(t *testing.T) {
	tool := makeTool(`{"type":"object","properties":{"n":{"type":"integer"}}}`)
	err := ValidateParams(tool, map[string]any{"n": float64(2.0)})
	if err != nil {
		t.Fatalf("integer-valued float64 should pass: %v", err)
	}
}

func TestValidateInteger_FractionalFloat(t *testing.T) {
	tool := makeTool(`{"type":"object","properties":{"n":{"type":"integer"}}}`)
	err := ValidateParams(tool, map[string]any{"n": float64(2.6)})
	if err == nil {
		t.Fatal("fractional float64 should fail integer validation")
	}
}

func TestValidateNumber_WholeFloat(t *testing.T) {
	tool := makeTool(`{"type":"object","properties":{"n":{"type":"number"}}}`)
	err := ValidateParams(tool, map[string]any{"n": float64(2.0)})
	if err != nil {
		t.Fatalf("number should accept whole float: %v", err)
	}
}

func TestValidateNumber_FractionalFloat(t *testing.T) {
	tool := makeTool(`{"type":"object","properties":{"n":{"type":"number"}}}`)
	err := ValidateParams(tool, map[string]any{"n": float64(2.6)})
	if err != nil {
		t.Fatalf("number should accept fractional float: %v", err)
	}
}

func TestValidateEnum_NumericMatch(t *testing.T) {
	// JSON schema enum [1, 2, 3] — values come as float64 from JSON
	tool := makeTool(`{"type":"object","properties":{"n":{"type":"number","enum":[1,2,3]}}}`)
	err := ValidateParams(tool, map[string]any{"n": float64(2)})
	if err != nil {
		t.Fatalf("float64(2) should match enum [1,2,3]: %v", err)
	}
}

func TestValidateEnum_StringMatch(t *testing.T) {
	tool := makeTool(`{"type":"object","properties":{"s":{"type":"string","enum":["a","b"]}}}`)
	err := ValidateParams(tool, map[string]any{"s": "a"})
	if err != nil {
		t.Fatalf("string 'a' should match enum: %v", err)
	}
}

func TestValidateEnum_NumericNoMatch(t *testing.T) {
	tool := makeTool(`{"type":"object","properties":{"n":{"type":"number","enum":[1,2]}}}`)
	err := ValidateParams(tool, map[string]any{"n": float64(1.5)})
	if err == nil {
		t.Fatal("float64(1.5) should NOT match enum [1,2]")
	}
}

func TestValidateInteger_JsonNumberWhole(t *testing.T) {
	tool := makeTool(`{"type":"object","properties":{"n":{"type":"integer"}}}`)
	err := ValidateParams(tool, map[string]any{"n": json.Number("2")})
	if err != nil {
		t.Fatalf("json.Number(\"2\") should pass integer validation: %v", err)
	}
}

func TestValidateInteger_JsonNumberFractional(t *testing.T) {
	tool := makeTool(`{"type":"object","properties":{"n":{"type":"integer"}}}`)
	err := ValidateParams(tool, map[string]any{"n": json.Number("2.5")})
	if err == nil {
		t.Fatal("json.Number(\"2.5\") should fail integer validation")
	}
}

func TestValidateEnum_ObjectValue(t *testing.T) {
	// Enum with object values — should not panic
	tool := makeTool(`{"type":"object","properties":{"v":{"enum":[{"a":1},{"b":2}]}}}`)
	err := ValidateParams(tool, map[string]any{"v": map[string]any{"a": float64(1)}})
	if err != nil {
		t.Fatalf("object enum match should pass: %v", err)
	}
}

func TestValidateEnum_ArrayValue(t *testing.T) {
	// Enum with array values — should not panic
	tool := makeTool(`{"type":"object","properties":{"v":{"enum":[[1,2],[3,4]]}}}`)
	err := ValidateParams(tool, map[string]any{"v": []any{float64(1), float64(2)}})
	if err != nil {
		t.Fatalf("array enum match should pass: %v", err)
	}
}

func TestGetInt_FractionalReturnsDefault(t *testing.T) {
	params := map[string]any{"k": float64(3.9)}
	got := getInt(params, "k", 0)
	if got != 0 {
		t.Fatalf("expected default 0 for fractional float, got %d", got)
	}
}

func TestGetInt_WholeFloat(t *testing.T) {
	params := map[string]any{"k": float64(3.0)}
	got := getInt(params, "k", 0)
	if got != 3 {
		t.Fatalf("expected 3, got %d", got)
	}
}
