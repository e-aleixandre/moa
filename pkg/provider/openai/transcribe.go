package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"

	"github.com/ealeixandre/moa/pkg/core"
)

const transcribeEndpoint = "/v1/audio/transcriptions"

// Compile-time check that OpenAI implements Transcriber.
var _ core.Transcriber = (*OpenAI)(nil)

// Transcribe sends audio to the OpenAI transcription API and returns the text.
// filename should include the extension (e.g. "audio.webm") so the API can detect
// the format. Supported formats: mp3, mp4, mpeg, mpga, m4a, wav, webm, ogg.
//
// opts.Model selects the transcription model; empty falls back to the package
// default. Note that response_format stays "json": the newer models reject
// "verbose_json", and we only ever read the text field anyway.
func (o *OpenAI) Transcribe(ctx context.Context, audio io.Reader, filename string, opts core.TranscribeOptions) (string, error) {
	model := opts.Model
	if model == "" {
		model = core.DefaultSTTModel
	}

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)

	// Write multipart form in a goroutine to stream without buffering.
	go func() {
		part, err := mw.CreateFormFile("file", filename)
		if err != nil {
			pw.CloseWithError(err)
			return
		}
		if _, err := io.Copy(part, audio); err != nil {
			pw.CloseWithError(err)
			return
		}
		if err := mw.WriteField("model", model); err != nil {
			pw.CloseWithError(err)
			return
		}
		if err := mw.WriteField("response_format", "json"); err != nil {
			pw.CloseWithError(err)
			return
		}
		if opts.Language != "" {
			if err := mw.WriteField("language", opts.Language); err != nil {
				pw.CloseWithError(err)
				return
			}
		}
		if opts.Prompt != "" {
			if err := mw.WriteField("prompt", opts.Prompt); err != nil {
				pw.CloseWithError(err)
				return
			}
		}
		pw.CloseWithError(mw.Close())
	}()

	url := o.baseURL + transcribeEndpoint
	req, err := http.NewRequestWithContext(ctx, "POST", url, pr)
	if err != nil {
		return "", fmt.Errorf("openai transcribe: creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+o.apiKey)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := o.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("openai transcribe: http: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("openai transcribe: HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("openai transcribe: decoding response: %w", err)
	}

	return result.Text, nil
}
