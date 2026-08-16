package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	"github.com/e-aleixandre/moa/pkg/attachment"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/tool"
)

// writeTestJPEG writes a small valid JPEG so the read tool takes its image path
// (magic-byte mime detection and dimensions included).
func writeTestJPEG(t *testing.T, path string) {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewGray(image.Rect(0, 0, 8, 8)), &jpeg.Options{Quality: 1}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

// sessionIndexIDs returns the attachment IDs registered under sessionID in the
// store rooted at baseDir. Reading the index file directly is deliberate: the
// test must observe what was actually persisted, not what an API reports.
func sessionIndexIDs(t *testing.T, baseDir, sessionID string) []string {
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

func newScopeInDir(t *testing.T, baseDir, sessionID string) *attachment.Scope {
	t.Helper()
	store, err := attachment.New(baseDir)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := attachment.NewScope(store, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	return scope
}

// registryWith returns a registry holding exactly the given tool VALUES. Used
// to share one read tool between two agents, as production does.
func registryWith(t *testing.T, tools ...core.Tool) *core.Registry {
	t.Helper()
	reg := core.NewRegistry()
	for _, tl := range tools {
		if err := reg.Register(tl); err != nil {
			t.Fatal(err)
		}
	}
	return reg
}

// imageBlockFromToolResult digs the image block out of the tool_result message
// the agent appended after running `read`.
func imageBlockFromToolResult(t *testing.T, msgs []core.AgentMessage) core.Content {
	t.Helper()
	for _, m := range msgs {
		for _, c := range m.Content {
			if c.Type == "image" {
				return c
			}
		}
	}
	t.Fatal("no image block found in the conversation")
	return core.Content{}
}

// T1 — the test that would have caught the v3 hole. The parent builds ONE read
// tool while holding the capability; an ephemeral agent WITHOUT capability then
// runs that very same core.Tool value. It must work inline: capturing the scope
// in NewRead's closure (or anywhere else at construction time) makes this fail.
func TestSharedReadToolStaysInlineForAnAgentWithoutCapability(t *testing.T) {
	workspace := t.TempDir()
	writeTestJPEG(t, filepath.Join(workspace, "shot.jpg"))
	storeDir := t.TempDir()
	// The parent owns the capability; the ephemeral agent below must not use it.
	newScopeInDir(t, storeDir, testParentSession)

	// Built by the parent, which DOES hold the capability.
	sharedRead := tool.NewRead(tool.ToolConfig{WorkspaceRoot: workspace})

	var sentImage core.Content
	provider := NewMockProvider(
		toolCallResponse("tc-1", "read", map[string]any{"path": "shot.jpg"}),
		func(req core.Request) (<-chan core.AssistantEvent, error) {
			for _, m := range req.Messages {
				for _, c := range m.Content {
					if c.Type == "image" {
						sentImage = c
					}
				}
			}
			return simpleTextResponse("done")(req)
		},
	)

	// The ephemeral agent registers the SAME tool value and has NO capability.
	ephemeral, err := New(AgentConfig{
		Provider:        provider,
		Model:           core.Model{ID: "test-model", Provider: "mock"},
		Tools:           registryWith(t, sharedRead),
		AttachmentScope: nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := ephemeral.Run(context.Background(), "read the screenshot")
	if err != nil {
		t.Fatal(err)
	}

	block := imageBlockFromToolResult(t, msgs)
	if block.AttachmentID != "" {
		t.Errorf("ephemeral agent externalized (AttachmentID=%q): the capability was captured by the shared tool instead of read from the invocation", block.AttachmentID)
	}
	if block.Data == "" {
		t.Error("ephemeral agent produced no inline image data")
	}
	if ids := sessionIndexIDs(t, storeDir, testParentSession); len(ids) != 0 {
		t.Errorf("ephemeral agent left %d reference(s) in the parent's index: %v", len(ids), ids)
	}
	if sentImage.Data == "" {
		t.Error("provider received no image bytes")
	}
}

// The positive half of T1: the same shared tool DOES externalize when the
// agent running it holds the capability. Without this, T1 could pass simply
// because externalization never happens.
func TestSharedReadToolExternalizesForAnAgentWithCapability(t *testing.T) {
	workspace := t.TempDir()
	writeTestJPEG(t, filepath.Join(workspace, "shot.jpg"))
	storeDir := t.TempDir()
	scope := newScopeInDir(t, storeDir, testOwnerSession)

	sharedRead := tool.NewRead(tool.ToolConfig{WorkspaceRoot: workspace})

	var sentImage core.Content
	provider := NewMockProvider(
		toolCallResponse("tc-1", "read", map[string]any{"path": "shot.jpg"}),
		func(req core.Request) (<-chan core.AssistantEvent, error) {
			for _, m := range req.Messages {
				for _, c := range m.Content {
					if c.Type == "image" {
						sentImage = c
					}
				}
			}
			return simpleTextResponse("done")(req)
		},
	)

	ag, err := New(AgentConfig{
		Provider:        provider,
		Model:           core.Model{ID: "test-model", Provider: "mock"},
		Tools:           registryWith(t, sharedRead),
		AttachmentScope: scope,
	})
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := ag.Run(context.Background(), "read the screenshot")
	if err != nil {
		t.Fatal(err)
	}

	// T3, main-agent half: the state keeps a descriptor with NO base64...
	block := imageBlockFromToolResult(t, msgs)
	if block.AttachmentID == "" {
		t.Fatal("agent with capability did not externalize the image")
	}
	if block.Data != "" {
		t.Error("externalized image still carries inline base64 in the agent state")
	}
	if block.MimeType != "image/jpeg" {
		t.Errorf("mime = %q, want image/jpeg (magic bytes)", block.MimeType)
	}
	if block.AttachmentSize == 0 {
		t.Error("descriptor carries no size, budgeting would read it as empty")
	}
	// ...the reference is registered under the owning session...
	ids := sessionIndexIDs(t, storeDir, testOwnerSession)
	if len(ids) != 1 || ids[0] != block.AttachmentID {
		t.Fatalf("session index = %v, want exactly [%s]", ids, block.AttachmentID)
	}
	// ...and the provider still receives real bytes.
	if sentImage.Data == "" {
		t.Fatal("provider received an empty image")
	}
	raw, err := base64.StdEncoding.DecodeString(sentImage.Data)
	if err != nil {
		t.Fatalf("provider image is not valid base64: %v", err)
	}
	original, err := os.ReadFile(filepath.Join(workspace, "shot.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, original) {
		t.Error("provider received bytes that differ from the file on disk")
	}
}

// A storage failure must degrade to inline, never to an error and never to a
// half-written descriptor. The store's base dir is made read-only so Put fails
// while the file itself is perfectly readable.
func TestReadFallsBackToInlineWhenStoringFails(t *testing.T) {
	workspace := t.TempDir()
	writeTestJPEG(t, filepath.Join(workspace, "shot.jpg"))
	storeDir := t.TempDir()
	scope := newScopeInDir(t, storeDir, testOwnerSession)

	stagingDir := filepath.Join(storeDir, "staging")
	if err := os.Chmod(stagingDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stagingDir, 0o700) })

	var sentImage core.Content
	provider := NewMockProvider(
		toolCallResponse("tc-1", "read", map[string]any{"path": "shot.jpg"}),
		func(req core.Request) (<-chan core.AssistantEvent, error) {
			for _, m := range req.Messages {
				for _, c := range m.Content {
					if c.Type == "image" {
						sentImage = c
					}
				}
			}
			return simpleTextResponse("done")(req)
		},
	)
	ag, err := New(AgentConfig{
		Provider:        provider,
		Model:           core.Model{ID: "test-model", Provider: "mock"},
		Tools:           registryWith(t, tool.NewRead(tool.ToolConfig{WorkspaceRoot: workspace})),
		AttachmentScope: scope,
	})
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := ag.Run(context.Background(), "read the screenshot")
	if err != nil {
		t.Fatalf("a storage failure turned a valid image into a run error: %v", err)
	}

	block := imageBlockFromToolResult(t, msgs)
	if block.AttachmentID != "" {
		t.Errorf("a failed Put still produced a descriptor (%q): a reference with no blob", block.AttachmentID)
	}
	if block.Data == "" {
		t.Error("failed Put produced neither a descriptor nor inline bytes: the image was lost")
	}
	if sentImage.Data == "" {
		t.Error("provider received no image after the storage failure")
	}
	if ids := sessionIndexIDs(t, storeDir, testOwnerSession); len(ids) != 0 {
		t.Errorf("failed Put left references behind: %v", ids)
	}
	entries, err := os.ReadDir(stagingDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("failed Put left %d staging file(s) behind", len(entries))
	}
}
