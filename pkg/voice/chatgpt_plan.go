package voice

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// ChatGPTCallsURL creates a direct v3 voice call on the ChatGPT plan (INFORME
// §9.3). The offer travels in the JSON body; the 201 body is the SDP answer.
const ChatGPTCallsURL = "https://chatgpt.com/backend-api/wham/realtime/calls?intent=quicksilver&architecture=avas"

const chatGPTVoiceModel = "gpt-live-1-codex"

// ChatGPTPlanConfig configures the chatgpt-plan adapter.
type ChatGPTPlanConfig struct {
	URL         string
	HTTPClient  *http.Client
	Credentials CredentialSource
	Peer        PeerConfig
	Billing     Billing
}

type chatGPTPlan struct{ cfg ChatGPTPlanConfig }

// NewChatGPTPlan returns the direct ChatGPT-plan adapter: one Pion peer per
// call, the oai-events datachannel as the single control source, and client
// delegation (handoffs) answered by moa's own backend loop.
func NewChatGPTPlan(cfg ChatGPTPlanConfig) Adapter {
	if cfg.URL == "" {
		cfg.URL = ChatGPTCallsURL
	}
	return &chatGPTPlan{cfg: cfg}
}

func (a *chatGPTPlan) ID() ProviderID   { return ChatGPTPlan }
func (a *chatGPTPlan) Billing() Billing { return a.cfg.Billing }
func (a *chatGPTPlan) Availability(context.Context) Availability {
	return Availability{State: "unknown"}
}

func (a *chatGPTPlan) Open(ctx context.Context, spec CallSpec) (Upstream, error) {
	peer, err := NewPeer(a.cfg.Peer)
	if err != nil {
		return nil, &OpenError{Class: ClassUnavailable}
	}
	up, err := a.open(ctx, spec, peer)
	if err != nil {
		_ = peer.Close()
		return nil, err
	}
	return up, nil
}

func (a *chatGPTPlan) open(ctx context.Context, spec CallSpec, peer *Peer) (*chatGPTUpstream, error) {
	offer, err := peer.Offer(ctx)
	if err != nil {
		return nil, Classify(err)
	}
	instructions := spec.Instructions
	if spec.InitialContext != "" {
		instructions += "\n\n" + spec.InitialContext
	}
	body, _ := json.Marshal(map[string]any{"sdp": offer, "session": map[string]any{
		"model":        chatGPTVoiceModel,
		"instructions": instructions,
		"audio":        map[string]any{"output": map[string]any{"voice": spec.Voice}},
		"delegation":   map[string]any{"type": "client"},
	}})
	thread, err := newUUID()
	if err != nil {
		return nil, &OpenError{Class: ClassUnavailable}
	}
	client := noRedirect(a.cfg.HTTPClient)
	answer, err := withAuthRetry(ctx, a.cfg.Credentials, true, func(c Credential) (string, error) {
		if c.AccountID == "" {
			return "", &OpenError{Class: ClassCredentials}
		}
		return a.post(ctx, client, body, thread, c)
	})
	if err != nil {
		return nil, err
	}
	if err := peer.SetAnswer(answer); err != nil {
		return nil, &OpenError{Class: ClassUnavailable}
	}
	if err := peer.WaitReady(ctx); err != nil {
		return nil, Classify(err)
	}
	// session.started on the datachannel is the ready signal.
	for {
		select {
		case data, ok := <-peer.Control():
			if !ok {
				return nil, &OpenError{Class: ClassUnavailable}
			}
			var env wsEvent
			if json.Unmarshal(data, &env) == nil && env.Type == "session.started" {
				u := &chatGPTUpstream{peer: peer, events: make(chan Event, upstreamEventQueue), stop: make(chan struct{}), readerDone: make(chan struct{})}
				go u.read()
				if spec.Opening == OpenOnSilence {
					go u.openOnSilence()
				}
				return u, nil
			}
		case <-ctx.Done():
			return nil, Classify(ctx.Err())
		}
	}
}

func (a *chatGPTPlan) post(ctx context.Context, client *http.Client, body []byte, thread string, c Credential) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return "", &OpenError{Class: ClassUnavailable}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("ChatGPT-Account-Id", c.AccountID)
	req.Header.Set("originator", "Codex Desktop")
	req.Header.Set("OpenAI-Alpha", "quicksilver=v2")
	req.Header.Set("Thread-Id", thread)
	resp, err := client.Do(req)
	if err != nil {
		return "", Classify(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusCreated {
		// The refusal body may echo credentials or signaling: discard it.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return "", statusError(resp.StatusCode)
	}
	answer, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil || len(answer) == 0 {
		return "", &OpenError{Class: ClassUnavailable}
	}
	return string(answer), nil
}

func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

type chatGPTUpstream struct {
	peer       *Peer
	events     chan Event
	stop       chan struct{}
	stopOnce   sync.Once
	closeOnce  sync.Once
	readerDone chan struct{}
	inAudio    inboundAudio // reader-owned
	// heard is set once anyone's speech is transcribed (OpenOnSilence).
	heard atomic.Bool
}

// OpenNudge is what the plan reads when the user picked up and said
// nothing: it has no "speak now" command and cannot count seconds, and a
// context append is what makes it speak. It says outright that the user has
// said nothing, so the voice never answers words nobody said.
const OpenNudge = "The user picked up but has not said anything yet; nothing they said exists in this call. Speak now: greet briefly and say why you are calling, as your instructions say."

// openOnSilence nudges the plan after OpenSilence() unless someone spoke.
func (u *chatGPTUpstream) openOnSilence() {
	t := time.NewTimer(OpenSilence())
	defer t.Stop()
	select {
	case <-t.C:
		if !u.heard.Load() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = u.InjectContext(ctx, OpenNudge)
			cancel()
		}
	case <-u.stop:
	}
}

// read is the single reader: datachannel control and decoded media both
// become events here, so no event is processed from two sources.
func (u *chatGPTUpstream) read() {
	defer close(u.readerDone)
	defer close(u.events)
	control, pcm := u.peer.Control(), u.peer.PCM()
	for control != nil || pcm != nil {
		select {
		case <-u.stop:
			return
		case data, ok := <-control:
			if !ok {
				control = nil
				continue
			}
			for _, ev := range parseChatGPT(data) {
				if ev.Kind == EventTranscript {
					u.heard.Store(true)
				}
				if !u.emit(ev) || ev.Kind == EventFailed {
					return
				}
			}
		case frame, ok := <-pcm:
			if !ok {
				pcm = nil
				continue
			}
			if !u.emit(Event{Kind: EventAudio, PCM: frame}) {
				return
			}
		}
	}
	u.emit(Event{Kind: EventClosed, Reason: "upstream_closed"})
}

func (u *chatGPTUpstream) emit(ev Event) bool {
	if !u.inAudio.admit(len(u.events), len(ev.PCM)) {
		ev = Event{Kind: EventFailed, Reason: FailInboundOverflow, Diagnostic: u.inAudio.overflow(len(ev.PCM))}
		select {
		case u.events <- ev:
		case <-u.stop:
		}
		return false
	}
	select {
	case u.events <- ev:
		return true
	case <-u.stop:
		return false
	}
}

func parseChatGPT(data []byte) []Event {
	var e struct {
		Type string `json:"type"`
		Item struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			Target  string `json:"target"`
			Text    string `json:"text"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"item"`
		Turn struct {
			ID         string `json:"id"`
			Role       string `json:"role"`
			Transcript string `json:"transcript"`
		} `json:"turn"`
	}
	if json.Unmarshal(data, &e) != nil {
		return nil
	}
	switch e.Type {
	case "error":
		if fatal, d := providerErrorFatal(data); fatal {
			return []Event{{Kind: EventFailed, Reason: FailProviderError, Diagnostic: d}}
		}
		return nil
	case "input_transcript.added":
		return []Event{{Kind: EventTranscript, Role: "user", Text: e.Item.Text}}
	case "output_transcript.added":
		return []Event{{Kind: EventTranscript, Role: "assistant", Text: e.Item.Text}}
	case "turn.done":
		if e.Turn.Role != "user" && e.Turn.Role != "assistant" {
			return nil
		}
		final := Event{Kind: EventTranscript, Role: e.Turn.Role, ItemID: e.Turn.ID, Text: e.Turn.Transcript, Final: true}
		if e.Turn.Role == "user" {
			return []Event{final, {Kind: EventUserTurnEnded}}
		}
		return []Event{final}
	case "delegation.created":
		if e.Item.Type != "delegation" || e.Item.Target != "client" || e.Item.ID == "" {
			return nil
		}
		var text bytes.Buffer
		for _, c := range e.Item.Content {
			if c.Type == "input_text" {
				text.WriteString(c.Text)
			}
		}
		return []Event{{Kind: EventHandoff, ItemID: e.Item.ID, Text: text.String()}}
	}
	return nil
}

func (u *chatGPTUpstream) Events() <-chan Event { return u.events }

func (u *chatGPTUpstream) SendAudio(_ context.Context, pcm []byte) error {
	if len(pcm)%2 != 0 || len(pcm) > MaxPCMFrameBytes {
		return errors.New("invalid PCM frame")
	}
	return u.peer.WritePCM(pcm)
}

// SendToolResults is not part of client delegation: tools run inside moa's
// backend loop and only its answer goes back (SendHandoffResult).
func (u *chatGPTUpstream) SendToolResults(context.Context, []ToolResult) error {
	return errors.New("chatgpt-plan answers handoffs, not tool batches")
}

func (u *chatGPTUpstream) control(ctx context.Context, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return u.peer.SendControl(ctx, b)
}

func inputText(text string) []any { return []any{map[string]any{"type": "input_text", "text": text}} }

// SendHandoffResult returns the backend's answer for one delegation item,
// split on rune boundaries into the 500-byte appends v3 accepts.
func (u *chatGPTUpstream) SendHandoffResult(ctx context.Context, itemID, text string) error {
	for _, chunk := range splitBytes(text, contextAppendMax) {
		if err := u.control(ctx, map[string]any{"type": "delegation.context.append", "delegation_item_id": itemID, "channel": "speakable", "content": inputText(chunk)}); err != nil {
			return err
		}
	}
	return nil
}

func (u *chatGPTUpstream) InjectContext(ctx context.Context, text string) error {
	for _, chunk := range splitBytes(text, contextAppendMax) {
		if err := u.control(ctx, map[string]any{"type": "session.context.append", "content": inputText(chunk)}); err != nil {
			return err
		}
	}
	return nil
}

func (u *chatGPTUpstream) RequestMinutes(ctx context.Context) error {
	return u.InjectContext(ctx, MinutesRequest)
}

// Close asks the provider to end the session over the control channel, then
// closes the peer (DTLS close) and waits for the reader within ctx.
func (u *chatGPTUpstream) Close(ctx context.Context, _ string) error {
	u.closeOnce.Do(func() {
		select {
		case <-u.peer.Done():
		default:
			_ = u.control(ctx, map[string]any{"type": "session.close"})
		}
		u.stopOnce.Do(func() { close(u.stop) })
		_ = u.peer.Close()
	})
	select {
	case <-u.readerDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// splitBytes cuts s into chunks of at most n bytes on rune boundaries.
func splitBytes(s string, n int) []string {
	if len(s) <= n {
		return []string{s}
	}
	var out []string
	for len(s) > 0 {
		end := min(n, len(s))
		for end > 0 && end < len(s) && !utf8.RuneStart(s[end]) {
			end--
		}
		if end == 0 {
			end = min(n, len(s))
		}
		out = append(out, s[:end])
		s = s[end:]
	}
	return out
}
