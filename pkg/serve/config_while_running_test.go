package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

// Config PATCHes while a session runs are accepted: one while a provider
// request is in flight, one while a permission request is pending. The pending
// request stays pending until someone decides it, and the changes reach every
// provider request after the one in flight: model, thinking, requested-model
// history, cost, the gate's mode for the next tool call, and persistence.
func TestConfigPatchWhileRunning_AppliesAtNextRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	million := &core.Usage{Input: 1_000_000}
	var reqMu sync.Mutex
	var reqs []core.Request
	record := func(msg core.Message) mockHandler {
		return func(_ context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
			reqMu.Lock()
			reqs = append(reqs, req)
			reqMu.Unlock()
			ch := make(chan core.AssistantEvent, 2)
			m := msg
			m.Timestamp = time.Now().Unix()
			ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &m}
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &m}
			close(ch)
			return ch, nil
		}
	}
	bashTurn := func(id, command string) core.Message {
		return core.Message{
			Role:       "assistant",
			Content:    []core.Content{core.ToolCallContent(id, "bash", map[string]any{"command": command})},
			StopReason: "tool_use",
			Usage:      million,
		}
	}
	inFlight, release := make(chan struct{}), make(chan struct{})
	first := record(bashTurn("tc-1", "echo one"))
	prov := newMockProvider(
		func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
			close(inFlight)
			<-release
			return first(ctx, req)
		},
		record(bashTurn("tc-2", "echo two")),
		record(core.Message{Role: "assistant", Content: []core.Content{core.TextContent("done")}, StopReason: "end_turn", Usage: million}),
	)
	moaCfg := core.MoaConfig{
		DisableSandbox:    true,
		AutoTitleModel:    "off",
		SessionBriefModel: "haiku",
		Permissions:       core.PermissionsConfig{Mode: "ask"},
	}
	mgr := newTestManagerWithConfig(t, ctx, prov, t.TempDir(), moaCfg)
	srv := httptest.NewServer(NewServer(mgr))
	defer srv.Close()

	oldModel, _ := core.ResolveModel("claude-haiku-4-5-20251001")
	newModel, _ := core.ResolveModel("claude-opus-5")
	if oldModel.Pricing == nil || newModel.Pricing == nil {
		t.Fatal("test models need catalog pricing")
	}
	sess, err := mgr.CreateSession(CreateOpts{Model: "haiku"})
	if err != nil {
		t.Fatal(err)
	}
	oldThinking, _ := bus.QueryTyped[bus.GetThinkingLevel, string](sess.runtime.Bus, bus.GetThinkingLevel{})
	newThinking := "high"
	if oldThinking == newThinking {
		newThinking = "low"
	}

	var cfgMu sync.Mutex
	var changes []bus.ConfigChanged
	sess.runtime.Bus.Subscribe(func(e bus.ConfigChanged) {
		cfgMu.Lock()
		changes = append(changes, e)
		cfgMu.Unlock()
	})

	if _, _, _, err := mgr.Send(sess.ID, "run two commands", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pending := func() string {
		info, err := bus.QueryTyped[bus.GetPendingApproval, bus.PendingApprovalInfo](
			sess.runtime.Bus, bus.GetPendingApproval{SessionID: sess.ID},
		)
		if err != nil || info.Permission == nil {
			return ""
		}
		return info.Permission.ID
	}
	patch := func(body string) map[string]any {
		t.Helper()
		resp := apiReq(t, srv, http.MethodPatch, "/api/sessions/"+sess.ID+"/config", body)
		defer resp.Body.Close() //nolint:errcheck
		var result map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("PATCH %s = %d (%v)", body, resp.StatusCode, err)
		}
		return result
	}

	<-inFlight
	if got := sessState(sess); got != StateRunning {
		t.Fatalf("state = %q, want running", got)
	}
	if result := patch(fmt.Sprintf(`{"model":"claude-opus-5","thinking":%q}`, newThinking)); result["thinking"] != newThinking {
		t.Fatalf("PATCH while running = %v", result)
	}
	close(release)

	var permissionID string
	pollUntil(t, 5*time.Second, "permission request", func() bool {
		permissionID = pending()
		return permissionID != ""
	})

	result := patch(fmt.Sprintf(`{"model":"claude-opus-5","thinking":%q,"permission_mode":"yolo"}`, newThinking))
	if result["thinking"] != newThinking || result["permission_mode"] != "yolo" {
		t.Fatalf("PATCH while waiting on a permission = %v", result)
	}

	// The pending request needs an explicit decision: neither the model change
	// nor the laxer permission mode may resolve it.
	time.Sleep(200 * time.Millisecond)
	if got := pending(); got != permissionID {
		t.Fatalf("pending permission = %q, want %q still pending", got, permissionID)
	}
	if got := sessState(sess); got != StatePermission {
		t.Fatalf("state = %q, want permission", got)
	}
	reqMu.Lock()
	if len(reqs) != 1 {
		reqMu.Unlock()
		t.Fatalf("provider requests before the decision = %d, want 1", len(reqs))
	}
	reqMu.Unlock()
	cfgMu.Lock()
	var announced bool
	for _, c := range changes {
		announced = announced || (c.Provider == "anthropic" && c.Thinking != "" && c.ContextWindow == newModel.MaxInput)
	}
	cfgMu.Unlock()
	if !announced {
		t.Fatal("no config_changed event announced the new model")
	}

	if err := sess.runtime.Bus.Execute(bus.ResolvePermission{PermissionID: permissionID, Approved: true}); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 5*time.Second, "run finished", func() bool { return sessState(sess) == StateIdle })

	reqMu.Lock()
	got := append([]core.Request(nil), reqs...)
	reqMu.Unlock()
	if len(got) != 3 {
		t.Fatalf("provider requests = %d, want 3 (a second permission prompt would have stopped the run)", len(got))
	}
	if got[0].Model.ID != oldModel.ID || got[0].Options.ThinkingLevel != oldThinking {
		t.Fatalf("in-flight request = %s/%q, want %s/%q", got[0].Model.ID, got[0].Options.ThinkingLevel, oldModel.ID, oldThinking)
	}
	for i, req := range got[1:] {
		if req.Model.ID != newModel.ID || req.Options.ThinkingLevel != newThinking {
			t.Fatalf("request %d = %s/%q, want %s/%q", i+2, req.Model.ID, req.Options.ThinkingLevel, newModel.ID, newThinking)
		}
	}

	wantRequested := fmt.Sprint([]string{oldModel.ID, newModel.ID, newModel.ID})
	requestedModels := func(msgs []core.AgentMessage) string {
		var ids []string
		for _, m := range msgs {
			if m.Role == "assistant" {
				ids = append(ids, m.RequestedModel)
			}
		}
		return fmt.Sprint(ids)
	}
	history := sess.History()
	if got := requestedModels(history); got != wantRequested {
		t.Fatalf("requested models in history = %s, want %s", got, wantRequested)
	}
	for _, m := range history {
		if m.Role == "tool_result" && m.IsError {
			t.Fatalf("tool %s failed: %+v", m.ToolCallID, m.Content)
		}
	}

	wantCost := oldModel.Pricing.Cost(*million) + 2*newModel.Pricing.Cost(*million)
	pollUntil(t, 5*time.Second, "session cost", func() bool {
		for _, info := range mgr.List() {
			if info.ID == sess.ID {
				return math.Abs(info.CostUSD-wantCost) < 1e-9
			}
		}
		return false
	})

	pollUntil(t, 5*time.Second, "persisted session", func() bool {
		saved, _, err := session.FindSessionReadOnly(mgr.sessionBaseDir, sess.ID)
		if err != nil {
			return false
		}
		var msgs []core.AgentMessage
		for _, e := range saved.Entries {
			if e.Message.Role != "" {
				msgs = append(msgs, e.Message)
			}
		}
		return saved.Metadata["model"] == "anthropic/"+newModel.ID &&
			saved.Metadata["thinking"] == newThinking &&
			saved.Metadata["permission_mode"] == "yolo" &&
			requestedModels(msgs) == wantRequested
	})
}

// A resumed session is rebuilt from its append-only tree, which keeps the
// thinking every earlier model signed. The next request must replay only what
// its own model signed: switching models drops the old model's thinking from
// the request, staying on the same model keeps it. History keeps both.
func TestResumeAfterModelSwitch_RequestOmitsOtherModelsThinking(t *testing.T) {
	for _, tc := range []struct {
		name         string
		switchTo     string
		wantModel    string
		wantThinking bool
	}{
		{"switched model", "claude-opus-5", "claude-opus-5", false},
		{"same model", "", "claude-haiku-4-5-20251001", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			var reqMu sync.Mutex
			var reqs []core.Request
			reply := func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
				reqMu.Lock()
				reqs = append(reqs, req)
				reqMu.Unlock()
				msg := core.Message{
					Role:       "assistant",
					Provider:   "anthropic",
					Content:    []core.Content{{Type: "thinking", Thinking: "reasoning", ThinkingSignature: "sig-" + req.Model.ID}, core.TextContent("answer")},
					StopReason: "end_turn",
					Timestamp:  time.Now().Unix(),
				}
				ch := make(chan core.AssistantEvent, 2)
				ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
				ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
				close(ch)
				return ch, nil
			}
			mgr := newTestManager(t, ctx, newMockProvider(reply, reply))
			sess, err := mgr.CreateSession(CreateOpts{Model: "haiku"})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, _, err := mgr.Send(sess.ID, "first", nil, "", ""); err != nil {
				t.Fatal(err)
			}
			pollUntil(t, 5*time.Second, "first run", func() bool { return sessState(sess) == StateIdle })
			if tc.switchTo != "" {
				if _, err := mgr.ReconfigureSession(sess.ID, tc.switchTo, ""); err != nil {
					t.Fatal(err)
				}
			}
			if err := mgr.CloseSession(sess.ID); err != nil {
				t.Fatal(err)
			}
			resumed, err := mgr.ResumeSession(sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			var restored bool
			for _, m := range resumed.History() {
				for _, c := range m.Content {
					restored = restored || c.ThinkingSignature == "sig-claude-haiku-4-5-20251001"
				}
			}
			if !restored {
				t.Fatal("resumed history lost the first model's thinking")
			}
			if _, _, _, err := mgr.Send(resumed.ID, "second", nil, "", ""); err != nil {
				t.Fatal(err)
			}
			pollUntil(t, 5*time.Second, "second run", func() bool { return sessState(resumed) == StateIdle })

			reqMu.Lock()
			defer reqMu.Unlock()
			if len(reqs) != 2 {
				t.Fatalf("provider requests = %d, want 2", len(reqs))
			}
			next := reqs[1]
			if next.Model.ID != tc.wantModel {
				t.Fatalf("resumed request model = %s, want %s", next.Model.ID, tc.wantModel)
			}
			var replayed bool
			for _, m := range next.Messages {
				for _, c := range m.Content {
					replayed = replayed || c.Type == "thinking"
				}
			}
			if replayed != tc.wantThinking {
				t.Fatalf("resumed request replays earlier thinking = %v, want %v", replayed, tc.wantThinking)
			}
		})
	}
}
