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

// Transcribe sends audio to the OpenAI Whisper API and returns the transcribed text.
// filename should include the extension (e.g. "audio.webm") so the API can detect
// the format. Supported formats: mp3, mp4, mpeg, mpga, m4a, wav, webm, ogg.
func (o *OpenAI) Transcribe(ctx context.Context, audio io.Reader, filename string) (string, error) {
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
		if err := mw.WriteField("model", "whisper-1"); err != nil {
			pw.CloseWithError(err)
			return
		}
		if err := mw.WriteField("response_format", "json"); err != nil {
			pw.CloseWithError(err)
			return
		}
		pw.CloseWithError(mw.Close())
	}()

	url := apiBaseURL + transcribeEndpoint
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
	defer resp.Body.Close()

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
