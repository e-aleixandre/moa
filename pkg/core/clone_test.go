package core

import "testing"

func TestCloneContent_DeepCopiesArguments(t *testing.T) {
	orig := []Content{
		TextContent("hello"),
		ImageContent("AAAA", "image/png"),
		ToolCallContent("tc", "edit", map[string]any{
			"path": "a.go",
			"opts": map[string]any{"deep": "x"},
			"list": []any{"one", map[string]any{"k": "v"}},
		}),
	}
	clone := CloneContent(orig)

	// Mutating the original's nested structures must not affect the clone.
	orig[0].Text = "changed"
	orig[2].Arguments["path"] = "b.go"
	orig[2].Arguments["opts"].(map[string]any)["deep"] = "y"
	orig[2].Arguments["list"].([]any)[1].(map[string]any)["k"] = "w"

	if clone[0].Text != "hello" {
		t.Fatalf("clone Text aliased: %q", clone[0].Text)
	}
	if clone[2].Arguments["path"] != "a.go" {
		t.Fatalf("clone Arguments aliased: %v", clone[2].Arguments["path"])
	}
	if clone[2].Arguments["opts"].(map[string]any)["deep"] != "x" {
		t.Fatalf("clone nested map aliased: %v", clone[2].Arguments["opts"])
	}
	if clone[2].Arguments["list"].([]any)[1].(map[string]any)["k"] != "v" {
		t.Fatalf("clone nested slice aliased: %v", clone[2].Arguments["list"])
	}
}

func TestCloneContent_NilIn(t *testing.T) {
	if got := CloneContent(nil); got != nil {
		t.Fatalf("CloneContent(nil) = %v, want nil", got)
	}
}

func TestContentClone_NilArguments(t *testing.T) {
	c := TextContent("x").Clone()
	if c.Arguments != nil {
		t.Fatalf("Clone of content with nil Arguments = %v, want nil", c.Arguments)
	}
}
