package voice

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"

	"github.com/coder/websocket"
)

// LiveSessionsURL is the OpenAI Live API endpoint (API key only).
const LiveSessionsURL = "wss://api.openai.com/v1/live/sessions"

// liveVoiceModel is the Live voice model; the backend model is the call's
// BackendModel through Responses delegation.
const liveVoiceModel = "gpt-live-1"

// liveAppendChars bounds one context append. Live takes up to 500 tokens per
// append; characters are a conservative stand-in, not a tokenizer.
const liveAppendChars = 1500

// LiveWSConfig configures the OpenAI Live API adapter.
type LiveWSConfig struct {
	URL         string
	HTTPClient  *http.Client
	Credentials CredentialSource
	Billing     Billing
}

type liveWS struct{ cfg LiveWSConfig }

// NewLiveWS returns the openai-live-api adapter. It authenticates with an API
// key only and therefore never refreshes OAuth.
func NewLiveWS(cfg LiveWSConfig) Adapter {
	if cfg.URL == "" {
		cfg.URL = LiveSessionsURL
	}
	return &liveWS{cfg: cfg}
}

func (a *liveWS) ID() ProviderID                            { return OpenAILiveAPI }
func (a *liveWS) Billing() Billing                          { return a.cfg.Billing }
func (a *liveWS) Availability(context.Context) Availability { return Availability{State: "unknown"} }

func (a *liveWS) Open(ctx context.Context, spec CallSpec) (Upstream, error) {
	conn, err := withAuthRetry(ctx, a.cfg.Credentials, false, func(c Credential) (*websocket.Conn, error) {
		return dialWS(ctx, a.cfg.HTTPClient, a.cfg.URL, http.Header{"Authorization": {"Bearer " + c.Token}})
	})
	if err != nil {
		return nil, err
	}
	instructions := spec.Instructions
	if spec.InitialContext != "" {
		instructions += "\n\n" + spec.InitialContext
	}
	init := map[string]any{"type": "session.start", "session": map[string]any{
		"model":        liveVoiceModel,
		"instructions": instructions,
		"audio": map[string]any{
			"format": map[string]any{"type": "audio/pcm", "rate": 24000},
			"output": map[string]any{"voice": spec.Voice},
		},
		"delegation": map[string]any{"type": "responses", "responses": map[string]any{
			"model":        spec.BackendModel.ID,
			"instructions": HandoffInstructions,
			"tools":        functionTools(spec.Tools),
			"tool_choice":  "auto",
		}},
	}}
	if err := handshake(ctx, conn, init, "session.started"); err != nil {
		_ = conn.CloseNow()
		return nil, err
	}
	up := &liveUpstream{pending: map[string][]ToolCall{}}
	up.wsUpstream = newWSUpstream(conn, up.parse)
	up.closeFrame = map[string]any{"type": "session.close"}
	up.closedType = "session.closed"
	up.start()
	if spec.SpeakFirst {
		if err := up.send(map[string]any{"type": "response.create"}); err != nil {
			_ = up.close(ctx)
			return nil, err
		}
	}
	return up, nil
}

type liveUpstream struct {
	*wsUpstream
	pending map[string][]ToolCall // by delegation, reader-owned
	// Cumulative delegated Responses token usage, reader-owned.
	tokIn, tokOut, tokTotal int64
}

func (u *liveUpstream) parse(typ string, raw []byte) []Event {
	var e struct {
		Delta        string          `json:"delta"`
		StartMS      *int64          `json:"start_ms"`
		EndMS        *int64          `json:"end_ms"`
		DelegationID string          `json:"delegation_id"`
		Event        json.RawMessage `json:"event"`
		Usage        *struct {
			Seconds *float64 `json:"seconds"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return nil
	}
	switch typ {
	// Live transcripts are timed fragments with no turn boundary: they are
	// recorded as they come, never promoted to finals.
	case "session.input_transcript.delta":
		return []Event{{Kind: EventTranscript, Role: "user", Text: e.Delta, StartMS: e.StartMS, EndMS: e.EndMS}}
	case "session.output_transcript.delta":
		return []Event{{Kind: EventTranscript, Role: "assistant", Text: e.Delta, StartMS: e.StartMS, EndMS: e.EndMS}}
	case "session.output_audio.delta":
		pcm, err := base64.StdEncoding.DecodeString(e.Delta)
		if err != nil || len(pcm) == 0 {
			return nil
		}
		return []Event{{Kind: EventAudio, PCM: pcm}}
	// usage.seconds is a cumulative snapshot: the latest one is the running
	// total, confirmed as final only by session.closed.
	case "session.usage.updated", "session.closed":
		if e.Usage == nil || e.Usage.Seconds == nil {
			return nil
		}
		return []Event{{Kind: EventUsage, Usage: &Usage{Seconds: e.Usage.Seconds, Final: typ == "session.closed"}}}
	case "response.event":
		return u.parseResponse(e.DelegationID, e.Event)
	}
	return nil
}

// parseResponse collects function calls from completed output items and
// releases them as one batch when that backend response completes.
func (u *liveUpstream) parseResponse(delegation string, raw json.RawMessage) []Event {
	var ev struct {
		Type     string `json:"type"`
		Response struct {
			Usage *struct {
				InputTokens  *int64 `json:"input_tokens"`
				OutputTokens *int64 `json:"output_tokens"`
				TotalTokens  *int64 `json:"total_tokens"`
			} `json:"usage"`
		} `json:"response"`
		Item struct {
			Type      string `json:"type"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"item"`
	}
	if json.Unmarshal(raw, &ev) != nil {
		return nil
	}
	switch ev.Type {
	case "response.output_item.done":
		if ev.Item.Type == "function_call" {
			u.pending[delegation] = append(u.pending[delegation], ToolCall{ID: ev.Item.CallID, Name: ev.Item.Name, Arguments: json.RawMessage(argsOrEmpty(ev.Item.Arguments))})
		}
	case "response.completed", "response.failed", "response.incomplete", "response.cancelled":
		batch := u.pending[delegation]
		delete(u.pending, delegation)
		var out []Event
		// Delegated tokens are billed apart from session seconds: accumulate
		// them and report the running totals.
		if r := ev.Response.Usage; r != nil && (r.InputTokens != nil || r.OutputTokens != nil || r.TotalTokens != nil) {
			if r.InputTokens != nil {
				u.tokIn += *r.InputTokens
			}
			if r.OutputTokens != nil {
				u.tokOut += *r.OutputTokens
			}
			if r.TotalTokens != nil {
				u.tokTotal += *r.TotalTokens
			}
			in, o, t := u.tokIn, u.tokOut, u.tokTotal
			out = append(out, Event{Kind: EventUsage, Usage: &Usage{InputTokens: &in, OutputTokens: &o, TotalTokens: &t}})
		}
		if len(batch) > 0 {
			out = append(out, Event{Kind: EventToolBatch, Tools: batch})
		}
		return out
	}
	return nil
}

func (u *liveUpstream) SendAudio(_ context.Context, pcm []byte) error {
	return u.sendAudio("session.input_audio.append", pcm)
}

// SendToolResults appends every result of the batch and continues the
// backend once.
func (u *liveUpstream) SendToolResults(_ context.Context, results []ToolResult) error {
	for _, r := range results {
		if err := u.send(map[string]any{"type": "response.item.create", "item": map[string]any{"type": "function_call_output", "call_id": r.ID, "output": string(r.Output)}}); err != nil {
			return err
		}
	}
	return u.send(map[string]any{"type": "response.create"})
}

func (u *liveUpstream) InjectContext(_ context.Context, text string) error {
	for _, chunk := range splitRunes(text, liveAppendChars) {
		if err := u.send(map[string]any{"type": "session.thinking.append", "delegation_id": nil, "content": chunk}); err != nil {
			return err
		}
	}
	return nil
}

func (u *liveUpstream) RequestMinutes(context.Context) error {
	return u.send(map[string]any{"type": "session.instructions.append", "delegation_id": nil, "content": MinutesRequest})
}

func (u *liveUpstream) Close(ctx context.Context, _ string) error { return u.close(ctx) }

// splitRunes cuts s into chunks of at most n runes, never inside a rune.
func splitRunes(s string, n int) []string {
	var out []string
	r := []rune(s)
	for len(r) > n {
		out = append(out, string(r[:n]))
		r = r[n:]
	}
	if len(r) > 0 {
		out = append(out, string(r))
	}
	return out
}
