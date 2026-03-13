package tool

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebSearch_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Subscription-Token"); got != "test-key" {
			t.Errorf("expected API key 'test-key', got %q", got)
		}
		if got := r.URL.Query().Get("q"); got != "golang testing" {
			t.Errorf("expected query 'golang testing', got %q", got)
		}
		if got := r.URL.Query().Get("count"); got != "3" {
			t.Errorf("expected count '3', got %q", got)
		}

		resp := braveResponse{}
		resp.Web.Results = []braveResult{
			{Title: "Go Testing", URL: "https://go.dev/doc/testing", Description: "How to test in Go", Age: "1 day ago"},
			{Title: "Testing Package", URL: "https://pkg.go.dev/testing", Description: "Package testing reference"},
			{Title: "Table-Driven Tests", URL: "https://go.dev/wiki/TableDrivenTests", Description: "A pattern for tests", Age: "3 months ago"},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Fatalf("failed to encode response: %v", err)
		}
	}))
	defer srv.Close()

	tool := newWebSearch(ToolConfig{BraveAPIKey: "test-key"}, srv.URL)
	result, err := tool.Execute(context.Background(), map[string]any{
		"query": "golang testing",
		"count": float64(3),
	}, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", result.Content[0].Text)
	}

	text := result.Content[0].Text
	if !strings.Contains(text, "Go Testing") {
		t.Error("result should contain 'Go Testing'")
	}
	if !strings.Contains(text, "https://go.dev/doc/testing") {
		t.Error("result should contain URL")
	}
	if !strings.Contains(text, "1 day ago") {
		t.Error("result should contain age")
	}
	if !strings.Contains(text, "Testing Package") {
		t.Error("result should contain second result title")
	}
	// Third result should not have Age line since it's empty... wait, it has "3 months ago"
	if !strings.Contains(text, "3 months ago") {
		t.Error("result should contain third result age")
	}
}

func TestWebSearch_EmptyResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := braveResponse{}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Fatalf("failed to encode response: %v", err)
		}
	}))
	defer srv.Close()

	tool := newWebSearch(ToolConfig{BraveAPIKey: "test-key"}, srv.URL)
	result, err := tool.Execute(context.Background(), map[string]any{
		"query": "xyzzynonexistent",
	}, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatal("should not be error result for no results")
	}
	if !strings.Contains(result.Content[0].Text, "No results found") {
		t.Errorf("expected 'No results found', got %q", result.Content[0].Text)
	}
}

func TestWebSearch_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		if _, err := w.Write([]byte("rate limited")); err != nil {
			t.Fatalf("failed to write response: %v", err)
		}
	}))
	defer srv.Close()

	tool := newWebSearch(ToolConfig{BraveAPIKey: "test-key"}, srv.URL)
	result, err := tool.Execute(context.Background(), map[string]any{
		"query": "test",
	}, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for HTTP 429")
	}
	if !strings.Contains(result.Content[0].Text, "429") {
		t.Errorf("expected status code in error, got %q", result.Content[0].Text)
	}
}

func TestWebSearch_MissingQuery(t *testing.T) {
	tool := newWebSearch(ToolConfig{BraveAPIKey: "test-key"}, "http://unused")
	result, err := tool.Execute(context.Background(), map[string]any{}, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for missing query")
	}
}

func TestWebSearch_CountClamp(t *testing.T) {
	var gotCount string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCount = r.URL.Query().Get("count")
		resp := braveResponse{}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Fatalf("failed to encode response: %v", err)
		}
	}))
	defer srv.Close()

	tool := newWebSearch(ToolConfig{BraveAPIKey: "key"}, srv.URL)

	// count > 20 should be clamped to 20
	_, err := tool.Execute(context.Background(), map[string]any{
		"query": "test",
		"count": float64(50),
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotCount != "20" {
		t.Errorf("expected count clamped to 20, got %q", gotCount)
	}
}

func TestWebSearch_ContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block forever — context should cancel
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	tool := newWebSearch(ToolConfig{BraveAPIKey: "key"}, srv.URL)
	_, err := tool.Execute(ctx, map[string]any{"query": "test"}, nil)

	if err == nil {
		t.Fatal("expected context cancellation error")
	}
}
