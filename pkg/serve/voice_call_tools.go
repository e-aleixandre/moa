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
	// Once the call is ending only its minutes and reads may still run: the
	// owner hung up, so nothing new may act on the session for them.
	if c.ending.Load() && voiceEffectTools[tc.Name] {
		return voiceErrorResult(tc.ID, "the call has ended; nothing more can be passed or answered in it")
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
		out := map[string]any{"session_id": target, "content": voiceResultText(res)}
		if target == c.sessionID {
			if pending := m.voicePendingSummary(target); pending != "" {
				out["pending"] = pending
			}
		}
		return voiceResult(tc.ID, out)
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
	case "pass_to_session":
		return c.passToSession(tc.ID, str("summary"))
	case "answer_question":
		answers, ok := stringList(args["answers"])
		if !ok {
			return voiceErrorResult(tc.ID, "answers must be a list of strings")
		}
		if a := str("answer"); a != "" {
			if len(answers) > 0 {
				return voiceErrorResult(tc.ID, "give either answer or answers, not both")
			}
			answers = []string{a}
		}
		return c.answerQuestion(tc.ID, str("ask_id"), answers)
	case "request_tap":
		return c.requestTap(tc.ID, str("item_id"))
	case "ask_session":
		return c.askSession(tc.ID, str("question"))
	}
	return voiceErrorResult(tc.ID, "this tool is not available in a voice call")
}

// voiceEffectTools act on the called session.
var voiceEffectTools = map[string]bool{"pass_to_session": true, "answer_question": true, "request_tap": true, "ask_session": true}

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
	prov, model, err := c.reg.backendProvider(c.id)
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

// backendProvider builds the handoff backend of call id bound to the plan
// sign-in the call was opened with, never to the text selection: a text API
// key must never pay for a plan call, nor another account serve it.
func (r *voiceCallRegistry) backendProvider(id string) (core.Provider, core.Model, error) {
	model := voiceModel()
	plan, ok := r.openedPlan(id)
	if !ok || r.opts.Backend == nil {
		return nil, model, errors.New("backend needs the plan sign-in")
	}
	prov, err := r.opts.Backend(model, plan)
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

// persistMinutes records the call in the called session exactly once:
// always a minutes record with the literal transcript as transcript.md,
// saved to disk at hangup even while the session is busy. Without the
// model's end_call it says the call dropped instead of inventing minutes.
// Passed comes from the server's ledger, already delivered and never sent
// again. Only Decided or Still open (which includes anything waiting for a
// tap) wake the session: one run, or one steer when busy.
func (c *voiceCall) persistMinutes(reason string) {
	m := c.reg.mgr
	sess, ok := m.Get(c.sessionID)
	if !ok {
		slog.Warn("voice call minutes not saved: session unavailable", "call", c.id)
		return
	}
	c.mu.Lock()
	minutes := c.minutes
	passed := append([]voicePassed(nil), c.passed...)
	c.mu.Unlock()
	var decided, open []string
	if minutes != nil {
		decided, open = minutes.decided, append([]string(nil), minutes.open...)
	}
	open = append(open, voicePendingTaps(sess)...)
	wake := len(decided) > 0 || len(open) > 0
	seconds := int(time.Since(c.started).Seconds())

	var b strings.Builder
	fmt.Fprintf(&b, "<voice_minutes>\nVoice call ended (%s) after %ds with %s, billing %s.\n\n", reason, seconds, c.provider, c.billing.Kind)
	if minutes == nil {
		b.WriteString("The call dropped before the voice model wrote its minutes. Only the observed transcript remains.\n\n")
	}
	section := func(title string, items []string) {
		fmt.Fprintf(&b, "%s:\n", title)
		if len(items) == 0 {
			b.WriteString("- (none)\n")
		}
		for _, it := range items {
			fmt.Fprintf(&b, "- %s\n", voiceEscape(it, "voice_minutes"))
		}
		b.WriteString("\n")
	}
	section("Decided", decided)
	b.WriteString("Passed to this session (already_delivered — do not execute again):\n")
	if len(passed) == 0 {
		b.WriteString("- (none)\n")
	}
	ledger := make([]map[string]any, 0, len(passed))
	for _, p := range passed {
		fmt.Fprintf(&b, "- [%s] already_delivered: %s\n", p.DeliveryID, voiceEscape(p.Summary, "voice_minutes"))
		ledger = append(ledger, map[string]any{"delivery_id": p.DeliveryID, "already_delivered": true})
	}
	b.WriteString("\n")
	section("Still open", open)
	b.WriteString("</voice_minutes>\n")
	text := b.String()

	custom := c.custom("minutes")
	for k, v := range map[string]any{"billing": c.billing.Kind, "reason": reason, "seconds": seconds, "dropped": minutes == nil, "passed": ledger, "wake": wake} {
		custom[k] = v
	}
	if c.usage != nil && c.usage.Seconds != nil {
		custom["usage_seconds"] = *c.usage.Seconds
	}
	usage := c.usageRecord(sess, seconds, len(passed))
	custom["usage"] = usage
	c.reg.recordUsage(usage)
	content := []core.Content{core.TextContent(text)}
	if m.attachStore != nil {
		d, err := m.attachStore.PutRef(sess.ID, []byte(c.transcript.Markdown()), attachment.PutMeta{Name: "transcript.md", Mime: "text/markdown", Kind: "file"})
		if err != nil {
			slog.Warn("voice call transcript not saved", "call", c.id, "error", err)
			content[0].Text += "\nThe transcript could not be saved.\n"
		} else {
			content = append([]core.Content{{Type: "document", AttachmentID: d.ID, AttachmentSize: d.Size, MimeType: d.Mime, Filename: d.Name}}, content...)
		}
	}
	// The record is display-only (the model never sees a session_event), so
	// it can be saved at once, busy or not; the wake carries the same text to
	// the model as an ordinary voice delivery and is the only part that may
	// wait in the queue.
	record := core.AgentMessage{Message: core.Message{Role: "session_event", Content: content, Timestamp: time.Now().Unix()}, Custom: custom}
	record.EnsureMsgID()
	// The save that holds this record derives the header marker from it
	// (servePersister.settleVoiceOwed), so a restart that loses the wake
	// finds it from the session header alone.
	if err := m.recordVoice(sess.ID, c.id+"/minutes", record); err != nil {
		slog.Warn("voice call minutes not saved", "call", c.id, "error", err)
		return
	}
	// The wake is owed by the saved record itself (wake=true), under an ID
	// derived from it: admitted here, or after a restart that lost it.
	m.wakeVoiceMinutes(sess)
}

// voicePendingTaps names what still waits for the owner's tap when a call
// ends: a requires_tap question or a permission, and a secret request only
// as a generic indicator. These are Still open, so the minutes wake.
func voicePendingTaps(sess *ManagedSession) []string {
	var out []string
	info, _ := bus.QueryTyped[bus.GetPendingApproval, bus.PendingApprovalInfo](sess.runtime.Bus, bus.GetPendingApproval{})
	if a := info.Ask; a != nil && a.RequiresTap {
		var qs []string
		for _, q := range a.Questions {
			qs = append(qs, q.Text)
		}
		out = append(out, "Waiting for your tap: question \""+strings.Join(qs, "; ")+"\"")
	}
	if p := info.Permission; p != nil && !voiceSecretTool(p.ToolName) {
		out = append(out, "Waiting for your tap: permission for "+p.ToolName)
	}
	if b := sess.runtime.Context().SecretBroker; b != nil && len(b.Pending()) > 0 {
		out = append(out, "A secret is waiting — answer it later on the secure sheet.")
	}
	return out
}
