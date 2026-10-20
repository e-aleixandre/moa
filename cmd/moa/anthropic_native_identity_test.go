package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/e-aleixandre/moa/pkg/auth"
	"github.com/e-aleixandre/moa/pkg/core"
)

const nativeIdentityPreamble = "You are Claude Code, Anthropic's official CLI for Claude."

// The only allowed fallback body delta is the adapter-generated first system
// block. Compare every other raw JSON field, including cache markers, verbatim.
func assertBackupIdentityDelta(t *testing.T, plan, api []byte) {
	t.Helper()
	var before, after map[string]json.RawMessage
	if err := json.Unmarshal(plan, &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(api, &after); err != nil {
		t.Fatal(err)
	}
	var system []json.RawMessage
	if err := json.Unmarshal(before["system"], &system); err != nil || len(system) == 0 {
		t.Fatalf("plan has no generated identity: %v", err)
	}
	var identity map[string]any
	if err := json.Unmarshal(system[0], &identity); err != nil {
		t.Fatal(err)
	}
	wantIdentity := map[string]any{"type": "text", "text": nativeIdentityPreamble}
	if len(system) == 1 {
		wantIdentity["cache_control"] = map[string]any{"type": "ephemeral"}
		if cache, ok := identity["cache_control"].(map[string]any); ok && cache["ttl"] == "1h" {
			wantIdentity["cache_control"] = map[string]any{"type": "ephemeral", "ttl": "1h"}
		}
	}
	if !reflect.DeepEqual(identity, wantIdentity) {
		t.Fatalf("unexpected generated identity block: %s", system[0])
	}
	if len(system) == 1 {
		if _, ok := after["system"]; ok {
			t.Error("API must omit system when only the generated identity existed")
		}
	} else {
		want, err := json.Marshal(system[1:])
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(after["system"], want) {
			t.Errorf("API must remove only the generated identity block; system got %s want %s", after["system"], want)
		}
	}
	delete(before, "system")
	delete(after, "system")
	if !reflect.DeepEqual(before, after) {
		t.Error("fallback changed a field outside the identity allowlist")
	}
}

type nativeIdentityCall struct {
	body   []byte
	header http.Header
}

type nativeIdentityWire struct {
	mu    sync.Mutex
	calls []nativeIdentityCall
	serve func(nativeIdentityCall, int) (int, http.Header, string)
}

type nativeIdentityRoute struct{ http.RoundTripper }

func (tr nativeIdentityRoute) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := tr.RoundTripper.RoundTrip(r)
	if resp != nil {
		// Quota admission validates the original pinned production origin,
		// not the local socket destination used by the test transport.
		resp.Request = r
	}
	return resp, err
}

func newNativeIdentityWire(t *testing.T, p *snapshotProvider) *nativeIdentityWire {
	t.Helper()
	tr := &nativeIdentityWire{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test-Origin") != "api.anthropic.com" || r.URL.Path != "/v1/messages" || r.Method != http.MethodPost {
			t.Error("unexpected HTTP route")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		call := nativeIdentityCall{body: body, header: r.Header.Clone()}
		tr.mu.Lock()
		n := len(tr.calls)
		tr.calls = append(tr.calls, call)
		tr.mu.Unlock()
		status, header, text := tr.serve(call, n)
		for k, values := range header {
			w.Header()[k] = values
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, text)
	}))
	t.Cleanup(srv.Close)
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	p.httpClient = &http.Client{Transport: nativeIdentityRoute{routeTransport{target: target, base: srv.Client().Transport}}}
	t.Cleanup(p.httpClient.CloseIdleConnections)
	return tr
}

func (tr *nativeIdentityWire) snapshot() []nativeIdentityCall {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return append([]nativeIdentityCall(nil), tr.calls...)
}

func nativeIdentityRequest() core.Request {
	model := core.Model{ID: "claude-opus-5-5", Provider: "anthropic"}
	return core.Request{
		Model: model, System: "Synthetic system: " + nativeIdentityPreamble,
		Tools: []core.ToolSpec{
			{Name: "read", Parameters: json.RawMessage(`{"type":"object","properties":{"payload":{"type":"object"}}}`)},
			{Name: "opaque_tool", Parameters: json.RawMessage(`{"type":"object","properties":{"payload":{"type":"object"}}}`)},
		},
		Messages: []core.Message{
			core.NewUserMessage(nativeIdentityPreamble),
			{Role: "assistant", Provider: "anthropic", Model: model.ID, Content: []core.Content{
				{Type: "thinking", Thinking: "synthetic reasoning", ThinkingSignature: "synthetic-signature-not-cryptographic"},
				{Type: "thinking", Redacted: true, ThinkingSignature: "synthetic-redacted-not-cryptographic"},
				core.ToolCallContent("historical-read", "read", map[string]any{"payload": map[string]any{"text": nativeIdentityPreamble}}),
				core.ToolCallContent("historical-opaque", "opaque_tool", map[string]any{"payload": map[string]any{"values": []any{"a", float64(7), true}}}),
			}},
			{Role: "tool_result", ToolCallID: "historical-read", ToolName: "read", Content: []core.Content{core.TextContent("historical read result")}},
			{Role: "tool_result", ToolCallID: "historical-opaque", ToolName: "opaque_tool", Content: []core.Content{core.TextContent(nativeIdentityPreamble)}},
			core.NewUserMessage("continue synthetic history"),
		},
		Options: core.StreamOptions{ThinkingLevel: "high", CacheRetention: "1h", MaxTokens: new(4096)},
	}
}

func assertNativeAPIHeaders(t *testing.T, h http.Header) {
	t.Helper()
	if h.Get("X-API-Key") != "fake-api-backup" || h.Get("Authorization") != "" || h.Get("x-app") != "" || strings.Contains(h.Get("User-Agent"), "claude-cli/") {
		t.Error("effective backup client did not identify as API")
	}
	if h.Get("anthropic-beta") != "interleaved-thinking-2025-05-14,fine-grained-tool-streaming-2025-05-14,thinking-binding-controls-2026-08-01" {
		t.Errorf("backup betas changed: %s", h.Get("anthropic-beta"))
	}
}

func TestNativeIdentityFallbackBody(t *testing.T) {
	for _, scope := range []string{"five_hour", "seven_day"} {
		for _, system := range []string{"", "real synthetic system", nativeIdentityPreamble} {
			name := "empty"
			if system != "" {
				name = "real"
				if system == nativeIdentityPreamble {
					name = "user_identity_verbatim"
				}
			}
			t.Run(scope+"/"+name, func(t *testing.T) {
				p, _, _, _ := backupRuntime(t)
				req := nativeIdentityRequest()
				req.System = system
				var fingerprints []core.RequestFingerprint
				req.Options.OnRequestFingerprint = func(build core.RequestFingerprintFunc) {
					fp, err := build()
					if err != nil {
						t.Fatal(err)
					}
					fingerprints = append(fingerprints, fp)
				}
				tr := newNativeIdentityWire(t, p)
				tr.serve = func(_ nativeIdentityCall, n int) (int, http.Header, string) {
					if n == 0 {
						return backupReject(scope)
					}
					return backupOK("end_turn")
				}
				finishBackup(t, p, context.Background(), req)
				calls := tr.snapshot()
				if len(calls) != 2 {
					t.Fatalf("requests=%d", len(calls))
				}
				assertBackupIdentityDelta(t, calls[0].body, calls[1].body)
				assertNativeAPIHeaders(t, calls[1].header)
				// Also exercise an existing, pre-change continuation binding: no
				// new fields or migration are required to obtain the corrected body.
				st, err := p.authStore.AnthropicBackupStatus()
				if err != nil {
					t.Fatal(err)
				}
				req.Options.ProviderBinding = &core.ProviderSource{Kind: "api_backup", PrimaryGeneration: st.Revision.Primary, BackupGeneration: st.Revision.Key, WireProfile: "oauth", Provider: req.Model.Provider, Model: req.Model.ID}
				finishBackup(t, p, context.Background(), req)
				calls = tr.snapshot()
				if len(calls) != 3 || !bytes.Equal(calls[1].body, calls[2].body) {
					t.Fatal("old binding changed body or origin")
				}
				assertNativeAPIHeaders(t, calls[2].header)
				if len(fingerprints) != len(calls) {
					t.Fatalf("fingerprints=%d requests=%d", len(fingerprints), len(calls))
				}
				for i, fp := range fingerprints {
					if fp.BodySHA256 != fmt.Sprintf("%x", sha256.Sum256(calls[i].body)) {
						t.Errorf("fingerprint %d does not describe actual body", i)
					}
				}
			})
		}
	}
}

func TestNativeIdentityUnchangedFixtures(t *testing.T) {
	for _, kind := range []string{"oauth", "api_key"} {
		t.Run(kind, func(t *testing.T) {
			f := newRuntimeFixture(t)
			f.commit(t, f.store, "anthropic", auth.Credential{Type: kind, Key: "fake-primary-api", Access: "fake-primary-oauth", Refresh: "fake-no-refresh", Expires: 4102444800000})
			req := nativeIdentityRequest()
			p := &snapshotProvider{model: req.Model, authStore: f.store}
			tr := newNativeIdentityWire(t, p)
			tr.serve = func(nativeIdentityCall, int) (int, http.Header, string) { return backupOK("end_turn") }
			finishBackup(t, p, context.Background(), req)
			calls := tr.snapshot()
			if len(calls) != 1 {
				t.Fatalf("requests=%d", len(calls))
			}
			fixture := filepath.Join("testdata", "anthropic-native-identity", kind+".json")
			want, err := os.ReadFile(fixture)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(calls[0].body, want) {
				t.Fatal("OAuth/ordinary API bytes changed from base fixture")
			}
			if bytes.Count(calls[0].body, []byte(nativeIdentityPreamble)) < 3 {
				t.Fatal("user-provided identity text was stripped")
			}
		})
	}
}

func nativeIdentityToolResponse() string {
	text := "event: message_start\ndata: {\"message\":{\"model\":\"claude-opus-5-5\",\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n"
	for n, tool := range []string{"Read", "opaque_tool"} {
		text += fmt.Sprintf("event: content_block_start\ndata: {\"index\":%d,\"content_block\":{\"type\":\"tool_use\",\"id\":\"new-%d\",\"name\":%q}}\n\nevent: content_block_delta\ndata: {\"index\":%d,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"payload\\\":{\\\"values\\\":[\\\"a\\\",7,true]}}\"}}\n\nevent: content_block_stop\ndata: {}\n\n", n, n, tool, n)
	}
	return text + "event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":2}}\n\nevent: message_stop\ndata: {}\n\n"
}

func TestNativeIdentityAgentToolRoundTrip(t *testing.T) {
	p, _, _, _ := backupRuntime(t)
	tr := newNativeIdentityWire(t, p)
	reg := core.NewRegistry()
	var mu sync.Mutex
	executed := map[string]int{}
	for _, name := range []string{"read", "opaque_tool"} {
		if err := reg.Register(core.Tool{Name: name, Parameters: nativeIdentityRequest().Tools[0].Parameters, Execute: func(_ context.Context, args map[string]any, _ func(core.Result)) (core.Result, error) {
			mu.Lock()
			defer mu.Unlock()
			executed[name]++
			if !reflect.DeepEqual(args, map[string]any{"payload": map[string]any{"values": []any{"a", float64(7), true}}}) {
				t.Errorf("decoded arguments changed: %#v", args)
			}
			return core.TextResult("result for " + name), nil
		}}); err != nil {
			t.Fatal(err)
		}
	}
	a, fs, persisted := newBackupAgent(t, p, reg)
	seed := nativeIdentityRequest()
	history := make([]core.AgentMessage, len(seed.Messages))
	for i, m := range seed.Messages {
		history[i] = core.WrapMessage(m)
	}
	if err := a.LoadMessages(history); err != nil {
		t.Fatal(err)
	}
	tr.serve = func(_ nativeIdentityCall, n int) (int, http.Header, string) {
		switch n {
		case 0:
			return backupReject("five_hour")
		case 1:
			return 200, http.Header{}, nativeIdentityToolResponse()
		default:
			return backupOK("end_turn")
		}
	}
	if _, err := a.SendWithMsgID(context.Background(), "continue with tools", "stable-user"); err != nil {
		t.Fatal(err)
	}
	calls := tr.snapshot()
	if len(calls) != 3 {
		t.Fatalf("HTTP calls=%d", len(calls))
	}
	assertBackupIdentityDelta(t, calls[0].body, calls[1].body)
	for _, call := range calls[1:] {
		assertNativeAPIHeaders(t, call.header)
		for _, token := range []string{"synthetic-signature-not-cryptographic", "synthetic-redacted-not-cryptographic", "historical-read", "historical-opaque", nativeIdentityPreamble, `"name":"Read"`, `"name":"opaque_tool"`, `"prefix_mismatch_behavior":"drop_block"`} {
			if !bytes.Contains(call.body, []byte(token)) {
				t.Errorf("history/codec lost %s", token)
			}
		}
	}
	for _, token := range []string{`"id":"new-0"`, `"id":"new-1"`, `"tool_use_id":"new-0"`, `"tool_use_id":"new-1"`, `"input":{"payload":{"values":["a",7,true]}}`, "result for read", "result for opaque_tool"} {
		if !bytes.Contains(calls[2].body, []byte(token)) {
			t.Errorf("tool continuation lost %s", token)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(executed, map[string]int{"read": 1, "opaque_tool": 1}) {
		t.Errorf("tools replayed or codec broken: %v", executed)
	}
	persisted.Messages = a.Messages()
	if err := fs.Save(persisted); err != nil {
		t.Fatal(err)
	}
	loaded, err := fs.Load(persisted.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(loaded.Entries)
	if err != nil || !bytes.Contains(data, []byte("synthetic-signature-not-cryptographic")) || !bytes.Contains(data, []byte("synthetic-redacted-not-cryptographic")) {
		t.Fatal("FileStore lost local signed history")
	}
}
