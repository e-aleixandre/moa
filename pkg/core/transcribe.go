package core

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
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

// NoFallbackError marks a Primary failure that the Fallback must not retry,
// e.g. the service rejected the audio itself, so a paid retry would fail too.
type NoFallbackError interface {
	error
	NoFallback() bool
}

// FallbackTranscriber tries Primary and, only if it fails, Fallback. It exists
// so dictation can ride the subscription and fall back to a billed API key.
// Either side may be nil.
type FallbackTranscriber struct {
	Primary  Transcriber
	Fallback Transcriber
}

var _ Transcriber = (*FallbackTranscriber)(nil)

func (f *FallbackTranscriber) Transcribe(ctx context.Context, audio io.Reader, filename string, opts TranscribeOptions) (string, error) {
	if f.Primary == nil {
		return f.Fallback.Transcribe(ctx, audio, filename, opts)
	}
	if f.Fallback == nil {
		return f.Primary.Transcribe(ctx, audio, filename, opts)
	}
	// The audio must be replayable for the second attempt.
	data, err := io.ReadAll(audio)
	if err != nil {
		return "", err
	}
	text, err := f.Primary.Transcribe(ctx, bytes.NewReader(data), filename, opts)
	if err == nil {
		slog.Info("dictation transcribed", "route", "plan")
		return text, nil
	}
	// The caller went away: do not spend the paid key on a dead request.
	if ctx.Err() != nil {
		return "", err
	}
	var nf NoFallbackError
	if errors.As(err, &nf) && nf.NoFallback() {
		slog.Warn("dictation: plan rejected the audio, not falling back", "reason", err.Error())
		return "", err
	}
	slog.Warn("dictation: plan failed, falling back to API key", "reason", err.Error())
	text, err = f.Fallback.Transcribe(ctx, bytes.NewReader(data), filename, opts)
	if err == nil {
		slog.Info("dictation transcribed", "route", "api_key")
	}
	return text, err
}
