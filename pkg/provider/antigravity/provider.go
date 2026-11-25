package antigravity

import (
	"context"
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

	mu       sync.Mutex
	sessions map[string]*agySession
	calls    map[string]*agySession // tool call id → session, for the mirror tools
	reaper   sync.Once
}

var (
	defaultOnce sync.Once
	defaultP    *Provider
)

// Default returns the shared provider.
func Default(version string) *Provider {
	defaultOnce.Do(func() {
		defaultP = &Provider{Version: version, sessions: map[string]*agySession{}, calls: map[string]*agySession{}}
	})
	return defaultP
}

// Stream sends the new user messages of req to agy as one prompt, or
// continues the agy turn whose tool call moa just mirrored, and streams agy's
// output until its next tool call or the end of its turn.
func (p *Provider) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	sessionID := sessionIDFromKey(req.Options.PromptCacheKey)
	if sessionID == "" {
		return nil, fmt.Errorf("Antigravity runs as a session of its own, not as a subagent or helper model")
	}
	// Only what follows the last assistant message is new to agy.
	last := -1
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "assistant" {
			last = i
			break
		}
	}
	fresh := req.Messages[last+1:]
	prompt := promptBlocks(fresh)
	var denied []string
	for _, m := range fresh {
		if m.Role == "tool_result" && m.ToolName == ApprovalTool && m.IsError {
			denied = append(denied, strings.TrimSuffix(m.ToolCallID, approvalSuffix))
		}
	}

	s := p.existing(sessionID)
	if s == nil {
		if len(prompt) == 0 {
			return nil, ErrNothingToSend
		}
		var err error
		if s, err = p.open(ctx, sessionID); err != nil {
			return nil, err
		}
	}
	for _, id := range denied {
		s.mu.Lock()
		t := s.tools[id]
		s.mu.Unlock()
		if t != nil {
			s.answer(t, false)
		}
	}

	s.mu.Lock()
	s.lastUsed = time.Now()
	idle := !s.turn
	s.mu.Unlock()
	if idle && len(prompt) == 0 {
		return nil, ErrNothingToSend
	}
	if idle {
		if err := s.setOption(ctx, "model", strings.TrimPrefix(req.Model.ID, modelPrefix)); err != nil {
			return nil, err
		}
		if lvl := thoughtLevel(s, req.Options.ThinkingLevel); lvl != "" {
			if err := s.setOption(ctx, "thought_level", lvl); err != nil {
				return nil, err
			}
		}
	}
	s.mu.Lock()
	if len(prompt) > 0 {
		if s.turn {
			// A message that arrives during a turn waits for it to end:
			// a session/prompt sent mid-turn can kill the session
			// (antigravity-cli #1448).
			s.waiting = append(s.waiting, prompt)
		} else {
			s.startPrompt(prompt)
		}
	}
	s.mu.Unlock()

	ch := make(chan core.AssistantEvent, 64)
	go p.relay(ctx, s, req.Model, ch)
	return ch, nil
}

// ErrNothingToSend is returned when a request carries no new user message
// and agy has no turn in flight: there is nothing to ask it, and moa never
// repeats an earlier prompt on its own.
var ErrNothingToSend = errors.New("nothing new to send to Antigravity: write a message")

const approvalSuffix = "/approve"

// relay turns agy's events into one assistant message.
func (p *Provider) relay(ctx context.Context, s *agySession, model core.Model, ch chan<- core.AssistantEvent) {
	defer close(ch)
	msg := core.Message{Role: "assistant", Provider: ProviderName, Model: model.ID, Timestamp: time.Now().Unix()}
	ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
	cur := "" // kind of the open block
	appendDelta := func(kind, delta string) {
		if cur != kind {
			if kind == "text" {
				msg.Content = append(msg.Content, core.TextContent(""))
				ch <- core.AssistantEvent{Type: core.ProviderEventTextStart, ContentIndex: len(msg.Content) - 1}
			} else {
				msg.Content = append(msg.Content, core.ThinkingContent(""))
				ch <- core.AssistantEvent{Type: core.ProviderEventThinkingStart, ContentIndex: len(msg.Content) - 1}
			}
			cur = kind
		}
		i := len(msg.Content) - 1
		if kind == "text" {
			msg.Content[i].Text += delta
			ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, ContentIndex: i, Delta: delta}
		} else {
			msg.Content[i].Thinking += delta
			ch <- core.AssistantEvent{Type: core.ProviderEventThinkingDelta, ContentIndex: i, Delta: delta}
		}
	}
	finishCall := func(id, name string, t *toolState) {
		args := map[string]any{"title": t.title, "kind": t.kind}
		if len(t.rawInput) > 0 {
			var in any
			if json.Unmarshal(t.rawInput, &in) == nil {
				args["input"] = in
			}
		}
		p.mu.Lock()
		p.calls[id] = s
		p.mu.Unlock()
		msg.Content = append(msg.Content, core.ToolCallContent(id, name, args))
		i := len(msg.Content) - 1
		ch <- core.AssistantEvent{Type: core.ProviderEventToolCallStart, ContentIndex: i, ToolCallID: id, ToolName: name}
		ch <- core.AssistantEvent{Type: core.ProviderEventToolCallEnd, ContentIndex: i, ToolCallID: id, ToolName: name}
		msg.StopReason = "tool_use"
		ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
	}

	for {
		ev, err := s.next(ctx)
		if err != nil {
			if ctx.Err() != nil {
				s.cancel()
			}
			ch <- core.AssistantEvent{Type: core.ProviderEventError, Error: err}
			return
		}
		switch ev.kind {
		case "text", "thought":
			appendDelta(ev.kind, ev.text)
		case "tool":
			s.mu.Lock()
			t := s.tools[ev.toolID]
			emit := t != nil && !t.emitted
			if emit {
				t.emitted = true
			}
			s.mu.Unlock()
			if emit {
				finishCall(t.id, MirrorTool, t)
				return
			}
		case "permission":
			s.mu.Lock()
			t := s.tools[ev.toolID]
			var id string
			emit := t != nil && t.perm != nil && !t.perm.answered && !t.perm.shown
			if emit {
				t.perm.shown = true
				id = t.id
				if t.emitted {
					id += approvalSuffix
				}
				t.emitted = true
			}
			s.mu.Unlock()
			if emit {
				finishCall(id, ApprovalTool, t)
				return
			}
		case "done":
			s.mu.Lock()
			s.turn = false
			if len(s.waiting) > 0 && ev.err == nil {
				next := s.waiting[0]
				s.waiting = s.waiting[1:]
				s.startPrompt(next)
				s.mu.Unlock()
				continue
			}
			s.waiting = nil
			s.mu.Unlock()
			if ev.err != nil {
				ch <- core.AssistantEvent{Type: core.ProviderEventError, Error: fmt.Errorf("antigravity: %w", ev.err)}
				return
			}
			msg.StopReason = "stop"
			if ev.stop == "max_tokens" {
				msg.StopReason = "max_tokens"
			}
			if len(msg.Content) == 0 {
				msg.Content = append(msg.Content, core.TextContent(""))
			}
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
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
			return p.watch(ctx, core.ToolCallIDFromContext(ctx), name == ApprovalTool)
		},
	}, true
}

// watch waits for agy's tool call to finish, approving it first when the
// user allowed its approval card.
func (p *Provider) watch(ctx context.Context, callID string, approve bool) (core.Result, error) {
	p.mu.Lock()
	s := p.calls[callID]
	p.mu.Unlock()
	if s == nil {
		return core.ErrorResult("Antigravity no longer runs this call (moa restarted or the session closed)."), nil
	}
	id := strings.TrimSuffix(callID, approvalSuffix)
	s.mu.Lock()
	t := s.tools[id]
	s.mu.Unlock()
	if t == nil {
		return core.ErrorResult("unknown Antigravity call"), nil
	}
	if approve {
		s.answer(t, true)
	}
	for {
		s.mu.Lock()
		status, changed := t.status, t.changed
		asking := t.perm != nil && !t.perm.answered
		s.mu.Unlock()
		switch {
		case status == "completed":
			return core.TextResult(t.resultText()), nil
		case status == "failed":
			return core.ErrorResult(t.resultText()), nil
		case asking:
			// agy now asks about this call: the approval card comes next.
			return core.TextResult("Antigravity asks for your approval."), nil
		}
		select {
		case <-changed:
		case <-s.conn.done:
			return core.ErrorResult("Antigravity stopped."), nil
		case <-ctx.Done():
			s.cancel()
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
			return nil
		default:
		}
	}
	return s
}

// open starts agy for a moa session and resumes its previous Antigravity
// conversation when there is one.
func (p *Provider) open(ctx context.Context, sessionID string) (*agySession, error) {
	p.reaper.Do(func() { go p.reap() })
	var cwd string
	if p.CWD != nil {
		cwd = p.CWD(sessionID)
	}
	if cwd == "" {
		cwd, _ = os.UserHomeDir()
	}
	cmd := exec.Command(agyBinary(), "acp")
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
	s := &agySession{key: sessionID, cwd: cwd, cmd: cmd, signal: make(chan struct{}, 1), tools: map[string]*toolState{}, lastUsed: time.Now()}
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
	if prev := loadSessionID(sessionID); prev != "" {
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
		saveSessionID(sessionID, res.SessionID)
	}
	s.id = res.SessionID
	s.options = res.ConfigOptions
	learnModels(res.ConfigOptions)
	p.mu.Lock()
	p.sessions[sessionID] = s
	p.mu.Unlock()
	return s, nil
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

// The Antigravity conversation of each moa session, so a restart resumes it.
var storeMu sync.Mutex

func storePath() string { return filepath.Join(core.ConfigDir(), "antigravity-sessions.json") }

func loadStore() map[string]string {
	m := map[string]string{}
	b, err := os.ReadFile(storePath())
	if err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func loadSessionID(sessionID string) string {
	storeMu.Lock()
	defer storeMu.Unlock()
	return loadStore()[sessionID]
}

func saveSessionID(sessionID, acpID string) {
	storeMu.Lock()
	defer storeMu.Unlock()
	m := loadStore()
	m[sessionID] = acpID
	b, _ := json.MarshalIndent(m, "", "  ")
	tmp := storePath() + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, storePath())
	}
}
