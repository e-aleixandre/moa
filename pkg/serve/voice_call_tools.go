package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/e-aleixandre/moa/pkg/attachment"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/voice"
)

// voiceResultCache bounds remembered tool results; a retried tool call ID
// gets its first result instead of a second effect.
const voiceResultCache = 512

// voiceHandoffSteps bounds one handoff's backend tool loop.
const voiceHandoffSteps = 8

func voiceResult(id string, v any) voice.ToolResult {
	b, _ := json.Marshal(v)
	return voice.ToolResult{ID: id, Output: b}
}

func voiceErrorResult(id, msg string) voice.ToolResult {
	b, _ := json.Marshal(map[string]any{"error": msg})
	return voice.ToolResult{ID: id, Output: b, IsError: true}
}

// execute runs one allowlisted tool for the called session. It is the only
// tool dispatcher of a call: unknown names never reach moa's general tools.
func (c *voiceCall) execute(ctx context.Context, tc voice.ToolCall) voice.ToolResult {
	c.mu.Lock()
	if r, ok := c.results[tc.ID]; ok && tc.ID != "" {
		c.mu.Unlock()
		return r
	}
	c.mu.Unlock()
	r := c.executeOnce(ctx, tc)
	c.mu.Lock()
	if tc.ID != "" && len(c.results) < voiceResultCache {
		c.results[tc.ID] = r
	}
	c.mu.Unlock()
	return r
}

func (c *voiceCall) executeOnce(_ context.Context, tc voice.ToolCall) voice.ToolResult {
	if !voice.Allowed(tc.Name) {
		return voiceErrorResult(tc.ID, "this tool is not available in a voice call")
	}
	var args map[string]any
	if len(tc.Arguments) > 0 && json.Unmarshal(tc.Arguments, &args) != nil {
		return voiceErrorResult(tc.ID, "arguments must be a JSON object")
	}
	str := func(key string) string { s, _ := args[key].(string); return s }
	m := c.reg.mgr
	switch tc.Name {
	case "read_session":
		target := str("session_id")
		if target == "" {
			target = c.sessionID
		}
		if err := m.voiceReadScope(c.sessionID, target); err != nil {
			return voiceErrorResult(tc.ID, err.Error())
		}
		res := m.readSession(target, sessionReadOptions{})
		if res.IsError {
			return voiceErrorResult(tc.ID, voiceResultText(res))
		}
		return voiceResult(tc.ID, map[string]any{"session_id": target, "content": voiceResultText(res)})
	case "book_list":
		ownerID, err := m.voiceBookOwner(c.sessionID)
		if err != nil {
			return voiceErrorResult(tc.ID, err.Error())
		}
		b, err := m.OwnerBookFiles(ownerID)
		if err != nil {
			return voiceErrorResult(tc.ID, "the book cannot be listed")
		}
		prefix := strings.Trim(str("path"), "/")
		var files []string
		for _, f := range b.Files {
			if prefix == "" || strings.HasPrefix(f.Path, prefix) {
				files = append(files, f.Path)
			}
		}
		return voiceResult(tc.ID, map[string]any{"files": files})
	case "book_read":
		ownerID, err := m.voiceBookOwner(c.sessionID)
		if err != nil {
			return voiceErrorResult(tc.ID, err.Error())
		}
		data, err := m.OwnerBookFile(ownerID, str("path"))
		if err != nil {
			return voiceErrorResult(tc.ID, "that book file cannot be read")
		}
		return voiceResult(tc.ID, map[string]any{"path": str("path"), "content": string(data)})
	case "end_call":
		decided, ok1 := stringList(args["decided"])
		open, ok2 := stringList(args["open"])
		if _, ok3 := stringList(args["passed"]); !ok1 || !ok2 || !ok3 {
			return voiceErrorResult(tc.ID, "decided, passed and open must be lists of strings")
		}
		c.mu.Lock()
		if c.minutes == nil {
			c.minutes = &voiceMinutes{decided: decided, open: open}
		}
		c.mu.Unlock()
		c.minutesOnce.Do(func() { close(c.minutesCh) })
		return voiceResult(tc.ID, map[string]any{"status": "ok"})
	}
	// ask_session, pass_to_session, answer_question and request_tap are the
	// session bridge of the next gate. They fail honestly until then.
	return voiceErrorResult(tc.ID, "not available yet")
}

func stringList(v any) ([]string, bool) {
	if v == nil {
		return nil, true
	}
	raw, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(raw))
	for _, x := range raw {
		s, ok := x.(string)
		if !ok {
			return nil, false
		}
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out, true
}

func voiceResultText(r core.Result) string {
	var b strings.Builder
	for _, c := range r.Content {
		b.WriteString(c.Text)
	}
	return b.String()
}

// voiceReadScope is the read-only scope of a call: the called session, and
// for an owner conversation also the sessions that owner owns.
func (m *Manager) voiceReadScope(called, target string) error {
	if target == called {
		return nil
	}
	sess, ok := m.Get(called)
	if !ok || sess.Kind != session.KindOwner {
		return errors.New("only the called session can be read")
	}
	ref := m.ownerRefFor(sess.CWD)
	store, err := m.ownerStore()
	if ref.id == "" || err != nil {
		return errors.New("only the called session can be read")
	}
	own, found, err := store.FindByID(ref.id)
	if err != nil || !found || own.SessionID != called {
		return errors.New("only the called session can be read")
	}
	if _, err := m.ownerSession(own, target); err != nil {
		return errors.New("that session is not one of this owner's sessions")
	}
	return nil
}

// voiceBookOwner is the owner whose book the called session may read.
func (m *Manager) voiceBookOwner(called string) (string, error) {
	sess, ok := m.Get(called)
	if !ok {
		return "", errors.New("the called session is not available")
	}
	ref := m.ownerRefFor(sess.CWD)
	if ref.id == "" {
		return "", errors.New("this session has no project book")
	}
	return ref.id, nil
}

// runHandoff answers one ChatGPT client delegation with moa's backend loop:
// the configured backend model through the plan, the voice allowlist only,
// every tool executed by this call's dispatcher. Its last text is the answer.
func (c *voiceCall) runHandoff(ctx context.Context, request string) string {
	prov, model, err := c.reg.backendProvider()
	if err != nil {
		return "moa's backend is not available for this call."
	}
	msgs := []core.Message{core.NewUserMessage(request)}
	for range voiceHandoffSteps {
		msg, err := streamMessage(ctx, prov, core.Request{Model: model, System: voice.HandoffInstructions, Messages: msgs, Tools: voice.ToolSpecs()})
		if err != nil {
			return "moa's backend failed to answer."
		}
		msgs = append(msgs, msg)
		var calls []core.Content
		var text strings.Builder
		for _, part := range msg.Content {
			switch part.Type {
			case "tool_call":
				calls = append(calls, part)
			case "text":
				text.WriteString(part.Text)
			}
		}
		if len(calls) == 0 {
			return text.String()
		}
		for _, call := range calls {
			args, _ := json.Marshal(call.Arguments)
			r := c.execute(ctx, voice.ToolCall{ID: call.ToolCallID, Name: call.ToolName, Arguments: args})
			msgs = append(msgs, core.NewToolResultMessage(call.ToolCallID, call.ToolName, []core.Content{core.TextContent(string(r.Output))}, r.IsError))
		}
	}
	return "moa's backend needed too many steps for this request."
}

// backendProvider builds the handoff backend through the plan sign-in only:
// a text API key must never pay for a plan call behind the user's back.
func (r *voiceCallRegistry) backendProvider() (core.Provider, core.Model, error) {
	model := voiceModel()
	if r.store == nil || r.mgr.providerFactory == nil {
		return nil, model, errors.New("no backend")
	}
	snap, err := r.store.PeekSnapshot(model.Provider)
	if err != nil || snap.Kind != "oauth" {
		return nil, model, errors.New("backend needs the plan sign-in")
	}
	prov, err := r.mgr.providerFactory(model)
	if err != nil {
		return nil, model, err
	}
	return prov, model, nil
}

func streamMessage(ctx context.Context, prov core.Provider, req core.Request) (core.Message, error) {
	ch, err := prov.Stream(ctx, req)
	if err != nil {
		return core.Message{}, err
	}
	var final *core.Message
	var failure error
	for ev := range ch {
		switch ev.Type {
		case core.ProviderEventDone:
			final = ev.Message
		case core.ProviderEventError:
			failure = ev.Error
		}
	}
	if failure != nil {
		return core.Message{}, failure
	}
	if final == nil {
		return core.Message{}, errors.New("no backend message")
	}
	return *final, nil
}

// persistMinutes appends the call's record to the called session: always a
// minutes block with the literal transcript as transcript.md. Without the
// model's end_call it says the call dropped instead of inventing minutes.
func (c *voiceCall) persistMinutes(reason string) {
	m := c.reg.mgr
	sess, ok := m.Get(c.sessionID)
	if !ok {
		slog.Warn("voice call minutes not saved: session unavailable", "call", c.id)
		return
	}
	c.mu.Lock()
	minutes := c.minutes
	c.mu.Unlock()
	seconds := int(time.Since(c.started).Seconds())
	var b strings.Builder
	fmt.Fprintf(&b, "Voice call ended (%s) after %ds with %s, billing %s.\n\n", reason, seconds, c.provider, c.billing.Kind)
	if minutes == nil {
		b.WriteString("The call dropped before the voice model wrote its minutes. Only the observed transcript is attached.\n")
	} else {
		section := func(title string, items []string) {
			fmt.Fprintf(&b, "%s:\n", title)
			if len(items) == 0 {
				b.WriteString("- (none)\n")
			}
			for _, it := range items {
				fmt.Fprintf(&b, "- %s\n", it)
			}
			b.WriteString("\n")
		}
		section("Decided", minutes.decided)
		section("Still open", minutes.open)
	}
	content := []core.Content{core.TextContent(b.String())}
	custom := map[string]any{"source": "voice_call", "kind": "minutes", "call_id": c.id, "provider": string(c.provider), "billing": c.billing.Kind, "reason": reason, "seconds": seconds, "dropped": minutes == nil}
	if c.usage != nil && c.usage.Seconds != nil {
		custom["usage_seconds"] = *c.usage.Seconds
	}
	if m.attachStore != nil {
		d, err := m.attachStore.PutRef(sess.ID, []byte(c.transcript.Markdown()), attachment.PutMeta{Name: "transcript.md", Mime: "text/markdown", Kind: "file"})
		if err != nil {
			slog.Warn("voice call transcript not saved", "call", c.id, "error", err)
			content[0].Text += "\nThe transcript could not be saved.\n"
		} else {
			content = append(content, core.Content{Type: "document", AttachmentID: d.ID, AttachmentSize: d.Size, MimeType: d.Mime, Filename: d.Name})
		}
	}
	msg := core.AgentMessage{Message: core.NewUserMessageWithContent(content), Custom: custom}
	msg.EnsureMsgID()
	if err := sess.runtime.Bus.Execute(bus.AppendToConversation{SessionID: sess.ID, Message: msg}); err != nil {
		slog.Warn("voice call minutes not saved", "call", c.id, "error", err)
		return
	}
	// Persistence and live clients re-sync from the agent's messages, as for
	// any other appended record.
	sess.runtime.Bus.Publish(bus.CommandExecuted{SessionID: sess.ID, Command: "voice_call", Messages: sess.runtime.Context().Agent.Messages()})
}
