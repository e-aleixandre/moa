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
	// Model is the provider's model id. Empty lets the provider pick its own
	// default, so callers that do not care keep working.
	Model string
}

// Transcriber converts audio to text. Providers that support speech-to-text
// (e.g. OpenAI) implement this interface.
type Transcriber interface {
	Transcribe(ctx context.Context, audio io.Reader, filename string, opts TranscribeOptions) (string, error)
}
