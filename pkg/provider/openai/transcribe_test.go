package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ealeixandre/moa/pkg/core"
)

// transcribeFields captures the multipart fields the provider sent, so the tests
// can assert on the wire format rather than on internal state.
func transcribeFields(t *testing.T, r *http.Request) map[string]string {
	t.Helper()
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		t.Fatalf("parsing multipart: %v", err)
	}
	fields := map[string]string{}
	for k, v := range r.MultipartForm.Value {
		if len(v) > 0 {
			fields[k] = v[0]
		}
	}
	return fields
}

func TestTranscribe_SendsConfiguredModel(t *testing.T) {
	var got map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != transcribeEndpoint {
			t.Errorf("wrong path: %s", r.URL.Path)
		}
		got = transcribeFields(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"hola"}`))
	}))
	defer server.Close()

	prov := NewWithBaseURL("test-key", server.URL)
	text, err := prov.Transcribe(context.Background(), strings.NewReader("audio"), "clip.webm",
		core.TranscribeOptions{Language: "es", Model: "whisper-1"})
	if err != nil {
		t.Fatal(err)
	}
	if text != "hola" {
		t.Errorf("text = %q, want %q", text, "hola")
	}
	if got["model"] != "whisper-1" {
		t.Errorf("model = %q, want whisper-1", got["model"])
	}
	if got["language"] != "es" {
		t.Errorf("language = %q, want es", got["language"])
	}
	// The newer models reject verbose_json, so this must stay "json".
	if got["response_format"] != "json" {
		t.Errorf("response_format = %q, want json", got["response_format"])
	}
}

// An empty Model must not reach the API as an empty field: callers that do not
// care should still get a working request.
func TestTranscribe_EmptyModelFallsBackToDefault(t *testing.T) {
	var got map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = transcribeFields(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"ok"}`))
	}))
	defer server.Close()

	prov := NewWithBaseURL("test-key", server.URL)
	if _, err := prov.Transcribe(context.Background(), strings.NewReader("audio"), "clip.webm",
		core.TranscribeOptions{}); err != nil {
		t.Fatal(err)
	}
	if got["model"] != core.DefaultSTTModel {
		t.Errorf("model = %q, want %q", got["model"], core.DefaultSTTModel)
	}
	// Optional hints must be omitted entirely rather than sent empty.
	if _, ok := got["language"]; ok {
		t.Errorf("language should be omitted when unset, got %q", got["language"])
	}
	if _, ok := got["prompt"]; ok {
		t.Errorf("prompt should be omitted when unset, got %q", got["prompt"])
	}
}

// The API's own message is the actual finding when a model rejects the request,
// so it must survive into the error.
func TestTranscribe_SurfacesAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"Audio file might be corrupted or unsupported"}}`))
	}))
	defer server.Close()

	prov := NewWithBaseURL("test-key", server.URL)
	_, err := prov.Transcribe(context.Background(), strings.NewReader("x"), "clip.webm", core.TranscribeOptions{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "corrupted or unsupported") {
		t.Errorf("error lost the API message: %v", err)
	}
}
