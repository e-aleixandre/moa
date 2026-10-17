package serve

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/voice"
)

// voiceProviderOrder is how Settings lists the providers: plans first.
var voiceProviderOrder = []voice.ProviderID{voice.ChatGPTPlan, voice.GrokPlan, voice.OpenAILiveAPI, voice.XAIAPI}

type voiceProviderStatus struct {
	ID      voice.ProviderID `json:"id"`
	Paid    bool             `json:"paid"`
	State   string           `json:"state"` // ready | not_connected
	Billing voice.Billing    `json:"billing"`
}

type voiceStatus struct {
	Provider     string                `json:"provider"`
	PaidProvider string                `json:"paid_provider"`
	BackendModel string                `json:"backend_model"`
	Default      voice.ProviderID      `json:"default"` // plan an unnamed call uses now; "" when none is signed in
	Providers    []voiceProviderStatus `json:"providers"`
	ActiveCalls  int                   `json:"active_calls"`
	Recent       []voiceUsage          `json:"recent"` // newest first
	// Calls are the owner-call settings; OwnerCallsPaidProvider is the paid
	// provider whose cost activating an owner needs accepted when no plan is
	// signed in ("" with a plan, or with no paid key at all).
	Calls                  core.VoiceCallsConfig `json:"calls"`
	OwnerCallsPaidProvider voice.ProviderID      `json:"owner_calls_paid_provider,omitempty"`
}

// voiceRecentCalls bounds the usage kept in memory for Settings.
const voiceRecentCalls = 20

// voiceUsage is what one call measured. Every figure is either the
// provider's own report or moa's measurement; there is no price here that
// nobody published. SessionCostDeltaUSD is how much the called session's
// text spend changed while the call was open: it includes anything else that
// session ran meanwhile, so it is a delta, not the call's cost.
type voiceUsage struct {
	CallID           string            `json:"call_id"`
	SessionID        string            `json:"session_id"`
	Provider         voice.ProviderID  `json:"provider"`
	Billing          string            `json:"billing"`
	EndedAt          int64             `json:"ended_at"`
	Reason           string            `json:"reason,omitempty"`
	Diagnostic       *voice.Diagnostic `json:"diagnostic,omitempty"`
	Seconds          int               `json:"seconds"`                    // wall clock, opened → ended
	ProviderSeconds  *float64          `json:"provider_seconds,omitempty"` // as the provider reported it
	InputTokens      *int64            `json:"input_tokens,omitempty"`
	OutputTokens     *int64            `json:"output_tokens,omitempty"`
	TotalTokens      *int64            `json:"total_tokens,omitempty"`
	Deliveries       int               `json:"deliveries"`
	Asks             int               `json:"asks"`
	AskLatencyMsMax  int64             `json:"ask_latency_ms_max,omitempty"`
	AskLatencyMsMean int64             `json:"ask_latency_ms_mean,omitempty"`
	// Speech latency: the user stops talking (provider's speech_stopped) →
	// first audio back, as moa saw it. Absent when the provider reports no
	// speech_stopped; zero would claim an instant answer.
	SpeechLatencyCount int   `json:"speech_latency_count,omitempty"`
	SpeechLatencyMsP50 int64 `json:"speech_latency_ms_p50,omitempty"`
	SpeechLatencyMsMax int64 `json:"speech_latency_ms_max,omitempty"`
	// Turn-end latency (ChatGPT plan): the user's turn.done → first audio
	// back. A different reference from the speech latency above (it includes
	// the provider's transcription), so it has its own fields.
	TurnEndLatencyCount int     `json:"turn_end_latency_count,omitempty"`
	TurnEndLatencyMsP50 int64   `json:"turn_end_latency_ms_p50,omitempty"`
	TurnEndLatencyMsMax int64   `json:"turn_end_latency_ms_max,omitempty"`
	SessionCostDeltaUSD float64 `json:"session_cost_delta_usd"`
}

func sessionCostUSD(sess *ManagedSession) float64 {
	if sess == nil || sess.runtime == nil || sess.runtime.Bus == nil {
		return 0
	}
	cost, _ := bus.QueryTyped[bus.GetSessionCost, float64](sess.runtime.Bus, bus.GetSessionCost{})
	return cost
}

func (c *voiceCall) usageRecord(sess *ManagedSession, seconds, deliveries int) voiceUsage {
	c.mu.Lock()
	asks, lat := c.asks, append([]time.Duration(nil), c.askLatency...)
	speech := append([]time.Duration(nil), c.speechLat...)
	turn := append([]time.Duration(nil), c.turnLat...)
	c.mu.Unlock()
	u := voiceUsage{CallID: c.id, SessionID: c.sessionID, Provider: c.provider, Billing: c.billing.Kind,
		EndedAt: time.Now().Unix(), Seconds: seconds, Deliveries: deliveries, Asks: asks,
		SessionCostDeltaUSD: max(sessionCostUSD(sess)-c.costStart, 0)}
	if c.usage != nil {
		u.ProviderSeconds, u.InputTokens, u.OutputTokens, u.TotalTokens = c.usage.Seconds, c.usage.InputTokens, c.usage.OutputTokens, c.usage.TotalTokens
	}
	if len(lat) > 0 {
		var sum time.Duration
		for _, d := range lat {
			sum += d
			u.AskLatencyMsMax = max(u.AskLatencyMsMax, d.Milliseconds())
		}
		u.AskLatencyMsMean = (sum / time.Duration(len(lat))).Milliseconds()
	}
	if len(speech) > 0 {
		sort.Slice(speech, func(i, j int) bool { return speech[i] < speech[j] })
		u.SpeechLatencyCount = len(speech)
		u.SpeechLatencyMsP50 = speech[(len(speech)-1)/2].Milliseconds()
		u.SpeechLatencyMsMax = speech[len(speech)-1].Milliseconds()
	}
	if len(turn) > 0 {
		sort.Slice(turn, func(i, j int) bool { return turn[i] < turn[j] })
		u.TurnEndLatencyCount = len(turn)
		u.TurnEndLatencyMsP50 = turn[(len(turn)-1)/2].Milliseconds()
		u.TurnEndLatencyMsMax = turn[len(turn)-1].Milliseconds()
	}
	return u
}

func (r *voiceCallRegistry) recordUsage(u voiceUsage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recent = append(r.recent, u)
	if n := len(r.recent) - voiceRecentCalls; n > 0 {
		r.recent = append([]voiceUsage(nil), r.recent[n:]...)
	}
}

func (r *voiceCallRegistry) recentUsage() []voiceUsage {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]voiceUsage, 0, len(r.recent))
	for i := len(r.recent) - 1; i >= 0; i-- {
		out = append(out, r.recent[i])
	}
	return out
}

// activeCalls counts calls still running, including those ending.
func (r *voiceCallRegistry) activeCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// ActiveVoiceCalls is what a release preflight reports beside running
// sessions and jobs: a restart ends every one of these calls.
func (m *Manager) ActiveVoiceCalls() int {
	m.voiceCallsMu.Lock()
	regs := append([]*voiceCallRegistry(nil), m.voiceCalls...)
	m.voiceCallsMu.Unlock()
	n := 0
	for _, r := range regs {
		n += r.activeCalls()
	}
	return n
}

// status is safe to show: availability is the credential kind's presence,
// never an account, token or upstream detail.
func (r *voiceCallRegistry) status() voiceStatus {
	cfg := voiceSettings()
	out := voiceStatus{Provider: cfg.Provider, PaidProvider: cfg.PaidProvider, BackendModel: cfg.BackendModel,
		Providers: []voiceProviderStatus{}, ActiveCalls: r.mgr.ActiveVoiceCalls(), Recent: r.recentUsage(), Calls: cfg.Calls}
	if !r.mgr.voicePlanSignedIn() {
		out.OwnerCallsPaidProvider = r.mgr.ownerCallsPaidProvider()
	}
	if p, ok := r.defaultPlan(); ok {
		out.Default = p
	}
	for _, p := range voiceProviderOrder {
		state := "not_connected"
		if r.selectable(p) {
			state = "ready"
		}
		out.Providers = append(out.Providers, voiceProviderStatus{ID: p, Paid: p.Paid(), State: state, Billing: voiceProviders[p].billing})
	}
	return out
}

// handleStatus is GET /api/voice/status.
func (r *voiceCallRegistry) handleStatus(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, r.status())
}

// handleSettings is PATCH /api/voice/settings. Each field present replaces
// the saved one; "" restores its default. Choosing a paid provider here only
// changes which paid option is offered: it never starts a paid call.
func (r *voiceCallRegistry) handleSettings(w http.ResponseWriter, req *http.Request) {
	var in struct {
		Provider     *string `json:"provider"`
		PaidProvider *string `json:"paid_provider"`
		BackendModel *string `json:"backend_model"`
		// Calls replaces the owner-call settings whole.
		Calls *core.VoiceCallsConfig `json:"calls"`
	}
	if err := json.NewDecoder(io.LimitReader(req.Body, maxJSONBodySize)).Decode(&in); err != nil {
		voiceError(w, http.StatusBadRequest, map[string]any{"error": "invalid_request"})
		return
	}
	trim := func(p *string) *string {
		if p == nil {
			return nil
		}
		v := strings.TrimSpace(*p)
		return &v
	}
	provider, paid, model := trim(in.Provider), trim(in.PaidProvider), trim(in.BackendModel)
	if provider != nil && *provider != "" && *provider != string(voice.ChatGPTPlan) && *provider != string(voice.GrokPlan) {
		voiceError(w, http.StatusBadRequest, map[string]any{"error": "invalid_provider"})
		return
	}
	if paid != nil && *paid != "" && !voice.ProviderID(*paid).Paid() {
		voiceError(w, http.StatusBadRequest, map[string]any{"error": "invalid_paid_provider"})
		return
	}
	if model != nil && *model != "" {
		m, ok := core.ResolveModel(*model)
		if !ok {
			voiceError(w, http.StatusBadRequest, map[string]any{"error": "unknown_model"})
			return
		}
		// The handoff is bound to the ChatGPT plan's OAuth sign-in
		// (newVoiceBackendFactory refuses any other provider), so a model of
		// another provider would fail every call after being saved.
		if m.Provider != "openai" {
			voiceError(w, http.StatusBadRequest, map[string]any{"error": "unsupported_model"})
			return
		}
	}
	if in.Calls != nil {
		if code := validCallsSettings(*in.Calls); code != "" {
			voiceError(w, http.StatusBadRequest, map[string]any{"error": code})
			return
		}
	}
	r.mgr.configMutationMu.Lock()
	err := core.SaveGlobalConfig(func(cfg *core.MoaConfig) {
		v := core.VoiceConfig{}
		if cfg.Voice != nil {
			v = *cfg.Voice
		}
		if provider != nil {
			v.Provider = *provider
		}
		if paid != nil {
			v.PaidProvider = *paid
		}
		if model != nil {
			v.BackendModel = *model
		}
		if in.Calls != nil {
			v.Calls = *in.Calls
		}
		if v.Provider == "" && v.PaidProvider == "" && v.BackendModel == "" && v.Calls.RingSeconds == 0 && v.Calls.PausedUntil == "" && v.Calls.Hours == nil {
			cfg.Voice = nil
		} else {
			cfg.Voice = &v
		}
	})
	r.mgr.configMutationMu.Unlock()
	if err != nil {
		voiceError(w, http.StatusInternalServerError, map[string]any{"error": "save_failed"})
		return
	}
	r.handleStatus(w, req)
}

// activity is what a restart would interrupt right now: runs in progress
// (including those waiting on a permission), live subagent and background
// jobs, and voice calls. A release preflight reads it before asking for a
// deploy; it is counts only.
type activity struct {
	SessionsRunning int `json:"sessions_running"`
	Jobs            int `json:"jobs"`
	VoiceCalls      int `json:"voice_calls"`
}

func (m *Manager) activity() activity {
	m.mu.RLock()
	sessions := make([]*ManagedSession, 0, len(m.sessions))
	for _, s := range m.sessions {
		if s != nil {
			sessions = append(sessions, s)
		}
	}
	m.mu.RUnlock()
	out := activity{VoiceCalls: m.ActiveVoiceCalls()}
	for _, s := range sessions {
		switch s.info().State {
		case StateRunning, StatePermission:
			out.SessionsRunning++
		}
		if s.runtime == nil || s.runtime.Bus == nil {
			continue
		}
		subs, _ := bus.QueryTyped[bus.GetSubagents, []bus.SubagentSnapshot](s.runtime.Bus, bus.GetSubagents{})
		out.Jobs += len(subs)
	}
	return out
}

// handleActivity is GET /api/activity.
func handleActivity(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, m.activity())
	}
}

// validCallsSettings returns the error code of an invalid calls block.
func validCallsSettings(c core.VoiceCallsConfig) string {
	if c.RingSeconds != 0 && (c.RingSeconds < 10 || c.RingSeconds > 120) {
		return "invalid_ring_seconds"
	}
	if c.PausedUntil != "" {
		if _, err := time.Parse(time.RFC3339, c.PausedUntil); err != nil {
			return "invalid_paused_until"
		}
	}
	if h := c.Hours; h != nil {
		_, ok1 := parseHHMM(h.From)
		_, ok2 := parseHHMM(h.Until)
		if _, err := time.LoadLocation(h.TZ); !ok1 || !ok2 || err != nil || h.TZ == "" || h.From == h.Until {
			return "invalid_hours"
		}
	}
	return ""
}
