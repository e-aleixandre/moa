package book

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/extension"
)

// The guard keeps the owner from writing the book badly; this gate is about the
// other half of the failure, acting without having read it. An owner that sends
// work to a session or asks the user a question without looking at its book
// repeats what the book already settled. The measured case for a cheap check:
// the text of the action itself, searched in the book, puts the right note in
// the top three far more often than the incoming message does.
//
// So, once per turn and only before an action with consequences, the first
// sessions send/new or ask_user post is refused with the three best hits for
// the action's own text, unless the turn already read the book. Resending the
// same call, or a corrected one, goes through. Nothing is injected anywhere
// else. Pure BM25: no model is involved.

const (
	gateHits      = 3
	gateSnippet   = 140
	gateQueryMax  = 1000
	gateIndexWait = 150 * time.Millisecond // the tool-call hook is cut at 200ms

	gateHeader = "Antes de enviar: esto de tu libro puede afectar"
	gateFooter = "Si sigue valiendo, reenvía la misma llamada; si no, corrígela."
)

// GateConfig wires the gate to one owner.
type GateConfig struct {
	Dir     string      // the book directory
	LogPath string      // JSON lines; the pilot's record, never leaves the host
	Enabled func() bool // read on every turn and call, so the switch is live
}

// GateExtension returns the owner session's action gate.
func GateExtension(cfg GateConfig) extension.Extension { return &gate{cfg: cfg} }

type gatePending struct {
	id       string
	tool     string
	args     map[string]any
	reason   string
	readBook bool
}

type gate struct {
	cfg GateConfig

	mu         sync.Mutex
	logMu      sync.Mutex // the log is written outside mu
	readBook   bool       // a book search or read happened in this turn
	fired      bool       // the gate has refused once in this turn
	epoch      int        // model requests so far; calls of one step share it
	firedEpoch int
	pending    *gatePending
	seq        int

	// Tokenizing a real book takes longer than a hook may run (about half a
	// second for 4 MB), so the index is built when the turn starts, reused
	// while the files are unchanged, and the call only has to search it.
	//
	// An index is searched only once this turn's build has confirmed it matches
	// the files (ready); a stale one would surface notes already removed.
	idx   *Index
	sig   string
	build chan struct{}
	gen   int  // turn counter; a warmer of an older turn publishes nothing
	ready bool // this turn's build finished
}

func (g *gate) enabled() bool { return g.cfg.Enabled != nil && g.cfg.Enabled() }

func (g *gate) Init(api extension.API) error {
	api.OnBeforeAgentStart(func(context.Context) ([]core.AgentMessage, error) {
		g.mu.Lock()
		if g.pending != nil {
			g.logResolved(g.pending, "abandoned")
			g.pending = nil
		}
		g.readBook, g.fired, g.epoch, g.firedEpoch = false, false, 0, 0
		g.build, g.ready = nil, false
		g.gen++
		if g.enabled() {
			g.build = make(chan struct{})
			go g.warm(g.gen, g.build)
		} else {
			g.idx, g.sig = nil, ""
		}
		g.mu.Unlock()
		return nil, nil
	})
	api.OnContext(func(context.Context, []core.AgentMessage) ([]core.AgentMessage, error) {
		g.mu.Lock()
		g.epoch++
		g.mu.Unlock()
		return nil, nil
	})
	api.OnAgentEnd(func(context.Context, core.AgentEvent) {
		g.mu.Lock()
		if g.pending != nil {
			g.logResolved(g.pending, "abandoned")
			g.pending = nil
		}
		g.mu.Unlock()
	})
	api.OnToolCall(g.onToolCall)
	return nil
}

func (g *gate) warm(gen int, done chan struct{}) {
	defer close(done)
	sig := bookSignature(g.cfg.Dir)
	g.mu.Lock()
	if g.gen != gen {
		g.mu.Unlock()
		return
	}
	if g.idx != nil && g.sig == sig {
		g.ready = true
		g.mu.Unlock()
		return
	}
	g.mu.Unlock()
	ix, err := NewIndex(g.cfg.Dir)
	if err != nil {
		slog.Warn("action gate: cannot index the book", "error", err)
		return
	}
	g.mu.Lock()
	if g.gen == gen {
		g.idx, g.sig, g.ready = ix, sig, true
	}
	g.mu.Unlock()
}

// bookSignature changes whenever a book file is added, removed or rewritten.
func bookSignature(dir string) string {
	h := fnv.New64a()
	_ = walkBook(dir, func(rel, full string) error {
		if info, err := os.Stat(full); err == nil {
			fmt.Fprintf(h, "%s|%d|%d\n", rel, info.Size(), info.ModTime().UnixNano())
		}
		return nil
	})
	return fmt.Sprintf("%x", h.Sum64())
}

func (g *gate) onToolCall(ctx context.Context, name string, args map[string]any) *core.ToolCallDecision {
	name = strings.ToLower(name)
	if name == "book" {
		if a, _ := args["action"].(string); a == "search" || a == "read" {
			g.mu.Lock()
			g.readBook = true
			if g.pending != nil {
				g.pending.readBook = true
			}
			g.mu.Unlock()
		}
		return nil
	}
	action, text, ok := gatedAction(name, args)
	if !ok || !g.enabled() {
		return nil
	}

	g.mu.Lock()
	if g.fired {
		defer g.mu.Unlock()
		p := g.pending
		if p == nil {
			return nil
		}
		if g.epoch == g.firedEpoch {
			// The same step as the refused call: the model has not seen the
			// refusal yet, so these calls are not its answer to it.
			return &core.ToolCallDecision{Block: true, Reason: p.reason, Kind: core.ToolCallDecisionKindPolicy}
		}
		outcome := "changed"
		if p.tool == name && reflect.DeepEqual(p.args, args) {
			outcome = "unchanged"
		}
		g.logResolved(p, outcome)
		g.pending = nil
		return nil
	}
	if g.readBook {
		g.mu.Unlock()
		return nil
	}
	build := g.build
	g.mu.Unlock()

	if build != nil {
		select {
		case <-build:
		case <-time.After(gateIndexWait):
		}
	}

	g.mu.Lock()
	if g.fired || g.readBook {
		g.mu.Unlock()
		return nil
	}
	if !g.ready {
		g.appendLog(map[string]any{"event": "skipped", "reason": "index not ready", "tool": name, "action": action})
		g.mu.Unlock()
		return nil
	}
	idx, gen := g.idx, g.gen
	g.mu.Unlock()

	// Ranking runs outside the lock, and a call the host already gave up on
	// (the hook deadline passed, so the action went ahead) must not afterwards
	// count as refused.
	hits, _ := idx.Search(text, gateHits)
	if len(hits) == 0 || ctx.Err() != nil {
		return nil
	}
	g.mu.Lock()
	// Checked again under the lock: waiting for it can outlast the deadline.
	if g.fired || g.readBook || g.gen != gen || ctx.Err() != nil {
		g.mu.Unlock()
		return nil
	}
	g.seq++
	p := &gatePending{
		id:     fmt.Sprintf("%d-%d", time.Now().UnixNano(), g.seq),
		tool:   name,
		args:   core.CloneArgs(args),
		reason: gateReason(hits),
	}
	g.fired, g.firedEpoch, g.pending = true, g.epoch, p
	g.mu.Unlock()

	// Paths only: a snippet would copy book text, secrets included, into a log.
	shown := make([]string, 0, len(hits))
	for _, h := range hits {
		shown = append(shown, h.Path)
	}
	g.appendLog(map[string]any{"event": "fired", "id": p.id, "tool": name, "action": action, "text": text, "results": shown})

	// Slow log I/O can also run the deadline out; the action then went ahead,
	// so the refusal is taken back rather than left in the state and the log.
	if ctx.Err() != nil {
		g.mu.Lock()
		if g.pending == p {
			g.fired, g.pending = false, nil
		}
		g.mu.Unlock()
		g.logResolved(p, "timed_out")
		return nil
	}
	return &core.ToolCallDecision{Block: true, Reason: p.reason, Kind: core.ToolCallDecisionKindPolicy}
}

func (g *gate) logResolved(p *gatePending, outcome string) {
	g.appendLog(map[string]any{"event": "resolved", "id": p.id, "outcome": outcome, "read_book": p.readBook})
}

func (g *gate) appendLog(entry map[string]any) {
	if g.cfg.LogPath == "" {
		return
	}
	g.logMu.Lock()
	defer g.logMu.Unlock()
	entry["at"] = time.Now().UTC().Format(time.RFC3339)
	line, err := json.Marshal(entry)
	if err == nil {
		err = os.MkdirAll(filepath.Dir(g.cfg.LogPath), 0o700)
	}
	if err == nil {
		var f *os.File
		if f, err = os.OpenFile(g.cfg.LogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			_ = f.Chmod(0o600) // OpenFile's mode only applies to a new file
			_, err = f.Write(append(line, '\n'))
			if cerr := f.Close(); err == nil {
				err = cerr
			}
		}
	}
	if err != nil {
		slog.Warn("action gate: cannot write its log", "error", err)
	}
}

func gateReason(hits []Hit) string {
	var sb strings.Builder
	sb.WriteString(gateHeader)
	for _, h := range hits {
		sb.WriteString("\n- " + h.Path)
		if s := gateClip(h.Snippet); s != "" {
			sb.WriteString(" — " + s)
		}
	}
	sb.WriteString("\n" + gateFooter)
	return sb.String()
}

func gateClip(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= gateSnippet {
		return s
	}
	return string([]rune(s)[:gateSnippet-1]) + "…"
}

// gatedAction recognises the calls with consequences: starting or directing a
// session, and posting a question to the user. text is what the call says.
func gatedAction(tool string, args map[string]any) (action, text string, ok bool) {
	str := func(m map[string]any, k string) string { s, _ := m[k].(string); return s }
	switch tool {
	case "sessions":
		action = str(args, "action")
		if action != "send" && action != "new" {
			return "", "", false
		}
		text = str(args, "text") + "\n" + str(args, "title")
	case "ask_user":
		action = str(args, "action")
		if action != "" && action != "post" {
			return "", "", false
		}
		qs, _ := args["questions"].([]any)
		if len(qs) == 0 {
			return "", "", false
		}
		action = "post"
		var parts []string
		for _, raw := range qs {
			q, _ := raw.(map[string]any)
			parts = append(parts, str(q, "question"), str(q, "context"))
			if opts, ok := q["options"].([]any); ok {
				for _, o := range opts {
					s, _ := o.(string)
					parts = append(parts, s)
				}
			}
		}
		text = strings.Join(parts, "\n")
	default:
		return "", "", false
	}
	text = strings.TrimSpace(text)
	if len(text) > gateQueryMax {
		text = text[:gateQueryMax]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
	}
	return action, text, text != ""
}
