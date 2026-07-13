package serve

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

// stubTranscriber records the audio it receives and returns a canned result.
// When block is non-nil, Transcribe blocks on it (to hold a concurrency slot).
type stubTranscriber struct {
	got    []byte
	result string
	err    error
	block  chan struct{}
	reads  atomic.Int32
}

func (s *stubTranscriber) Transcribe(_ context.Context, audio io.Reader, _ string, _ core.TranscribeOptions) (string, error) {
	b, _ := io.ReadAll(audio)
	s.reads.Add(1)
	if s.block != nil {
		<-s.block
	} else {
		s.got = b
	}
	return s.result, s.err
}

// audioReads is the number of times Transcribe has begun (safe under -race).
func (s *stubTranscriber) audioReads() int { return int(s.reads.Load()) }

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

// TestHandleTranscribe_ConcurrencyLimit verifies that once the in-flight
// transcription slots are full, further uploads are shed with 503 rather than
// piling up (each holding the extended read deadline).
func TestHandleTranscribe_ConcurrencyLimit(t *testing.T) {
	const limit = 4
	block := make(chan struct{})
	stub := &stubTranscriber{result: "ok", block: block}
	mgr := &Manager{transcriber: stub}
	handler := handleTranscribe(mgr)

	var started sync.WaitGroup
	started.Add(limit)
	var inflight atomic.Int32

	// Saturate the limit with requests that block inside Transcribe.
	for i := 0; i < limit; i++ {
		go func() {
			body, ct := multipartAudio(t, "audio", "r.webm", []byte("audio"))
			req := httptest.NewRequest("POST", "/api/transcribe", body)
			req.Header.Set("Content-Type", ct)
			rec := httptest.NewRecorder()
			inflight.Add(1)
			started.Done()
			handler(rec, req)
		}()
	}
	started.Wait()
	// Wait until all four are actually inside Transcribe (holding a slot).
	deadline := time.Now().Add(2 * time.Second)
	for stub.audioReads() < limit && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	// The next request should be rejected immediately with 503.
	body, ct := multipartAudio(t, "audio", "r.webm", []byte("audio"))
	req := httptest.NewRequest("POST", "/api/transcribe", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("over-limit status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}

	close(block) // release the blocked handlers
}
