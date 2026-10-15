package serve

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/e-aleixandre/moa/pkg/auth"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/tasks"
	"github.com/pion/webrtc/v4"
)

const vpDeadline = 3 * time.Second

var vpWSProviders = []string{"grok-plan", "xai-api", "openai-live-api"}
var vpAllProviders = []string{"grok-plan", "xai-api", "chatgpt-plan", "openai-live-api"}

type vpLimits struct {
	grace, maximum, minutes, teardown time.Duration
	bufferedBytes, briefChars         int
}

// Only hookup changes belong here when the voice factory/clock/selector exist.
// These current options do NOT wire the absent voice adapters. Never register
// test routes or implement policy here. Budgets are test-only, not defaults.
func vpServerOptions(store *auth.Store, logins *auth.ProviderLoginManager, client *http.Client, limits vpLimits, backend func(core.Model, auth.CredentialSnapshot) (core.Provider, error)) []ServerOption {
	// Same loopback-only ICE as the provider fixture: no STUN, no external host.
	var se webrtc.SettingEngine
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	se.SetIncludeLoopbackCandidate(true)
	se.SetIPFilter(func(ip net.IP) bool { return ip.IsLoopback() })
	se.SetRemoteIPFilter(func(ip net.IP) bool { return ip.IsLoopback() })
	se.SetICETimeouts(time.Second, 2*time.Second, 100*time.Millisecond)
	return []ServerOption{WithProviderCredentials(store, logins), WithRealtimeClientSecretBroker(func() (string, bool) {
		s, e := store.ResolveSnapshot(context.Background(), "openai")
		return s.Token, e == nil && s.Kind == "api_key"
	}, client), WithVoiceCalls(VoiceCallOptions{
		HTTPClient:   client,
		Limits:       VoiceCallLimits{Grace: limits.grace, Maximum: limits.maximum, Minutes: limits.minutes, Teardown: limits.teardown, BufferedBytes: limits.bufferedBytes, BriefChars: limits.briefChars},
		PeerSettings: &se,
		ICEServers:   []webrtc.ICEServer{},
		Backend:      backend,
	})}
}

type vpBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *vpBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}
func (b *vpBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }

type vpRequest struct {
	origin, path, method, authorization, account string
	query, contentType, alpha, thread            string
	body                                         []byte
}
type vpUpstream struct {
	t           *testing.T
	srv         *httptest.Server
	mu          sync.Mutex
	requests    []vpRequest
	peers       []*vpPeer
	reject      int
	rejectOld   bool
	refreshes   int
	refreshed   map[string]bool
	refreshGate <-chan struct{}
	oldSeen     chan struct{}
	backend     *vpVoiceText
}
type vpPeer struct {
	conn           *websocket.Conn
	rtc            *vpRTC
	callID         string
	backend        *vpVoiceText
	provider       string
	mu             sync.Mutex
	frames         []map[string]any
	closed         chan struct{}
	sidebandClosed chan struct{}
	writeMu        sync.Mutex
	timeline       int
}

// Wire sources: grok_voz_moa.py:85-135; PLAN fact 11 and voice-live.js's
// Responses codec; INFORME §9.3 and fuentes-mac-static Appendix B. No RPC.
func vpNewUpstream(t *testing.T) *vpUpstream {
	t.Helper()
	u := &vpUpstream{t: t, refreshed: map[string]bool{}, oldSeen: make(chan struct{}, 8)}
	u.srv = httptest.NewServer(http.HandlerFunc(u.serve))
	t.Cleanup(func() {
		u.mu.Lock()
		ps := append([]*vpPeer(nil), u.peers...)
		u.mu.Unlock()
		for _, p := range ps {
			_ = p.Close()
		}
		u.srv.Close()
	})
	return u
}
func (u *vpUpstream) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	origin := r.Header.Get("X-Voice-Test-Origin")
	q := vpRequest{origin: origin, path: r.URL.Path, method: r.Method, authorization: r.Header.Get("Authorization"), account: r.Header.Get("ChatGPT-Account-Id"), query: r.URL.RawQuery, contentType: r.Header.Get("Content-Type"), alpha: r.Header.Get("OpenAI-Alpha"), thread: r.Header.Get("Thread-Id"), body: raw}
	u.mu.Lock()
	u.requests = append(u.requests, q)
	reject, old, gate := u.reject, u.rejectOld, u.refreshGate
	u.mu.Unlock()
	if origin == "auth.x.ai" && r.URL.Path == "/.well-known/openid-configuration" {
		writeJSON(w, 200, map[string]any{"issuer": "https://auth.x.ai", "token_endpoint": "https://auth.x.ai/oauth2/token", "device_authorization_endpoint": "https://auth.x.ai/oauth2/device/code"})
		return
	}
	if (origin == "auth.x.ai" && r.URL.Path == "/oauth2/token") || (origin == "auth.openai.com" && r.URL.Path == "/oauth/token") {
		form, _ := url.ParseQuery(string(raw))
		token := form.Get("refresh_token")
		if gate != nil {
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}
		u.mu.Lock()
		used := u.refreshed[token]
		if !used {
			u.refreshed[token] = true
			u.refreshes++
		}
		u.mu.Unlock()
		if used || token == "" {
			writeJSON(w, 400, map[string]any{"error": "invalid_grant"})
			return
		}
		access := "VP-ACCESS-ROTATED"
		if origin == "auth.openai.com" {
			access = providerTestJWT("vp-account", "VP-ACCESS-ROTATED")
		}
		writeJSON(w, 200, map[string]any{"access_token": access, "refresh_token": "VP-REFRESH-ROTATED", "expires_in": 3600})
		return
	}
	if old && strings.Contains(q.authorization, "VP-ACCESS-OLD") {
		select {
		case u.oldSeen <- struct{}{}:
		default:
		}
		writeJSON(w, 401, map[string]any{"error": map[string]any{"message": "expired VP-ACCESS-OLD"}})
		return
	}
	if origin == "cli-chat-proxy.grok.com" && r.URL.Path == "/v1/responses" {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"text-control\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n")
		return
	}
	if reject != 0 {
		writeJSON(w, reject, map[string]any{"error": map[string]any{"message": "refused " + q.authorization + " VP-UPSTREAM-CALL VP-SDP VP-ICE"}})
		return
	}
	if origin == "chatgpt.com" && strings.HasSuffix(r.URL.Path, "/realtime/calls") {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		p := &vpPeer{provider: "chatgpt-plan", backend: u.backend}
		rtc, answer, err := vpAnswerRTCPOST(u.t, r.Context(), raw, func(data []byte) {
			var v map[string]any
			if json.Unmarshal(data, &v) == nil {
				p.mu.Lock()
				p.frames = append(p.frames, v)
				p.mu.Unlock()
			}
		})
		if err != nil {
			writeJSON(w, 400, map[string]any{"error": "invalid_offer"})
			return
		}
		p.rtc, p.closed = rtc, rtc.closed
		u.mu.Lock()
		p.callID = fmt.Sprintf("rtc_VP-UPSTREAM-CALL-%d", len(u.peers)+1)
		u.peers = append(u.peers, p)
		u.mu.Unlock()
		// Synthetic resource URL: the report preserved ID extraction, not the
		// full Location. This is not an assertion of its real provider shape.
		w.Header().Set("Location", "https://api.openai.com/v1/live/"+p.callID)
		w.Header().Set("Content-Type", "application/sdp")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, answer)
		return
	}
	if origin == "api.openai.com" && strings.HasPrefix(r.URL.Path, "/v1/live/rtc_") {
		u.mu.Lock()
		var p *vpPeer
		for _, candidate := range u.peers {
			if candidate.callID == strings.TrimPrefix(r.URL.Path, "/v1/live/") {
				p = candidate
				break
			}
		}
		u.mu.Unlock()
		if p == nil {
			http.NotFound(w, r)
			return
		}
		u.serveSideband(w, r, p)
		return
	}
	provider := ""
	switch {
	case origin == "api.x.ai" && r.URL.Path == "/v1/realtime":
		provider = "grok-plan"
		if strings.Contains(q.authorization, "VP-KEY") {
			provider = "xai-api"
		}
	case origin == "api.openai.com" && r.URL.Path == "/v1/live/sessions":
		provider = "openai-live-api"
	case origin == "api.openai.com" && strings.HasPrefix(r.URL.Path, "/v1/live/rtc_"):
		provider = "chatgpt-plan"
	}
	if provider == "" {
		http.NotFound(w, r)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	p := &vpPeer{conn: conn, provider: provider, closed: make(chan struct{})}
	u.mu.Lock()
	u.peers = append(u.peers, p)
	u.mu.Unlock()
	defer close(p.closed)
	defer conn.CloseNow() //nolint:errcheck
	if provider == "grok-plan" || provider == "xai-api" {
		_ = p.send(r.Context(), map[string]any{"type": "session.created", "session": map[string]any{"id": "VP-UPSTREAM-CALL"}})
	}
	for {
		_, data, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		var v map[string]any
		if json.Unmarshal(data, &v) != nil {
			return
		}
		p.mu.Lock()
		p.frames = append(p.frames, v)
		p.mu.Unlock()
		switch v["type"] {
		case "session.update":
			_ = p.send(r.Context(), map[string]any{"type": "session.updated", "session": v["session"]})
		case "session.start":
			_ = p.send(r.Context(), map[string]any{"type": "session.started", "session": map[string]any{"id": "VP-UPSTREAM-CALL"}})
		case "session.close":
			_ = p.send(r.Context(), map[string]any{"type": "session.closed", "usage": map[string]any{"seconds": 2}})
			return
		}
	}
}

type vpTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (rt vpTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	switch r.URL.Host {
	case "api.x.ai", "api.openai.com", "chatgpt.com", "auth.x.ai", "auth.openai.com", "cli-chat-proxy.grok.com":
	default:
		return nil, fmt.Errorf("test forbids external origin %s", r.URL.Host)
	}
	out := r.Clone(r.Context())
	out.URL.Scheme = rt.target.Scheme
	out.URL.Host = rt.target.Host
	out.Host = rt.target.Host
	out.Header.Set("X-Voice-Test-Origin", r.URL.Host)
	return rt.base.RoundTrip(out)
}
func (u *vpUpstream) client() *http.Client {
	target, _ := url.Parse(u.srv.URL)
	// A client timeout on an upgraded response could close the socket and
	// falsely satisfy a lifecycle assertion. The SUT owns connection deadlines.
	return &http.Client{Transport: vpTransport{target: target, base: u.srv.Client().Transport}}
}
func (u *vpUpstream) snapshot() ([]vpRequest, []*vpPeer, int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]vpRequest(nil), u.requests...), append([]*vpPeer(nil), u.peers...), u.refreshes
}
func (u *vpUpstream) peer(t *testing.T, provider string) *vpPeer {
	t.Helper()
	var out *vpPeer
	pollUntil(t, vpDeadline, "upstream handshake "+provider, func() bool {
		_, ps, _ := u.snapshot()
		for _, p := range ps {
			if p.provider == provider {
				out = p
				return true
			}
		}
		return false
	})
	return out
}
func (p *vpPeer) send(ctx context.Context, v any) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	b, _ := json.Marshal(v)
	p.mu.Lock()
	conn := p.conn
	p.mu.Unlock()
	if conn != nil {
		return conn.Write(ctx, websocket.MessageText, b)
	}
	return p.rtc.sendControl(ctx, v)
}
func (p *vpPeer) Close() error {
	p.mu.Lock()
	conn := p.conn
	p.mu.Unlock()
	if conn != nil {
		_ = conn.CloseNow()
	}
	if p.rtc != nil {
		return p.rtc.Close()
	}
	return nil
}

// Appendix B's v3 control is sideband WS. This is just the observed raw
// transport and session.close, not a tool engine or a handoff implementation.
func (u *vpUpstream) serveSideband(w http.ResponseWriter, r *http.Request, p *vpPeer) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	p.mu.Lock()
	if p.conn != nil {
		p.mu.Unlock()
		_ = conn.CloseNow()
		return
	}
	p.conn = conn
	p.sidebandClosed = make(chan struct{})
	done := p.sidebandClosed
	p.mu.Unlock()
	defer close(done)
	defer conn.CloseNow() //nolint:errcheck
	_ = p.send(r.Context(), map[string]any{"type": "session.started", "session": map[string]any{"id": p.callID}})
	for {
		_, raw, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		var v map[string]any
		if json.Unmarshal(raw, &v) != nil {
			return
		}
		p.mu.Lock()
		if len(p.frames) >= 256 {
			p.mu.Unlock()
			return
		}
		p.frames = append(p.frames, v)
		p.mu.Unlock()
		if v["type"] == "session.close" {
			_ = p.rtc.Close()
			return
		}
	}
}
func (p *vpPeer) emit(t *testing.T, v any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), vpDeadline)
	defer cancel()
	if err := p.send(ctx, v); err != nil {
		t.Fatal(err)
	}
}
func (p *vpPeer) snapshot() []map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]map[string]any(nil), p.frames...)
}
func (p *vpPeer) await(t *testing.T, desc string, pred func(map[string]any) bool) map[string]any {
	t.Helper()
	var out map[string]any
	pollUntil(t, vpDeadline, desc, func() bool {
		for _, v := range p.snapshot() {
			if pred(v) {
				out = v
				return true
			}
		}
		return false
	})
	return out
}
func (p *vpPeer) tool(t *testing.T, id, name string, args any) {
	t.Helper()
	b, _ := json.Marshal(args)
	switch p.provider {
	case "chatgpt-plan":
		if p.backend == nil {
			t.Fatal("v3 tool scenario requires the separate text-provider double")
		}
		text := p.backend.script(id, name, args)
		p.emit(t, map[string]any{"type": "delegation.created", "item": map[string]any{
			"id": id, "type": "delegation", "target": "client", "content": []any{map[string]any{"type": "input_text", "text": text}},
		}})
	case "openai-live-api":
		// Live delegates Responses events; response.completed is backend state,
		// never a speech/transcript final boundary. See live-delegation §Handle.
		p.emit(t, map[string]any{"type": "response.event", "delegation_id": "delegation-" + id, "event": map[string]any{"type": "response.created", "response": map[string]any{"id": "response-" + id, "status": "in_progress", "output": []any{}, "tools": []any{}, "instructions": nil}}})
		p.emit(t, map[string]any{"type": "response.event", "delegation_id": "delegation-" + id, "event": map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "function_call", "call_id": id, "name": name, "arguments": string(b)}}})
		p.emit(t, map[string]any{"type": "response.event", "delegation_id": "delegation-" + id, "event": map[string]any{"type": "response.completed", "response": map[string]any{"id": "response-" + id, "status": "completed", "output": []any{}, "tools": []any{}, "instructions": nil}}})
	default:
		p.emit(t, map[string]any{"type": "response.function_call_arguments.done", "call_id": id, "name": name, "arguments": string(b)})
		p.emit(t, map[string]any{"type": "response.done", "response": map[string]any{"status": "completed"}})
	}
}
func (p *vpPeer) result(t *testing.T, id string) map[string]any {
	t.Helper()
	if p.provider == "chatgpt-plan" {
		var result map[string]any
		p.await(t, "v3 delegation result "+id, func(v map[string]any) bool {
			if v["type"] != "delegation.context.append" || v["delegation_item_id"] != id {
				return false
			}
			var text strings.Builder
			for _, frame := range p.snapshot() {
				if frame["type"] == "delegation.context.append" && frame["delegation_item_id"] == id {
					content, _ := frame["content"].([]any)
					for _, raw := range content {
						part, _ := raw.(map[string]any)
						s, _ := part["text"].(string)
						text.WriteString(s)
					}
				}
			}
			return json.Unmarshal([]byte(text.String()), &result) == nil && result != nil
		})
		return result
	}
	v := p.await(t, "upstream result "+id, func(v map[string]any) bool {
		typ := "conversation.item.create"
		if p.provider == "openai-live-api" {
			typ = "response.item.create"
		}
		i, _ := v["item"].(map[string]any)
		return v["type"] == typ && i["type"] == "function_call_output" && i["call_id"] == id
	})
	i := v["item"].(map[string]any)
	s, ok := i["output"].(string)
	var result map[string]any
	if !ok || json.Unmarshal([]byte(s), &result) != nil {
		t.Fatalf("tool result is not a JSON object: %#v", i)
	}
	return result
}
func vpError(v map[string]any) bool {
	return v["error"] != nil || v["is_error"] == true || v["status"] == "error" || v["status"] == "rejected"
}
func (p *vpPeer) user(t *testing.T, id, text string) {
	if p.provider == "chatgpt-plan" {
		p.emit(t, map[string]any{"type": "input_transcript.added", "item": map[string]any{"text": text}})
		p.emit(t, map[string]any{"type": "turn.done", "turn": map[string]any{"id": id, "role": "user", "transcript": text}})
		return
	}
	if p.provider == "openai-live-api" {
		p.liveTranscript(t, "input", id, text)
		return
	}
	p.emit(t, map[string]any{"type": "input_audio_buffer.speech_started", "item_id": id})
	p.emit(t, map[string]any{"type": "input_audio_buffer.speech_stopped", "item_id": id})
	p.emit(t, map[string]any{"type": "conversation.item.input_audio_transcription.completed", "item_id": id, "transcript": text})
}
func (p *vpPeer) assistant(t *testing.T, id, text string, final bool) {
	if p.provider == "chatgpt-plan" {
		p.emit(t, map[string]any{"type": "output_transcript.added", "item": map[string]any{"text": text}})
		if final {
			p.emit(t, map[string]any{"type": "turn.done", "turn": map[string]any{"id": id, "role": "assistant", "transcript": text}})
		}
		return
	}
	if p.provider == "openai-live-api" {
		// Live has timed fragments, NO completed-turn event or is_final field.
		// Never turn the caller's final flag into a fabricated provider event.
		p.liveTranscript(t, "output", id, text)
		return
	}
	if final {
		p.emit(t, map[string]any{"type": "response.output_audio_transcript.done", "item_id": id, "transcript": text})
		p.emit(t, map[string]any{"type": "response.done", "response": map[string]any{"status": "completed"}})
	} else {
		p.emit(t, map[string]any{"type": "response.output_audio_transcript.delta", "item_id": id, "delta": text})
	}
}

// https://developers.openai.com/api/docs/guides/live-conversations,
// "Transcript deltas", fetched 2026-10-15. Timestamps describe intervals,
// not completed turns; delivery gaps do not prove the speaker stopped.
func (p *vpPeer) liveTranscript(t *testing.T, side, id, text string) {
	p.mu.Lock()
	start := p.timeline
	p.timeline += 200
	end := p.timeline
	p.mu.Unlock()
	p.emit(t, map[string]any{"type": "session." + side + "_transcript.delta", "event_id": id, "delta": text, "start_ms": start, "end_ms": end})
}
func (p *vpPeer) audio(t *testing.T, pcm []byte) {
	if p.rtc != nil {
		ctx, cancel := context.WithTimeout(context.Background(), vpDeadline)
		defer cancel()
		if err := p.rtc.SendPCM(ctx, pcm); err != nil {
			t.Fatal(err)
		}
		return
	}
	typ := "response.output_audio.delta"
	if p.provider == "openai-live-api" {
		typ = "session.output_audio.delta"
	}
	p.emit(t, map[string]any{"type": typ, "delta": base64.StdEncoding.EncodeToString(pcm)})
}

type vpFixture struct {
	t                *testing.T
	mgr              *Manager
	srv              *httptest.Server
	handler          http.Handler
	up               *vpUpstream
	store            *auth.Store
	prov             *mockProvider
	voiceText        *vpVoiceText
	backendMu        sync.Mutex
	backendPlans     []auth.CredentialSnapshot // plan each handoff backend was bound to
	sess             *ManagedSession
	root, provider   string
	log, clientBytes vpBuffer
	limits           vpLimits
	created          bool
	harnessOnly      bool
}

func vpDefaultLimits() vpLimits {
	return vpLimits{grace: 100 * time.Millisecond, maximum: time.Hour, minutes: 100 * time.Millisecond, teardown: 100 * time.Millisecond, bufferedBytes: 48_000, briefChars: 1024}
}

func vpNewFixture(t *testing.T, provider string, handlers ...mockHandler) *vpFixture {
	return vpNewFixtureWithLimits(t, provider, vpDefaultLimits(), handlers...)
}

func vpNewFixtureWithLimits(t *testing.T, provider string, limits vpLimits, handlers ...mockHandler) *vpFixture {
	t.Helper()
	clearProviderTestEnv(t)
	root := t.TempDir()
	t.Setenv("MOA_CONFIG_DIR", filepath.Join(root, "config"))
	f := &vpFixture{t: t, root: root, provider: provider, limits: limits}
	t.Cleanup(func() {
		if !f.created && !f.harnessOnly {
			t.Log("RED_COVERAGE: no call created; post-create invariants/stimuli unexecuted")
		}
	})
	f.up = vpNewUpstream(t)
	f.store = auth.NewStoreWithHTTPClient(filepath.Join(root, "auth.json"), f.up.client())
	c := auth.Credential{Type: "oauth", Access: "VP-ACCESS-OLD", Refresh: "VP-REFRESH-OLD", Expires: time.Now().Add(time.Hour).UnixMilli()}
	providerID := "xai"
	if strings.HasPrefix(provider, "chatgpt") || strings.HasPrefix(provider, "openai") {
		providerID = "openai"
		c.Access = providerTestJWT("vp-account", "VP-ACCESS-OLD")
		c.AccountID = "vp-account"
	}
	if provider == "xai-api" || provider == "openai-live-api" {
		c = auth.Credential{Type: "api_key", Key: "VP-KEY-" + providerID}
	}
	if _, err := f.store.CommitLogin(providerID, "", c); err != nil {
		t.Fatal(err)
	}
	cfg := core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off"}
	f.prov = newMockProvider(handlers...)
	f.voiceText = &vpVoiceText{fallback: f.prov, scripts: map[string]vpVoiceTool{}}
	f.up.backend = f.voiceText
	if err := os.MkdirAll(filepath.Join(root, "workspace"), 0o700); err != nil {
		t.Fatal(err)
	}
	f.mgr = newTestManagerWithConfig(t, context.Background(), f.voiceText, filepath.Join(root, "workspace"), cfg)
	if _, err := f.mgr.tasks.Create(context.Background(), tasks.CreateInput{Title: "voice fixture control", Place: tasks.PlaceBacklog, ProjectKey: "voice-tests"}); err != nil {
		t.Fatal(err)
	}
	if f.mgr.attachStore == nil {
		t.Fatal("real attachment store unavailable")
	}
	var err error
	f.sess, err = f.mgr.CreateSession(CreateOpts{Title: "voice test"})
	if err != nil {
		t.Fatal(err)
	}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&f.log, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	f.start()
	t.Cleanup(func() { f.srv.Close() })
	return f
}
func (f *vpFixture) start() {
	logins := auth.NewProviderLoginManagerWithHTTPClient(context.Background(), f.store, f.up.client())
	f.t.Cleanup(logins.Close)
	backend := func(_ core.Model, plan auth.CredentialSnapshot) (core.Provider, error) {
		f.backendMu.Lock()
		f.backendPlans = append(f.backendPlans, plan)
		f.backendMu.Unlock()
		return f.voiceText, nil
	}
	opts := append(vpServerOptions(f.store, logins, f.up.client(), f.limits, backend), WithAuthToken("vp-owner-token", false), WithDeviceStorePath(filepath.Join(f.root, "devices.json")))
	f.handler = NewServer(f.mgr, opts...)
	f.srv = httptest.NewServer(f.handler)
}
func (f *vpFixture) owner() http.Header {
	return http.Header{"Cookie": {authCookieName + "=vp-owner-token"}, "Origin": {f.srv.URL}}
}
func (f *vpFixture) request(method, path string, body any, headers http.Header) (int, []byte, http.Header, error) {
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	r, e := http.NewRequest(method, f.srv.URL+path, bytes.NewReader(b))
	if e != nil {
		return 0, nil, nil, e
	}
	r.Header = headers.Clone()
	r.Header.Set("Content-Type", "application/json")
	if method != "GET" {
		r.Header.Set("X-Moa-Request", "1")
	}
	client := &http.Client{Timeout: vpDeadline, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, e := client.Do(r)
	if e != nil {
		return 0, nil, nil, e
	}
	defer resp.Body.Close() //nolint:errcheck
	data, e := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	_, _ = f.clientBytes.Write([]byte(vpJSON(resp.Header)))
	_, _ = f.clientBytes.Write(data)
	return resp.StatusCode, data, resp.Header, e
}
func (f *vpFixture) mustRequest(method, path string, body any, headers http.Header, want int) []byte {
	f.t.Helper()
	s, b, _, e := f.request(method, path, body, headers)
	if e != nil {
		f.t.Fatal(e)
	}
	if s != want {
		if method == http.MethodPost && path == "/api/voice/calls" && s == http.StatusMethodNotAllowed {
			f.t.Fatalf("RED_STAGE=entry_unavailable: %s %s = %d, want %d; post-create behavior unexecuted: %s", method, path, s, want, b)
		}
		f.t.Fatalf("%s %s = %d, want %d: %s", method, path, s, want, b)
	}
	return b
}

type vpCreated struct {
	ID       string `json:"call_id"`
	Provider string `json:"provider"`
	WSURL    string `json:"ws_url"`
	Billing  struct {
		Kind  string  `json:"kind"`
		Price float64 `json:"usd_per_minute"`
	} `json:"billing"`
}

func (f *vpFixture) createFor(sid, provider string, headers http.Header) vpCreated {
	f.t.Helper()
	raw := f.mustRequest("POST", "/api/voice/calls", map[string]any{"session_id": sid, "provider": provider}, headers, 201)
	var c vpCreated
	if e := json.Unmarshal(raw, &c); e != nil {
		f.t.Fatal(e)
	}
	if c.ID == "" || c.Provider != provider || c.WSURL == "" {
		f.t.Fatalf("invalid created call: %s", raw)
	}
	u, e := url.Parse(c.WSURL)
	if e != nil {
		f.t.Fatal(e)
	}
	base, _ := url.Parse(f.srv.URL)
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Host != "" && u.Host != base.Host) || u.Path != "/api/voice/calls/"+c.ID+"/ws" {
		f.t.Fatalf("WS must be on moa without URL credentials: %q", c.WSURL)
	}
	f.created = true
	f.t.Log("RED_STAGE=create_reached; subsequent stimuli/assertions eligible")
	return c
}
func (f *vpFixture) create() vpCreated {
	f.t.Helper()
	return f.createFor(f.sess.ID, f.provider, f.owner())
}

type vpFrame struct {
	kind websocket.MessageType
	data []byte
}
type vpCall struct {
	f      *vpFixture
	c      vpCreated
	conn   *websocket.Conn
	mu     sync.Mutex
	frames []vpFrame
	done   chan struct{}
}

func (f *vpFixture) dial(c vpCreated, headers http.Header, read bool) *vpCall {
	f.t.Helper()
	u, _ := url.Parse(c.WSURL)
	if u.Host == "" {
		u, _ = url.Parse(f.srv.URL + c.WSURL)
	}
	ctx, cancel := context.WithTimeout(context.Background(), vpDeadline)
	defer cancel()
	conn, resp, e := websocket.Dial(ctx, u.String(), &websocket.DialOptions{HTTPHeader: headers})
	if resp != nil {
		_, _ = f.clientBytes.Write([]byte(vpJSON(resp.Header)))
	}
	if e != nil {
		s := 0
		if resp != nil {
			s = resp.StatusCode
		}
		f.t.Fatalf("call upgrade = %d: %v", s, e)
	}
	out := &vpCall{f: f, c: c, conn: conn, done: make(chan struct{})}
	f.t.Cleanup(func() {
		_ = conn.CloseNow()
		if read {
			select {
			case <-out.done:
			case <-time.After(vpDeadline):
				f.t.Error("test WS reader did not terminate")
			}
		}
	})
	if read {
		go func() {
			defer close(out.done)
			for {
				kind, b, e := conn.Read(context.Background())
				if e != nil {
					return
				}
				_, _ = f.clientBytes.Write(b)
				out.mu.Lock()
				if len(out.frames) < 2048 {
					out.frames = append(out.frames, vpFrame{kind, b})
				}
				out.mu.Unlock()
			}
		}()
	}
	return out
}
func (f *vpFixture) call() *vpCall {
	f.t.Helper()
	c := f.dial(f.create(), f.owner(), true)
	c.await("listening state", func(v map[string]any) bool { return v["type"] == "state" && v["state"] == "listening" })
	return c
}
func (c *vpCall) snapshot() []vpFrame {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]vpFrame(nil), c.frames...)
}
func (c *vpCall) await(desc string, pred func(map[string]any) bool) map[string]any {
	c.f.t.Helper()
	var out map[string]any
	pollUntil(c.f.t, vpDeadline, desc, func() bool {
		for _, f := range c.snapshot() {
			var v map[string]any
			if f.kind == websocket.MessageText && json.Unmarshal(f.data, &v) == nil && pred(v) {
				out = v
				return true
			}
		}
		return false
	})
	return out
}
func (c *vpCall) send(v any) {
	c.f.t.Helper()
	b, _ := json.Marshal(v)
	ctx, cancel := context.WithTimeout(context.Background(), vpDeadline)
	defer cancel()
	if e := c.conn.Write(ctx, websocket.MessageText, b); e != nil {
		c.f.t.Fatal(e)
	}
}
func (c *vpCall) hangup() {
	c.f.t.Helper()
	c.f.mustRequest("POST", "/api/voice/calls/"+c.c.ID+"/hangup", map[string]any{}, c.f.owner(), 204)
}
func vpClosed(t *testing.T, p *vpPeer) {
	t.Helper()
	vpClosedWithin(t, p, vpDeadline)
}
func vpClosedWithin(t *testing.T, p *vpPeer, budget time.Duration) {
	t.Helper()
	deadline := time.NewTimer(budget)
	defer deadline.Stop()
	select {
	case <-p.closed:
	case <-deadline.C:
		t.Fatal("moa did not close upstream before deadline; cleanup has not run")
	}
	p.mu.Lock()
	done := p.sidebandClosed
	p.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-deadline.C:
			t.Fatal("peer closed but authenticated control socket still open")
		}
	}
}
func vpClosedBy(t *testing.T, p *vpPeer, start time.Time, budget time.Duration) {
	t.Helper()
	left := budget - time.Since(start)
	if left <= 0 {
		t.Fatalf("close operation exceeded %s before the transport observer ran", budget)
	}
	vpClosedWithin(t, p, left)
}
func vpNoSecrets(t *testing.T, s string) {
	t.Helper()
	for _, v := range []string{"VP-ACCESS-OLD", "VP-ACCESS-ROTATED", "VP-REFRESH-OLD", "VP-REFRESH-ROTATED", "VP-KEY-", "VP-UPSTREAM-CALL", "VP-SDP", "VP-ICE"} {
		if strings.Contains(s, v) {
			t.Errorf("secret escaped: %q", v)
		}
	}
}
func vpJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
func vpPCM() []byte {
	b := make([]byte, 960)
	for i := 0; i < len(b)/2; i++ {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(int16((i%48-24)*700)))
	}
	return b
}
func (f *vpFixture) records(kind string) []core.AgentMessage {
	var out []core.AgentMessage
	for _, m := range f.sess.History() {
		if m.Custom["source"] == "voice_call" && m.Custom["kind"] == kind {
			out = append(out, m)
		}
	}
	return out
}
func (f *vpFixture) waitRecords(kind string, n int) []core.AgentMessage {
	f.t.Helper()
	var out []core.AgentMessage
	pollUntil(f.t, vpDeadline, fmt.Sprintf("%d actual %s records", n, kind), func() bool { out = f.records(kind); return len(out) == n })
	return out
}
func (f *vpFixture) saved() *session.Session {
	f.t.Helper()
	if e := f.sess.runtime.Flush(); e != nil {
		f.t.Fatal(e)
	}
	s, _, e := session.FindSessionReadOnly(f.mgr.sessionBaseDir, f.sess.ID)
	if e != nil {
		f.t.Fatal(e)
	}
	return s
}
func (f *vpFixture) appendUser(text string) {
	f.t.Helper()
	if e := f.sess.runtime.Bus.Execute(bus.AppendToConversation{Message: core.WrapMessage(core.NewUserMessage(text))}); e != nil {
		f.t.Fatal(e)
	}
}
func vpStable(t *testing.T, fn func() bool, why string) {
	t.Helper()
	end := time.Now().Add(150 * time.Millisecond)
	for time.Now().Before(end) {
		if !fn() {
			t.Fatal(why)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
