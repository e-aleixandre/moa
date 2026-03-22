package jsonutil

import (
	"testing"
)

func TestParseComplete(t *testing.T) {
	p := &PartialParser{}
	result := p.Parse(`{"path":"/foo","content":"hello"}`)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result["path"] != "/foo" {
		t.Errorf("path = %v, want /foo", result["path"])
	}
	if result["content"] != "hello" {
		t.Errorf("content = %v, want hello", result["content"])
	}
}

func TestParseTruncatedStringValue(t *testing.T) {
	p := &PartialParser{}
	result := p.Parse(`{"path":"/foo","content":"hello wor`)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result["path"] != "/foo" {
		t.Errorf("path = %v, want /foo", result["path"])
	}
	if v, ok := result["content"]; !ok {
		t.Error("expected content key to be present")
	} else if v != "hello wor" {
		t.Errorf("content = %v, want 'hello wor'", v)
	}
}

func TestParseTruncatedKey(t *testing.T) {
	p := &PartialParser{}
	result := p.Parse(`{"path":"/foo","con`)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result["path"] != "/foo" {
		t.Errorf("path = %v, want /foo", result["path"])
	}
}

func TestParseTruncatedAfterColon(t *testing.T) {
	p := &PartialParser{}
	result := p.Parse(`{"path":`)
	// May return {} or nil — either is acceptable as long as no panic
	if len(result) > 0 {
		// If it managed to parse something, path should not have garbage
		if result["path"] != nil {
			t.Error("should not have path key with no value")
		}
	}
}

func TestParseEmpty(t *testing.T) {
	p := &PartialParser{}
	result := p.Parse("")
	if result != nil {
		t.Errorf("expected nil for empty input, got %v", result)
	}
}

func TestParseNestedObjects(t *testing.T) {
	p := &PartialParser{}
	result := p.Parse(`{"args":{"key":"val`)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	args, ok := result["args"]
	if !ok {
		t.Fatal("expected args key")
	}
	argsMap, ok := args.(map[string]any)
	if !ok {
		t.Fatalf("expected args to be map, got %T", args)
	}
	if argsMap["key"] != "val" {
		t.Errorf("args.key = %v, want 'val'", argsMap["key"])
	}
}

func TestParseArrayOfObjects(t *testing.T) {
	p := &PartialParser{}
	result := p.Parse(`{"questions":[{"text":"hi`)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	questions, ok := result["questions"]
	if !ok {
		t.Fatal("expected questions key")
	}
	arr, ok := questions.([]any)
	if !ok {
		t.Fatalf("expected questions to be array, got %T", questions)
	}
	if len(arr) == 0 {
		t.Fatal("expected at least one element")
	}
}

func TestParseEscapedStrings(t *testing.T) {
	p := &PartialParser{}
	result := p.Parse(`{"content":"line \"one\ntwo`)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	v, ok := result["content"]
	if !ok {
		t.Fatal("expected content key")
	}
	s, ok := v.(string)
	if !ok {
		t.Fatalf("expected string, got %T", v)
	}
	if len(s) == 0 {
		t.Error("expected non-empty content")
	}
}

func TestParseTruncatedUnicodeEscape(t *testing.T) {
	p := &PartialParser{}
	_ = p.Parse(`{"text":"\u00`)
	// Should not panic; result may be nil or have text key — either is fine.
}

func TestMonotonicNonRegression(t *testing.T) {
	p := &PartialParser{}

	// First parse with 2 keys
	r1 := p.Parse(`{"path":"/foo","content":"hello"}`)
	if len(r1) < 2 {
		t.Fatal("expected 2 keys")
	}

	// Second parse with 1 key — should NOT regress
	r2 := p.Parse(`{"path":"/foo"}`)
	if len(r2) < 2 {
		t.Errorf("regression: got %d keys, want >= 2", len(r2))
	}
}

func TestProgressiveParsing(t *testing.T) {
	p := &PartialParser{}

	// Simulate streaming
	_ = p.Parse(`{"pa`)
	// May be nil — that's ok

	r2 := p.Parse(`{"path":"/foo`)
	if r2 == nil {
		t.Fatal("expected non-nil after path value started")
	}

	r3 := p.Parse(`{"path":"/foo","content":"hel`)
	if r3 == nil {
		t.Fatal("expected non-nil result")
	}
	if r3["path"] != "/foo" {
		t.Errorf("path = %v, want /foo", r3["path"])
	}

	r4 := p.Parse(`{"path":"/foo","content":"hello world"}`)
	if r4 == nil {
		t.Fatal("expected non-nil result")
	}
	if r4["content"] != "hello world" {
		t.Errorf("content = %v, want 'hello world'", r4["content"])
	}
}

func TestReset(t *testing.T) {
	p := &PartialParser{}
	p.Parse(`{"path":"/foo"}`)
	p.Reset()
	result := p.Parse("")
	if result != nil {
		t.Errorf("expected nil after reset, got %v", result)
	}
}

func TestParseJustOpenBrace(t *testing.T) {
	p := &PartialParser{}
	result := p.Parse(`{`)
	// Should return {} or nil — either acceptable
	if len(result) != 0 {
		t.Errorf("expected empty map or nil for just '{', got %v", result)
	}
}

func TestParseCompleteArray(t *testing.T) {
	p := &PartialParser{}
	result := p.Parse(`{"items":[1,2,3],"name":"test"}`)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result["name"] != "test" {
		t.Errorf("name = %v, want test", result["name"])
	}
	items, ok := result["items"].([]any)
	if !ok {
		t.Fatal("expected items to be array")
	}
	if len(items) != 3 {
		t.Errorf("len(items) = %d, want 3", len(items))
	}
}

func TestParseTruncatedNumber(t *testing.T) {
	p := &PartialParser{}
	result := p.Parse(`{"count":42,"name":"te`)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	// Should have at least count
	if v, ok := result["count"]; ok {
		if v != float64(42) {
			t.Errorf("count = %v, want 42", v)
		}
	}
}

func TestMonotonicMerge(t *testing.T) {
	p := &PartialParser{}

	// First parse: key "a"
	r1 := p.Parse(`{"a":"1"}`)
	if r1 == nil || r1["a"] != "1" {
		t.Fatal("expected a=1")
	}

	// Second parse: different key "b" (same count) — should preserve "a"
	r2 := p.Parse(`{"b":"2"}`)
	if r2["a"] != "1" {
		t.Error("key 'a' should be preserved after equal-length different-key parse")
	}
	if r2["b"] != "2" {
		t.Error("key 'b' should be present from new parse")
	}
}

func TestParseTrailingComma(t *testing.T) {
	p := &PartialParser{}
	result := p.Parse(`{"path":"/foo",`)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result["path"] != "/foo" {
		t.Errorf("path = %v, want /foo", result["path"])
	}
}
