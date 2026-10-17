package serve

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// The read_session tests drive the human path through the real HTTP /send
// endpoint with the optional "session_mentions" field and observe the
// persisted "human_session_mentions" receipt. Field, tool and receipt names are
// literals on purpose: nothing here depends on production symbols that do not
// exist yet, so the suite compiles today and fails on behaviour.

const rsTool = "read_session"

// Metadata must not contain the content sentinel: returning only a title or
// message ID must never satisfy a successful-read assertion.
const rsTargetTitle = "PRIVATE_SESSION_TITLE"
const rsTargetMsgID = "target-message"

type rsEnv struct {
	t    *testing.T
	mgr  *Manager
	srv  *httptest.Server
	root string
	prov *scriptedProvider
}

// newRSEnv builds a real Manager (real stores and factories); only the model
// is faked. A nil fn answers every request with a short reply.
func newRSEnv(t *testing.T, fn func(core.Request) <-chan core.AssistantEvent) *rsEnv {
	t.Helper()
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	if fn == nil {
		fn = func(core.Request) <-chan core.AssistantEvent { return simpleResponse("ok") }
	}
	prov := &scriptedProvider{fn: fn}
	mgr := newTestManager(t, context.Background(), prov)
	srv := httptest.NewServer(NewServer(mgr))
	t.Cleanup(srv.Close)
	return &rsEnv{t: t, mgr: mgr, srv: srv, root: t.TempDir(), prov: prov}
}

func (e *rsEnv) session(cwd string) *ManagedSession {
	e.t.Helper()
	s, err := e.mgr.CreateSession(CreateOpts{CWD: cwd})
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

// target is a normal session of the same codebase carrying a sentinel that
// only a successful read can return.
func (e *rsEnv) target(sentinel string) *ManagedSession {
	e.t.Helper()
	s, err := e.mgr.CreateSession(CreateOpts{CWD: e.root, Title: rsTargetTitle})
	if err != nil {
		e.t.Fatal(err)
	}
	appendConversationTestMessage(s, rsTargetMsgID, "user", sentinel, nil)
	return s
}

func rsBlock(label, id string) string {
	return "[Mentions · moa]\n@" + label + " = session/" + id + " · read it with read_session session_id=" + id
}

// send posts to the real /send endpoint. mentions == nil omits the field.
func (e *rsEnv) send(callerID, text string, mentions any, extra map[string]any) (int, string) {
	e.t.Helper()
	payload := map[string]any{"text": text}
	if mentions != nil {
		payload["session_mentions"] = mentions
	}
	for k, v := range extra {
		payload[k] = v
	}
	body, err := json.Marshal(payload)
	if err != nil {
		e.t.Fatal(err)
	}
	return e.sendRaw(callerID, string(body))
}

func (e *rsEnv) sendRaw(callerID, body string) (int, string) {
	e.t.Helper()
	resp := apiReq(e.t, e.srv, "POST", "/api/sessions/"+callerID+"/send", body)
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// mention sends text with the explicit selection and waits for delivery.
func (e *rsEnv) mention(caller *ManagedSession, text string, ids ...string) {
	e.t.Helper()
	status, body := e.send(caller.ID, text, ids, nil)
	if status != http.StatusAccepted {
		e.t.Fatalf("send with session_mentions = %d %s", status, body)
	}
	rsSettle(e.t, caller)
}

func rsSettle(t *testing.T, s *ManagedSession) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if !s.runtime.WaitSettled(ctx) {
		t.Fatal("session did not settle")
	}
	if err := s.runtime.Flush(); err != nil {
		t.Fatal(err)
	}
}

// rsReader returns the caller's read_session tool or fails: a denial test is
// only meaningful when the tool exists, so "unknown tool" is never a pass.
func rsReader(t *testing.T, s *ManagedSession) core.Tool {
	t.Helper()
	tool, ok := s.infra.toolReg.Get(rsTool)
	if !ok {
		t.Fatalf("normal conversation %s has no %s tool registered", s.ID, rsTool)
	}
	return tool
}

func rsRead(t *testing.T, s *ManagedSession, params map[string]any) core.Result {
	t.Helper()
	tool := rsReader(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := tool.Execute(ctx, params, nil)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func rsExpectRead(t *testing.T, s *ManagedSession, id, sentinel string) {
	t.Helper()
	res := rsRead(t, s, map[string]any{"session_id": id})
	if res.IsError || !strings.Contains(toolText(res), sentinel) {
		t.Errorf("read %s: want content %q, got error=%v %q", id, sentinel, res.IsError, toolText(res))
	}
}

// rsExpectDenied requires an error result that leaks no content. The tool must
// exist (rsReader), so a missing tool can never satisfy this.
func rsExpectDenied(t *testing.T, s *ManagedSession, id string, sentinels ...string) {
	t.Helper()
	res := rsRead(t, s, map[string]any{"session_id": id})
	rsCheckDenied(t, id, res, sentinels)
}

func rsCheckDenied(t *testing.T, id string, res core.Result, sentinels []string) {
	t.Helper()
	if !res.IsError {
		t.Errorf("read %s should be denied, got %q", id, toolText(res))
	}
	for _, s := range sentinels {
		if strings.Contains(toolText(res), s) {
			t.Errorf("denied read %s leaked %q", id, s)
		}
	}
}

// rsDeniedNotMentioned is the stricter variant for "no reference" cases.
func rsDeniedNotMentioned(t *testing.T, s *ManagedSession, id string, sentinels ...string) {
	t.Helper()
	res := rsRead(t, s, map[string]any{"session_id": id})
	rsCheckDenied(t, id, res, sentinels)
	if !strings.Contains(toolText(res), "mentioned") {
		t.Errorf("read %s: want the not-mentioned error, got %q", id, toolText(res))
	}
}

// rsReceipts lists, per original user message of the active branch (in-flight
// turn included), the IDs of its human_session_mentions receipt. Decoding via
// JSON keeps the test independent of []string vs []any.
func rsReceipts(t *testing.T, s *ManagedSession) [][]string {
	t.Helper()
	var out [][]string
	s.runtime.Context().VisitTranscript(func(m *core.AgentMessage) {
		if m.Role != "user" {
			return
		}
		raw, ok := m.Custom["human_session_mentions"]
		if !ok {
			return
		}
		b, err := json.Marshal(raw)
		if err != nil {
			t.Errorf("receipt not marshalable: %v", err)
			return
		}
		var ids []string
		if err := json.Unmarshal(b, &ids); err != nil {
			t.Errorf("receipt shape %s: %v", b, err)
		}
		out = append(out, ids)
	})
	return out
}

func rsUserCount(s *ManagedSession) int {
	n := 0
	s.runtime.Context().VisitTranscript(func(m *core.AgentMessage) {
		if m.Role == "user" {
			n++
		}
	})
	return n
}

// rsUserEntryIDs returns the tree entry IDs of the active branch's user messages.
func rsUserEntryIDs(s *ManagedSession) []string {
	var ids []string
	for _, e := range s.runtime.Context().Tree.Path() {
		if e.Message.Role == "user" {
			ids = append(ids, e.ID)
		}
	}
	return ids
}

func rsEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func rsCheckReceipts(t *testing.T, s *ManagedSession, want ...[]string) {
	t.Helper()
	got := rsReceipts(t, s)
	if len(got) != len(want) {
		t.Errorf("receipts = %v, want %v", got, want)
		return
	}
	for i := range got {
		if !rsEq(got[i], want[i]) {
			t.Errorf("receipts = %v, want %v", got, want)
			return
		}
	}
}

func rsText(m *core.AgentMessage) string {
	var sb strings.Builder
	for _, c := range m.Content {
		sb.WriteString(c.Text)
	}
	return sb.String()
}

// rsExpectOptionsDenied exercises every read option against an existing target
// the caller may not read. Scope errors need not use the not-mentioned reason.
func rsExpectOptionsDenied(t *testing.T, s *ManagedSession, id, messageID, sentinel, wantReason string) {
	t.Helper()
	leaks := []string{sentinel, rsTargetTitle}
	for name, extra := range map[string]map[string]any{
		"plain":             {},
		"message_id":        {"message_id": messageID},
		"message_id+offset": {"message_id": messageID, "offset": 0},
		"offset":            {"offset": 0},
		"tools":             {"tools": true},
		"limit":             {"limit": 5},
	} {
		params := map[string]any{"session_id": id}
		for k, v := range extra {
			params[k] = v
		}
		res := rsRead(t, s, params)
		rsCheckDenied(t, id+" "+name, res, leaks)
		if wantReason != "" && !strings.Contains(toolText(res), wantReason) {
			t.Errorf("read %s (%s): want reason %q, got %q", id, name, wantReason, toolText(res))
		}
	}
}
