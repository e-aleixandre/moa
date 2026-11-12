package book

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/extension"
)

// Feature: the owner's book is consulted before an action with consequences
//
//   Background: an owner session with the gate switched on, a book with a sheet
//   saying Katxiña has no stock until next year.
//
//   Scenario: the first send of a turn, with nothing read, is refused once
//     When the owner calls sessions send about ordering Katxiña
//     Then the call is refused with "Antes de enviar" and at most 3 book hits
//     And the refusal ends with the retry-or-correct instruction
//   Scenario: the retry goes through
//     When the same call is sent again
//     Then it runs; a corrected call runs too; the log says which it was
//   Scenario: reading the book first means no gate (book search or read, not list)
//   Scenario: only sessions new/send and ask_user post are gated
//   Scenario: no hits means no refusal and no log
//   Scenario: once per turn; the next turn gates again
//   Scenario: calls in the same step as the refused one are refused with it
//   Scenario: switched off (the default) it does nothing, and writes no log
//   Scenario: a refused call never retried is logged as abandoned
//   Scenario: a sheet written between turns is found by the next turn

type gateRig struct {
	t       *testing.T
	dir     string
	logPath string
	on      bool
	host    *extension.Host
	gate    *gate
}

func newGateRig(t *testing.T) *gateRig {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "book")
	r := &gateRig{t: t, dir: dir, logPath: filepath.Join(t.TempDir(), "action-gate.jsonl"), on: true}
	r.sheet("work/savia-reposicion.md", "# Savia: reposición\n\nKatxiña Blanco está sin stock hasta el año que viene; va por fuera del pedido.\n")
	for i := 0; i < 5; i++ {
		r.sheet("areas/otros/ficha-"+string(rune('a'+i))+".md", "# Ficha\n\nNada que ver con vinos.\n")
	}
	r.host = extension.NewHost(core.NewRegistry(), nil)
	r.gate = GateExtension(GateConfig{Dir: dir, LogPath: r.logPath, Enabled: func() bool { return r.on }}).(*gate)
	if err := r.host.Load(r.gate); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.gate.logWG.Wait) // before the temp dir goes
	r.turn()
	return r
}

func (r *gateRig) sheet(rel, body string) {
	r.t.Helper()
	full := filepath.Join(r.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
		r.t.Fatal(err)
	}
}

// turn starts a new agent run; step is one model request inside it.
func (r *gateRig) turn() { r.host.FireBeforeAgentStart(context.Background()); r.step() }
func (r *gateRig) step() { r.host.FireContext(context.Background(), nil) }

func (r *gateRig) call(name string, args map[string]any) *core.ToolCallDecision {
	return r.host.FireToolCall(context.Background(), name, args)
}

func (r *gateRig) log() []map[string]any {
	r.t.Helper()
	r.gate.logWG.Wait()
	return r.readLog()
}

// readLog does not wait for the writer, for callers whose own trigger is
// asynchronous (a WaitGroup may not be waited on while it is being added to).
func (r *gateRig) readLog() []map[string]any {
	r.t.Helper()
	data, err := os.ReadFile(r.logPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		r.t.Fatal(err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" { // the writer created the file and has not written yet
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			r.t.Fatalf("log line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func send(text string) map[string]any {
	return map[string]any{"action": "send", "session_id": "s1", "text": text}
}

const katxina = "Prepara la propuesta de pedido para Ikili incluyendo Katxiña Blanco"

func TestGateRefusesTheFirstActionOnceWithWhatTheBookSays(t *testing.T) {
	r := newGateRig(t)
	d := r.call("sessions", send(katxina))
	if d == nil || !d.Block {
		t.Fatal("first send was not refused")
	}
	for _, want := range []string{"Antes de enviar: esto de tu libro puede afectar", "work/savia-reposicion.md", "Si sigue valiendo, reenvía la misma llamada; si no, corrígela."} {
		if !strings.Contains(d.Reason, want) {
			t.Fatalf("reason lacks %q:\n%s", want, d.Reason)
		}
	}
	if n := strings.Count(d.Reason, "\n- "); n < 1 || n > 3 {
		t.Fatalf("%d hits shown:\n%s", n, d.Reason)
	}
	if len(d.Reason) > 1500 {
		t.Fatalf("reason is %d bytes", len(d.Reason))
	}
	r.step()
	if d := r.call("sessions", send(katxina)); d != nil {
		t.Fatalf("the retry was refused: %s", d.Reason)
	}
	// Once per turn: a different send in the same turn is not gated again.
	if d := r.call("sessions", send("Otra cosa sobre Katxiña")); d != nil {
		t.Fatalf("second gate in one turn: %s", d.Reason)
	}
	log := r.log()
	if len(log) != 2 || log[0]["event"] != "fired" || log[1]["event"] != "resolved" || log[1]["outcome"] != "unchanged" || log[0]["id"] != log[1]["id"] {
		t.Fatalf("log = %v", log)
	}
	if log[0]["tool"] != "sessions" || log[0]["action"] != "send" || !strings.Contains(log[0]["text"].(string), "Katxiña") {
		t.Fatalf("fired entry = %v", log[0])
	}
	if shown, _ := log[0]["results"].([]any); len(shown) == 0 {
		t.Fatalf("no results logged: %v", log[0])
	}
}

func TestGateRetryCorrectedIsLoggedAsChanged(t *testing.T) {
	r := newGateRig(t)
	r.call("sessions", send(katxina))
	r.step()
	if d := r.call("sessions", send("Prepara el pedido de Ikili sin Katxiña Blanco")); d != nil {
		t.Fatalf("corrected call refused: %s", d.Reason)
	}
	log := r.log()
	if len(log) != 2 || log[1]["outcome"] != "changed" {
		t.Fatalf("log = %v", log)
	}
}

func TestGateSkipsATurnThatReadTheBook(t *testing.T) {
	for _, read := range []map[string]any{
		{"action": "search", "query": "katxiña"},
		{"action": "read", "path": "work/savia-reposicion.md"},
	} {
		r := newGateRig(t)
		if d := r.call("book", read); d != nil {
			t.Fatal("the book call itself was refused")
		}
		if d := r.call("sessions", send(katxina)); d != nil {
			t.Fatalf("gated after %v: %s", read, d.Reason)
		}
		if len(r.log()) != 0 {
			t.Fatalf("log = %v", r.log())
		}
	}
	// A listing is not a reading of the matter.
	r := newGateRig(t)
	r.call("book", map[string]any{"action": "list"})
	if d := r.call("sessions", send(katxina)); d == nil {
		t.Fatal("book list lifted the gate")
	}
}

func TestGateOnlyCoversActionsWithConsequences(t *testing.T) {
	notGated := []struct {
		tool string
		args map[string]any
	}{
		{"sessions", map[string]any{"action": "list"}},
		{"sessions", map[string]any{"action": "read", "session_id": "s1", "text": katxina}},
		{"sessions", map[string]any{"action": "answer", "session_id": "s1", "answers": []any{katxina}}},
		{"ask_user", map[string]any{"action": "list"}},
		{"ask_user", map[string]any{"action": "withdraw", "question_id": "q1"}},
		{"bash", map[string]any{"command": "echo " + katxina}},
		{"tasks", map[string]any{"action": "create", "title": katxina}},
	}
	for _, c := range notGated {
		r := newGateRig(t)
		if d := r.call(c.tool, c.args); d != nil {
			t.Fatalf("%s %v gated", c.tool, c.args)
		}
	}
	gated := []struct {
		tool string
		args map[string]any
	}{
		{"sessions", map[string]any{"action": "new", "cwd": "/x", "text": katxina}},
		{"ask_user", map[string]any{"questions": []any{map[string]any{"question": "¿Incluyo Katxiña Blanco?", "context": "pedido Ikili"}}}},
		{"ask_user", map[string]any{"action": "post", "questions": []any{map[string]any{"question": "¿Incluyo Katxiña Blanco?"}}}},
	}
	for _, c := range gated {
		r := newGateRig(t)
		if d := r.call(c.tool, c.args); d == nil || !d.Block {
			t.Fatalf("%s %v not gated", c.tool, c.args)
		}
	}
}

func TestGateDoesNotBlockWithoutHits(t *testing.T) {
	r := newGateRig(t)
	if d := r.call("sessions", send("zzzqqq xxyyzz")); d != nil {
		t.Fatalf("blocked with no hits: %s", d.Reason)
	}
	if len(r.log()) != 0 {
		t.Fatalf("log = %v", r.log())
	}
}

func TestGateIsOffByDefaultAndWritesNothing(t *testing.T) {
	r := newGateRig(t)
	r.on = false
	r.turn()
	if d := r.call("sessions", send(katxina)); d != nil {
		t.Fatalf("gated while off: %s", d.Reason)
	}
	if _, err := os.Stat(r.logPath); err == nil {
		t.Fatal("a log was written while off")
	}
}

func TestGateFiresAgainNextTurnAndLogsAbandoned(t *testing.T) {
	r := newGateRig(t)
	r.call("sessions", send(katxina))
	r.turn() // the owner never retried
	log := r.log()
	if len(log) != 2 || log[1]["event"] != "resolved" || log[1]["outcome"] != "abandoned" {
		t.Fatalf("log = %v", log)
	}
	if d := r.call("sessions", send(katxina)); d == nil {
		t.Fatal("the new turn was not gated")
	}
}

func TestGateRefusesTheWholeStepNotJustTheFirstCall(t *testing.T) {
	r := newGateRig(t)
	first := r.call("sessions", send(katxina))
	second := r.call("sessions", send("Y también Katxiña para otra sesión"))
	if first == nil || second == nil {
		t.Fatal("a call of the same step went through the gate")
	}
	fired := 0
	for _, e := range r.log() {
		if e["event"] == "fired" {
			fired++
		}
	}
	if fired != 1 {
		t.Fatalf("%d fired entries for one step: %v", fired, r.log())
	}
	r.step()
	if d := r.call("sessions", send(katxina)); d != nil {
		t.Fatalf("retry refused: %s", d.Reason)
	}
}

func TestGateSeesTheBookAsItIsAtTheStartOfTheTurn(t *testing.T) {
	r := newGateRig(t)
	r.sheet("work/nuevo.md", "# Zarzamora\n\nLa zarzamora de Ikili queda descatalogada.\n")
	r.turn()
	d := r.call("sessions", send("Pide zarzamora para Ikili"))
	if d == nil || !strings.Contains(d.Reason, "work/nuevo.md") {
		t.Fatalf("new sheet not found: %v", d)
	}
}

func TestGateTrimsLongSnippets(t *testing.T) {
	r := newGateRig(t)
	r.sheet("work/largo.md", "# Largo\n\nKatxiña "+strings.Repeat("palabra ", 400)+"\n")
	d := r.call("sessions", send("Katxiña largo"))
	if d == nil || len(d.Reason) > 1500 {
		t.Fatalf("reason = %v", d)
	}
}

func TestGateLogsAbandonedWhenTheRunEnds(t *testing.T) {
	r := newGateRig(t)
	r.call("sessions", send(katxina))
	r.host.FireObserver(core.AgentEvent{Type: core.AgentEventEnd})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(r.readLog()) < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	log := r.log()
	if len(log) != 2 || log[1]["outcome"] != "abandoned" {
		t.Fatalf("log = %v", log)
	}
}

// A call the host gave up on (its deadline passed and the action went ahead)
// must not afterwards count as a refusal.
func TestGateLateCallChangesNothing(t *testing.T) {
	r := newGateRig(t)
	g := &gate{cfg: GateConfig{Dir: r.dir, LogPath: r.logPath, Enabled: func() bool { return true }}}
	t.Cleanup(g.logWG.Wait)
	g.gen++
	g.on = true
	ix, err := NewIndex(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	g.idx, g.ready = ix, true
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if d := g.onToolCall(ctx, "sessions", send(katxina)); d != nil {
		t.Fatal("a call past its deadline was refused")
	}
	if g.fired || len(r.log()) != 0 {
		t.Fatalf("state changed: fired=%v log=%v", g.fired, r.log())
	}
	if d := g.onToolCall(context.Background(), "sessions", send(katxina)); d == nil {
		t.Fatal("the next call in time was not gated")
	}
}

func TestGateNeverUsesAnIndexThisTurnDidNotConfirm(t *testing.T) {
	r := newGateRig(t)
	g := &gate{cfg: GateConfig{Dir: r.dir, LogPath: r.logPath, Enabled: func() bool { return true }}}
	ix, _ := NewIndex(r.dir)
	g.on = true
	g.idx, g.ready = ix, false // a previous turn's index, rebuild unfinished
	if d := g.onToolCall(context.Background(), "sessions", send(katxina)); d != nil {
		t.Fatal("gated on an unconfirmed index")
	}
	g.logWG.Wait()
	log := r.log()
	if len(log) != 1 || log[0]["event"] != "skipped" {
		t.Fatalf("log = %v", log)
	}
}

func TestGateLogKeepsPathsNotBookText(t *testing.T) {
	r := newGateRig(t)
	r.sheet("work/secreto.md", "# Secreto\n\nKatxiña token=EXAMPLE_SECRET_NOT_IN_ACTION\n")
	r.turn()
	r.call("sessions", send("Katxiña token"))
	r.gate.logWG.Wait()
	data, _ := os.ReadFile(r.logPath)
	if strings.Contains(string(data), "EXAMPLE_SECRET") || !strings.Contains(string(data), "work/secreto.md") {
		t.Fatalf("log = %s", data)
	}
}
