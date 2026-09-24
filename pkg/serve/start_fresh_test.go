package serve

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

// recordingProvider answers every request and keeps a copy of what was sent,
// so a test can assert on the exact context a request carried.
type recordingProvider struct {
	mu   sync.Mutex
	reqs []core.Request
}

func (p *recordingProvider) Stream(_ context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	if isTestAutoTitleRequest(req) {
		return simpleResponse("title"), nil
	}
	p.mu.Lock()
	p.reqs = append(p.reqs, req)
	p.mu.Unlock()
	return simpleResponse("ok"), nil
}

func (p *recordingProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.reqs)
}

func (p *recordingProvider) last() core.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reqs[len(p.reqs)-1]
}

// newFreshTestManager is newTestManager with a model that has a context
// window: the cut is measured against it.
func newFreshTestManager(t *testing.T, ctx context.Context, provider core.Provider) *Manager {
	t.Helper()
	moaCfg := core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off"}
	mgr := NewManager(ctx, ManagerConfig{
		ProviderFactory: func(_ core.Model) (core.Provider, error) { return provider, nil },
		DefaultModel:    core.Model{ID: "claude-haiku-4-5-20251001", Provider: "anthropic", MaxInput: 200_000},
		WorkspaceRoot:   t.TempDir(),
		MoaCfg:          moaCfg,
		ConfigLoader:    isolatedTestConfigLoader(t, moaCfg),
		SessionBaseDir:  t.TempDir(),
		SchedulePath:    filepath.Join(t.TempDir(), "schedules.json"),
	})
	t.Cleanup(func() {
		mgr.mu.RLock()
		ids := make([]string, 0, len(mgr.sessions))
		for id := range mgr.sessions {
			ids = append(ids, id)
		}
		mgr.mu.RUnlock()
		for _, id := range ids {
			_ = mgr.Delete(id)
		}
		mgr.Shutdown()
	})
	return mgr
}

func requestTexts(req core.Request) []string {
	var out []string
	for _, m := range req.Messages {
		text := ""
		for _, c := range m.Content {
			text += c.Text
		}
		out = append(out, m.Role+":"+text)
	}
	return out
}

func sendAndWait(t *testing.T, mgr *Manager, sess *ManagedSession, text string) {
	t.Helper()
	if _, _, _, err := mgr.Send(sess.ID, text, nil, "", ""); err != nil {
		t.Fatalf("send %q: %v", text[:min(len(text), 20)], err)
	}
	pollUntil(t, 10*time.Second, "run finished", func() bool {
		return sessState(sess) == StateIdle && sess.runtime.Context().Agent.Messages()[len(sess.runtime.Context().Agent.Messages())-1].Role == "assistant"
	})
	sess.runtime.Bus.Drain(2 * time.Second)
}

// turn builds a user message big enough (~10k tokens) that a few of them
// exceed what a fresh start keeps (KeepRecent plus the summary reserve).
func turn(i int) string {
	return "turn-" + string(rune('A'+i)) + " " + strings.Repeat("x", 40_000)
}

func firstUserTag(req core.Request) string {
	for _, m := range req.Messages {
		if m.Role == "user" {
			for _, c := range m.Content {
				if strings.HasPrefix(c.Text, "turn-") {
					return c.Text[:6]
				}
			}
		}
	}
	return ""
}

func displayed(t *testing.T, sess *ManagedSession) []core.AgentMessage {
	t.Helper()
	msgs, err := bus.QueryTyped[bus.GetDisplayMessages, []core.AgentMessage](sess.runtime.Bus, bus.GetDisplayMessages{})
	if err != nil {
		t.Fatal(err)
	}
	return msgs
}

// Scenario: cut the old context after the cache expired, then restart, then
// compact on top of the cut.
func TestStartFresh_CutsModelContextKeepsTranscriptAndSurvivesRestart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prov := &recordingProvider{}
	mgr := newFreshTestManager(t, ctx, prov)
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 6 {
		sendAndWait(t, mgr, sess, turn(i))
	}
	beforePct := sess.info().ContextPercent
	callsBefore := prov.count()

	res, err := mgr.ExecCommand(sess.ID, "/start-fresh", "")
	if err != nil || !res.OK || res.Message != "started fresh" {
		t.Fatalf("start-fresh = %+v, %v", res, err)
	}
	sess.runtime.Bus.Drain(2 * time.Second)

	// No model was called to summarize.
	if got := prov.count(); got != callsBefore {
		t.Fatalf("provider calls = %d, want %d: starting fresh must not call a model", got, callsBefore)
	}

	// The transcript still has every turn, plus the marker right before the
	// first message the model still sees.
	shown := displayed(t, sess)
	var users []string
	markerAt, firstKeptAt := -1, -1
	var firstKept string
	for i, m := range shown {
		if m.Role == "user" {
			users = append(users, m.Content[0].Text[:6])
		}
		if m.Role == "session_event" && m.Custom["type"] == "fresh_marker" {
			markerAt = i
			firstKept, _ = m.Custom["first_kept_msg_id"].(string)
			if m.Content[0].Text != session.FreshMarkerText {
				t.Fatalf("marker text = %q", m.Content[0].Text)
			}
		}
		if firstKept != "" && m.MsgID == firstKept {
			firstKeptAt = i
		}
	}
	if len(users) != 6 {
		t.Fatalf("transcript users = %v, want all 6 turns", users)
	}
	if markerAt < 0 || firstKeptAt != markerAt+1 {
		t.Fatalf("marker at %d, first kept at %d: want the marker right before the cut", markerAt, firstKeptAt)
	}

	// The model's context now starts at the cut, as FindCutPoint decides.
	agentMsgs := sess.runtime.Context().Agent.Messages()
	if agentMsgs[0].MsgID != firstKept {
		t.Fatalf("agent context starts at %s, want %s", agentMsgs[0].MsgID, firstKept)
	}
	afterPct := sess.info().ContextPercent
	if afterPct >= beforePct {
		t.Fatalf("context percent %d -> %d, want it to drop", beforePct, afterPct)
	}

	sendAndWait(t, mgr, sess, "after the cut")
	req := prov.last()
	tag := firstUserTag(req)
	if tag == "" || tag == "turn-A" || tag == "turn-B" {
		t.Fatalf("next request starts at %q (%d msgs), want it to start at the cut", tag, len(req.Messages))
	}
	if req.Messages[0].Role != "user" && req.Messages[0].Role != "assistant" {
		t.Fatalf("request starts with %s", req.Messages[0].Role)
	}
	t.Logf("pct %d -> %d, cut starts at %s, req msgs %d, first role %s", beforePct, afterPct, tag, len(req.Messages), req.Messages[0].Role)
	cutTag := tag
	cutLen := len(req.Messages)

	// Restart: drop from memory, resume from disk.
	id := sess.ID
	mgr.mu.Lock()
	delete(mgr.sessions, id)
	mgr.mu.Unlock()
	sess.runtime.Close()
	resumed, err := mgr.ResumeSession(id)
	if err != nil {
		t.Fatal(err)
	}
	shown = displayed(t, resumed)
	markers := 0
	for _, m := range shown {
		if m.Custom["type"] == "fresh_marker" {
			markers++
		}
	}
	if markers != 1 {
		t.Fatalf("resumed transcript has %d fresh markers, want 1", markers)
	}
	sendAndWait(t, mgr, resumed, "after restart")
	req = prov.last()
	if got := firstUserTag(req); got != cutTag {
		t.Fatalf("after restart the request starts at %q, want %q", got, cutTag)
	}
	if len(req.Messages) != cutLen+2 {
		t.Fatalf("after restart the request has %d messages, want %d", len(req.Messages), cutLen+2)
	}

	// A compaction on top of a cut session summarizes only what the model
	// still sees.
	callsBefore = prov.count()
	res, err = mgr.ExecCommand(id, "/compact", "")
	if err != nil || !res.OK {
		t.Fatalf("compact = %+v, %v", res, err)
	}
	pollUntil(t, 10*time.Second, "compaction settled", func() bool {
		return prov.count() > callsBefore && sessState(resumed) == StateIdle
	})
	resumed.runtime.Bus.Drain(2 * time.Second)
	summarized := strings.Join(requestTexts(prov.last()), "\n")
	if strings.Contains(summarized, "turn-A") {
		t.Fatalf("compaction after a cut summarized pre-cut messages")
	}
	if n := len(resumed.runtime.Context().Agent.Messages()); n == 0 {
		t.Fatal("agent context empty after compaction")
	}
}

// Nothing to cut: a short conversation is left as it is.
func TestStartFresh_NothingToCut(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prov := &recordingProvider{}
	mgr := newFreshTestManager(t, ctx, prov)
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	sendAndWait(t, mgr, sess, "hello")
	before := len(sess.runtime.Context().Agent.Messages())
	res, err := mgr.ExecCommand(sess.ID, "/start-fresh", "")
	if err != nil || !res.OK || !strings.HasPrefix(res.Message, "nothing to cut") {
		t.Fatalf("start-fresh = %+v, %v", res, err)
	}
	if got := len(sess.runtime.Context().Agent.Messages()); got != before {
		t.Fatalf("messages %d -> %d", before, got)
	}
	for _, m := range displayed(t, sess) {
		if m.Custom["type"] == "fresh_marker" {
			t.Fatal("marker drawn for a no-op")
		}
	}
}

// While a run is in flight the action is refused.
func TestStartFresh_RefusedWhileBusy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newFreshTestManager(t, ctx, newMockProvider(delayedResponseHandler(2*time.Second, "slow")))
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := mgr.Send(sess.ID, "work", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 5*time.Second, "running", func() bool { return sessState(sess) == StateRunning })
	if _, err := mgr.ExecCommand(sess.ID, "/start-fresh", ""); err == nil {
		t.Fatal("start-fresh accepted while running")
	}
}
