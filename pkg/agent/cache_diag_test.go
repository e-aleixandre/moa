package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

func pfx(section string, msg, blk int, h string) core.RequestPrefixHash {
	return core.RequestPrefixHash{Section: section, Message: msg, Block: blk, SHA256: h}
}

func fpA() core.RequestFingerprint {
	return core.RequestFingerprint{
		BodySHA256: "aaaaaaaaaaaaaaaaaaaa", ToolsSHA256: "t", SystemSHA256: "s", OptionsSHA256: "o",
		Prefixes:    []core.RequestPrefixHash{pfx("tools", -1, 0, "h1"), pfx("system", -1, 0, "h2"), pfx("messages", 0, 0, "h3"), pfx("messages", 1, 0, "h4")},
		Breakpoints: []core.RequestBreakpoint{{Section: "messages", Message: 1, Block: 0, TTL: "1h", TTLExplicit: true}},
	}
}

func TestDiffFingerprintsFirstDivergence(t *testing.T) {
	cur := core.RequestFingerprint{
		BodySHA256: "bbbbbbbbbbbbbbbbbbbb", ToolsSHA256: "t", SystemSHA256: "s", OptionsSHA256: "o",
		Prefixes:    []core.RequestPrefixHash{pfx("tools", -1, 0, "h1"), pfx("system", -1, 0, "h2"), pfx("messages", 0, 0, "h3"), pfx("messages", 1, 0, "CHANGED"), pfx("messages", 2, 0, "h5")},
		Breakpoints: []core.RequestBreakpoint{{Section: "messages", Message: 2, Block: 0, TTL: "5m"}},
	}
	got := attrMap(diffFingerprints(fpA(), cur))
	want := map[string]any{
		"common_blocks": 3, "last_common": "messages:0:0", "first_diff": "messages:1:0",
		"prev_is_prefix": false, "read_point": "none",
		"tools_same": true, "system_same": true, "options_same": true,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v (all: %v)", k, got[k], v, got)
		}
	}
}

func TestDiffFingerprintsAppendKeepsReadPoint(t *testing.T) {
	cur := fpA()
	cur.BodySHA256 = "cccccccccccccccccccc"
	cur.Prefixes = append(cur.Prefixes, pfx("messages", 2, 0, "h5"))
	got := attrMap(diffFingerprints(fpA(), cur))
	if got["prev_is_prefix"] != true || got["common_blocks"] != 4 || got["first_diff"] != "messages:2:0" || got["read_point"] != "messages:1:0=1h" {
		t.Errorf("append diff = %v", got)
	}
}

func attrMap(a []any) map[string]any {
	m := map[string]any{}
	for i := 0; i+1 < len(a); i += 2 {
		m[a[i].(string)] = a[i+1]
	}
	return m
}

func TestCacheDiagSinkIsOffWithoutFlag(t *testing.T) {
	t.Setenv("MOA_DEBUG_CACHE", "")
	if NewCacheDiag("k") != nil {
		t.Fatal("sink must not exist without the flag")
	}
}

func TestCacheDiagSinkLinksRequestsAndLogsNoContent(t *testing.T) {
	t.Setenv("MOA_DEBUG_CACHE", "1")
	var buf bytes.Buffer
	prev := slog.Default()
	defer slog.SetDefault(prev)
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))

	sink := NewCacheDiag("moa:session:abc")
	if sink == nil {
		t.Fatal("no sink with the flag on")
	}
	sink(func() (core.RequestFingerprint, error) { return fpA(), nil })
	cur := fpA()
	cur.Prefixes = append(cur.Prefixes, pfx("messages", 2, 0, "h5"))
	sink(func() (core.RequestFingerprint, error) { return cur, nil })

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %d: %s", len(lines), buf.String())
	}
	var first, second map[string]any
	_ = json.Unmarshal([]byte(lines[0]), &first)
	_ = json.Unmarshal([]byte(lines[1]), &second)
	if first["msg"] != "request fingerprint" || first["cache_key"] != "moa:session:abc" {
		t.Errorf("first = %v", first)
	}
	if _, linked := first["prev_is_prefix"]; linked {
		t.Errorf("first request has nothing to compare: %v", first)
	}
	if second["prev_is_prefix"] != true {
		t.Errorf("second = %v", second)
	}
}

func TestRequestShapeIsOffWithoutFlag(t *testing.T) {
	t.Setenv("MOA_DEBUG_CACHE", "")
	var buf bytes.Buffer
	prev := slog.Default()
	defer slog.SetDefault(prev)
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	var s requestShape
	s.log(core.Request{Messages: []core.Message{core.NewUserMessage("x")}})
	if buf.Len() != 0 {
		t.Fatalf("shape logged without the flag: %s", buf.String())
	}
}

// The prefix link must survive the end of a run: the first request of the
// next Send is compared against the last request of the previous one.
func TestRequestShapeLinksAcrossRuns(t *testing.T) {
	t.Setenv("MOA_DEBUG_CACHE", "1")
	var buf bytes.Buffer
	prev := slog.Default()
	defer slog.SetDefault(prev)
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))

	prov := NewMockProvider(simpleTextResponse("one"), simpleTextResponse("two"))
	ag, err := New(AgentConfig{Provider: prov, Model: core.Model{ID: "m", Provider: "openai"}, SystemPrompt: "s", MaxTurns: 5, MaxRunDuration: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ag.Run(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := ag.Send(context.Background(), "second"); err != nil {
		t.Fatal(err)
	}
	var shapes []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil && m["msg"] == "request shape" {
			shapes = append(shapes, m)
		}
	}
	if len(shapes) != 2 {
		t.Fatalf("shapes = %d: %s", len(shapes), buf.String())
	}
	if shapes[1]["prev_is_prefix"] != true {
		t.Errorf("second run's shape is not linked to the first: %v", shapes[1])
	}
}

// A repaired response is one message built from several requests: it keeps the
// quota of the last response, like its usage.
func TestMergeAssistantTakesLatestQuota(t *testing.T) {
	a := &core.Message{Role: "assistant", RateLimit: &core.RateLimit{FiveHourUtil: 0.25}}
	b := &core.Message{Role: "assistant", RateLimit: &core.RateLimit{FiveHourUtil: 0.26}}
	if got := mergeAssistant(a, b).RateLimit.FiveHourUtil; got != 0.26 {
		t.Errorf("merged quota = %v, want 0.26", got)
	}
	if got := mergeAssistant(a, &core.Message{Role: "assistant"}).RateLimit; got == nil || got.FiveHourUtil != 0.25 {
		t.Errorf("a response without headers must not erase the earlier reading: %v", got)
	}
}
