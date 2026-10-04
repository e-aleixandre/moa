package anthropic

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

const fpSSE = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"m1","model":"claude-opus-5","usage":{"input_tokens":1,"output_tokens":0}}}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}` + "\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n\n"

var plainModel = core.Model{ID: "claude-3-5-haiku-latest", Provider: "anthropic"}

func fpRequest(msgs ...core.Message) core.Request {
	return core.Request{
		Model:    plainModel,
		System:   "system prompt",
		Messages: msgs,
		Tools: []core.ToolSpec{
			{Name: "alpha", Description: "a", Parameters: json.RawMessage(`{"type":"object"}`)},
			{Name: "beta", Description: "b", Parameters: json.RawMessage(`{"type":"object"}`)},
		},
	}
}

func fpOf(t *testing.T, req core.Request, oauth bool) (core.RequestFingerprint, []byte) {
	t.Helper()
	body, err := buildRequestBody(req, oauth)
	if err != nil {
		t.Fatal(err)
	}
	fp, err := fingerprintRequestBody(body, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	return fp, body
}

func convo(n int) []core.Message {
	var msgs []core.Message
	for i := 0; i < n; i++ {
		msgs = append(msgs, core.NewUserMessage(fmt.Sprintf("question %d", i)))
		msgs = append(msgs, core.Message{Role: "assistant", Content: []core.Content{core.TextContent(fmt.Sprintf("answer %d", i))}})
	}
	return append(msgs, core.NewUserMessage("last"))
}

func TestFingerprintBodyHashIsActualWireBytesAndObserverIsInert(t *testing.T) {
	for _, key := range []string{"sk-ant-api03-test", "sk-ant-oat-test"} {
		var mu sync.Mutex
		var bodies [][]byte
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			mu.Lock()
			bodies = append(bodies, raw)
			mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, fpSSE)
		}))
		a := NewWithBaseURL(key, srv.URL)

		run := func(observe func(core.RequestFingerprint)) {
			req := fpRequest(convo(2)...)
			req.Options.OnRequestFingerprint = observe
			ch, err := a.Stream(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			for range ch {
			}
		}
		var got []core.RequestFingerprint
		run(nil)
		run(func(fp core.RequestFingerprint) { got = append(got, fp) })
		srv.Close()

		if len(bodies) != 2 || string(bodies[0]) != string(bodies[1]) {
			t.Fatalf("%s: observer changed the wire body or request count (%d requests)", key, len(bodies))
		}
		sum := sha256.Sum256(bodies[1])
		if len(got) != 1 || got[0].BodySHA256 != hex.EncodeToString(sum[:]) {
			t.Fatalf("%s: captures=%d, body hash does not match the wire bytes", key, len(got))
		}
	}
}

func TestFingerprintOptionsHashFollowsFinalBody(t *testing.T) {
	a, _ := fpOf(t, fpRequest(convo(1)...), false)
	r := fpRequest(convo(1)...)
	r.Options.ThinkingLevel = "off" // resolves to the same body as unset on this model
	b, _ := fpOf(t, r, false)
	if a.OptionsSHA256 != b.OptionsSHA256 || a.BodySHA256 != b.BodySHA256 {
		t.Fatal("requested options that resolve to the same body must hash the same")
	}
	n := 123
	r.Options.MaxTokens = &n
	c, _ := fpOf(t, r, false)
	if c.OptionsSHA256 == a.OptionsSHA256 {
		t.Fatal("a different resolved max_tokens must change the options hash")
	}
	if c.ToolsSHA256 != a.ToolsSHA256 || c.SystemSHA256 != a.SystemSHA256 {
		t.Fatal("options must not leak into the tools/system hashes")
	}
}

func prefixKey(p core.RequestPrefixHash) string {
	return fmt.Sprintf("%s/%d/%d", p.Section, p.Message, p.Block)
}

func TestFingerprintPrefixesAppendStableAndDivergeAtEditedBlock(t *testing.T) {
	short, _ := fpOf(t, fpRequest(convo(2)...), false)
	long, _ := fpOf(t, fpRequest(convo(4)...), false)
	longByKey := map[string]string{}
	for _, p := range long.Prefixes {
		longByKey[prefixKey(p)] = p.SHA256
	}
	// The last user block carries the moving marker in "short"; its content hash
	// must still equal the same block in "long" only where the content matches,
	// so compare everything but the final (differing) message.
	for _, p := range short.Prefixes[:len(short.Prefixes)-1] {
		if longByKey[prefixKey(p)] != p.SHA256 {
			t.Fatalf("prefix %s changed when the conversation grew", prefixKey(p))
		}
	}

	edited := convo(4)
	edited[2] = core.NewUserMessage("question 1 EDITED")
	ed, _ := fpOf(t, fpRequest(edited...), false)
	edByKey := map[string]string{}
	for _, p := range ed.Prefixes {
		edByKey[prefixKey(p)] = p.SHA256
	}
	first := ""
	for _, p := range long.Prefixes {
		if edByKey[prefixKey(p)] != p.SHA256 {
			first = prefixKey(p)
			break
		}
	}
	if first != "messages/2/0" {
		t.Fatalf("first divergence at %q, want messages/2/0", first)
	}

	roleSwap := convo(4)
	roleSwap[1] = core.NewUserMessage("answer 0") // same text, other role
	rs, _ := fpOf(t, fpRequest(roleSwap...), false)
	diff := false
	for i, p := range long.Prefixes {
		if i < len(rs.Prefixes) && rs.Prefixes[i].SHA256 != p.SHA256 {
			diff = true
		}
	}
	if !diff {
		t.Fatal("role is part of the message envelope and must change the prefix hash")
	}
}

func TestFingerprintNestedCacheControlIsContent(t *testing.T) {
	mk := func(top, nested bool) string {
		tool := `{"name":"x","input_schema":{"type":"object"`
		if nested {
			tool += `,"cache_control":{"type":"ephemeral"}`
		}
		tool += `}`
		if top {
			tool += `,"cache_control":{"type":"ephemeral","ttl":"1h"}`
		}
		return `{"model":"m","max_tokens":1,"tools":[` + tool + `}],"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"1","content":[{"type":"text","text":"t","cache_control":{"type":"ephemeral"}}]}]}]}`
	}
	fp := func(top, nested bool) core.RequestFingerprint {
		f, err := fingerprintRequestBody([]byte(mk(top, nested)), time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	base, withTop, withNested := fp(false, false), fp(true, false), fp(false, true)
	if base.ToolsSHA256 != withTop.ToolsSHA256 {
		t.Fatal("a top-level tool marker must not change the tools hash")
	}
	if len(withTop.Breakpoints) != 1 || withTop.Breakpoints[0] != (core.RequestBreakpoint{Section: "tools", Message: -1, Block: 0, TTL: "1h", TTLExplicit: true}) {
		t.Fatalf("breakpoints = %+v", withTop.Breakpoints)
	}
	if base.ToolsSHA256 == withNested.ToolsSHA256 {
		t.Fatal("cache_control nested in a schema is content and must change the hash")
	}
	// tool_result nested content counts as ONE block of the message.
	var msgBlocks int
	for _, p := range base.Prefixes {
		if p.Section == "messages" {
			msgBlocks++
		}
	}
	if msgBlocks != 1 {
		t.Fatalf("tool_result produced %d message prefixes, want 1", msgBlocks)
	}
}

func TestFingerprintTTLAndMovingMarkerKeepContentHashes(t *testing.T) {
	r := fpRequest(convo(2)...)
	a, _ := fpOf(t, r, false)
	r.Options.CacheRetention = "1h"
	b, _ := fpOf(t, r, false)
	if a.BodySHA256 == b.BodySHA256 {
		t.Fatal("different TTL must change the body hash")
	}
	for i := range a.Prefixes {
		if a.Prefixes[i] != b.Prefixes[i] {
			t.Fatal("TTL changed a content prefix hash")
		}
	}
	if a.ToolsSHA256 != b.ToolsSHA256 || a.SystemSHA256 != b.SystemSHA256 || a.OptionsSHA256 != b.OptionsSHA256 {
		t.Fatal("TTL changed a section hash")
	}
	if a.Breakpoints[0].TTL != "5m" || a.Breakpoints[0].TTLExplicit || b.Breakpoints[0].TTL != "1h" || !b.Breakpoints[0].TTLExplicit {
		t.Fatalf("TTL not recorded: %+v / %+v", a.Breakpoints, b.Breakpoints)
	}

	short, _ := fpOf(t, fpRequest(convo(1)...), false)
	long, _ := fpOf(t, fpRequest(convo(2)...), false)
	if short.Breakpoints[len(short.Breakpoints)-1].Message == long.Breakpoints[len(long.Breakpoints)-1].Message {
		t.Fatal("marker should have moved with the last user message")
	}
	off := fpRequest(convo(1)...)
	off.Options.CacheRetention = core.CacheOff
	o, _ := fpOf(t, off, false)
	if len(o.Breakpoints) != 0 {
		t.Fatal("cache off must report no breakpoints")
	}
}

func TestFingerprintNumbersAreNotRoundedThroughFloat64(t *testing.T) {
	h := func(n string) string {
		f, err := fingerprintRequestBody([]byte(`{"model":"m","big":`+n+`,"messages":[]}`), time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		return f.OptionsSHA256
	}
	if h("9007199254740993") == h("9007199254740992") {
		t.Fatal("integers beyond 2^53 collapsed: numbers must be decoded verbatim")
	}
}

func TestFingerprintHelperErrorLeaksNothing(t *testing.T) {
	_, err := fingerprintRequestBody([]byte(`{"model":"SECRET_VALUE`), time.Time{})
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("err = %v", err)
	}
	_, err = fingerprintRequestBody([]byte(`{"messages":["SECRET_VALUE"]}`), time.Time{})
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("err = %v", err)
	}
}

func TestFingerprintAndStoredAuditContainNoContent(t *testing.T) {
	const (
		text = "SENTINEL_PLAIN_TEXT"
		sig  = "SENTINEL_SIGNATURE"
		img  = "U0VOVElORUxfSU1BR0U=" // base64 of SENTINEL_IMAGE
		key  = "sk-ant-api03-SENTINEL_AUTH"
	)
	req := fpRequest(
		core.NewUserMessageWithContent([]core.Content{core.TextContent(text), core.ImageContent(img, "image/png")}),
		core.Message{Role: "assistant", Provider: "anthropic", Model: plainModel.ID, Content: []core.Content{
			{Type: "thinking", Thinking: "SENTINEL_THINKING", ThinkingSignature: sig},
			core.ToolCallContent("t1", "alpha", map[string]any{"arg": "SENTINEL_ARG"}),
		}},
		core.NewToolResultMessage("t1", "alpha", []core.Content{core.TextContent("SENTINEL_RESULT")}, false),
	)
	req.Options.APIKey = key
	var fp core.RequestFingerprint
	req.Options.OnRequestFingerprint = func(f core.RequestFingerprint) { fp = f }

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, fpSSE)
	}))
	defer srv.Close()
	ch, err := NewWithBaseURL("x", srv.URL).Stream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}

	dir := t.TempDir()
	store := session.NewSubagentStore(dir, "s1")
	if err := store.RecordRequestFingerprint("sa-1", "", fp); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(store.Dir(), "sa-1.cache.json"))
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(fp)
	for _, blob := range []string{string(raw), string(encoded)} {
		for _, s := range []string{"SENTINEL", "U0VOVElORUw", sig, key, "sk-ant", "Bearer"} {
			if strings.Contains(blob, s) {
				t.Fatalf("audit contains %q", s)
			}
		}
	}
}

func TestFingerprintRetryIsOneCaptureAndFallbackIsTwo(t *testing.T) {
	var mu sync.Mutex
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		hits++
		n := hits
		mu.Unlock()
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, fpSSE)
	}))
	defer srv.Close()
	var captures []core.RequestFingerprint
	req := fpRequest(convo(1)...)
	req.Options.OnRequestFingerprint = func(f core.RequestFingerprint) { captures = append(captures, f) }
	ch, err := NewWithBaseURL("sk-ant-api03-x", srv.URL).Stream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	if hits != 2 || len(captures) != 1 {
		t.Fatalf("hits=%d captures=%d, want 2 HTTP attempts and 1 logical capture", hits, len(captures))
	}

	// Fast fallback rebuilds the body: two captures, two requests, no extra.
	hits = 0
	captures = nil
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		hits++
		if strings.Contains(string(raw), `"speed":"fast"`) {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"Usage credits are required for fast mode."}}`)
			return
		}
		_, _ = io.WriteString(w, fpSSE)
	}))
	defer fast.Close()
	model, _ := core.ResolveModel("claude-opus-5")
	req = core.Request{Model: model, Messages: []core.Message{core.NewUserMessage("hi")}}
	req.Options.Fast = true
	req.Options.OnRequestFingerprint = func(f core.RequestFingerprint) { captures = append(captures, f) }
	ch, err = NewWithBaseURL("sk-ant-api03-x", fast.URL).Stream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	if hits != 2 || len(captures) != 2 || captures[0].BodySHA256 == captures[1].BodySHA256 || captures[0].OptionsSHA256 == captures[1].OptionsSHA256 {
		t.Fatalf("hits=%d captures=%d, want 2 requests and 2 distinct captures", hits, len(captures))
	}
}

func stressRequest(msgs, blocks int) core.Request {
	var out []core.Message
	for i := 0; i < msgs; i++ {
		var user, asst []core.Content
		for b := 0; b < blocks; b++ {
			user = append(user, core.TextContent(strings.Repeat("u", 400)+fmt.Sprint(i, b)))
			asst = append(asst, core.TextContent(strings.Repeat("a", 400)+fmt.Sprint(i, b)))
		}
		out = append(out, core.NewUserMessageWithContent(user), core.Message{Role: "assistant", Content: asst})
	}
	out = append(out, core.NewUserMessage("last"))
	return fpRequest(out...)
}

func TestFingerprintStoredSizeMeasurement(t *testing.T) {
	for name, req := range map[string]core.Request{"small": fpRequest(convo(2)...), "stress_100x10": stressRequest(100, 10)} {
		fp, body := fpOf(t, req, false)
		store := session.NewSubagentStore(t.TempDir(), "s")
		for i := 0; i < 3; i++ { // size must not grow with the request count
			if err := store.RecordRequestFingerprint("sa-1", "", fp); err != nil {
				t.Fatal(err)
			}
		}
		st, err := os.Stat(filepath.Join(store.Dir(), "sa-1.cache.json"))
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: body=%d bytes prefixes=%d stored=%d bytes", name, len(body), len(fp.Prefixes), st.Size())
	}
}

func BenchmarkFingerprintStress100x10(b *testing.B) {
	body, err := buildRequestBody(stressRequest(100, 10), false)
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := fingerprintRequestBody(body, time.Time{}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStreamBuildBodyNilObserver(b *testing.B) {
	req := stressRequest(100, 10)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := buildRequestBody(req, false); err != nil {
			b.Fatal(err)
		}
	}
}
