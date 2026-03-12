package core

import (
	"context"
	"io"
)

// Transcriber converts audio to text. Providers that support speech-to-text
// (e.g. OpenAI Whisper) implement this interface.
type Transcriber interface {
	Transcribe(ctx context.Context, audio io.Reader, filename string) (string, error)
}
