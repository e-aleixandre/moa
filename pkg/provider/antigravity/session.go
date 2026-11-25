package antigravity

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// agySession is one `agy acp` process serving one moa session.
type agySession struct {
	key  string
	cwd  string
	cmd  *exec.Cmd
	conn *rpcConn
	id   string // ACP session id

	mu     sync.Mutex
	queue  []turnEvent
	signal chan struct{} // non-blocking wake for the queue reader
	tools  map[string]*toolState
	// turn is true while a session/prompt is in flight; the goroutine that
	// sent it clears it. gen numbers the prompts; the output of every prompt
	// up to dropGen (a stopped turn) is discarded.
	turn    bool
	gen     int
	dropGen int
	// waiting is the prompt that arrived during a turn, sent when it ends.
	waiting *prompt
	// mark is the last user message agy received (see messageKey).
	mark      string
	options   []configOption
	lastUsed  time.Time
	closeOnce sync.Once
}

type prompt struct {
	blocks []any
	mark   string
}

type turnEvent struct {
	kind   string // text, thought, tool, permission, done
	text   string
	toolID string
	stop   string
	err    error
}

type toolState struct {
	id       string
	title    string
	kind     string
	status   string
	rawInput json.RawMessage
	content  []json.RawMessage
	rawOut   json.RawMessage
	perm     *permRequest
	emitted  bool // mirrored as a card
	changed  chan struct{}
}

type permRequest struct {
	rpcID    json.RawMessage
	options  []permOption
	answered bool
	shown    bool // an approval card was emitted for it
}

type permOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

type configOption struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Category     string `json:"category"`
	CurrentValue string `json:"currentValue"`
	Options      []struct {
		Value string `json:"value"`
		Name  string `json:"name"`
	} `json:"options"`
}

// push queues agy's output for the relay, dropping a stopped turn's.
func (s *agySession) push(ev turnEvent) {
	s.mu.Lock()
	if s.gen <= s.dropGen && ev.kind != "done" {
		s.mu.Unlock()
		return
	}
	s.queue = append(s.queue, ev)
	s.mu.Unlock()
	select {
	case s.signal <- struct{}{}:
	default:
	}
}

// next blocks until an event is queued or ctx ends.
func (s *agySession) next(ctx context.Context) (turnEvent, error) {
	for {
		s.mu.Lock()
		if len(s.queue) > 0 {
			ev := s.queue[0]
			s.queue = s.queue[1:]
			s.mu.Unlock()
			return ev, nil
		}
		s.mu.Unlock()
		select {
		case <-s.signal:
		case <-s.conn.done:
			return turnEvent{}, errConnClosed
		case <-ctx.Done():
			return turnEvent{}, ctx.Err()
		}
	}
}

func (s *agySession) tool(id string) *toolState {
	t := s.tools[id]
	if t == nil {
		t = &toolState{id: id, changed: make(chan struct{})}
		s.tools[id] = t
	}
	return t
}

// touch wakes whoever waits on t. Caller holds s.mu.
func (t *toolState) touch() {
	close(t.changed)
	t.changed = make(chan struct{})
}

type toolUpdate struct {
	SessionUpdate string            `json:"sessionUpdate"`
	ToolCallID    string            `json:"toolCallId"`
	Title         *string           `json:"title"`
	Kind          *string           `json:"kind"`
	Status        *string           `json:"status"`
	RawInput      json.RawMessage   `json:"rawInput"`
	RawOutput     json.RawMessage   `json:"rawOutput"`
	Content       []json.RawMessage `json:"content"`
}

func (s *agySession) mergeTool(u toolUpdate) *toolState {
	t := s.tool(u.ToolCallID)
	if u.Title != nil {
		t.title = *u.Title
	}
	if u.Kind != nil {
		t.kind = *u.Kind
	}
	if u.Status != nil {
		t.status = *u.Status
	}
	if len(u.RawInput) > 0 {
		t.rawInput = u.RawInput
	}
	if len(u.RawOutput) > 0 {
		t.rawOut = u.RawOutput
	}
	if u.Content != nil {
		t.content = u.Content
	}
	t.touch()
	return t
}

// onMessage handles what agy sends: session/update notifications and its
// requests to the client.
func (s *agySession) onMessage(m rpcMessage) {
	switch m.Method {
	case "session/update":
		var p struct {
			Update json.RawMessage `json:"update"`
		}
		if json.Unmarshal(m.Params, &p) != nil {
			return
		}
		var head struct {
			SessionUpdate string `json:"sessionUpdate"`
			Content       struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		_ = json.Unmarshal(p.Update, &head)
		switch head.SessionUpdate {
		case "agent_message_chunk":
			if head.Content.Type == "text" && head.Content.Text != "" {
				s.push(turnEvent{kind: "text", text: head.Content.Text})
			}
		case "agent_thought_chunk":
			if head.Content.Type == "text" && head.Content.Text != "" {
				s.push(turnEvent{kind: "thought", text: head.Content.Text})
			}
		case "tool_call", "tool_call_update":
			var u toolUpdate
			if json.Unmarshal(p.Update, &u) != nil || u.ToolCallID == "" {
				return
			}
			s.mu.Lock()
			t := s.mergeTool(u)
			ready := !t.emitted && t.status != "" && t.status != "pending"
			s.mu.Unlock()
			if ready {
				s.push(turnEvent{kind: "tool", toolID: u.ToolCallID})
			}
		default:
			slog.Debug("antigravity: update ignored", "type", head.SessionUpdate)
		}
	case "session/request_permission":
		var p struct {
			ToolCall toolUpdate   `json:"toolCall"`
			Options  []permOption `json:"options"`
		}
		if json.Unmarshal(m.Params, &p) != nil || p.ToolCall.ToolCallID == "" {
			_ = s.conn.replyError(m.ID, -32602, "invalid params")
			return
		}
		s.mu.Lock()
		t := s.mergeTool(p.ToolCall)
		t.perm = &permRequest{rpcID: m.ID, options: p.Options}
		s.mu.Unlock()
		s.push(turnEvent{kind: "permission", toolID: p.ToolCall.ToolCallID})
	default:
		if len(m.ID) > 0 {
			// moa advertises no fs, terminal or elicitation capability.
			_ = s.conn.replyError(m.ID, -32601, "method not supported by moa: "+m.Method)
		}
	}
}

// answer resolves a pending permission request with the first option of the
// wanted kind ("allow" or "reject"), once.
func (s *agySession) answer(t *toolState, allow bool) {
	s.mu.Lock()
	perm := t.perm
	if perm == nil || perm.answered {
		s.mu.Unlock()
		return
	}
	perm.answered = true
	s.mu.Unlock()
	want := "reject"
	if allow {
		want = "allow"
	}
	for _, kind := range []string{want + "_once", want + "_always"} {
		for _, o := range perm.options {
			if o.Kind == kind {
				_ = s.conn.reply(perm.rpcID, map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": o.OptionID}})
				return
			}
		}
	}
	_ = s.conn.reply(perm.rpcID, map[string]any{"outcome": map[string]any{"outcome": "cancelled"}})
}

// startPrompt sends one prompt and owns its turn: when agy answers, it ends
// the turn, sends the prompt that waited for it, or reports the end to the
// relay. Caller holds s.mu.
func (s *agySession) startPrompt(p prompt) {
	s.turn = true
	s.gen++
	gen := s.gen
	s.mark = p.mark
	saveMark(s.key, s.mark)
	go func() {
		var res struct {
			StopReason string `json:"stopReason"`
		}
		err := s.conn.call(context.Background(), "session/prompt", map[string]any{"sessionId": s.id, "prompt": p.blocks}, &res)
		s.mu.Lock()
		s.turn = false
		next := s.waiting
		s.waiting = nil
		if next != nil && err == nil {
			s.startPrompt(*next)
			s.mu.Unlock()
			return
		}
		// A waiting prompt is not sent after an error: it stays after the
		// mark and goes with the next message. A stopped turn nobody waits
		// on ends silently.
		stopped := gen <= s.dropGen
		s.mu.Unlock()
		if stopped && next == nil {
			return
		}
		s.push(turnEvent{kind: "done", stop: res.StopReason, err: err})
	}()
}

// stop ends what moa waits on (Stop): it cancels agy's turn, answers its
// pending permission requests "cancelled" as ACP asks, and drops the prompt
// that waited and the stopped turn's remaining output.
func (s *agySession) stop() {
	s.mu.Lock()
	turn := s.turn
	s.dropGen = s.gen
	s.queue = nil
	s.waiting = nil
	var pending []*permRequest
	for _, t := range s.tools {
		if t.perm != nil && !t.perm.answered {
			t.perm.answered = true
			pending = append(pending, t.perm)
		}
	}
	s.mu.Unlock()
	if turn {
		_ = s.conn.notify("session/cancel", map[string]any{"sessionId": s.id})
	}
	for _, perm := range pending {
		_ = s.conn.reply(perm.rpcID, map[string]any{"outcome": map[string]any{"outcome": "cancelled"}})
	}
}

// setOption applies a session config option (model, thought level) by its
// category when value is one of its choices and not already current.
func (s *agySession) setOption(ctx context.Context, category, value string) error {
	for i, o := range s.options {
		if o.Category != category || o.CurrentValue == value {
			continue
		}
		known := false
		for _, c := range o.Options {
			known = known || c.Value == value
		}
		if !known {
			return nil
		}
		if err := s.conn.call(ctx, "session/set_config_option", map[string]any{"sessionId": s.id, "configId": o.ID, "value": value}, nil); err != nil {
			return fmt.Errorf("antigravity: set %s: %w", category, err)
		}
		s.options[i].CurrentValue = value
	}
	return nil
}

func (s *agySession) close() {
	s.closeOnce.Do(func() {
		if s.cmd != nil && s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
			_ = s.cmd.Wait()
		}
	})
}

// resultText renders an agy tool call's outcome for its card. Caller holds
// the session's mu.
func (t *toolState) resultText() string {
	var b strings.Builder
	for _, raw := range t.content {
		var c struct {
			Type    string `json:"type"`
			Content struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			Path    string  `json:"path"`
			OldText *string `json:"oldText"`
			NewText string  `json:"newText"`
		}
		if json.Unmarshal(raw, &c) != nil {
			continue
		}
		switch c.Type {
		case "content":
			if c.Content.Type == "text" {
				b.WriteString(c.Content.Text)
				b.WriteString("\n")
			}
		case "diff":
			fmt.Fprintf(&b, "--- %s\n", c.Path)
			if c.OldText != nil {
				for _, l := range strings.Split(*c.OldText, "\n") {
					b.WriteString("-" + l + "\n")
				}
			}
			for _, l := range strings.Split(c.NewText, "\n") {
				b.WriteString("+" + l + "\n")
			}
		}
	}
	if b.Len() == 0 && len(t.rawOut) > 0 {
		b.WriteString(rawString(t.rawOut))
	}
	if b.Len() == 0 {
		b.WriteString(t.status)
	}
	return strings.TrimRight(b.String(), "\n")
}

func agyBinary() string {
	if p := os.Getenv("MOA_AGY_BIN"); p != "" {
		return p
	}
	if p, err := exec.LookPath("agy"); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	return home + "/.local/bin/agy"
}
