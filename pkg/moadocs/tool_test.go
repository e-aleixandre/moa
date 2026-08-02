package moadocs

import (
	"context"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

// text flattens a tool result the way the model receives it.
func text(res core.Result) string {
	var sb strings.Builder
	for _, c := range res.Content {
		sb.WriteString(c.Text)
	}
	return sb.String()
}

func TestTool_ReturnsThePage(t *testing.T) {
	tool := NewTool()
	res, err := tool.Execute(context.Background(), map[string]any{"page": "configuration"}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %s", text(res))
	}
	if !strings.Contains(text(res), "# Configuration") {
		t.Errorf("expected the configuration page, got %.60q", text(res))
	}
}

// A wrong page name must not end the attempt: telling the model what does
// exist turns a dead end into a retry that answers the user.
func TestTool_WrongPageNameListsTheRealOnes(t *testing.T) {
	tool := NewTool()
	for _, page := range []any{"", "webhooks", nil} {
		res, err := tool.Execute(context.Background(), map[string]any{"page": page}, nil)
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
		if !res.IsError {
			t.Fatalf("page %v should have failed", page)
		}
		if !strings.Contains(text(res), "automation") {
			t.Errorf("error should list the available pages, got %q", text(res))
		}
	}
}

func TestTool_IsReadOnly(t *testing.T) {
	// Reading documentation must never be gated behind an approval prompt.
	if got := NewTool().Effect; got != core.EffectReadOnly {
		t.Errorf("expected a read-only effect, got %q", got)
	}
}
