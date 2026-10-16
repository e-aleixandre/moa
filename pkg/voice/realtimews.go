package voice

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"

	"github.com/coder/websocket"
)

// XAIRealtimeURL is the xAI realtime endpoint for both grok-plan and xai-api.
const XAIRealtimeURL = "wss://api.x.ai/v1/realtime?model=grok-voice-latest"

// MinutesRequest asks the voice model for its minutes before teardown.
const MinutesRequest = "The call is ending now. Call end_call with what was decided, what was passed to the session and what is still open. Do not say anything else."

// RealtimeWSConfig configures the xAI realtime adapter.
type RealtimeWSConfig struct {
	ID          ProviderID // grok-plan or xai-api
	URL         string
	HTTPClient  *http.Client
	Credentials CredentialSource
	// OAuth enables the single store refresh after a 401; false for keys.
	OAuth   bool
	Billing Billing
}

type realtimeWS struct{ cfg RealtimeWSConfig }

// NewRealtimeWS returns the xAI realtime adapter (grok-plan / xai-api).
func NewRealtimeWS(cfg RealtimeWSConfig) Adapter {
	if cfg.URL == "" {
		cfg.URL = XAIRealtimeURL
	}
	return &realtimeWS{cfg: cfg}
}

func (a *realtimeWS) ID() ProviderID   { return a.cfg.ID }
func (a *realtimeWS) Billing() Billing { return a.cfg.Billing }
func (a *realtimeWS) Availability(context.Context) Availability {
	return Availability{State: "unknown"}
}

func (a *realtimeWS) Open(ctx context.Context, spec CallSpec) (Upstream, error) {
	conn, err := withAuthRetry(ctx, a.cfg.Credentials, a.cfg.OAuth, func(c Credential) (*websocket.Conn, error) {
		return dialWS(ctx, a.cfg.HTTPClient, a.cfg.URL, http.Header{"Authorization": {"Bearer " + c.Token}})
	})
	if err != nil {
		return nil, err
	}
	instructions := spec.Instructions
	if spec.InitialContext != "" {
		instructions += "\n\n" + spec.InitialContext
	}
	pcm := map[string]any{"type": "audio/pcm", "rate": 24000}
	init := map[string]any{"type": "session.update", "session": map[string]any{
		"voice":          spec.Voice,
		"instructions":   instructions,
		"tools":          functionTools(spec.Tools),
		"turn_detection": map[string]any{"type": "server_vad"},
		"audio":          map[string]any{"input": map[string]any{"format": pcm}, "output": map[string]any{"format": pcm}},
	}}
	if err := handshake(ctx, conn, init, "session.updated"); err != nil {
		_ = conn.CloseNow()
		return nil, err
	}
	up := &realtimeUpstream{}
	up.wsUpstream = newWSUpstream(conn, up.parse)
	up.start()
	if spec.SpeakFirst {
		if err := up.send(map[string]any{"type": "response.create"}); err != nil {
			_ = up.close(ctx)
			return nil, err
		}
	}
	return up, nil
}

// handshake writes the initialization frame and waits for its ready event,
// skipping anything else. A provider error before ready fails the open.
func handshake(ctx context.Context, conn *websocket.Conn, init any, ready string) error {
	b, _ := json.Marshal(init)
	if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
		return Classify(err)
	}
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return Classify(err)
		}
		var env wsEvent
		if json.Unmarshal(data, &env) != nil {
			continue
		}
		switch env.Type {
		case ready:
			return nil
		case "error":
			return &OpenError{Class: ClassUnavailable}
		}
	}
}

type realtimeUpstream struct {
	*wsUpstream
	pending []ToolCall // reader-owned
}

func (u *realtimeUpstream) parse(typ string, raw []byte) []Event {
	var e struct {
		ItemID     string `json:"item_id"`
		Transcript string `json:"transcript"`
		Delta      string `json:"delta"`
		CallID     string `json:"call_id"`
		Name       string `json:"name"`
		Arguments  string `json:"arguments"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return nil
	}
	switch typ {
	case "input_audio_buffer.speech_started":
		return []Event{{Kind: EventSpeechStarted}}
	case "input_audio_buffer.speech_stopped":
		return []Event{{Kind: EventSpeechStopped}}
	case "conversation.item.input_audio_transcription.completed":
		return []Event{{Kind: EventTranscript, Role: "user", ItemID: e.ItemID, Text: e.Transcript, Final: true}}
	case "response.output_audio_transcript.delta":
		return []Event{{Kind: EventTranscript, Role: "assistant", ItemID: e.ItemID, Text: e.Delta}}
	case "response.output_audio_transcript.done":
		return []Event{{Kind: EventTranscript, Role: "assistant", ItemID: e.ItemID, Text: e.Transcript, Final: true}}
	case "response.output_audio.delta", "response.audio.delta":
		pcm, err := base64.StdEncoding.DecodeString(e.Delta)
		if err != nil || len(pcm) == 0 {
			return nil
		}
		return []Event{{Kind: EventAudio, PCM: pcm}}
	case "response.function_call_arguments.done":
		u.pending = append(u.pending, ToolCall{ID: e.CallID, Name: e.Name, Arguments: json.RawMessage(argsOrEmpty(e.Arguments))})
		return nil
	case "response.done":
		if len(u.pending) == 0 {
			return []Event{{Kind: EventResponseDone}}
		}
		batch := u.pending
		u.pending = nil
		return []Event{{Kind: EventToolBatch, Tools: batch}, {Kind: EventResponseDone}}
	}
	return nil
}

func argsOrEmpty(s string) string {
	if s == "" {
		return "{}"
	}
	return s
}

func (u *realtimeUpstream) SendAudio(_ context.Context, pcm []byte) error {
	return u.sendAudio("input_audio_buffer.append", pcm)
}

// SendToolResults answers a whole batch, then asks for exactly one response.
func (u *realtimeUpstream) SendToolResults(_ context.Context, results []ToolResult) error {
	for _, r := range results {
		if err := u.send(map[string]any{"type": "conversation.item.create", "item": map[string]any{"type": "function_call_output", "call_id": r.ID, "output": string(r.Output)}}); err != nil {
			return err
		}
	}
	return u.send(map[string]any{"type": "response.create"})
}

func (u *realtimeUpstream) systemItem(text string) map[string]any {
	return map[string]any{"type": "conversation.item.create", "item": map[string]any{"type": "message", "role": "system", "content": []any{map[string]any{"type": "input_text", "text": text}}}}
}

func (u *realtimeUpstream) InjectContext(_ context.Context, text string) error {
	return u.send(u.systemItem(text))
}

func (u *realtimeUpstream) RequestMinutes(context.Context) error {
	if err := u.send(u.systemItem(MinutesRequest)); err != nil {
		return err
	}
	return u.send(map[string]any{"type": "response.create"})
}

func (u *realtimeUpstream) Close(ctx context.Context, _ string) error { return u.close(ctx) }
