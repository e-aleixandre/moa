//go:build !windows

package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/secretaction"
)

const secretTestValue = "Unlock-Me-7f3a9c"

type secretFixture struct {
	mgr      *Manager
	srv      *httptest.Server
	root     string
	sess     *ManagedSession
	requests chan secretaction.Request
	resolved chan string

	mu       sync.Mutex
	wsFrames bytes.Buffer // every bus event, as the WS would serialize it
}

// newSecretFixture builds a manager whose files all live under one temp root
// (sessions, tasks, attachments, HOME, config), so a byte scan of that root
// covers everything the session persisted.
func newSecretFixture(t *testing.T, handlers ...mockHandler) *secretFixture {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOA_CONFIG_DIR", filepath.Join(root, "config"))
	moaCfg := core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off"}
	provider := newMockProvider(handlers...)
	mgr := NewManager(ctx, ManagerConfig{
		ProviderFactory: func(_ core.Model) (core.Provider, error) { return provider, nil },
		DefaultModel:    core.Model{ID: "claude-haiku-4-5-20251001", Provider: "anthropic"},
		WorkspaceRoot:   workspace,
		MoaCfg:          moaCfg,
		ConfigLoader: func(string) core.MoaConfig {
			return moaCfg
		},
		SessionBaseDir: filepath.Join(root, "sessions"),
		SchedulePath:   filepath.Join(root, "schedules.json"),
	})
	t.Setenv("HOME", filepath.Join(root, "home"))
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
	srv := httptest.NewServer(NewServer(mgr))
	t.Cleanup(srv.Close)
	f := &secretFixture{mgr: mgr, srv: srv, root: root, requests: make(chan secretaction.Request, 4), resolved: make(chan string, 4)}
	sess, err := mgr.CreateSession(CreateOpts{CWD: workspace})
	if err != nil {
		t.Fatal(err)
	}
	f.sess = sess
	f.watch(t, sess)
	return f
}

func (f *secretFixture) watch(t *testing.T, sess *ManagedSession) {
	t.Cleanup(sess.runtime.Bus.SubscribeAll(func(event any) {
		if ev, ok := wsEventFromBus(event); ok {
			if b, err := json.Marshal(ev); err == nil {
				f.mu.Lock()
				f.wsFrames.Write(b)
				f.wsFrames.WriteByte('\n')
				f.mu.Unlock()
			}
		}
		// The raw bus payload too: no subscriber may ever see a value.
		if b, err := json.Marshal(event); err == nil {
			f.mu.Lock()
			f.wsFrames.Write(b)
			f.mu.Unlock()
		}
		switch e := event.(type) {
		case bus.SecretRequested:
			f.requests <- e.Request
		case bus.SecretResolved:
			f.resolved <- e.ID
		}
	}))
}

func (f *secretFixture) awaitRequest(t *testing.T) secretaction.Request {
	t.Helper()
	select {
	case r := <-f.requests:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("no secret_request was published")
	}
	return secretaction.Request{}
}

func (f *secretFixture) post(t *testing.T, sessionID, reqID, body string) int {
	t.Helper()
	req, err := http.NewRequest("POST", f.srv.URL+"/api/sessions/"+sessionID+"/secret-requests/"+reqID, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Moa-Request", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	return resp.StatusCode
}

func secretToolCall(command string) mockHandler {
	return toolCallHandlerFor("tc-secret", "run_with_secret", map[string]any{
		"command": command,
		"fields":  []any{map[string]any{"label": "Keychain password", "kind": "password", "alias": "agents-keychain"}},
	})
}

func toolResultText(t *testing.T, msgs []core.AgentMessage, callID string) string {
	t.Helper()
	for _, m := range msgs {
		if m.Role == "tool_result" && m.ToolCallID == callID {
			var sb strings.Builder
			for _, c := range m.Content {
				sb.WriteString(c.Text)
			}
			return sb.String()
		}
	}
	t.Fatalf("no tool_result for %s", callID)
	return ""
}

func scanTreeFor(t *testing.T, root string, needle []byte) []string {
	t.Helper()
	var hits []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err == nil && bytes.Contains(b, needle) {
			hits = append(hits, path)
		}
		return nil
	})
	return hits
}

func TestSecretRequestAllowFlowLeavesNoTrace(t *testing.T) {
	f := newSecretFixture(t,
		secretToolCall(`read -s -p "Password: " p; echo; echo "got ${#p}"; echo "$p" >/dev/null`),
		simpleResponseHandler("unlocked"),
	)
	if _, _, _, err := f.mgr.Send(f.sess.ID, "unlock the keychain", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	req := f.awaitRequest(t)
	if req.SessionID != f.sess.ID || req.Kind != secretaction.KindCommand || req.Requester != "main" || req.ActionFingerprint == "" {
		t.Fatalf("request = %+v", req)
	}
	// A reconnecting client gets the card back.
	init := buildInitData(f.sess, bus.StreamingAggregate{}, nil, "")
	if len(init.PendingSecretRequests) != 1 || init.PendingSecretRequests[0].ID != req.ID {
		t.Fatalf("init pending = %+v", init.PendingSecretRequests)
	}
	if got := sessState(f.sess); got != StatePermission {
		t.Fatalf("state while waiting = %s, want permission", got)
	}

	if code := f.post(t, f.sess.ID, req.ID, `{"action":"allow","values":[{"index":0,"value":"`+secretTestValue+`"}]}`); code != http.StatusNoContent {
		t.Fatalf("allow = %d", code)
	}
	pollUntil(t, 15*time.Second, "run finished", func() bool { return sessState(f.sess) == StateIdle })
	f.sess.runtime.Bus.Drain(2 * time.Second)

	result := toolResultText(t, f.sess.History(), "tc-secret")
	if !strings.Contains(result, "got 16") || !strings.Contains(result, "1 password prompt answered") {
		t.Fatalf("tool result = %s", result)
	}
	if init := buildInitData(f.sess, bus.StreamingAggregate{}, nil, ""); len(init.PendingSecretRequests) != 0 {
		t.Fatal("card still restored after the answer")
	}
	select {
	case id := <-f.resolved:
		if id != req.ID {
			t.Fatalf("resolved %q", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no secret_resolved")
	}

	// Close the session so everything is flushed, then scan every byte it left.
	id := f.sess.ID
	if err := f.mgr.Delete(id); err != nil && !strings.Contains(err.Error(), "not found") {
		t.Logf("delete: %v", err)
	}
	f.mu.Lock()
	frames := f.wsFrames.Bytes()
	f.mu.Unlock()
	if bytes.Contains(frames, []byte(secretTestValue)) {
		t.Fatal("the value reached a bus/WS event")
	}
	if !bytes.Contains(frames, []byte(`"type":"secret_request"`)) || !bytes.Contains(frames, []byte(`"type":"secret_resolved"`)) {
		t.Fatalf("WS never carried the card events")
	}
	if hits := scanTreeFor(t, f.root, []byte(secretTestValue)); len(hits) > 0 {
		t.Fatalf("value persisted in %v", hits)
	}
}

func TestSecretRequestPersistedSessionHasNoValueBeforeDelete(t *testing.T) {
	f := newSecretFixture(t,
		secretToolCall(`read -p "Password: " p; echo "echoed"`),
		simpleResponseHandler("done"),
	)
	if _, _, _, err := f.mgr.Send(f.sess.ID, "go", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	req := f.awaitRequest(t)
	if code := f.post(t, f.sess.ID, req.ID, `{"action":"allow","values":[{"index":0,"value":"`+secretTestValue+`"}]}`); code != http.StatusNoContent {
		t.Fatalf("allow = %d", code)
	}
	pollUntil(t, 15*time.Second, "run finished", func() bool { return sessState(f.sess) == StateIdle })
	f.sess.runtime.Bus.Drain(2 * time.Second)
	result := toolResultText(t, f.sess.History(), "tc-secret")
	if !strings.Contains(result, secretaction.Placeholder) || strings.Contains(result, secretTestValue) {
		t.Fatalf("echo not redacted in the transcript: %s", result)
	}
	// The persisted session (still open) must not hold it either.
	pollUntil(t, 5*time.Second, "session persisted", func() bool {
		return len(scanTreeFor(t, filepath.Join(f.root, "sessions"), []byte("echoed"))) > 0
	})
	if hits := scanTreeFor(t, f.root, []byte(secretTestValue)); len(hits) > 0 {
		t.Fatalf("value persisted in %v", hits)
	}
}

func TestSecretRequestDeny(t *testing.T) {
	f := newSecretFixture(t,
		secretToolCall(`touch ran-anyway; read -s -p "Password: " p`),
		simpleResponseHandler("ok"),
	)
	if _, _, _, err := f.mgr.Send(f.sess.ID, "go", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	req := f.awaitRequest(t)
	if code := f.post(t, f.sess.ID, req.ID, `{"action":"deny"}`); code != http.StatusNoContent {
		t.Fatalf("deny = %d", code)
	}
	pollUntil(t, 10*time.Second, "run finished", func() bool { return sessState(f.sess) == StateIdle })
	result := toolResultText(t, f.sess.History(), "tc-secret")
	if !strings.Contains(strings.ToLower(result), "denied") {
		t.Fatalf("deny result = %s", result)
	}
	if _, err := os.Stat(filepath.Join(f.sess.CWD, "ran-anyway")); err == nil {
		t.Fatal("command ran after deny")
	}
}

func TestSecretRequestHTTPValidation(t *testing.T) {
	f := newSecretFixture(t,
		secretToolCall(`read -s -p "Password: " p; echo ok`),
		simpleResponseHandler("ok"),
	)
	if _, _, _, err := f.mgr.Send(f.sess.ID, "go", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	req := f.awaitRequest(t)
	broker := f.sess.runtime.Context().SecretBroker
	for name, tc := range map[string]struct {
		session, id, body string
		want              int
	}{
		"unknown session": {"nope", req.ID, `{"action":"deny"}`, http.StatusNotFound},
		"unknown request": {f.sess.ID, "sec_999", `{"action":"allow","values":[{"index":0,"value":"x"}]}`, http.StatusNotFound},
		"missing value":   {f.sess.ID, req.ID, `{"action":"allow","values":[]}`, http.StatusBadRequest},
		"empty value":     {f.sess.ID, req.ID, `{"action":"allow","values":[{"index":0,"value":""}]}`, http.StatusBadRequest},
		"bad index":       {f.sess.ID, req.ID, `{"action":"allow","values":[{"index":3,"value":"x"}]}`, http.StatusBadRequest},
		"duplicate index": {f.sess.ID, req.ID, `{"action":"allow","values":[{"index":0,"value":"x"},{"index":0,"value":"y"}]}`, http.StatusBadRequest},
		"bad action":      {f.sess.ID, req.ID, `{"action":"maybe"}`, http.StatusBadRequest},
		"newline value":   {f.sess.ID, req.ID, `{"action":"allow","values":[{"index":0,"value":"pw\nsecond"}]}`, http.StatusBadRequest},
		"CR value":        {f.sess.ID, req.ID, `{"action":"allow","values":[{"index":0,"value":"pw\rx"}]}`, http.StatusBadRequest},
		"escape value":    {f.sess.ID, req.ID, `{"action":"allow","values":[{"index":0,"value":"pw\u001b[31m"}]}`, http.StatusBadRequest},
		"DEL value":       {f.sess.ID, req.ID, `{"action":"allow","values":[{"index":0,"value":"pw\u007f"}]}`, http.StatusBadRequest},
		"tab value":       {f.sess.ID, req.ID, `{"action":"allow","values":[{"index":0,"value":"pw\tx"}]}`, http.StatusBadRequest},
		"malformed":       {f.sess.ID, req.ID, `{"action":`, http.StatusBadRequest},
	} {
		if code := f.post(t, tc.session, tc.id, tc.body); code != tc.want {
			t.Errorf("%s: status %d, want %d", name, code, tc.want)
		}
	}
	if broker == nil || !broker.IsPending(req.ID) {
		t.Fatal("a rejected answer resolved the card")
	}
	if code := f.post(t, f.sess.ID, req.ID, `{"action":"deny"}`); code != http.StatusNoContent {
		t.Fatalf("deny = %d", code)
	}
	if code := f.post(t, f.sess.ID, req.ID, `{"action":"deny"}`); code != http.StatusNotFound {
		t.Fatalf("second answer = %d, want 404", code)
	}
}

func TestSecretRequestStopWhilePending(t *testing.T) {
	f := newSecretFixture(t,
		secretToolCall(`read -s -p "Password: " p`),
		simpleResponseHandler("never"),
	)
	if _, _, _, err := f.mgr.Send(f.sess.ID, "go", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	req := f.awaitRequest(t)
	if err := f.mgr.Cancel(f.sess.ID); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 10*time.Second, "run stopped", func() bool { return sessState(f.sess) == StateIdle })
	select {
	case id := <-f.resolved:
		if id != req.ID {
			t.Fatalf("resolved %q", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop left the card on the clients")
	}
	if f.sess.runtime.Context().SecretBroker.IsPending(req.ID) {
		t.Fatal("still pending after Stop")
	}
	if result := toolResultText(t, f.sess.History(), "tc-secret"); !strings.Contains(result, "cancelled") {
		t.Fatalf("result = %s", result)
	}
}

func TestSecretRequestOwnerCannotAnswer(t *testing.T) {
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	provider := newMockProvider(secretToolCall(`read -s -p "Password: " p; echo ok`))
	mgr := newTestManager(t, context.Background(), provider)
	root := t.TempDir()
	_, ownerSess := ownerWithSession(t, mgr, root, "Winerim")
	child, err := mgr.CreateSession(CreateOpts{CWD: root, Origin: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan secretaction.Request, 1)
	t.Cleanup(child.runtime.Bus.Subscribe(func(e bus.SecretRequested) { requests <- e.Request }))
	if _, _, _, err := mgr.Send(child.ID, "unlock", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	var req secretaction.Request
	select {
	case req = <-requests:
	case <-time.After(10 * time.Second):
		t.Fatal("no secret request")
	}

	tool, _ := ownerSess.infra.toolReg.Get(SessionsToolName)
	schema := strings.ToLower(string(tool.Parameters) + tool.Description)
	for _, forbidden := range []string{"secret", "password"} {
		if strings.Contains(schema, forbidden) {
			t.Fatalf("the sessions tool mentions %q", forbidden)
		}
	}
	for _, params := range []map[string]any{
		{"action": "answer", "session_id": child.ID, "ask_id": req.ID, "answers": []any{"hunter2"}},
		{"action": "send", "session_id": child.ID, "text": "hunter2"},
		{"action": "allow", "session_id": child.ID, "id": req.ID},
	} {
		runSessionsTool(t, ownerSess, params)
		if !child.runtime.Context().SecretBroker.IsPending(req.ID) {
			t.Fatalf("owner resolved the card with %v", params)
		}
	}
	if _, ok := ownerSess.infra.toolReg.Get("run_with_secret"); !ok {
		t.Fatal("precondition: owner registry should be a normal session registry")
	}
}

func TestSecretRequestSubagentCarriesLabel(t *testing.T) {
	f := newSecretFixture(t,
		toolCallHandlerFor("tc-sub", "subagent", map[string]any{"task": "unlock it"}),
		secretToolCall(`read -s -p "Password: " p; echo "sub ${#p}"`),
		simpleResponseHandler("child done"),
		simpleResponseHandler("parent done"),
	)
	if _, _, _, err := f.mgr.Send(f.sess.ID, "delegate", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	req := f.awaitRequest(t)
	if !strings.HasPrefix(req.Requester, "subagent · ") || req.SessionID != f.sess.ID {
		t.Fatalf("subagent request = %+v", req)
	}
	if code := f.post(t, f.sess.ID, req.ID, `{"action":"allow","values":[{"index":0,"value":"`+secretTestValue+`"}]}`); code != http.StatusNoContent {
		t.Fatalf("allow = %d", code)
	}
	pollUntil(t, 20*time.Second, "parent finished", func() bool { return sessState(f.sess) == StateIdle })
	f.mgr.Delete(f.sess.ID) //nolint:errcheck
	if hits := scanTreeFor(t, f.root, []byte(secretTestValue)); len(hits) > 0 {
		t.Fatalf("value persisted in %v", hits)
	}
}

// A card's ID answers only that card: posting session A's request ID to
// session B's URL must not deliver A's value to B.
func TestSecretRequestIDDoesNotResolveInAnotherSession(t *testing.T) {
	f := newSecretFixture(t,
		secretToolCall(`read -s -p "Password: " p; echo "A ${#p}"`),
		secretToolCall(`read -s -p "Password: " p; touch ran-b; echo "B ${#p}"`),
		simpleResponseHandler("done"),
		simpleResponseHandler("done"),
	)
	if _, _, _, err := f.mgr.Send(f.sess.ID, "a", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	reqA := f.awaitRequest(t)
	sessB, err := f.mgr.CreateSession(CreateOpts{CWD: f.sess.CWD})
	if err != nil {
		t.Fatal(err)
	}
	f.watch(t, sessB)
	if _, _, _, err := f.mgr.Send(sessB.ID, "b", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	reqB := f.awaitRequest(t)
	if reqA.ID == reqB.ID {
		t.Errorf("two sessions share request ID %q", reqA.ID)
	}
	if code := f.post(t, sessB.ID, reqA.ID, `{"action":"allow","values":[{"index":0,"value":"`+secretTestValue+`"}]}`); code != http.StatusNotFound {
		t.Fatalf("A's ID through B's URL = %d, want 404", code)
	}
	if !f.sess.runtime.Context().SecretBroker.IsPending(reqA.ID) || !sessB.runtime.Context().SecretBroker.IsPending(reqB.ID) {
		t.Fatal("a cross-session answer resolved a card")
	}
	_ = f.mgr.Cancel(f.sess.ID)
	_ = f.mgr.Cancel(sessB.ID)
}

// A delayed answer to a card of a closed run must not approve the command a
// resumed session asks for later.
func TestSecretRequestStaleApprovalAfterResume(t *testing.T) {
	f := newSecretFixture(t,
		secretToolCall(`read -s -p "Password: " p; echo "A ${#p}"`),
		secretToolCall(`read -s -p "Password: " p; touch ran-b; echo "UNAPPROVED_B:${#p}"`),
		simpleResponseHandler("done"),
		simpleResponseHandler("done"),
	)
	id := f.sess.ID
	if _, _, _, err := f.mgr.Send(id, "a", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	reqA := f.awaitRequest(t)
	if err := f.mgr.Cancel(id); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 10*time.Second, "run stopped", func() bool { return sessState(f.sess) == StateIdle })
	if err := f.mgr.CloseSession(id); err != nil {
		t.Fatal(err)
	}
	resumed, err := f.mgr.ResumeSession(id)
	if err != nil {
		t.Fatal(err)
	}
	f.watch(t, resumed)
	if _, _, _, err := f.mgr.Send(id, "b", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	reqB := f.awaitRequest(t)
	if code := f.post(t, id, reqA.ID, `{"action":"allow","values":[{"index":0,"value":"`+secretTestValue+`"}]}`); code != http.StatusNotFound {
		t.Fatalf("stale approval = %d, want 404", code)
	}
	if !resumed.runtime.Context().SecretBroker.IsPending(reqB.ID) {
		t.Fatal("the stale approval resolved the new card")
	}
	if _, err := os.Stat(filepath.Join(resumed.CWD, "ran-b")); err == nil {
		t.Fatal("the new command ran on a stale approval")
	}
	_ = f.mgr.Cancel(id)
}

// L2: the control-byte restriction protects the terminal; a browser field
// still takes a value with a TAB, as it did before.
func TestSecretRequestBrowserAcceptsControlBytes(t *testing.T) {
	f := newSecretFixture(t)
	broker := f.sess.runtime.Context().SecretBroker
	if broker == nil {
		t.Fatal("session has no secret broker")
	}
	type result struct {
		ans secretaction.Answer
		err error
	}
	done := make(chan result, 1)
	go func() {
		ans, err := broker.Request(context.Background(), secretaction.Request{
			Kind: secretaction.KindBrowser, Origin: "https://example.com",
			Fields: []secretaction.Field{{Label: "Password", Kind: secretaction.FieldPassword}},
		})
		done <- result{ans, err}
	}()
	req := f.awaitRequest(t)
	if code := f.post(t, f.sess.ID, req.ID, `{"action":"allow","values":[{"index":0,"value":"pw\tx"}]}`); code != http.StatusNoContent {
		t.Fatalf("browser value with TAB = %d, want 204", code)
	}
	select {
	case r := <-done:
		defer r.ans.Wipe()
		if r.err != nil || !r.ans.Allow || len(r.ans.Values) != 1 || string(r.ans.Values[0]) != "pw\tx" {
			t.Fatalf("answer = allow %v err %v", r.ans.Allow, r.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("browser request not answered")
	}
}
