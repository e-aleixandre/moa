package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"path/filepath"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

const planTranscribeURL = "https://chatgpt.com/backend-api/transcribe"

// Cloudflare in front of chatgpt.com answers 403 (HTML) to Go's default
// User-Agent; the neutral Codex identity the text provider uses passes.

// PlanCredentials returns the current ChatGPT OAuth access token and account
// id. It owns refreshing (the auth store serialises it with every other user
// of the credential), so the transcriber never rotates tokens itself.
type PlanCredentials func(ctx context.Context) (token, accountID string, err error)

// PlanTranscriber dictates through the ChatGPT subscription: the endpoint the
// Codex desktop app uses, reached with the user's OAuth login instead of a
// billed API key. The service picks the model; opts.Model is ignored.
type PlanTranscriber struct {
	creds  PlanCredentials
	url    string
	client *http.Client
}

var _ core.Transcriber = (*PlanTranscriber)(nil)

func NewPlanTranscriber(creds PlanCredentials) *PlanTranscriber {
	return &PlanTranscriber{creds: creds, url: planTranscribeURL, client: &http.Client{Timeout: 2 * time.Minute}}
}

// Transcribe errors never carry the response body or credentials: callers log
// them.
func (p *PlanTranscriber) Transcribe(ctx context.Context, audio io.Reader, filename string, opts core.TranscribeOptions) (string, error) {
	token, account, err := p.creds(ctx)
	if err != nil {
		return "", fmt.Errorf("plan transcribe: credentials: %w", err)
	}
	if token == "" || account == "" {
		return "", errors.New("plan transcribe: no ChatGPT login")
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, filepath.Base(filename)))
	ctype := mime.TypeByExtension(filepath.Ext(filename))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	h.Set("Content-Type", ctype)
	part, err := mw.CreatePart(h)
	if err != nil {
		return "", fmt.Errorf("plan transcribe: building request: %w", err)
	}
	if _, err := io.Copy(part, audio); err != nil {
		return "", fmt.Errorf("plan transcribe: reading audio: %w", err)
	}
	if opts.Language != "" {
		_ = mw.WriteField("language", opts.Language)
	}
	if opts.Prompt != "" {
		_ = mw.WriteField("prompt", opts.Prompt)
	}
	if err := mw.Close(); err != nil {
		return "", fmt.Errorf("plan transcribe: building request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, &buf)
	if err != nil {
		return "", fmt.Errorf("plan transcribe: building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("ChatGPT-Account-Id", account)
	req.Header.Set("originator", "Codex Desktop")
	req.Header.Set("User-Agent", codexUserAgent)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := p.client.Do(req)
	if err != nil {
		// url.Error embeds the URL only, never headers.
		return "", fmt.Errorf("plan transcribe: network: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return "", fmt.Errorf("plan transcribe: HTTP %d", resp.StatusCode)
	}
	var out struct {
		Text *string `json:"text"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil || out.Text == nil {
		return "", errors.New("plan transcribe: unexpected response format")
	}
	return *out.Text, nil
}
