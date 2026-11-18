package serve

// The bytes each provider receives across a restart, not just
// core.Request.System.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/memory"
	"github.com/e-aleixandre/moa/pkg/provider/anthropic"
	"github.com/e-aleixandre/moa/pkg/provider/openai"
)

type wireRecorder struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (w *wireRecorder) add(b []byte) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.bodies = append(w.bodies, b)
	return len(w.bodies) - 1
}

func (w *wireRecorder) all() [][]byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([][]byte(nil), w.bodies...)
}

func anthropicWireServer(t *testing.T, rec *wireRecorder) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		n := rec.add(b)
		ev := func(name, data string) string { return "event: " + name + "\ndata: " + data + "\n\n" }
		s := ev("message_start", `{"type":"message_start","message":{"id":"msg_`+fmt.Sprint(n)+`","role":"assistant","model":"claude-opus-5-5","usage":{"input_tokens":5,"output_tokens":1}}}`)
		s += ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`)
		s += ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}`)
		s += ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-`+fmt.Sprint(n)+`"}}`)
		s += ev("content_block_stop", `{"type":"content_block_stop","index":0}`)
		s += ev("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`)
		s += ev("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"ok `+fmt.Sprint(n)+`"}}`)
		s += ev("content_block_stop", `{"type":"content_block_stop","index":1}`)
		s += ev("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`)
		s += ev("message_stop", `{"type":"message_stop"}`)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, s)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func openaiWireServer(t *testing.T, rec *wireRecorder) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		n := rec.add(b)
		ev := func(d string) string { return "data: " + d + "\n\n" }
		text := fmt.Sprintf("ok %d", n)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w,
			ev(`{"type":"response.output_item.added","item":{"type":"message","id":"msg_`+fmt.Sprint(n)+`","role":"assistant","content":[{"type":"output_text","text":""}],"status":"in_progress"}}`)+
				ev(`{"type":"response.output_text.delta","delta":"`+text+`"}`)+
				ev(`{"type":"response.output_item.done","item":{"type":"message","id":"msg_`+fmt.Sprint(n)+`","role":"assistant","content":[{"type":"output_text","text":"`+text+`"}],"status":"completed"}}`)+
				ev(`{"type":"response.completed","response":{"id":"resp_`+fmt.Sprint(n)+`","status":"completed","usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

type wireKind struct {
	name     string
	model    core.Model
	server   func(*testing.T, *wireRecorder) string
	provider func(url string) core.Provider
	msgsKey  string
}

var wireKinds = []wireKind{
	{
		name:   "anthropic-oauth",
		model:  core.Model{ID: "claude-opus-5-5", Provider: "anthropic", MaxInput: 200_000},
		server: anthropicWireServer,
		provider: func(url string) core.Provider {
			return anthropic.NewWithBaseURL("sk-ant-oat-synthetic-not-a-credential", url)
		},
		msgsKey: "messages",
	},
	{
		name:     "anthropic-apikey",
		model:    core.Model{ID: "claude-opus-5-5", Provider: "anthropic", MaxInput: 200_000},
		server:   anthropicWireServer,
		provider: func(url string) core.Provider { return anthropic.NewWithBaseURL("sk-ant-api03-synthetic", url) },
		msgsKey:  "messages",
	},
	{
		name:     "openai-responses",
		model:    core.Model{ID: "gpt-5.3-codex", Provider: "openai", MaxInput: 200_000},
		server:   openaiWireServer,
		provider: func(url string) core.Provider { return openai.NewWithBaseURL("test-key", url) },
		msgsKey:  "input",
	},
}

var (
	wireCacheFirst = regexp.MustCompile(`\{"cache_control":\{[^{}]*\},?`)
	wireCacheLast  = regexp.MustCompile(`,"cache_control":\{[^{}]*\}`)
)

func stripCache(b []byte) []byte {
	return wireCacheLast.ReplaceAll(wireCacheFirst.ReplaceAll(b, []byte("{")), nil)
}

// splitWire returns every top-level field except the conversation, and the
// conversation items, as sent.
func splitWire(t *testing.T, body []byte, msgsKey string) (map[string]json.RawMessage, []json.RawMessage) {
	t.Helper()
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		t.Fatalf("%v\n%s", err, body)
	}
	var msgs []json.RawMessage
	if raw, ok := top[msgsKey]; ok {
		if err := json.Unmarshal(raw, &msgs); err != nil {
			t.Fatal(err)
		}
	}
	delete(top, msgsKey)
	return top, msgs
}

func wireManager(t *testing.T, ctx context.Context, k wireKind, url, sessionDir string, day time.Time) *Manager {
	t.Helper()
	moaCfg := core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off"}
	prov := k.provider(url)
	mgr := NewManager(ctx, ManagerConfig{
		ProviderFactory: func(_ core.Model) (core.Provider, error) { return prov, nil },
		DefaultModel:    k.model,
		WorkspaceRoot:   t.TempDir(),
		MoaCfg:          moaCfg,
		ConfigLoader:    func(string) core.MoaConfig { return moaCfg },
		SessionBaseDir:  sessionDir,
		SchedulePath:    filepath.Join(t.TempDir(), "schedules.json"),
		promptClock:     func() time.Time { return day },
	})
	t.Cleanup(mgr.Shutdown)
	return mgr
}

// changeEverythingElse is a restart a day later in which every frozen input
// moved: AGENTS.md, the last commit, memory, skills and .moa/verify.json.
func changeEverythingElse(t *testing.T, cwd string) {
	t.Helper()
	changeEverything(t, cwd)
	mem := memory.New(core.ConfigDir(), cwd)
	if _, err := mem.Write(memory.Memory{Name: "new-fact", Description: "A fact written after the session froze", Body: "x", Scope: memory.ScopeProject, Lifecycle: memory.LifecycleDurable}); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(cwd, ".moa", "skills", "late-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: late-skill\ndescription: A skill added after the session froze\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".moa", "verify.json"), []byte(`{"checks":[{"name":"t","command":"true"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFrozenReview_RestartSendsTheSameWirePrefix(t *testing.T) {
	for _, k := range wireKinds {
		t.Run(k.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			t.Setenv("HOME", t.TempDir())
			t.Setenv("MOA_CONFIG_DIR", t.TempDir())
			sessionDir := t.TempDir()
			cwd := frozenRepo(t)

			rec1 := &wireRecorder{}
			mgr1 := wireManager(t, ctx, k, k.server(t, rec1), sessionDir, frozenDay1)
			sess, err := mgr1.CreateSession(CreateOpts{CWD: cwd})
			if err != nil {
				t.Fatal(err)
			}
			for i := range 3 {
				sendAndWait(t, mgr1, sess, fmt.Sprintf("turn %d", i))
			}
			b1 := rec1.all()
			before := b1[len(b1)-1]
			stopFrozenTestManager(t, mgr1)

			changeEverythingElse(t, cwd)
			rec2 := &wireRecorder{}
			mgr2 := wireManager(t, ctx, k, k.server(t, rec2), sessionDir, frozenDay2)
			resumed, err := mgr2.ResumeSession(sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			sendAndWait(t, mgr2, resumed, "after the restart")
			b2 := rec2.all()
			after := b2[0]

			top1, msgs1 := splitWire(t, before, k.msgsKey)
			top2, msgs2 := splitWire(t, after, k.msgsKey)
			keys := map[string]bool{}
			for key := range top1 {
				keys[key] = true
			}
			for key := range top2 {
				keys[key] = true
			}
			var sorted []string
			for key := range keys {
				sorted = append(sorted, key)
			}
			sort.Strings(sorted)
			for _, key := range sorted {
				if key == "max_output_tokens" {
					// Not part of the cached prefix; the resumed model resolves
					// its output cap differently.
					continue
				}
				a, b := stripCache(top1[key]), stripCache(top2[key])
				if !bytes.Equal(a, b) {
					t.Errorf("field %q changed across the restart:\n--- before\n%.2000s\n--- after\n%.2000s", key, a, b)
				}
			}
			if len(msgs2) < len(msgs1) {
				t.Fatalf("after the restart the conversation has %d items, before %d", len(msgs2), len(msgs1))
			}
			for i := range msgs1 {
				a, b := stripCache(msgs1[i]), stripCache(msgs2[i])
				if !bytes.Equal(a, b) {
					t.Errorf("conversation item %d changed across the restart:\n--- before\n%s\n--- after\n%s", i, a, b)
				}
			}
			if !strings.Contains(string(before), "sig-") && k.msgsKey == "messages" {
				t.Error("no signed thinking in the prefix: the test would not see a binding change")
			}
			if strings.Contains(string(after), "late-skill") {
				t.Error("the skill added after freezing reached the request")
			}
		})
	}
}
