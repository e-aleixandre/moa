package antigravity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// idleTimeout closes an agy process nobody has used for this long; the next
// message resumes its conversation (session/resume) in a new process.
const idleTimeout = 15 * time.Minute

// Provider is the process-wide Antigravity provider. It keeps one agy process
// per moa session.
type Provider struct {
	Version string // moa's version, sent as clientInfo
	// CWD returns a moa session's working directory, where agy runs (and
	// finds the project's AGENTS.md). Set by the server.
	CWD func(sessionID string) string
	// FullAccess reports whether a moa session approves every tool call
	// itself (permission mode yolo). agy then runs with its documented
	// --dangerously-skip-permissions instead of asking. Set by the server.
	FullAccess func(sessionID string) bool

	mu       sync.Mutex
	sessions map[string]*agySession
	calls    map[string]call // moa tool call id → agy call, for the mirror tools
	reaper   sync.Once
}

var (
	defaultOnce sync.Once
	defaultP    *Provider
)

// Default returns the shared provider.
func Default(version string) *Provider {
	defaultOnce.Do(func() {
		defaultP = &Provider{Version: version, sessions: map[string]*agySession{}, calls: map[string]call{}}
	})
	return defaultP
}

// Stream sends agy the user messages that came after the last one it
// received (the mark), as one prompt, or continues the agy turn whose tool
// call moa just mirrored; it streams agy's output until its next tool call or
// the end of its turn.
func (p *Provider) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	sessionID := sessionIDFromKey(req.Options.PromptCacheKey)
	if sessionID == "" {
		return nil, core.NoAutoRetry(fmt.Errorf("Antigravity runs as a session of its own, not as a subagent or helper model"))
	}
	s := p.existing(sessionID)
	mark := loadEntry(sessionID).Mark
	if s != nil {
		s.mu.Lock()
		mark = s.mark
		s.mu.Unlock()
	}
	next, ok := newPrompt(req.Messages, mark)
	if !ok {
		return nil, core.NoAutoRetry(ErrHistoryDiverged)
	}
	p.rejectDenied(sessionID, req.Messages)

	// A changed permission mode takes effect at the next prompt: agy reads
	// it at start, so its process is restarted and resumes the conversation.
	fullAccess := p.FullAccess != nil && p.FullAccess(sessionID)
	if s != nil && next != nil && s.fullAccess != fullAccess && s.quiet() {
		p.drop(sessionID, s)
		s = nil
	}
	if s == nil {
		if next == nil {
			return nil, core.NoAutoRetry(ErrNothingToSend)
		}
		var err error
		if s, err = p.open(ctx, sessionID, fullAccess); err != nil {
			return nil, core.NoAutoRetry(err)
		}
	}
	s.mu.Lock()
	s.lastUsed = time.Now()
	idle := !s.turn && len(s.queue) == 0
	s.mu.Unlock()
	if idle && next == nil {
		return nil, core.NoAutoRetry(ErrNothingToSend)
	}
	if !s.turnRunning() && next != nil {
		if err := s.setOption(ctx, "model", strings.TrimPrefix(req.Model.ID, modelPrefix)); err != nil {
			return nil, core.NoAutoRetry(err)
		}
		if lvl := thoughtLevel(s, req.Options.ThinkingLevel); lvl != "" {
			if err := s.setOption(ctx, "thought_level", lvl); err != nil {
				return nil, core.NoAutoRetry(err)
			}
		}
	}
	if next != nil {
		s.mu.Lock()
		if s.turn {
			// A message that arrives during a turn waits for it to end: a
			// session/prompt sent mid-turn can kill the session
			// (antigravity-cli #1448). It replaces an earlier waiting one,
			// since it carries every message after the mark.
			s.waiting = next
		} else {
			s.startPrompt(*next)
		}
		s.mu.Unlock()
	}

	ch := make(chan core.AssistantEvent, 64)
	go p.relay(ctx, s, req.Model, ch)
	return ch, nil
}

// ErrNothingToSend is returned when a request carries no new user message
// and agy has no turn in flight: there is nothing to ask it, and moa never
// repeats an earlier prompt on its own.
var ErrNothingToSend = errors.New("nothing new to send to Antigravity: write a message")

// ErrHistoryDiverged is returned when the last message agy received is no
// longer in the session's history (it was rewound or edited elsewhere).
var ErrHistoryDiverged = errors.New("this conversation no longer matches Gemini's own history. Start a new session to continue")

func (s *agySession) turnRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turn
}

// newPrompt collects the user messages after the one keyed mark (all of them
// when mark is empty). ok is false when mark is not in msgs.
func newPrompt(msgs []core.Message, mark string) (*prompt, bool) {
	start := 0
	if mark != "" {
		start = -1
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Role == "user" && messageKey(msgs[i]) == mark {
				start = i + 1
				break
			}
		}
		if start < 0 {
			return nil, false
		}
	}
	var blocks []any
	last := ""
	for _, m := range msgs[start:] {
		if b := promptBlocks(m); len(b) > 0 {
			blocks = append(blocks, b...)
			last = messageKey(m)
		}
	}
	if len(blocks) == 0 {
		return nil, true
	}
	return &prompt{blocks: blocks, mark: last}, true
}

// messageKey identifies a user message across requests: its MsgID, which the
// loop assigns to every message it keeps, or a digest of it otherwise.
func messageKey(m core.Message) string {
	if m.MsgID != "" {
		return m.MsgID
	}
	h := sha256.New()
	fmt.Fprintf(h, "%d\x00", m.Timestamp)
	for _, c := range m.Content {
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00", c.Type, c.Text, c.Data)
	}
	return "h:" + hex.EncodeToString(h.Sum(nil)[:12])
}

// rejectDenied answers "reject" to the approvals the gate denied: their tool
// results follow the last assistant message.
func (p *Provider) rejectDenied(sessionID string, msgs []core.Message) {
	for i := len(msgs) - 1; i >= 0 && msgs[i].Role != "assistant"; i-- {
		m := msgs[i]
		if m.Role != "tool_result" || m.ToolName != ApprovalTool || !m.IsError {
			continue
		}
		if c, ok := p.takeCall(sessionID, m.ToolCallID); ok {
			c.s.mu.Lock()
			t := c.s.tools[c.toolID]
			c.s.mu.Unlock()
			if t != nil {
				c.s.answer(t, false)
			}
		}
	}
}

// call is a mirrored agy tool call, by its moa tool call id.
type call struct {
	session string
	s       *agySession
	toolID  string
	approve bool
}

// callID names agy's call toolID in moa: unique per moa session (agy's ids
// are only unique within its own process) and in the charset every
// provider accepts for tool call ids.
func callID(sessionID, toolID string, approval bool) string {
	id := sessionID + "_" + toolID
	if approval {
		id += "_approve"
	}
	return strings.Map(func(r rune) rune {
		if r == '_' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			return r
		}
		return '_'
	}, id)
}

func (p *Provider) takeCall(sessionID, id string) (call, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	c, ok := p.calls[id]
	if ok && c.session == sessionID {
		delete(p.calls, id)
		return c, true
	}
	return call{}, false
}

// relay turns agy's events into one assistant message.
func (p *Provider) relay(ctx context.Context, s *agySession, model core.Model, ch chan<- core.AssistantEvent) {
	defer close(ch)
	emit := func(ev core.AssistantEvent) bool {
		select {
		case ch <- ev:
			return true
		case <-ctx.Done():
			s.stop()
			return false
		}
	}
	msg := core.Message{Role: "assistant", Provider: ProviderName, Model: model.ID, Timestamp: time.Now().Unix()}
	start := msg
	if !emit(core.AssistantEvent{Type: core.ProviderEventStart, Partial: &start}) {
		return
	}
	cur := "" // kind of the open block
	appendDelta := func(kind, delta string) bool {
		if cur != kind {
			cur = kind
			if kind == "text" {
				msg.Content = append(msg.Content, core.TextContent(""))
				if !emit(core.AssistantEvent{Type: core.ProviderEventTextStart, ContentIndex: len(msg.Content) - 1}) {
					return false
				}
			} else {
				msg.Content = append(msg.Content, core.ThinkingContent(""))
				if !emit(core.AssistantEvent{Type: core.ProviderEventThinkingStart, ContentIndex: len(msg.Content) - 1}) {
					return false
				}
			}
		}
		i := len(msg.Content) - 1
		if kind == "text" {
			msg.Content[i].Text += delta
			return emit(core.AssistantEvent{Type: core.ProviderEventTextDelta, ContentIndex: i, Delta: delta})
		}
		msg.Content[i].Thinking += delta
		return emit(core.AssistantEvent{Type: core.ProviderEventThinkingDelta, ContentIndex: i, Delta: delta})
	}
	// finishCall ends the message with agy's call as a tool call moa runs.
	finishCall := func(toolID string, approval bool) {
		s.mu.Lock()
		t := s.tools[toolID]
		args := map[string]any{"title": t.title, "kind": t.kind}
		var in any
		if len(t.rawInput) > 0 && json.Unmarshal(t.rawInput, &in) == nil {
			args["input"] = in
		}
		s.mu.Unlock()
		name := MirrorTool
		if approval {
			name = ApprovalTool
		}
		id := callID(s.key, toolID, approval)
		p.mu.Lock()
		p.calls[id] = call{session: s.key, s: s, toolID: toolID, approve: approval}
		p.mu.Unlock()
		msg.Content = append(msg.Content, core.ToolCallContent(id, name, args))
		i := len(msg.Content) - 1
		msg.StopReason = "tool_use"
		_ = emit(core.AssistantEvent{Type: core.ProviderEventToolCallStart, ContentIndex: i, ToolCallID: id, ToolName: name}) &&
			emit(core.AssistantEvent{Type: core.ProviderEventToolCallEnd, ContentIndex: i, ToolCallID: id, ToolName: name}) &&
			emit(core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg})
	}

	for {
		ev, err := s.next(ctx)
		if err != nil {
			if ctx.Err() != nil {
				s.stop()
				return
			}
			emit(core.AssistantEvent{Type: core.ProviderEventError, Error: core.NoAutoRetry(fmt.Errorf("antigravity: %w", err))})
			return
		}
		switch ev.kind {
		case "text", "thought":
			if !appendDelta(ev.kind, ev.text) {
				return
			}
		case "tool", "permission":
			approval := ev.kind == "permission"
			s.mu.Lock()
			t := s.tools[ev.toolID]
			emitCall := false
			if t != nil && !approval && !t.emitted {
				emitCall = true
			}
			if t != nil && approval && t.perm != nil && !t.perm.answered && !t.perm.shown {
				t.perm.shown = true
				emitCall = true
			}
			if emitCall {
				t.emitted = true
			}
			s.mu.Unlock()
			if emitCall {
				finishCall(ev.toolID, approval)
				return
			}
		case "done":
			if ev.err != nil {
				emit(core.AssistantEvent{Type: core.ProviderEventError, Error: core.NoAutoRetry(fmt.Errorf("antigravity: %w", ev.err))})
				return
			}
			msg.StopReason = "stop"
			if ev.stop == "max_tokens" {
				msg.StopReason = "max_tokens"
			}
			if len(msg.Content) == 0 {
				msg.Content = append(msg.Content, core.TextContent(""))
			}
			emit(core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg})
			return
		}
	}
}

// ProviderTool resolves the mirror tools of agy's tool calls.
func (p *Provider) ProviderTool(name string) (core.Tool, bool) {
	switch name {
	case MirrorTool, ApprovalTool:
	default:
		return core.Tool{}, false
	}
	return core.Tool{
		Name:        name,
		Label:       "Antigravity",
		Description: "A tool call Antigravity runs itself; moa shows it.",
		Parameters:  json.RawMessage(`{"type":"object"}`),
		Execute: func(ctx context.Context, _ map[string]any, _ func(core.Result)) (core.Result, error) {
			return p.watch(ctx, core.ToolCallIDFromContext(ctx))
		},
	}, true
}

// watch waits for agy's tool call to finish, approving it first when the
// user allowed its approval card.
func (p *Provider) watch(ctx context.Context, id string) (core.Result, error) {
	p.mu.Lock()
	c, ok := p.calls[id]
	delete(p.calls, id)
	p.mu.Unlock()
	if !ok {
		return core.ErrorResult("Antigravity no longer runs this call (moa restarted or the session closed)."), nil
	}
	s := c.s
	s.mu.Lock()
	t := s.tools[c.toolID]
	s.mu.Unlock()
	if t == nil {
		return core.ErrorResult("unknown Antigravity call"), nil
	}
	if c.approve {
		s.answer(t, true)
	}
	for {
		s.mu.Lock()
		status, changed := t.status, t.changed
		asking := t.perm != nil && !t.perm.answered
		var text string
		if status == "completed" || status == "failed" {
			text = t.resultText()
			// The card keeps the result; agy's copy is no longer needed.
			t.content, t.rawOut, t.rawInput = nil, nil, nil
		}
		s.mu.Unlock()
		switch {
		case status == "completed":
			return core.TextResult(text), nil
		case status == "failed":
			return core.ErrorResult(text), nil
		case asking:
			// agy now asks about this call: the approval card comes next.
			return core.TextResult("Antigravity asks for your approval."), nil
		}
		select {
		case <-changed:
		case <-s.conn.done:
			return core.ErrorResult("Antigravity stopped."), nil
		case <-ctx.Done():
			s.stop()
			return core.Result{}, ctx.Err()
		}
	}
}

func (p *Provider) existing(sessionID string) *agySession {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.sessions[sessionID]
	if s != nil {
		select {
		case <-s.conn.done:
			delete(p.sessions, sessionID)
			go s.close() // agy exited on its own: reap it
			return nil
		default:
		}
	}
	return s
}

// open starts agy for a moa session and resumes its previous Antigravity
// conversation when there is one.
func (p *Provider) open(ctx context.Context, sessionID string, fullAccess bool) (*agySession, error) {
	p.reaper.Do(func() { go p.reap() })
	var cwd string
	if p.CWD != nil {
		cwd = p.CWD(sessionID)
	}
	if cwd == "" {
		cwd, _ = os.UserHomeDir()
	}
	args := []string{"acp"}
	if fullAccess {
		args = append(args, "--dangerously-skip-permissions")
	}
	cmd := exec.Command(agyBinary(), args...)
	cmd.Dir = cwd
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("antigravity: start agy: %w (install Antigravity CLI and run `agy acp --login`)", err)
	}
	entry := loadEntry(sessionID)
	s := &agySession{key: sessionID, cwd: cwd, cmd: cmd, fullAccess: fullAccess, signal: make(chan struct{}, 1), tools: map[string]*toolState{}, lastUsed: time.Now(), mark: entry.Mark}
	s.conn = newRPCConn(stdout, stdin, s.onMessage)
	fail := func(err error) (*agySession, error) {
		s.close()
		return nil, err
	}
	initCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := s.conn.call(initCtx, "initialize", map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{},
		"clientInfo":         map[string]any{"name": "moa", "version": p.Version},
	}, nil); err != nil {
		return fail(fmt.Errorf("antigravity: initialize: %w", err))
	}
	var res struct {
		SessionID     string         `json:"sessionId"`
		ConfigOptions []configOption `json:"configOptions"`
	}
	params := map[string]any{"cwd": cwd, "mcpServers": []any{}}
	resumed := false
	if prev := entry.ACP; prev != "" {
		params["sessionId"] = prev
		if err := s.conn.call(initCtx, "session/resume", params, &res); err == nil {
			res.SessionID = prev
			resumed = true
		} else {
			slog.Warn("antigravity: resume failed, starting a new conversation", "session", sessionID, "error", err)
		}
		delete(params, "sessionId")
	}
	if !resumed {
		if err := s.conn.call(initCtx, "session/new", params, &res); err != nil {
			var re *rpcError
			if errors.As(err, &re) && re.Code == -32000 {
				return fail(fmt.Errorf("Antigravity is not signed in: run `agy acp --login` on the server"))
			}
			return fail(fmt.Errorf("antigravity: session/new: %w", err))
		}
		saveACP(sessionID, res.SessionID)
	}
	s.id = res.SessionID
	s.options = res.ConfigOptions
	learnModels(res.ConfigOptions)
	p.mu.Lock()
	p.sessions[sessionID] = s
	p.mu.Unlock()
	return s, nil
}

// drop closes s, the agy process of sessionID.
func (p *Provider) drop(sessionID string, s *agySession) {
	p.mu.Lock()
	if p.sessions[sessionID] == s {
		delete(p.sessions, sessionID)
	}
	p.mu.Unlock()
	s.close()
}

func (p *Provider) reap() {
	for range time.Tick(time.Minute) {
		p.mu.Lock()
		for id, s := range p.sessions {
			s.mu.Lock()
			idle := !s.turn && time.Since(s.lastUsed) > idleTimeout
			s.mu.Unlock()
			if idle {
				delete(p.sessions, id)
				go s.close()
			}
		}
		p.mu.Unlock()
	}
}

// thoughtLevel maps moa's thinking position (off, low, medium, high, xhigh)
// onto agy's thought_level choices by order: the selector labels those
// positions with agy's own names (Model.ReasoningEfforts).
func thoughtLevel(s *agySession, level string) string {
	pos := slices.Index(core.ThinkingLevels, level)
	if pos < 0 {
		return ""
	}
	for _, o := range s.options {
		if o.Category != "thought_level" || len(o.Options) == 0 {
			continue
		}
		return o.Options[min(pos, len(o.Options)-1)].Value
	}
	return ""
}

// The Antigravity conversation of each moa session and the last user message
// agy received in it, so a restart resumes both.
var storeMu sync.Mutex

type entry struct {
	ACP  string `json:"acp_session"`
	Mark string `json:"mark,omitempty"`
}

func storePath() string { return filepath.Join(core.ConfigDir(), "antigravity-sessions.json") }

func loadStore() map[string]entry {
	m := map[string]entry{}
	if b, err := os.ReadFile(storePath()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func loadEntry(sessionID string) entry {
	storeMu.Lock()
	defer storeMu.Unlock()
	return loadStore()[sessionID]
}

func updateEntry(sessionID string, f func(*entry)) {
	storeMu.Lock()
	defer storeMu.Unlock()
	m := loadStore()
	e := m[sessionID]
	f(&e)
	m[sessionID] = e
	b, _ := json.MarshalIndent(m, "", "  ")
	tmp := storePath() + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, storePath())
	}
}

func saveACP(sessionID, acpID string) { updateEntry(sessionID, func(e *entry) { e.ACP = acpID }) }

func saveMark(sessionID, mark string) { updateEntry(sessionID, func(e *entry) { e.Mark = mark }) }
