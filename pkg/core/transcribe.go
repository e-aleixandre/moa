package core

import (
	"context"
	"io"
)

// TranscribeOptions tunes a speech-to-text request.
type TranscribeOptions struct {
	// Language is an ISO-639-1 hint (e.g. "es", "en"). Empty lets the provider
	// auto-detect. Setting it avoids mis-detection on short/ambiguous audio.
	Language string
	// Prompt biases the decoder toward specific vocabulary/spelling. Optional.
	Prompt string
}

// Transcriber converts audio to text. Providers that support speech-to-text
// (e.g. OpenAI Whisper) implement this interface.
type Transcriber interface {
	Transcribe(ctx context.Context, audio io.Reader, filename string, opts TranscribeOptions) (string, error)
}
