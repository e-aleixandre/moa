//go:build !windows

package verify

import (
	"context"
	"strings"
	"testing"

	"github.com/ealeixandre/moa/pkg/core"
)

// resultText extracts the text content from a tool result.
func resultText(r core.Result) string {
	var sb strings.Builder
	for _, c := range r.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	return sb.String()
}

func TestTool_AllPass(t *testing.T) {
	dir := t.TempDir()
	writeVerifyJSON(t, dir, Config{Checks: []Check{
		{Name: "a", Command: "echo ok"},
		{Name: "b", Command: "echo ok"},
	}})

	tool := NewTool(dir)
	result, err := tool.Execute(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected non-error result, got: %s", resultText(result))
	}
	text := resultText(result)
	if !strings.Contains(text, "all 2 checks passed") {
		t.Fatalf("expected all-pass message, got: %s", text)
	}
	if !strings.Contains(text, "Running 2 checks:") {
		t.Fatalf("expected preamble, got: %s", text)
	}
}

func TestTool_SomeFail(t *testing.T) {
	dir := t.TempDir()
	writeVerifyJSON(t, dir, Config{Checks: []Check{
		{Name: "pass", Command: "echo ok"},
		{Name: "fail", Command: "exit 1"},
	}})

	tool := NewTool(dir)
	result, err := tool.Execute(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true when a check fails")
	}
	text := resultText(result)
	if !strings.Contains(text, "1/2 checks passed") {
		t.Fatalf("expected partial pass message, got: %s", text)
	}
}

func TestTool_FilterChecks(t *testing.T) {
	dir := t.TempDir()
	writeVerifyJSON(t, dir, Config{Checks: []Check{
		{Name: "build", Command: "echo build"},
		{Name: "test", Command: "echo test"},
		{Name: "lint", Command: "echo lint"},
	}})

	tool := NewTool(dir)
	params := map[string]any{
		"checks": []any{"build", "lint"},
	}
	result, err := tool.Execute(context.Background(), params, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected non-error result, got: %s", resultText(result))
	}
	text := resultText(result)
	if !strings.Contains(text, "Running 2 checks:") {
		t.Fatalf("expected 2 checks in preamble, got: %s", text)
	}
	if !strings.Contains(text, "build:") {
		t.Fatalf("expected build in preamble, got: %s", text)
	}
	if !strings.Contains(text, "lint:") {
		t.Fatalf("expected lint in preamble, got: %s", text)
	}
	// "test" should NOT appear in preamble
	if strings.Contains(text, "test:") {
		t.Fatalf("expected test to be filtered out, got: %s", text)
	}
}

func TestTool_InvalidFilter(t *testing.T) {
	dir := t.TempDir()
	writeVerifyJSON(t, dir, Config{Checks: []Check{
		{Name: "build", Command: "echo ok"},
	}})

	tool := NewTool(dir)
	params := map[string]any{
		"checks": []any{"nonexistent"},
	}
	result, err := tool.Execute(context.Background(), params, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for invalid filter")
	}
	text := resultText(result)
	if !strings.Contains(text, "unknown checks: nonexistent") {
		t.Fatalf("expected unknown check error, got: %s", text)
	}
	if !strings.Contains(text, "available: build") {
		t.Fatalf("expected available list, got: %s", text)
	}
}

func TestTool_NoConfig(t *testing.T) {
	tool := NewTool(t.TempDir())
	result, err := tool.Execute(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result when no config exists")
	}
	text := resultText(result)
	if !strings.Contains(text, "no .moa/verify.json") {
		t.Fatalf("expected 'no config' error, got: %s", text)
	}
}
