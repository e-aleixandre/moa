package history

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/askuser"
	"github.com/e-aleixandre/moa/pkg/core"
)

// Reproductions from the independent review of 981ebdea: every output,
// errors included, stays within MaxOutputBytes, and the walk honours
// cancellation.

func TestSearchOutputIsBoundedWithALongQuery(t *testing.T) {
	query := strings.Repeat("x", MaxQueryBytes)
	var msgs []core.AgentMessage
	for i := 0; i < 100; i++ {
		msgs = append(msgs, askResult(fmt.Sprintf("m%015d", i), query, askuser.Responder{Kind: "user", Via: "network"}))
	}
	out, bad := run(t, msgs, map[string]any{"action": "search", "query": query, "limit": 50})
	if bad || len(out) > MaxOutputBytes {
		t.Fatalf("output bytes=%d, max=%d (error=%v)", len(out), MaxOutputBytes, bad)
	}
	if !strings.Contains(out, "Continue with after=") {
		t.Fatalf("a cut result must say how to continue: %q", out[len(out)-200:])
	}
}

func TestSearchOutputIsBoundedWithAnEscapedQuery(t *testing.T) {
	query := strings.Repeat("\x00", MaxQueryBytes)
	var msgs []core.AgentMessage
	for i := 0; i < 100; i++ {
		msgs = append(msgs, msg(fmt.Sprintf("m%015d", i), "tool_result", query))
	}
	out, bad := run(t, msgs, map[string]any{"action": "search", "query": query, "limit": 50})
	if bad || len(out) > MaxOutputBytes {
		t.Fatalf("output bytes=%d, max=%d (error=%v)", len(out), MaxOutputBytes, bad)
	}
}

func TestAroundOutputIsBoundedWithLongOwnerNames(t *testing.T) {
	var msgs []core.AgentMessage
	for i := 0; i < 41; i++ {
		msgs = append(msgs, askResult(fmt.Sprintf("m%015d", i), "yes", askuser.Responder{Kind: "owner", Name: strings.Repeat("n", 400)}))
	}
	out, bad := run(t, msgs, map[string]any{"action": "around", "id": "m000000000000020", "before": 20, "after_count": 20})
	if bad || len(out) > MaxOutputBytes {
		t.Fatalf("output bytes=%d, max=%d (error=%v)", len(out), MaxOutputBytes, bad)
	}
	if strings.Count(out, "\n") != 41 {
		t.Fatalf("want all 41 messages of the window, got %d lines", strings.Count(out, "\n"))
	}
}

func TestUnknownIDErrorIsBounded(t *testing.T) {
	out, bad := run(t, nil, map[string]any{"action": "read", "id": strings.Repeat("x", MaxOutputBytes*2)})
	if !bad || len(out) > MaxOutputBytes {
		t.Fatalf("error output bytes=%d, max=%d (error=%v)", len(out), MaxOutputBytes, bad)
	}
}

func TestAlreadyCancelledCallDoesNotWalk(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	tool := NewTool(func(fn func(*core.AgentMessage)) error {
		called = true
		m := msg("x", "user", "hello")
		fn(&m)
		return nil
	})
	if _, err := tool.Execute(ctx, map[string]any{"action": "search", "query": "hello"}, nil); called || err == nil {
		t.Fatalf("cancelled Execute walks=%v, err=%v", called, err)
	}
}

func TestCancellationStopsTheWalk(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tool := NewTool(func(fn func(*core.AgentMessage)) error {
		for i := 0; i < 10000; i++ {
			if i == 500 {
				cancel()
			}
			m := msg(fmt.Sprintf("m%d", i), "user", "hello")
			fn(&m)
		}
		return nil
	})
	if _, err := tool.Execute(ctx, map[string]any{"action": "search", "query": "hello"}, nil); err == nil {
		t.Fatal("a call cancelled mid-walk returned success")
	}
}

func TestDecomposedAccentsMatch(t *testing.T) {
	msgs := []core.AgentMessage{msg("nfd", "user", "Cafe\u0301: decisio\u0301n final"), msg("nfc", "user", "Café con decisión")}
	for _, q := range []string{"cafe decision", "café decisión", "Cafe\u0301 decisio\u0301n"} {
		out, bad := run(t, msgs, map[string]any{"action": "search", "query": q})
		if bad || !strings.Contains(out, "id=nfd") || !strings.Contains(out, "id=nfc") {
			t.Fatalf("query %q: %q", q, out)
		}
	}
	// The snippet quotes the original text, marks included.
	out, _ := run(t, msgs, map[string]any{"action": "search", "query": "final"})
	if !strings.Contains(out, "decisio\u0301n final") {
		t.Fatalf("snippet lost the original text: %q", out)
	}
}

// A huge message is folded a chunk at a time: a term across the cut still
// matches, and the snippet sits on it.
func TestHugeMessageMatchesAcrossChunks(t *testing.T) {
	body := strings.Repeat("a", foldChunk-3) + "NEEDLE" + strings.Repeat("b", 3*foldChunk) + " Último"
	out, bad := run(t, []core.AgentMessage{msg("big", "tool_result", body)}, map[string]any{"action": "search", "query": "needle ultimo"})
	if bad || !strings.Contains(out, "id=big") || !strings.Contains(out, "NEEDLE") {
		t.Fatalf("%q", out)
	}
	if out, _ := run(t, []core.AgentMessage{msg("big", "tool_result", body)}, map[string]any{"action": "search", "query": "needle absent"}); !strings.HasPrefix(out, "0 matches") {
		t.Fatalf("%q", out)
	}
}
