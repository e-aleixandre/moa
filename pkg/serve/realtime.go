package serve

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Realtime voice prototype support.
//
// This mints a short-lived ("ephemeral") OpenAI Realtime client secret so the
// browser can open a WebRTC session directly with OpenAI without ever seeing
// the real API key. It is deliberately minimal: the browser configures the
// session (instructions, tools, voice behaviour) itself over the data channel
// after connecting. moa serve stays out of the audio path entirely.
//
// This is the ONLY backend surface the voice prototype needs. No audio, no
// gpt-realtime protocol, no bridge logic lives in Go — that is all client-side
// in static/voice.html (throwaway prototype).

const (
	realtimeSecretsURL = "https://api.openai.com/v1/realtime/client_secrets"
	// realtimeModel is the model minted into the ephemeral token. Swap this for
	// the newer model when it ships — nothing else in the design changes.
	realtimeModel = "gpt-realtime"
	realtimeVoice = "marin"
)

// mintRealtimeToken asks OpenAI for an ephemeral client secret using the
// server's standard API key. Returns the raw JSON body from OpenAI (which the
// browser reads .value from) or an error.
func mintRealtimeToken(ctx context.Context, apiKey string) ([]byte, error) {
	reqBody := fmt.Sprintf(`{"session":{"type":"realtime","model":%q,"audio":{"output":{"voice":%q}}}}`,
		realtimeModel, realtimeVoice)

	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", realtimeSecretsURL, bytes.NewReader([]byte(reqBody)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("openai realtime token: HTTP %d: %s", resp.StatusCode, string(body))
	}
	return body, nil
}

// handleRealtimeToken mints and returns an ephemeral realtime token. Gated on
// an OpenAI API key being configured (same credential family as transcription).
func handleRealtimeToken(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if mgr.openaiKey == "" {
			http.Error(w, "realtime voice not available (no OpenAI API key configured)", http.StatusServiceUnavailable)
			return
		}
		body, err := mintRealtimeToken(r.Context(), mgr.openaiKey)
		if err != nil {
			http.Error(w, "failed to mint realtime token: "+err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}
}
