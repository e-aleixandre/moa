package history

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/askuser"
	"github.com/e-aleixandre/moa/pkg/core"
)

func msg(id, role, text string) core.AgentMessage {
	return core.AgentMessage{Message: core.Message{MsgID: id, Role: role, Content: []core.Content{core.TextContent(text)}, Timestamp: time.Date(2026, 10, 12, 9, 13, 0, 0, time.UTC).Unix()}}
}

func visitorOf(msgs []core.AgentMessage) Visitor {
	return func(fn func(*core.AgentMessage)) error {
		for i := range msgs {
			fn(&msgs[i])
		}
		return nil
	}
}

func run(t *testing.T, msgs []core.AgentMessage, params map[string]any) (string, bool) {
	t.Helper()
	res, err := NewTool(visitorOf(msgs)).Execute(context.Background(), params, nil)
	if err != nil {
		t.Fatal(err)
	}
	return res.Content[0].Text, res.IsError
}

func askResult(id, text string, responder any) core.AgentMessage {
	m := msg(id, "tool_result", text)
	m.ToolName = "ask_user"
	m.ToolCallID = "tc-" + id
	if responder != nil {
		m.Custom = map[string]any{askuser.CustomKey: responder}
	}
	return m
}

func sample() []core.AgentMessage {
	owner := msg("u2", "user", "Desplegad cuando el CI esté verde")
	owner.Custom = map[string]any{"source": "owner", "owner_name": "Serena"}
	call := msg("a2", "assistant", "")
	call.Content = []core.Content{core.ToolCallContent("tc-q1", "ask_user", map[string]any{"questions": []any{map[string]any{"question": "¿Qué puerto?"}}})}
	return []core.AgentMessage{
		msg("u1", "user", "El PUERTO canario es 7413, no lo olvides."),
		msg("a1", "assistant", "Anotado: puerto canario 7413."),
		owner,
		call,
		askResult("q1", "[Answered by the owner agent \"Serena\" on the user's behalf, not by the user]\n8443", askuser.Responder{Kind: askuser.ResponderOwner, Name: "Serena"}),
		askResult("q2", "sí", map[string]any{"kind": "user", "via": "device:abc"}),
		askResult("q3", "vale", nil),
		{Message: core.Message{MsgID: "c1", Role: "session_event", Content: []core.Content{core.TextContent("✂ Context compacted (300K tokens summarized)")}}},
		msg("u3", "user", "¿Cuál era el puerto canario?"),
	}
}

func TestSearchIsCaseAndAccentInsensitiveAndChronological(t *testing.T) {
	out, isErr := run(t, sample(), map[string]any{"action": "search", "query": "puerto CANARIO"})
	if isErr {
		t.Fatal(out)
	}
	iu1, ia1, iu3 := strings.Index(out, "id=u1"), strings.Index(out, "id=a1"), strings.Index(out, "id=u3")
	if iu1 < 0 || ia1 < iu1 || iu3 < ia1 {
		t.Fatalf("want u1, a1, u3 in order:\n%s", out)
	}
	if !strings.Contains(out, "3 matches") || !strings.Contains(out, "7413") {
		t.Fatalf("missing count or snippet:\n%s", out)
	}
	// Accents fold both ways.
	if out, _ := run(t, sample(), map[string]any{"action": "search", "query": "este"}); !strings.Contains(out, "id=u2") {
		t.Fatalf("accent fold failed:\n%s", out)
	}
}

func TestSearchRequiresEveryTermAndSupportsPhrases(t *testing.T) {
	out, _ := run(t, sample(), map[string]any{"action": "search", "query": "canario olvides"})
	if !strings.Contains(out, "id=u1") || strings.Contains(out, "id=a1") {
		t.Fatalf("AND semantics:\n%s", out)
	}
	out, _ = run(t, sample(), map[string]any{"action": "search", "query": `"canario 7413"`})
	if !strings.Contains(out, "id=a1") || strings.Contains(out, "id=u1") {
		t.Fatalf("phrase semantics:\n%s", out)
	}
	out, _ = run(t, sample(), map[string]any{"action": "search", "query": "inexistente"})
	if !strings.Contains(out, "0 matches") || !strings.Contains(out, "does not prove") {
		t.Fatalf("zero hits:\n%s", out)
	}
}

func TestSearchLabelsWhoAnsweredEachAsk(t *testing.T) {
	out, _ := run(t, sample(), map[string]any{"action": "search", "role": "ask_user"})
	for _, want := range []string{
		`id=q1`, `answered by the owner agent "Serena"`,
		`id=q2`, `answered by the user (via device:abc)`,
		`id=q3`, `answered by: unknown`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "id=u1") {
		t.Fatalf("role filter leaked other messages:\n%s", out)
	}
	// Who wrote a user-role message is labelled from its recorded source.
	out, _ = run(t, sample(), map[string]any{"action": "search", "query": "desplegad"})
	if !strings.Contains(out, `owner agent "Serena"`) {
		t.Fatalf("owner message not labelled:\n%s", out)
	}
	// The question itself is searchable through the tool call arguments.
	out, _ = run(t, sample(), map[string]any{"action": "search", "query": "que puerto"})
	if !strings.Contains(out, "id=a2") {
		t.Fatalf("tool call arguments not searched:\n%s", out)
	}
}

func TestSearchPagesWithAfterAndStaysBounded(t *testing.T) {
	var msgs []core.AgentMessage
	big := strings.Repeat("relleno ", 2000)
	for i := 0; i < 500; i++ {
		msgs = append(msgs, msg(fmt.Sprintf("m%03d", i), "tool_result", big+"aguja "+big))
	}
	out, _ := run(t, msgs, map[string]any{"action": "search", "query": "aguja", "limit": 50})
	if len(out) > MaxOutputBytes {
		t.Fatalf("output %d bytes > %d", len(out), MaxOutputBytes)
	}
	if !strings.Contains(out, "500 matches") || !strings.Contains(out, "after=") {
		t.Fatalf("no count or continuation:\n%s", out[:min(len(out), 600)])
	}
	out, _ = run(t, msgs, map[string]any{"action": "search", "query": "aguja", "after": "m497"})
	if !strings.Contains(out, "id=m498") || !strings.Contains(out, "id=m499") || strings.Contains(out, "id=m497") {
		t.Fatalf("after paging:\n%s", out)
	}
	if _, isErr := run(t, msgs, map[string]any{"action": "search", "query": "aguja", "after": "nope"}); !isErr {
		t.Fatal("unknown after id must be an error")
	}
}

func TestReadPaginatesLongMessagesOnRuneBoundaries(t *testing.T) {
	long := strings.Repeat("ñ", 20000) // 40000 bytes
	msgs := []core.AgentMessage{msg("x", "tool_result", long)}
	out, isErr := run(t, msgs, map[string]any{"action": "read", "id": "x"})
	if isErr || len(out) > MaxOutputBytes || !strings.Contains(out, "offset=") {
		t.Fatalf("read page: err=%v len=%d tail=%q", isErr, len(out), out[max(0, len(out)-200):])
	}
	if strings.ContainsRune(out, '\uFFFD') {
		t.Fatal("cut inside a rune")
	}
	out, _ = run(t, msgs, map[string]any{"action": "read", "id": "x", "offset": 39998})
	if !strings.Contains(out, "ñ") || strings.Contains(out, "offset=") {
		t.Fatalf("last page:\n%s", out)
	}
	if _, isErr := run(t, msgs, map[string]any{"action": "read", "id": "missing"}); !isErr {
		t.Fatal("unknown id must be an error")
	}
}

func TestAroundShowsNeighboursAndBoundaries(t *testing.T) {
	out, isErr := run(t, sample(), map[string]any{"action": "around", "id": "q3", "before": 1, "after": 2})
	if isErr {
		t.Fatal(out)
	}
	iq2, iq3, ic1, iu3 := strings.Index(out, "id=q2"), strings.Index(out, "id=q3"), strings.Index(out, "id=c1"), strings.Index(out, "id=u3")
	if iq2 < 0 || iq3 < iq2 || ic1 < iq3 || iu3 < ic1 || strings.Contains(out, "id=q1") {
		t.Fatalf("window order:\n%s", out)
	}
	if !strings.Contains(out, "Context compacted") {
		t.Fatalf("compaction boundary not shown:\n%s", out)
	}
}

func TestBinaryContentAndThinkingAreNeverDumped(t *testing.T) {
	m := msg("img", "user", "mira la captura")
	m.Content = append(m.Content, core.ImageContent(strings.Repeat("QUJD", 1000), "image/png"), core.DocumentContent("JVBERi0", "application/pdf", "plan.pdf"))
	a := msg("th", "assistant", "visible")
	a.Content = append([]core.Content{core.ThinkingContent("secreto interno")}, a.Content...)
	msgs := []core.AgentMessage{m, a}
	out, _ := run(t, msgs, map[string]any{"action": "read", "id": "img"})
	if strings.Contains(out, "QUJD") || !strings.Contains(out, "[image image/png]") || !strings.Contains(out, "[document plan.pdf]") {
		t.Fatalf("attachments:\n%s", out)
	}
	if out, _ := run(t, msgs, map[string]any{"action": "search", "query": "secreto"}); !strings.Contains(out, "0 matches") {
		t.Fatalf("thinking searched:\n%s", out)
	}
}

func TestBadArguments(t *testing.T) {
	for _, p := range []map[string]any{
		{"action": "search"},
		{"action": "search", "query": strings.Repeat("x", MaxQueryBytes+1)},
		{"action": "read"},
		{"action": "around"},
		{"action": "nope"},
	} {
		if out, isErr := run(t, sample(), p); !isErr {
			t.Fatalf("%v accepted:\n%s", p, out)
		}
	}
}

func TestAroundWidestWindowStaysBounded(t *testing.T) {
	var msgs []core.AgentMessage
	for i := 0; i < 100; i++ {
		msgs = append(msgs, msg(fmt.Sprintf("m%03d", i), "tool_result", strings.Repeat("ñ", 5000)))
	}
	out, _ := run(t, msgs, map[string]any{"action": "around", "id": "m050", "before": 99, "after_count": 99})
	if len(out) > MaxOutputBytes || strings.Count(out, "id=m") != 41 {
		t.Fatalf("len=%d items=%d", len(out), strings.Count(out, "id=m"))
	}
}

// bigTranscript builds a transcript of about size bytes, with the needle in
// its very first message.
func bigTranscript(size int) []core.AgentMessage {
	chunk := strings.Repeat("línea de salida de una herramienta con texto corriente 0123456789\n", 160) // ~10 KB
	msgs := []core.AgentMessage{msg("first", "user", "el puerto canario es 7413")}
	for n, i := 0, 0; n < size; i++ {
		role := []string{"user", "assistant", "tool_result"}[i%3]
		msgs = append(msgs, msg(fmt.Sprintf("m%07d", i), role, chunk))
		n += len(chunk)
	}
	return msgs
}

// A 50 MB session: one search must stay fast, and must not hold on to memory
// proportional to the transcript.
func TestSearchOnFiftyMegabytes(t *testing.T) {
	if testing.Short() {
		t.Skip("large transcript")
	}
	msgs := bigTranscript(50 << 20)
	start := time.Now()
	out, _ := run(t, msgs, map[string]any{"action": "search", "query": "puerto canario"})
	elapsed := time.Since(start)
	if !strings.Contains(out, "1 matches") || !strings.Contains(out, "id=first") {
		t.Fatalf("needle not found:\n%s", out)
	}
	t.Logf("50 MB search: %v", elapsed)
	if elapsed > 5*time.Second {
		t.Fatalf("search took %v", elapsed)
	}
}

func BenchmarkSearch50MB(b *testing.B) {
	msgs := bigTranscript(50 << 20)
	tool := NewTool(visitorOf(msgs))
	params := map[string]any{"action": "search", "query": "puerto canario"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := tool.Execute(context.Background(), params, nil); err != nil {
			b.Fatal(err)
		}
	}
}

// Earlier history results are copies, not originals: searching them again
// would echo every past search back as a new match.
func TestSearchSkipsItsOwnEarlierResults(t *testing.T) {
	call := msg("a9", "assistant", "")
	call.Content = []core.Content{core.ToolCallContent("tc-h", "history", map[string]any{"action": "search", "query": "puerto canario"})}
	res := msg("h9", "tool_result", "1 matches for \"puerto canario\"…")
	res.ToolName = "history"
	msgs := append(sample(), call, res)
	out, _ := run(t, msgs, map[string]any{"action": "search", "query": "puerto canario"})
	if strings.Contains(out, "id=a9") || strings.Contains(out, "id=h9") || !strings.Contains(out, "3 matches") {
		t.Fatalf("own results searched:\n%s", out)
	}
	// They are still part of the conversation when reading around.
	if out, _ := run(t, msgs, map[string]any{"action": "around", "id": "h9", "before": 1}); !strings.Contains(out, "id=a9") {
		t.Fatalf("around:\n%s", out)
	}
}
