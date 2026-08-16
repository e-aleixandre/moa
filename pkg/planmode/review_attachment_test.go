package planmode

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/attachment"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/tool"
)

const reviewParentSession = "0123456789abcdef01234567"

// reviewMockProvider drives the reviewer: first a read tool call, then a
// verdict. It records the image block that reached the "provider".
type reviewMockProvider struct {
	calls     int
	imagePath string
	sent      core.Content
}

func (p *reviewMockProvider) Stream(_ context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	p.calls++
	for _, m := range req.Messages {
		for _, c := range m.Content {
			if c.Type == "image" {
				p.sent = c
			}
		}
	}
	ch := make(chan core.AssistantEvent, 10)
	var msg core.Message
	if p.calls == 1 {
		msg = core.Message{
			Role: "assistant",
			Content: []core.Content{
				core.ToolCallContent("tc-1", "read", map[string]any{"path": p.imagePath}),
			},
			StopReason: "tool_use",
			Timestamp:  time.Now().Unix(),
		}
	} else {
		msg = core.Message{
			Role:       "assistant",
			Content:    []core.Content{core.TextContent("VERDICT: APPROVED")},
			StopReason: "end_turn",
			Timestamp:  time.Now().Unix(),
		}
	}
	go func() {
		defer close(ch)
		ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
		ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
	}()
	return ch, nil
}

func writeReviewJPEG(t *testing.T, path string) {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewGray(image.Rect(0, 0, 8, 8)), &jpeg.Options{Quality: 1}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func indexIDs(t *testing.T, baseDir, sessionID string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(baseDir, "sessions", sessionID+".json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var index map[string]json.RawMessage
	if err := json.Unmarshal(data, &index); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(index))
	for id := range index {
		ids = append(ids, id)
	}
	return ids
}

// T2 — the code reviewer is ephemeral: its conversation is discarded, so any
// reference it produced would stay in the parent's index forever with nothing
// left to resolve it. ReviewCode runs on the parent's TOOL context, which
// already carries the parent's capability, and it consumes the parent's own
// `read` tool value. It must still work inline.
func TestReviewCodeInheritsNoCapabilityAndStaysInline(t *testing.T) {
	workspace := t.TempDir()
	writeReviewJPEG(t, filepath.Join(workspace, "shot.jpg"))
	storeDir := t.TempDir()
	store, err := attachment.New(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	parentScope, err := attachment.NewScope(store, reviewParentSession)
	if err != nil {
		t.Fatal(err)
	}

	// The parent's registry, holding the very tool value the parent uses.
	parentTools := core.NewRegistry()
	if err := parentTools.Register(tool.NewRead(tool.ToolConfig{WorkspaceRoot: workspace})); err != nil {
		t.Fatal(err)
	}

	provider := &reviewMockProvider{imagePath: "shot.jpg"}
	// The context a parent tool call carries: capability already installed.
	ctx := attachment.WithScope(context.Background(), parentScope)

	result, err := ReviewCode(ctx, ReviewConfig{
		ProviderFactory: func(core.Model) (core.Provider, error) { return provider, nil },
		Model:           core.Model{ID: "test-model", Provider: "mock"},
		ThinkingLevel:   "medium",
		ParentTools:     parentTools,
	}, "some change", []string{"shot.jpg"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Approved {
		t.Fatalf("unexpected verdict: %+v", result)
	}

	if provider.sent.Type != "image" {
		t.Fatal("the reviewer never sent an image to the provider")
	}
	if provider.sent.AttachmentID != "" {
		t.Errorf("ephemeral reviewer externalized (AttachmentID=%q): it inherited the parent's capability", provider.sent.AttachmentID)
	}
	if provider.sent.Data == "" {
		t.Error("ephemeral reviewer sent an empty image")
	}
	if ids := indexIDs(t, storeDir, reviewParentSession); len(ids) != 0 {
		t.Errorf("ephemeral reviewer left %d orphan reference(s) in the parent's index: %v", len(ids), ids)
	}
}
