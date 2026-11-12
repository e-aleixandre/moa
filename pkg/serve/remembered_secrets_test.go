//go:build !windows

package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/secretaction"
)

const rememberTestValue = "Night-Remembered-3c81f0"

// askEcho prompts with echo off and then echoes the value back, so
// redaction is exercised too.
const askEcho = `printf 'Password for vault: '; stty -echo; IFS= read -r p; stty echo; echo; echo "echo:$p"`

const testAlias = "vault-agents"

func vaultToolCall(id, command, alias string) mockHandler {
	return toolCallHandlerFor(id, "run_with_secret", map[string]any{
		"command": command,
		"fields": []any{map[string]any{"label": "Vault password", "kind": "password", "alias": alias}},
	})
}

// recordingProvider wraps handlers so every request a model would receive is
// kept for the leak scan.
type providerLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *providerLog) wrap(h mockHandler) mockHandler {
	return func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
		if b, err := json.Marshal(req); err == nil {
			l.mu.Lock()
			l.buf.Write(b)
			l.mu.Unlock()
		}
		return h(ctx, req)
	}
}

func (l *providerLog) bytes() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]byte(nil), l.buf.Bytes()...)
}

// captureLogs sends slog output to a buffer for the test's duration.
func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

func (f *secretFixture) memoryStatus(t *testing.T, method string) map[string]any {
	t.Helper()
	return f.memoryStatusAt(t, method, "/api/remembered-secrets")
}

func (f *secretFixture) memoryStatusAt(t *testing.T, method, path string) map[string]any {
	t.Helper()
	req, _ := http.NewRequest(method, f.srv.URL+path, nil)
	req.Header.Set("X-Moa-Request", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s /api/remembered-secrets = %d", method, resp.StatusCode)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q", cc)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func (f *secretFixture) runToIdle(t *testing.T, sess *ManagedSession, prompt string) {
	t.Helper()
	if _, _, _, err := f.mgr.Send(sess.ID, prompt, nil, "", ""); err != nil {
		t.Fatal(err)
	}
}

func waitMemoryIdle(t *testing.T, sess *ManagedSession) {
	t.Helper()
	pollUntil(t, 20*time.Second, "run finished", func() bool { return sessState(sess) == StateIdle })
	sess.runtime.Bus.Drain(2 * time.Second)
}

func TestRememberedSecretAnswersOtherSessionsAndLeavesNoTrace(t *testing.T) {
	logs := captureLogs(t)
	var plog providerLog
	f := newSecretFixture(t,
		plog.wrap(vaultToolCall("tc-open", askEcho, testAlias)),
		plog.wrap(simpleResponseHandler("unlocked")),
		plog.wrap(vaultToolCall("tc-use", askEcho+"; echo SECOND", testAlias)),
		plog.wrap(simpleResponseHandler("used")),
	)
	f.runToIdle(t, f.sess, "sign in")
	req := f.awaitRequest(t)
	if req.HoldMaxHours != secretaction.MaxHoldHours {
		t.Fatalf("card does not offer a hold: %+v", req)
	}
	if code := f.post(t, f.sess.ID, req.ID, `{"action":"allow","holdHours":2,"values":[{"index":0,"value":"`+rememberTestValue+`"}]}`); code != http.StatusNoContent {
		t.Fatalf("allow with hold = %d", code)
	}
	waitMemoryIdle(t, f.sess)
	st := f.memoryStatus(t, "GET")
	entries, _ := st["entries"].([]any)
	if len(entries) != 1 || entries[0].(map[string]any)["alias"] != testAlias || entries[0].(map[string]any)["kind"] != "password" {
		t.Fatalf("not remembered: %v", st)
	}
	expires, _ := time.Parse(time.RFC3339Nano, entries[0].(map[string]any)["expiresAt"].(string))
	if d := time.Until(expires); d < 119*time.Minute || d > 2*time.Hour {
		t.Fatalf("expires in %v, want 2h", d)
	}

	// Another session, as if in another project: answered with no card.
	other, err := f.mgr.CreateSession(CreateOpts{CWD: f.sess.CWD})
	if err != nil {
		t.Fatal(err)
	}
	f.watch(t, other)
	f.runToIdle(t, other, "list items")
	waitMemoryIdle(t, other)
	select {
	case r := <-f.requests:
		t.Fatalf("a card was shown for a remembered secret: %+v", r)
	default:
	}
	result := toolResultText(t, other.History(), "tc-use")
	if !strings.Contains(result, "SECOND") || !strings.Contains(result, "echo:"+secretaction.Placeholder) || !strings.Contains(result, "chose to remember") {
		t.Fatalf("remembered tool result = %s", result)
	}

	st = f.memoryStatus(t, "GET")
	uses, _ := st["uses"].([]any)
	if len(uses) != 2 {
		t.Fatalf("uses = %v", uses)
	}
	use := uses[1].(map[string]any)
	if use["kind"] != "use" || use["sessionId"] != other.ID || !strings.Contains(use["command"].(string), "SECOND") || use["cwd"] != other.CWD || use["at"] == "" {
		t.Fatalf("use record = %v", use)
	}
	if !bytes.Contains([]byte(logs.String()), []byte("remembered secret used")) {
		t.Fatalf("use not logged: %s", []byte(logs.String()))
	}

	// Scan before deleting anything: persisted sessions, config, HOME, the
	// bus/WS, the model's inputs, the logs, the status endpoint.
	needle := []byte(rememberTestValue)
	if hits := scanTreeFor(t, f.root, needle); len(hits) > 0 {
		t.Fatalf("value on disk: %v", hits)
	}
	f.mu.Lock()
	frames := append([]byte(nil), f.wsFrames.Bytes()...)
	f.mu.Unlock()
	statusJSON, _ := json.Marshal(st)
	for name, b := range map[string][]byte{"bus/WS": frames, "provider": plog.bytes(), "logs": []byte(logs.String()), "status": statusJSON} {
		if bytes.Contains(b, needle) {
			t.Fatalf("value reached %s", name)
		}
	}
	if len(plog.bytes()) == 0 || !bytes.Contains(plog.bytes(), []byte("chose to remember")) {
		t.Fatal("provider log did not capture the tool results")
	}
}

func TestForgetShowsTheCardAgain(t *testing.T) {
	f := newSecretFixture(t,
		vaultToolCall("tc-1", askEcho, testAlias),
		simpleResponseHandler("one"),
		vaultToolCall("tc-2", askEcho, testAlias),
		simpleResponseHandler("two"),
	)
	f.runToIdle(t, f.sess, "first")
	req := f.awaitRequest(t)
	if code := f.post(t, f.sess.ID, req.ID, `{"action":"allow","holdHours":1,"values":[{"index":0,"value":"`+rememberTestValue+`"}]}`); code != http.StatusNoContent {
		t.Fatalf("allow = %d", code)
	}
	waitMemoryIdle(t, f.sess)
	// Forgetting another alias leaves it; forgetting its alias drops it.
	st := f.memoryStatusAt(t, "DELETE", "/api/remembered-secrets?alias=sudo-prod")
	if entries, _ := st["entries"].([]any); len(entries) != 1 {
		t.Fatalf("forgetting another alias: %v", st)
	}
	st = f.memoryStatusAt(t, "DELETE", "/api/remembered-secrets?alias="+testAlias)
	if entries, _ := st["entries"].([]any); len(entries) != 0 {
		t.Fatalf("after DELETE: %v", st)
	}
	f.runToIdle(t, f.sess, "second")
	req = f.awaitRequest(t)
	if code := f.post(t, f.sess.ID, req.ID, `{"action":"deny"}`); code != http.StatusNoContent {
		t.Fatalf("deny = %d", code)
	}
	waitMemoryIdle(t, f.sess)
}

func TestRememberedOtherAliasStillAsks(t *testing.T) {
	f := newSecretFixture(t,
		vaultToolCall("tc-1", askEcho, testAlias),
		simpleResponseHandler("one"),
		vaultToolCall("tc-2", askEcho, "sudo-prod"),
		simpleResponseHandler("two"),
	)
	f.runToIdle(t, f.sess, "first")
	req := f.awaitRequest(t)
	if code := f.post(t, f.sess.ID, req.ID, `{"action":"allow","holdHours":1,"values":[{"index":0,"value":"`+rememberTestValue+`"}]}`); code != http.StatusNoContent {
		t.Fatalf("allow = %d", code)
	}
	waitMemoryIdle(t, f.sess)
	f.runToIdle(t, f.sess, "second")
	// Another alias gets its own card (which may itself offer to remember).
	req = f.awaitRequest(t)
	if req.Fields[0].Alias != "sudo-prod" || req.HoldMaxHours != secretaction.MaxHoldHours {
		t.Fatalf("second card = %+v", req)
	}
	if code := f.post(t, f.sess.ID, req.ID, `{"action":"deny"}`); code != http.StatusNoContent {
		t.Fatalf("deny = %d", code)
	}
	waitMemoryIdle(t, f.sess)
}

func TestNoAliasCannotBeRemembered(t *testing.T) {
	f := newSecretFixture(t,
		toolCallHandlerFor("tc-1", "run_with_secret", map[string]any{
			"command": askEcho,
			"fields":  []any{map[string]any{"label": "One-off", "kind": "password"}},
		}),
		simpleResponseHandler("one"),
	)
	f.runToIdle(t, f.sess, "first")
	req := f.awaitRequest(t)
	if req.HoldMaxHours != 0 {
		t.Fatalf("a field without alias was offered remembering: %+v", req)
	}
	if code := f.post(t, f.sess.ID, req.ID, `{"action":"allow","holdHours":1,"values":[{"index":0,"value":"x"}]}`); code != http.StatusBadRequest {
		t.Fatalf("forced hold = %d, want 400", code)
	}
	if code := f.post(t, f.sess.ID, req.ID, `{"action":"deny"}`); code != http.StatusNoContent {
		t.Fatalf("deny = %d", code)
	}
	waitMemoryIdle(t, f.sess)
}

func TestRememberHoldValidation(t *testing.T) {
	f := newSecretFixture(t, vaultToolCall("tc-1", askEcho, testAlias), simpleResponseHandler("one"))
	f.runToIdle(t, f.sess, "first")
	req := f.awaitRequest(t)
	value := `"values":[{"index":0,"value":"` + rememberTestValue + `"}]`
	for _, body := range []string{
		`{"action":"allow","holdHours":11,` + value + `}`,
		`{"action":"allow","holdHours":-1,` + value + `}`,
		`{"action":"allow","holdHours":1.5,` + value + `}`,
		`{"action":"allow","holdHours":"2",` + value + `}`,
		`{"action":"deny","holdHours":2}`,
		`{"action":"allow","holdHours":1,"values":[{"index":0,"value":"` + strings.Repeat("a", secretaction.MaxHoldValueBytes+1) + `"}]}`,
	} {
		if code := f.post(t, f.sess.ID, req.ID, body); code != http.StatusBadRequest {
			t.Errorf("%.60s… = %d, want 400", body, code)
		}
	}
	// None of them resolved the card or opened anything.
	if len(f.mgr.secretMemory.Status().Entries) != 0 {
		t.Fatal("an invalid hold remembered something")
	}
	if code := f.post(t, f.sess.ID, req.ID, `{"action":"allow","holdHours":null,`+value+`}`); code != http.StatusNoContent {
		t.Fatalf("null hold (single use) = %d", code)
	}
	waitMemoryIdle(t, f.sess)
	if len(f.mgr.secretMemory.Status().Entries) != 0 {
		t.Fatal("a single use remembered something")
	}
}

func TestRememberedSecretsDoNotSurviveRestart(t *testing.T) {
	f := newSecretFixture(t, vaultToolCall("tc-1", askEcho, testAlias), simpleResponseHandler("one"))
	f.runToIdle(t, f.sess, "first")
	req := f.awaitRequest(t)
	if code := f.post(t, f.sess.ID, req.ID, `{"action":"allow","holdHours":10,"values":[{"index":0,"value":"`+rememberTestValue+`"}]}`); code != http.StatusNoContent {
		t.Fatalf("allow = %d", code)
	}
	waitMemoryIdle(t, f.sess)
	if len(f.mgr.secretMemory.Status().Entries) != 1 {
		t.Fatal("not remembered")
	}
	// A new server process on the same data: nothing to restore.
	g := newSecretFixtureAt(t, f.root, vaultToolCall("tc-2", askEcho, testAlias), simpleResponseHandler("two"))
	if len(g.mgr.secretMemory.Status().Entries) != 0 {
		t.Fatal("remembered secrets survived a restart")
	}
	g.runToIdle(t, g.sess, "again")
	req = g.awaitRequest(t)
	if code := g.post(t, g.sess.ID, req.ID, `{"action":"deny"}`); code != http.StatusNoContent {
		t.Fatalf("deny = %d", code)
	}
	waitMemoryIdle(t, g.sess)
	// And stopping the old server wipes its copy.
	f.mgr.Shutdown()
	if len(f.mgr.secretMemory.Status().Entries) != 0 {
		t.Fatal("remembered after shutdown")
	}
}

func TestForgetDotAliases(t *testing.T) {
	f := newSecretFixture(t)
	for _, alias := range []string{".", ".."} {
		key := secretaction.MemoryKey{Alias: alias, Kind: secretaction.FieldPassword}
		if _, _, err := f.mgr.secretMemory.RememberMany([]secretaction.MemoryKey{key}, [][]byte{[]byte("x")}, time.Hour, f.mgr.secretMemory.Epoch(), secretaction.MemoryUse{}); err != nil {
			t.Fatal(err)
		}
		st := f.memoryStatusAt(t, "DELETE", "/api/remembered-secrets?alias="+url.QueryEscape(alias))
		if entries, _ := st["entries"].([]any); len(entries) != 0 {
			t.Fatalf("alias %q not forgotten: %v", alias, st)
		}
	}
}
