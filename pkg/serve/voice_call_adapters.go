package serve

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/e-aleixandre/moa/pkg/auth"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/voice"
	"github.com/pion/webrtc/v4"
)

// VoiceCallLimits are the technical budgets of a voice call. They are
// resource bounds injectable for tests, not product settings.
type VoiceCallLimits struct {
	// Grace is how long a call survives without a connected client.
	Grace time.Duration
	// Maximum is the hard duration of a call.
	Maximum time.Duration
	// Minutes is how long ending waits for the model's minutes.
	Minutes time.Duration
	// Teardown bounds closing the upstream.
	Teardown time.Duration
	// BufferedBytes bounds the audio queued towards the client.
	BufferedBytes int
	// BriefChars bounds, in Unicode characters, the fallback context a call
	// starts with and the brief injected later.
	BriefChars int
}

func (l VoiceCallLimits) withDefaults() VoiceCallLimits {
	d := VoiceCallLimits{Grace: 10 * time.Second, Maximum: 60 * time.Minute, Minutes: 8 * time.Second, Teardown: 2 * time.Second, BufferedBytes: voice.MaxPendingAudio, BriefChars: 8000}
	if l.Grace > 0 {
		d.Grace = l.Grace
	}
	if l.Maximum > 0 {
		d.Maximum = l.Maximum
	}
	if l.Minutes > 0 {
		d.Minutes = l.Minutes
	}
	if l.Teardown > 0 {
		d.Teardown = l.Teardown
	}
	if l.BufferedBytes > 0 {
		d.BufferedBytes = l.BufferedBytes
	}
	if l.BriefChars > 0 {
		d.BriefChars = l.BriefChars
	}
	return d
}

// VoiceCallOptions injects transport for voice calls. Production leaves it
// zero; tests route providers to local fakes and keep ICE on loopback.
type VoiceCallOptions struct {
	HTTPClient *http.Client
	Limits     VoiceCallLimits
	// PeerSettings and ICEServers configure the ChatGPT-plan WebRTC peer.
	// nil ICEServers selects the peer's public STUN default.
	PeerSettings *webrtc.SettingEngine
	ICEServers   []webrtc.ICEServer
}

// WithVoiceCalls overrides the voice call transport and budgets.
func WithVoiceCalls(o VoiceCallOptions) ServerOption {
	return func(so *serverOptions) { so.voice = o }
}

// voiceBackendModel is the ChatGPT-plan handoff and Live delegation backend
// until voice.backend_model is configurable.
const voiceBackendModel = "gpt-6-sol"

var voiceProviders = map[voice.ProviderID]struct {
	store   string
	voice   string
	billing voice.Billing
}{
	voice.GrokPlan:      {"xai", "eve", voice.Billing{Kind: "plan_unverified"}},
	voice.XAIAPI:        {"xai", "eve", voice.Billing{Kind: "per_minute", USDPerMinute: 0.08}},
	voice.ChatGPTPlan:   {"openai", "cove", voice.Billing{Kind: "plan_unverified"}},
	voice.OpenAILiveAPI: {"openai", "marin", voice.Billing{Kind: "per_minute", USDPerMinute: 0.05}},
}

// pairedPaid is the explicit paid choice offered when a plan fails.
var pairedPaid = map[voice.ProviderID]voice.ProviderID{voice.GrokPlan: voice.XAIAPI, voice.ChatGPTPlan: voice.OpenAILiveAPI}

func voiceSelection(p voice.ProviderID) auth.SnapshotSelection {
	if p.Paid() {
		return auth.SelectVoiceAPIKey
	}
	return auth.SelectPlanOAuth
}

// storeVoiceCredentials narrows the Store to one provider and selection for
// one call. It remembers the snapshot it resolved so a refresh rotates
// exactly that credential, shared with any concurrent text refresh.
type storeVoiceCredentials struct {
	store    *auth.Store
	provider string
	sel      auth.SnapshotSelection
	mu       sync.Mutex
	last     auth.CredentialSnapshot
}

var errVoiceCredential = errors.New("voice credential unavailable")

func (s *storeVoiceCredentials) accept(snap auth.CredentialSnapshot) (voice.Credential, error) {
	want := "oauth"
	if s.sel == auth.SelectVoiceAPIKey {
		want = "api_key"
	}
	if snap.Kind != want || snap.Token == "" {
		return voice.Credential{}, errVoiceCredential
	}
	s.mu.Lock()
	s.last = snap
	s.mu.Unlock()
	return voice.Credential{Token: snap.Token, AccountID: snap.AccountID}, nil
}

func (s *storeVoiceCredentials) Resolve(ctx context.Context) (voice.Credential, error) {
	snap, err := s.store.ResolveSnapshot(ctx, s.provider, s.sel)
	if err != nil {
		return voice.Credential{}, err
	}
	return s.accept(snap)
}

func (s *storeVoiceCredentials) Refresh(ctx context.Context, rejected voice.Credential) (voice.Credential, error) {
	if s.sel != auth.SelectPlanOAuth {
		return voice.Credential{}, errVoiceCredential
	}
	s.mu.Lock()
	snap := s.last
	s.mu.Unlock()
	next, err := s.store.RefreshOAuthIfGeneration(ctx, snap, rejected.Token)
	if err != nil {
		return voice.Credential{}, err
	}
	return s.accept(next)
}

// adapter builds the adapter of p with a credential source of its own.
func (r *voiceCallRegistry) adapter(p voice.ProviderID) (voice.Adapter, bool) {
	info, ok := voiceProviders[p]
	if !ok || r.store == nil {
		return nil, false
	}
	creds := &storeVoiceCredentials{store: r.store, provider: info.store, sel: voiceSelection(p)}
	switch p {
	case voice.GrokPlan, voice.XAIAPI:
		return voice.NewRealtimeWS(voice.RealtimeWSConfig{ID: p, HTTPClient: r.opts.HTTPClient, Credentials: creds, OAuth: p == voice.GrokPlan, Billing: info.billing}), true
	case voice.OpenAILiveAPI:
		return voice.NewLiveWS(voice.LiveWSConfig{HTTPClient: r.opts.HTTPClient, Credentials: creds, Billing: info.billing}), true
	case voice.ChatGPTPlan:
		return voice.NewChatGPTPlan(voice.ChatGPTPlanConfig{HTTPClient: r.opts.HTTPClient, Credentials: creds, Billing: info.billing,
			Peer: voice.PeerConfig{ICEServers: r.opts.ICEServers, SettingEngine: r.opts.PeerSettings}}), true
	}
	return nil, false
}

// selectable reports, without network or refresh, whether p's credential
// kind is present in the Store.
func (r *voiceCallRegistry) selectable(p voice.ProviderID) bool {
	info, ok := voiceProviders[p]
	if !ok || r.store == nil {
		return false
	}
	snap, err := r.store.PeekSnapshot(info.store, voiceSelection(p))
	if err != nil {
		return false
	}
	if p.Paid() {
		return snap.Kind == "api_key"
	}
	return snap.Kind == "oauth"
}

// defaultPlan picks a plan provider when the caller named none: ChatGPT
// first (full duplex), then Grok. It never picks a paid provider: omitting
// provider is not consent to pay.
func (r *voiceCallRegistry) defaultPlan() (voice.ProviderID, bool) {
	for _, p := range []voice.ProviderID{voice.ChatGPTPlan, voice.GrokPlan} {
		if r.selectable(p) {
			return p, true
		}
	}
	return "", false
}

func voiceModel() core.Model {
	if m, ok := core.ResolveModel(voiceBackendModel); ok {
		return m
	}
	return core.Model{ID: voiceBackendModel, Provider: "openai"}
}
