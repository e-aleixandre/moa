package openai

import (
	"bytes"
	"context"
	"encoding/json"
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

// The endpoint is fixed. Cloudflare in front of chatgpt.com answers 403 (HTML)
// to Go's default User-Agent; the neutral Codex identity that the text
// provider uses (codexUserAgent) passes.
const planTranscribeURL = "https://chatgpt.com/backend-api/transcribe"

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
	// budget bounds the whole plan attempt (credentials included) for an audio
	// of the given size, so a hung plan hands over to the API key quickly.
	budget func(audioBytes int) time.Duration
}

var _ core.Transcriber = (*PlanTranscriber)(nil)

func NewPlanTranscriber(creds PlanCredentials) *PlanTranscriber {
	return &PlanTranscriber{
		creds: creds,
		url:   planTranscribeURL,
		client: &http.Client{
			// A redirect would replay the audio and the Codex identity to
			// another origin; a 3xx is just a plan failure.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		budget: planBudget,
	}
}

// planBudget: 15 s base plus 2 s per MiB, capped at 60 s. The measured plan
// latency is ~0.7 s for a few seconds of audio (0.65-1.9 s over the 15/10
// samples and 0.6-0.74 s on 17/10), so the base is >20x the normal case; the
// per-MiB margin covers a slow upload of a long note (the 25 MiB limit lands
// on the cap).
func planBudget(audioBytes int) time.Duration {
	const mib = 1 << 20
	d := 15*time.Second + time.Duration(audioBytes)*2*time.Second/mib
	return min(d, 60*time.Second)
}

// Transcribe errors are fixed strings with a safe category (see
// transcribeError): callers log and return them.
func (p *PlanTranscriber) Transcribe(parent context.Context, audio io.Reader, filename string, opts core.TranscribeOptions) (string, error) {
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
		return "", &transcribeError{msg: "plan transcribe: building request", reason: "request"}
	}
	n, err := io.Copy(part, audio)
	if err != nil {
		return "", &transcribeError{msg: "plan transcribe: reading audio", reason: "request"}
	}
	if opts.Language != "" {
		_ = mw.WriteField("language", opts.Language)
	}
	if opts.Prompt != "" {
		_ = mw.WriteField("prompt", opts.Prompt)
	}
	if err := mw.Close(); err != nil {
		return "", &transcribeError{msg: "plan transcribe: building request", reason: "request"}
	}

	// A child context: the API key fallback keeps living on the parent.
	ctx, cancel := context.WithTimeout(parent, p.budget(int(n)))
	defer cancel()

	token, account, err := p.creds(ctx)
	if err != nil {
		if parent.Err() != nil || ctx.Err() != nil {
			return "", transportError("plan transcribe", parent, ctx.Err())
		}
		return "", &transcribeError{msg: "plan transcribe: credentials unavailable", reason: "credentials"}
	}
	if token == "" || account == "" {
		return "", &transcribeError{msg: "plan transcribe: no ChatGPT login", reason: "credentials"}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, &buf)
	if err != nil {
		return "", &transcribeError{msg: "plan transcribe: building request", reason: "request"}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("ChatGPT-Account-Id", account)
	req.Header.Set("originator", "Codex Desktop")
	req.Header.Set("User-Agent", codexUserAgent)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := p.client.Do(req)
	if err != nil {
		return "", transportError("plan transcribe", parent, err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		// The service refusing the audio: the key would refuse it too.
		return "", &transcribeError{
			msg:        fmt.Sprintf("plan transcribe: HTTP %d", resp.StatusCode),
			reason:     fmt.Sprintf("http_%d", resp.StatusCode),
			noFallback: resp.StatusCode == http.StatusBadRequest,
		}
	}
	var out struct {
		Text *string `json:"text"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil || out.Text == nil {
		return "", &transcribeError{msg: "plan transcribe: unexpected response format", reason: "format"}
	}
	return *out.Text, nil
}
