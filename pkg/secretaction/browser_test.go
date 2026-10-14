package secretaction

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// fakePlaywright registers browser_evaluate/browser_type tools that answer the
// way the Playwright MCP does (verified 13/10: evaluate prints "### Result"
// followed by pretty JSON; type echoes the fill() call with the value).
type fakePlaywright struct {
	mu      sync.Mutex
	origin  string
	fields  map[string]map[string]string // target -> probe
	typed   []map[string]any
	evals   int
	onTyped func()
}

func (f *fakePlaywright) register(reg *core.Registry) {
	reg.Register(core.Tool{ //nolint:errcheck
		Name:       "mcp__playwright__browser_evaluate",
		Parameters: json.RawMessage(`{"type":"object","properties":{"function":{"type":"string"},"element":{"type":"string"},"target":{"type":"string"}},"required":["function"]}`),
		Execute: func(_ context.Context, args map[string]any, _ func(core.Result)) (core.Result, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.evals++
			target, _ := args["target"].(string)
			var payload any
			if target == "" {
				payload = f.origin
			} else {
				probe, ok := f.fields[target]
				if !ok {
					return core.ErrorResult("### Error\nRef " + target + " not found"), nil
				}
				m := map[string]any{"origin": f.origin}
				for k, v := range probe {
					m[k] = v
				}
				payload = m
			}
			out, _ := json.MarshalIndent(payload, "", "  ")
			return core.TextResult("### Result\n" + string(out) + "\n### Ran Playwright code\n```js\nawait page.evaluate('...');\n```"), nil
		},
	})
	reg.Register(core.Tool{ //nolint:errcheck
		Name:       "mcp__playwright__browser_type",
		Parameters: json.RawMessage(`{"type":"object","properties":{"element":{"type":"string"},"target":{"type":"string"},"text":{"type":"string"},"submit":{"type":"boolean"}},"required":["target","text"]}`),
		Execute: func(_ context.Context, args map[string]any, _ func(core.Result)) (core.Result, error) {
			f.mu.Lock()
			f.typed = append(f.typed, args)
			cb := f.onTyped
			f.mu.Unlock()
			if cb != nil {
				cb()
			}
			text, _ := args["text"].(string)
			target, _ := args["target"].(string)
			body := fmt.Sprintf("### Ran Playwright code\n```js\nawait page.locator('%s').fill('%s');\n```\n### Snapshot\n```yaml\n- textbox \"Password\" [ref=e2]: %s\n```", target, strings.ReplaceAll(text, "'", `\'`), text)
			return core.TextResult(body), nil
		},
	})
}

func newBrowserFixture(t *testing.T) (*core.Registry, *Broker, *fakePlaywright, chan Request) {
	t.Helper()
	reg := core.NewRegistry()
	f := &fakePlaywright{
		origin: "https://idmsa.apple.com",
		fields: map[string]map[string]string{
			"e1": {"type": "email", "name": "account", "id": "account_name", "label": "Email or Phone Number", "autocomplete": "username"},
			"e2": {"type": "password", "name": "password", "id": "password_text", "label": "Password", "autocomplete": "current-password"},
		},
	}
	f.register(reg)
	broker := NewBroker("sess-b")
	requests := make(chan Request, 4)
	broker.SetHooks(func(r Request) { requests <- r }, nil)
	SyncBrowserFillTool(reg, broker)
	return reg, broker, f, requests
}

func runBrowserFill(t *testing.T, reg *core.Registry, params map[string]any) chan core.Result {
	t.Helper()
	tool, ok := reg.Get(BrowserFillToolName)
	if !ok {
		t.Fatal("browser_fill_secret not registered despite Playwright tools")
	}
	out := make(chan core.Result, 1)
	go func() {
		res, err := tool.Execute(context.Background(), params, nil)
		if err != nil {
			res = core.ErrorResult(err.Error())
		}
		out <- res
	}()
	return out
}

func loginParams(submit bool) map[string]any {
	return map[string]any{
		"fields": []any{
			map[string]any{"target": "e1", "element": "Apple ID field", "kind": "username", "label": "Apple ID", "alias": "apple-id"},
			map[string]any{"target": "e2", "element": "password field", "kind": "password", "label": "Password"},
		},
		"submit": submit,
	}
}

func resultText(r core.Result) string {
	var sb strings.Builder
	for _, c := range r.Content {
		sb.WriteString(c.Text)
	}
	return sb.String()
}

func TestBrowserFillToolRegisteredOnlyWithPlaywright(t *testing.T) {
	reg := core.NewRegistry()
	broker := NewBroker("s")
	SyncBrowserFillTool(reg, broker)
	if _, ok := reg.Get(BrowserFillToolName); ok {
		t.Fatal("registered without any browser MCP")
	}
	(&fakePlaywright{}).register(reg)
	SyncBrowserFillTool(reg, broker)
	if _, ok := reg.Get(BrowserFillToolName); !ok {
		t.Fatal("not registered with browser_type + browser_evaluate present")
	}
	reg.Unregister("mcp__playwright__browser_evaluate")
	SyncBrowserFillTool(reg, broker)
	if _, ok := reg.Get(BrowserFillToolName); ok {
		t.Fatal("still registered after browser_evaluate went away")
	}
}

func TestBrowserFillShowsRealOriginAndFillsRedacted(t *testing.T) {
	reg, broker, f, requests := newBrowserFixture(t)
	done := runBrowserFill(t, reg, loginParams(true))
	var req Request
	select {
	case req = <-requests:
	case <-time.After(2 * time.Second):
		t.Fatal("no card")
	}
	if req.Kind != KindBrowser || req.Origin != "https://idmsa.apple.com" || !req.Submit {
		t.Fatalf("card = %+v", req)
	}
	if len(req.Fields) != 2 || req.Fields[0].RealLabel != "Email or Phone Number" || req.Fields[0].RealType != "email" ||
		req.Fields[1].RealType != "password" || req.Fields[1].Autocomplete != "current-password" || req.Fields[0].Alias != "apple-id" ||
		req.Fields[1].Element != "password field" || req.Fields[1].Index != 1 {
		t.Fatalf("card fields = %+v", req.Fields)
	}
	if want := BrowserFingerprint(req.Origin, req.Fields, true); req.ActionFingerprint != want || want == "" {
		t.Fatalf("fingerprint %q, want %q", req.ActionFingerprint, want)
	}
	if err := broker.Resolve(req.ID, true, [][]byte{[]byte("me@icloud.com"), []byte("Hunter2'not-real")}); err != nil {
		t.Fatal(err)
	}
	res := <-done
	text := resultText(res)
	if res.IsError {
		t.Fatalf("fill failed: %s", text)
	}
	for _, leak := range []string{"Hunter2", "me@icloud.com", "not-real"} {
		if strings.Contains(text, leak) {
			t.Fatalf("result leaks %q:\n%s", leak, text)
		}
	}
	if !strings.Contains(text, "Apple ID") || !strings.Contains(text, "Password") || !strings.Contains(text, Placeholder) {
		t.Fatalf("result does not report the fill:\n%s", text)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.typed) != 2 || f.typed[0]["text"] != "me@icloud.com" || f.typed[1]["text"] != "Hunter2'not-real" ||
		f.typed[0]["submit"] == true || f.typed[1]["submit"] != true || f.typed[1]["target"] != "e2" {
		t.Fatalf("typed = %+v", f.typed)
	}
}

func TestBrowserFillAbortsWhenOriginChanges(t *testing.T) {
	reg, broker, f, requests := newBrowserFixture(t)
	done := runBrowserFill(t, reg, loginParams(false))
	req := <-requests
	f.mu.Lock()
	f.origin = "https://evil.example"
	f.mu.Unlock()
	if err := broker.Resolve(req.ID, true, [][]byte{[]byte("u"), []byte("p")}); err != nil {
		t.Fatal(err)
	}
	res := <-done
	if !strings.Contains(resultText(res), "page origin changed; nothing was filled") {
		t.Fatalf("result = %s", resultText(res))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.typed) != 0 {
		t.Fatalf("typed after origin change: %+v", f.typed)
	}
}

func TestBrowserFillProbeFailureShowsNoCard(t *testing.T) {
	reg, broker, _, _ := newBrowserFixture(t)
	params := map[string]any{"fields": []any{map[string]any{"target": "missing", "element": "x", "kind": "password", "label": "Password"}}}
	res := <-runBrowserFill(t, reg, params)
	if !res.IsError {
		t.Fatalf("probe failure not an error: %s", resultText(res))
	}
	if len(broker.Pending()) != 0 {
		t.Fatal("card shown despite probe failure")
	}
}

func TestBrowserFillDenied(t *testing.T) {
	reg, broker, f, requests := newBrowserFixture(t)
	done := runBrowserFill(t, reg, loginParams(true))
	req := <-requests
	if err := broker.Resolve(req.ID, false, nil); err != nil {
		t.Fatal(err)
	}
	res := <-done
	if !res.IsError || res.Custom["rejected"] != true {
		t.Errorf("human denial must carry structured rejection: %+v", res)
	}
	if !strings.Contains(strings.ToLower(resultText(res)), "denied") {
		t.Fatalf("deny result = %s", resultText(res))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.typed) != 0 {
		t.Fatal("typed after deny")
	}
}

func TestBrowserFillCancellationIsNotDenial(t *testing.T) {
	reg, broker, f, _ := newBrowserFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker.SetHooks(func(Request) { cancel() }, nil)
	tool, _ := reg.Get(BrowserFillToolName)
	res, err := tool.Execute(ctx, loginParams(false), nil)
	if err != nil || !res.IsError || res.Custom["rejected"] == true || resultText(res) != "Error: cancelled" {
		t.Errorf("cancellation is not human denial: result=%+v error=%v", res, err)
	}
	if len(f.typed) != 0 {
		t.Fatal("typed after cancellation")
	}
}
