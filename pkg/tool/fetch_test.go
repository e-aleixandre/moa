package tool

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const sampleArticleHTML = `<!DOCTYPE html>
<html>
<head><title>Test Article</title></head>
<body>
<nav><a href="/">Home</a> | <a href="/about">About</a></nav>
<article>
<h1>How to Write Go Tests</h1>
<p>Testing in Go is straightforward. The testing package provides support for automated testing of Go packages.</p>
<p>Use the go test command to run tests. Tests are functions with names beginning with Test that take a *testing.T argument.</p>
<h2>Table-Driven Tests</h2>
<p>Table-driven tests use a slice of test cases. Each test case has input and expected output.</p>
<pre><code>func TestAdd(t *testing.T) {
    tests := []struct{a, b, want int}{
        {1, 2, 3},
        {0, 0, 0},
    }
    for _, tt := range tests {
        got := Add(tt.a, tt.b)
        if got != tt.want {
            t.Errorf("Add(%d, %d) = %d, want %d", tt.a, tt.b, got, tt.want)
        }
    }
}
</code></pre>
</article>
<footer>Copyright 2025</footer>
</body>
</html>`

func TestFetch_ReadabilityExtraction(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(sampleArticleHTML))
	}))
	defer srv.Close()

	tool := NewFetch(ToolConfig{})
	result, err := tool.Execute(context.Background(), map[string]any{
		"url": srv.URL,
	}, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", result.Content[0].Text)
	}

	text := result.Content[0].Text

	// Should contain metadata header
	if !strings.Contains(text, "URL: "+srv.URL) {
		t.Error("result should contain URL header")
	}
	if !strings.Contains(text, "---") {
		t.Error("result should contain separator")
	}

	// Should contain article content
	if !strings.Contains(text, "Testing in Go") {
		t.Error("result should contain article text")
	}
	if !strings.Contains(text, "Table-Driven Tests") {
		t.Error("result should contain section heading")
	}
}

func TestFetch_RawMode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html><body><p>Hello</p></body></html>"))
	}))
	defer srv.Close()

	tool := NewFetch(ToolConfig{})
	result, err := tool.Execute(context.Background(), map[string]any{
		"url": srv.URL,
		"raw": true,
	}, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", result.Content[0].Text)
	}

	text := result.Content[0].Text
	if !strings.Contains(text, "<p>Hello</p>") {
		t.Error("raw mode should return HTML")
	}
}

func TestFetch_InvalidURL(t *testing.T) {
	tool := NewFetch(ToolConfig{})

	tests := []struct {
		name string
		url  string
	}{
		{"empty", ""},
		{"no scheme", "example.com"},
		{"ftp scheme", "ftp://example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tool.Execute(context.Background(), map[string]any{
				"url": tt.url,
			}, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !result.IsError {
				t.Error("expected error result for invalid URL")
			}
		})
	}
}

func TestFetch_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	tool := NewFetch(ToolConfig{})
	result, err := tool.Execute(context.Background(), map[string]any{
		"url": srv.URL,
	}, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for 404")
	}
	if !strings.Contains(result.Content[0].Text, "404") {
		t.Errorf("expected 404 in error, got %q", result.Content[0].Text)
	}
}

func TestFetch_ContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tool := NewFetch(ToolConfig{})
	_, err := tool.Execute(ctx, map[string]any{"url": srv.URL}, nil)

	if err == nil {
		t.Fatal("expected context cancellation error")
	}
}

func TestExtractContent_Fallback(t *testing.T) {
	// Minimal HTML that readability can't extract an article from
	html := `<html><body><p>Short</p></body></html>`
	_, md := extractContent("http://example.com", html)
	if md == "" {
		t.Error("extractContent should return something even for minimal HTML")
	}
}
