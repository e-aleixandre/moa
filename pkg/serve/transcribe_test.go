package serve

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ealeixandre/moa/pkg/core"
)

// stubTranscriber records the audio it receives and returns a canned result.
type stubTranscriber struct {
	got    []byte
	result string
	err    error
}

func (s *stubTranscriber) Transcribe(_ context.Context, audio io.Reader, _ string, _ core.TranscribeOptions) (string, error) {
	s.got, _ = io.ReadAll(audio)
	return s.result, s.err
}

func multipartAudio(t *testing.T, field, filename string, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile(field, filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatalf("write audio: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return &buf, mw.FormDataContentType()
}

func TestHandleTranscribe_Success(t *testing.T) {
	stub := &stubTranscriber{result: "hola mundo"}
	mgr := &Manager{transcriber: stub}

	body, ct := multipartAudio(t, "audio", "recording.webm", []byte("fake-audio-bytes"))
	req := httptest.NewRequest("POST", "/api/transcribe", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()

	handleTranscribe(mgr)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "hola mundo") {
		t.Errorf("body = %q, want transcribed text", rec.Body.String())
	}
	if string(stub.got) != "fake-audio-bytes" {
		t.Errorf("transcriber got %q, want the uploaded audio", stub.got)
	}
}

func TestHandleTranscribe_NoTranscriber(t *testing.T) {
	mgr := &Manager{transcriber: nil}
	req := httptest.NewRequest("POST", "/api/transcribe", nil)
	rec := httptest.NewRecorder()

	handleTranscribe(mgr)(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestHandleTranscribe_MissingAudioField(t *testing.T) {
	stub := &stubTranscriber{result: "x"}
	mgr := &Manager{transcriber: stub}

	body, ct := multipartAudio(t, "notaudio", "x.webm", []byte("data"))
	req := httptest.NewRequest("POST", "/api/transcribe", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()

	handleTranscribe(mgr)(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}
