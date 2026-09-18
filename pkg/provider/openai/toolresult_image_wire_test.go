package openai_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/attachment"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/provider/openai"
	"github.com/e-aleixandre/moa/pkg/tool"
)

// The whole chain the user actually exercises: `read` externalizes a PNG into
// the attachment store, the materializer puts the bytes back at request time,
// and the OpenAI transport writes them to the wire. The regression this covers
// is the image disappearing between the tool result and the HTTP body, with no
// error raised anywhere.
func TestReadImageReachesTheWireInsideToolOutput(t *testing.T) {
	dir := t.TempDir()
	imgPath := filepath.Join(dir, "captura.png")
	raw, err := base64.StdEncoding.DecodeString(
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(imgPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := attachment.New(filepath.Join(dir, "attachments"))
	if err != nil {
		t.Fatal(err)
	}
	const sessionID = "0123456789abcdef01234567"
	scope, err := attachment.NewScope(store, sessionID)
	if err != nil {
		t.Fatal(err)
	}

	readTool := tool.NewRead(tool.ToolConfig{WorkspaceRoot: dir, DisableSandbox: true})
	res, err := readTool.Execute(attachment.WithScope(context.Background(), scope), map[string]any{"path": imgPath}, nil)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if res.Content[0].AttachmentID == "" {
		t.Fatal("read did not externalize the image, so this is not the real path")
	}

	toolResult := core.NewToolResultMessage("call-1", "read", res.Content, false)
	msgs, err := store.MaterializeMessages(sessionID, []core.Message{
		core.NewUserMessage("look at the file"),
		{Role: "assistant", Provider: "openai", Model: "gpt-5.6-terra", Content: []core.Content{
			core.ToolCallContent("call-1", "read", map[string]any{"path": imgPath}),
		}},
		toolResult,
	})
	if err != nil {
		t.Fatalf("materialize failed: %v", err)
	}

	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			t.Errorf("read body: %v", readErr)
			return
		}
		if err := json.Unmarshal(payload, &body); err != nil {
			t.Errorf("decode body: %v", err)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\"}}\n\n"))
	}))
	defer server.Close()

	prov := openai.NewWithBaseURL("test-key", server.URL)
	ch, err := prov.Stream(context.Background(), core.Request{
		Model:    core.Model{ID: "gpt-5.6-terra"},
		Messages: msgs,
	})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}

	input, _ := body["input"].([]any)
	var output []any
	for _, item := range input {
		entry, _ := item.(map[string]any)
		if entry["type"] == "function_call_output" && entry["call_id"] == "call-1" {
			output, _ = entry["output"].([]any)
		}
	}
	if len(output) != 1 {
		t.Fatalf("tool output parts = %d, want the image: %v", len(output), output)
	}
	part, _ := output[0].(map[string]any)
	if part["type"] != "input_image" {
		t.Fatalf("tool output part type = %v, want input_image", part["type"])
	}
	url, _ := part["image_url"].(string)
	if !strings.HasPrefix(url, "data:image/png;base64,") {
		t.Fatalf("image_url = %.40q", url)
	}
	if !strings.Contains(url, base64.StdEncoding.EncodeToString(raw)) {
		t.Fatal("the bytes on the wire are not the ones read from disk")
	}
}
