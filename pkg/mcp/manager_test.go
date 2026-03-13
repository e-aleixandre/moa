package mcp

import (
	"context"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// --- sanitizeToolName ---

func TestSanitizeToolName_Valid(t *testing.T) {
	cases := []struct{ in, want string }{
		{"mcp__db__query", "mcp__db__query"},
		{"hello-world_123", "hello-world_123"},
		{"a", "a"},
	}
	for _, tc := range cases {
		if got := sanitizeToolName(tc.in); got != tc.want {
			t.Errorf("sanitizeToolName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSanitizeToolName_Invalid(t *testing.T) {
	cases := []struct{ in, want string }{
		{"hello.world", "hello_world"},
		{"path/to/tool", "path_to_tool"},
		{"has spaces", "has_spaces"},
		{"special@#$chars", "special___chars"},
	}
	for _, tc := range cases {
		if got := sanitizeToolName(tc.in); got != tc.want {
			t.Errorf("sanitizeToolName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSanitizeToolName_TooLong(t *testing.T) {
	long := strings.Repeat("a", 100)
	got := sanitizeToolName(long)
	if len(got) != 64 {
		t.Errorf("len = %d, want 64", len(got))
	}
}

func TestSanitizeToolName_Empty(t *testing.T) {
	if got := sanitizeToolName(""); got != "unnamed" {
		t.Errorf("sanitizeToolName(\"\") = %q, want \"unnamed\"", got)
	}
	// All invalid chars → all replaced → then empty after? No, they become '_'.
	if got := sanitizeToolName("..."); got != "___" {
		t.Errorf("sanitizeToolName(\"...\") = %q, want \"___\"", got)
	}
}

// --- convertMCPResult ---

func TestConvertMCPResult_Nil(t *testing.T) {
	r := convertMCPResult(nil)
	if len(r.Content) != 1 || r.Content[0].Text != "(no result)" {
		t.Fatalf("unexpected result for nil: %+v", r)
	}
}

func TestConvertMCPResult_Empty(t *testing.T) {
	r := convertMCPResult(&sdkmcp.CallToolResult{})
	if len(r.Content) != 1 || r.Content[0].Text != "(empty result)" {
		t.Fatalf("unexpected result for empty: %+v", r)
	}
}

func TestConvertMCPResult_Text(t *testing.T) {
	r := convertMCPResult(&sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "hello"}},
	})
	if len(r.Content) != 1 || r.Content[0].Type != "text" || r.Content[0].Text != "hello" {
		t.Fatalf("unexpected: %+v", r)
	}
	if r.IsError {
		t.Fatal("unexpected IsError=true")
	}
}

func TestConvertMCPResult_Image(t *testing.T) {
	r := convertMCPResult(&sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.ImageContent{Data: []byte("png-data"), MIMEType: "image/png"}},
	})
	if len(r.Content) != 1 || r.Content[0].Type != "image" {
		t.Fatalf("unexpected: %+v", r)
	}
	if r.Content[0].MimeType != "image/png" {
		t.Fatalf("MimeType = %q", r.Content[0].MimeType)
	}
}

func TestConvertMCPResult_Error(t *testing.T) {
	r := convertMCPResult(&sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "something went wrong"}},
		IsError: true,
	})
	if !r.IsError {
		t.Fatal("expected IsError=true")
	}
	if r.Content[0].Text != "something went wrong" {
		t.Fatalf("text = %q", r.Content[0].Text)
	}
}

func TestConvertMCPResult_UnknownContentType(t *testing.T) {
	r := convertMCPResult(&sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.AudioContent{Data: []byte("audio"), MIMEType: "audio/wav"}},
	})
	if len(r.Content) != 1 || r.Content[0].Type != "text" {
		t.Fatalf("expected JSON fallback text, got: %+v", r)
	}
}

// --- In-memory integration test ---

func TestWrapMCPTool_InMemory(t *testing.T) {
	ctx := context.Background()

	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "test", Version: "0.1"}, nil)
	type greetInput struct {
		Name string
	}
	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:        "greet",
		Description: "Greets a person",
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, input greetInput) (*sdkmcp.CallToolResult, any, error) {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "Hello, " + input.Name + "!"}},
		}, nil, nil
	})

	st, ct := sdkmcp.NewInMemoryTransports()

	serverSession, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer func() { _ = serverSession.Close() }()

	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "moa-test", Version: "0.1"}, nil)
	session, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	// Discover tools
	list, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(list.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(list.Tools))
	}

	// Wrap as core.Tool
	tool, err := wrapMCPTool("test-server", list.Tools[0], session)
	if err != nil {
		t.Fatalf("wrapMCPTool: %v", err)
	}

	// Verify metadata
	if tool.Name != "mcp__test-server__greet" {
		t.Fatalf("Name = %q", tool.Name)
	}
	if tool.Label != "test-server/greet" {
		t.Fatalf("Label = %q", tool.Label)
	}
	if tool.Description != "Greets a person" {
		t.Fatalf("Description = %q", tool.Description)
	}

	// Call tool
	result, err := tool.Execute(ctx, map[string]any{"Name": "World"}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(result.Content) != 1 || result.Content[0].Text != "Hello, World!" {
		t.Fatalf("result = %+v", result)
	}
	if result.IsError {
		t.Fatal("unexpected IsError")
	}
}

func TestWrapMCPTool_ErrorResult(t *testing.T) {
	ctx := context.Background()

	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "test", Version: "0.1"}, nil)
	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name: "fail",
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, input any) (*sdkmcp.CallToolResult, any, error) {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "bad input"}},
			IsError: true,
		}, nil, nil
	})

	st, ct := sdkmcp.NewInMemoryTransports()
	serverSession, _ := server.Connect(ctx, st, nil)
	defer func() { _ = serverSession.Close() }()

	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "moa-test", Version: "0.1"}, nil)
	session, _ := client.Connect(ctx, ct, nil)
	defer func() { _ = session.Close() }()

	list, _ := session.ListTools(ctx, nil)
	tool, _ := wrapMCPTool("test-server", list.Tools[0], session)

	result, err := tool.Execute(ctx, map[string]any{}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true")
	}
}
