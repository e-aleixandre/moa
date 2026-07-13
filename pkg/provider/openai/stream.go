package openai

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/jsonutil"
)

// Responses API SSE event types we handle.
const (
	eventOutputItemAdded   = "response.output_item.added"
	eventOutputTextDelta   = "response.output_text.delta"
	eventFuncCallArgsDelta = "response.function_call_arguments.delta"
	eventFuncCallArgsDone  = "response.function_call_arguments.done"
	eventOutputItemDone    = "response.output_item.done"
	eventCompleted         = "response.completed"
	eventIncomplete        = "response.incomplete"
	eventFailed            = "response.failed"
	eventError             = "error"
	// Reasoning summary events (thinking).
	eventReasoningSummaryDelta = "response.reasoning_summary_text.delta"
)

// event is the raw SSE JSON payload from the Responses API.
type event struct {
	Type        string          `json:"type"`
	Item        *item           `json:"item,omitempty"`
	ItemRaw     json.RawMessage `json:"-"` // full JSON of item (set during parsing)
	Delta       string          `json:"delta,omitempty"`
	OutputIndex int             `json:"output_index"`
	Response    *struct {
		ID     string `json:"id"`
		Model  string `json:"model"`
		Status string `json:"status"`
		Usage  *struct {
			InputTokens        int `json:"input_tokens"`
			OutputTokens       int `json:"output_tokens"`
			TotalTokens        int `json:"total_tokens"`
			InputTokensDetails *struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"input_tokens_details"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		// EndTurn mirrors codex's ResponseCompleted.end_turn (Option<bool>): the
		// backend can mark a completed response as "not the end of the turn —
		// resend the conversation as-is to continue". nil means the field was
		// absent (the common case on the current backend). A *bool is required
		// to tell absent (nil) from false.
		EndTurn *bool `json:"end_turn"`
	} `json:"response,omitempty"`
	// For function_call_arguments.done
	Arguments string `json:"arguments,omitempty"`
	// For error events
	Message string `json:"message,omitempty"`
	Code    string `json:"code,omitempty"`
}

type item struct {
	Type      string `json:"type"` // "message", "function_call", "reasoning"
	ID        string `json:"id,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Status    string `json:"status,omitempty"`
	// Phase is the output-message phase ("commentary"/"final_answer"). OpenAI
	// warns that dropping it when replaying manually causes early stopping.
	Phase   string `json:"phase,omitempty"`
	Content []struct {
		Type    string `json:"type"` // "output_text" | "refusal" | reasoning "text"
		Text    string `json:"text"`
		Refusal string `json:"refusal"`
	} `json:"content,omitempty"`
	Summary []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"summary,omitempty"`
}

// slot tracks one output item of a response, keyed by its output_index. The
// Responses API interleaves events from multiple concurrent output items
// (reasoning, message, function_call), each carrying an output_index; keeping a
// per-index slot — as the reference clients (pi, codex) do — is the only way to
// route deltas and finalizations to the right content block without collapsing
// or duplicating them.
type slot struct {
	kind         string // "message" | "function_call" | "reasoning"
	contentIndex int    // index into message.Content this slot owns

	// function_call
	callID   string
	callName string
	callItem string // "fc_..." output-item id
	argsJSON strings.Builder
	// partial JSON parsing for streaming tool call arguments
	partialParser jsonutil.PartialParser
	lastParseLen  int
	done          bool // materialized already (dedupe args.done vs item.done)

	// message
	msgItemID string
	msgPhase  string
}

// streamState tracks the evolving message across SSE events.
type streamState struct {
	message core.Message
	started bool

	// slots holds the in-flight output items keyed by output_index.
	slots map[int]*slot
}

// getSlot returns the slot for an output index if it matches kind.
func (s *streamState) getSlot(idx int, kind string) *slot {
	sl := s.slots[idx]
	if sl != nil && sl.kind == kind {
		return sl
	}
	return nil
}

// consumeStream parses Responses API SSE and emits normalized AssistantEvents.
func consumeStream(ctx context.Context, body io.Reader, ch chan<- core.AssistantEvent) {
	state := &streamState{
		message: core.Message{
			Role:      "assistant",
			Provider:  "openai",
			Timestamp: time.Now().Unix(),
		},
		slots: make(map[int]*slot),
	}
	sentTerminal := false

	defer func() {
		if !sentTerminal {
			ch <- core.AssistantEvent{
				Type:  core.ProviderEventError,
				Error: fmt.Errorf("stream ended without response.completed"),
			}
		}
	}()

	reader := bufio.NewReaderSize(body, 1024*1024)

	for {
		if ctx.Err() != nil {
			ch <- core.AssistantEvent{Type: core.ProviderEventError, Error: ctx.Err()}
			sentTerminal = true
			return
		}

		line, err := readLine(reader)
		if err != nil {
			if err == io.EOF {
				return
			}
			ch <- core.AssistantEvent{Type: core.ProviderEventError, Error: fmt.Errorf("read: %w", err)}
			sentTerminal = true
			return
		}

		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		// Some Responses API events use "event:" lines — we only need "data:" lines.
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := line[6:]
		if data == "[DONE]" {
			return
		}

		var ev event
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}
		// For output_item.done events, preserve the raw JSON of the item so we
		// can store it verbatim as ThinkingSignature (avoids losing unknown fields).
		if ev.Type == eventOutputItemDone {
			var raw struct {
				Item json.RawMessage `json:"item"`
			}
			if json.Unmarshal([]byte(data), &raw) == nil {
				ev.ItemRaw = raw.Item
			}
		}

		terminal := processEvent(state, &ev, ch)
		if terminal {
			sentTerminal = true
			return
		}
	}
}

// processEvent handles a single SSE event. Returns true if it emitted a terminal event.
func processEvent(state *streamState, ev *event, ch chan<- core.AssistantEvent) bool {
	switch ev.Type {
	case eventOutputItemAdded:
		if ev.Item == nil {
			return false
		}
		state.ensureStarted(ch)
		switch ev.Item.Type {
		case "function_call":
			sl := &slot{
				kind:     "function_call",
				callID:   ev.Item.CallID,
				callName: ev.Item.Name,
				callItem: ev.Item.ID,
			}
			if ev.Item.Arguments != "" {
				sl.argsJSON.WriteString(ev.Item.Arguments)
			}
			// Reserve a content block now so the call keeps a stable index even
			// when other output items interleave; arguments are filled on done.
			tc := core.ToolCallContent(sl.callID, sl.callName, nil)
			tc.ToolCallItemID = sl.callItem
			sl.contentIndex = state.appendBlock(tc)
			state.slots[ev.OutputIndex] = sl
			ch <- core.AssistantEvent{
				Type:         core.ProviderEventToolCallStart,
				ContentIndex: sl.contentIndex,
				ToolCallID:   sl.callID,
				ToolName:     sl.callName,
			}
		case "message":
			sl := &slot{kind: "message", msgItemID: ev.Item.ID, msgPhase: ev.Item.Phase}
			sl.contentIndex = state.appendBlock(core.TextContent(""))
			state.slots[ev.OutputIndex] = sl
		case "reasoning":
			sl := &slot{kind: "reasoning"}
			sl.contentIndex = state.appendBlock(core.Content{Type: "thinking"})
			state.slots[ev.OutputIndex] = sl
		}

	case eventOutputTextDelta:
		state.ensureStarted(ch)
		if ev.Delta == "" {
			return false
		}
		sl := state.getSlot(ev.OutputIndex, "message")
		if sl != nil {
			state.message.Content[sl.contentIndex].Text += ev.Delta
		} else {
			// Fallback: a text delta without a preceding message item.
			state.message.Content = appendOrUpdateText(state.message.Content, ev.Delta)
		}
		ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, Delta: ev.Delta}

	case eventReasoningSummaryDelta:
		if ev.Delta != "" {
			ch <- core.AssistantEvent{Type: core.ProviderEventThinkingDelta, Delta: ev.Delta}
		}

	case eventFuncCallArgsDelta:
		sl := state.getSlot(ev.OutputIndex, "function_call")
		if sl == nil || ev.Delta == "" {
			return false
		}
		sl.argsJSON.WriteString(ev.Delta)
		evt := core.AssistantEvent{
			Type:         core.ProviderEventToolCallDelta,
			ContentIndex: sl.contentIndex,
			Delta:        ev.Delta,
			ToolCallID:   sl.callID,
			ToolName:     sl.callName,
		}
		// Throttled partial parse: only every 200 bytes to cap CPU cost.
		accumulated := sl.argsJSON.String()
		if len(accumulated)-sl.lastParseLen >= 200 {
			if parsed := sl.partialParser.Parse(accumulated); parsed != nil {
				evt.PartialArgs = parsed
			}
			sl.lastParseLen = len(accumulated)
		}
		ch <- evt

	case eventFuncCallArgsDone:
		sl := state.getSlot(ev.OutputIndex, "function_call")
		if sl == nil {
			return false
		}
		argsStr := ev.Arguments
		if argsStr == "" {
			argsStr = sl.argsJSON.String()
		}
		state.finalizeToolCall(sl, argsStr, ch)

	case eventOutputItemDone:
		if ev.Item == nil {
			return false
		}
		switch ev.Item.Type {
		case "message":
			sl := state.getSlot(ev.OutputIndex, "message")
			if sl == nil {
				delete(state.slots, ev.OutputIndex)
				return false
			}
			// Reconcile final text (output_text and refusal) from the
			// authoritative done event, and carry the item id + phase so the
			// message can be replayed verbatim next request. Dropping phase
			// makes OpenAI stop early on later turns.
			var text string
			for _, c := range ev.Item.Content {
				switch c.Type {
				case "output_text":
					text += c.Text
				case "refusal":
					text += c.Refusal
				}
			}
			id := ev.Item.ID
			if id == "" {
				id = sl.msgItemID
			}
			phase := ev.Item.Phase
			if phase == "" {
				phase = sl.msgPhase
			}
			if text != "" {
				state.message.Content[sl.contentIndex].Text = text
			}
			state.message.Content[sl.contentIndex].TextSignature = encodeTextSignature(id, phase)
			delete(state.slots, ev.OutputIndex)
		case "function_call":
			sl := state.getSlot(ev.OutputIndex, "function_call")
			// Reconcile from the authoritative item in case
			// function_call_arguments.done never arrived (a stream variant or a
			// dropped event). Without this the call is silently lost and the
			// loop sees "no tools" and ends the turn — a stall. Dedupe if
			// arguments.done already finalized it.
			if sl != nil && !sl.done {
				if ev.Item.CallID != "" {
					sl.callID = ev.Item.CallID
				}
				if ev.Item.Name != "" {
					sl.callName = ev.Item.Name
				}
				if ev.Item.ID != "" {
					sl.callItem = ev.Item.ID
				}
				argsStr := ev.Item.Arguments
				if argsStr == "" {
					argsStr = sl.argsJSON.String()
				}
				state.finalizeToolCall(sl, argsStr, ch)
			}
			delete(state.slots, ev.OutputIndex)
		case "reasoning":
			sl := state.getSlot(ev.OutputIndex, "reasoning")
			// Store the raw item JSON as ThinkingSignature so it can be sent
			// back verbatim in future requests (preserves encrypted_content,
			// summary[].type, and any other fields the API requires).
			signature := string(ev.ItemRaw)
			if signature == "" {
				// Fallback: re-marshal the parsed item (lossy but better than nothing).
				if raw, err := json.Marshal(ev.Item); err == nil {
					signature = string(raw)
				}
			}
			// Prefer the summary; fall back to reasoning content text for the
			// human-visible thinking (matches pi: summary || content).
			var thinkingText string
			for _, s := range ev.Item.Summary {
				if thinkingText != "" {
					thinkingText += "\n\n"
				}
				thinkingText += s.Text
			}
			if thinkingText == "" {
				for _, c := range ev.Item.Content {
					if c.Text == "" {
						continue
					}
					if thinkingText != "" {
						thinkingText += "\n\n"
					}
					thinkingText += c.Text
				}
			}
			if sl != nil {
				state.message.Content[sl.contentIndex].Thinking = thinkingText
				state.message.Content[sl.contentIndex].ThinkingSignature = signature
			} else {
				state.message.Content = append(state.message.Content, core.Content{
					Type: "thinking", Thinking: thinkingText, ThinkingSignature: signature,
				})
			}
			delete(state.slots, ev.OutputIndex)
		}

	case eventCompleted:
		return state.finalize(ev, ch)

	case eventIncomplete:
		// Distinct terminal event (the canonical form; some backends send it
		// instead of response.completed with status "incomplete"). Handle it
		// identically so the agent receives the persisted max_tokens result.
		return state.finalize(ev, ch)

	case eventFailed:
		errMsg := "response failed"
		if ev.Response != nil && ev.Response.Error != nil {
			errMsg = ev.Response.Error.Message
		}
		ch <- core.AssistantEvent{Type: core.ProviderEventError, Error: fmt.Errorf("openai: %s", errMsg)}
		return true

	case eventError:
		errMsg := ev.Message
		if errMsg == "" {
			errMsg = ev.Code
		}
		if errMsg == "" {
			errMsg = "unknown error"
		}
		ch <- core.AssistantEvent{Type: core.ProviderEventError, Error: fmt.Errorf("openai: %s", errMsg)}
		return true
	}

	return false
}

// appendBlock appends a content block and returns its index.
func (s *streamState) appendBlock(c core.Content) int {
	s.message.Content = append(s.message.Content, c)
	return len(s.message.Content) - 1
}

// ensureStarted emits the ProviderEventStart exactly once.
func (s *streamState) ensureStarted(ch chan<- core.AssistantEvent) {
	if s.started {
		return
	}
	s.started = true
	partial := s.message
	ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &partial}
}

// finalizeToolCall fills a slot's reserved tool_call block with parsed arguments
// and emits ToolCallEnd once, marking the slot done so the same call is not
// finalized twice (args.done then item.done).
func (s *streamState) finalizeToolCall(sl *slot, argsStr string, ch chan<- core.AssistantEvent) {
	if sl.done {
		return
	}
	var args map[string]any
	if argsStr != "" {
		_ = json.Unmarshal([]byte(argsStr), &args)
	}
	blk := &s.message.Content[sl.contentIndex]
	blk.ToolCallID = sl.callID
	blk.ToolName = sl.callName
	blk.ToolCallItemID = sl.callItem
	blk.Arguments = args
	sl.done = true
	ch <- core.AssistantEvent{
		Type:         core.ProviderEventToolCallEnd,
		ContentIndex: sl.contentIndex,
		ToolCallID:   sl.callID,
		ToolName:     sl.callName,
	}
}

// finalize handles response.completed or response.incomplete. Precedence,
// matching the reference clients (codex): (1) incomplete → max_tokens;
// (2) tool calls → tool_use;
// (3) end_turn:false → "continue" (the backend wants the conversation resent
// as-is to keep going — codex turn.rs:2298); (4) no substantive content and no
// continue signal → typed EmptyResponseError (the loop re-samples once before
// surfacing it); (5) normal Done.
func (s *streamState) finalize(ev *event, ch chan<- core.AssistantEvent) bool {
	var endTurn *bool
	if ev.Response != nil {
		endTurn = ev.Response.EndTurn
		s.message.StopReason = mapStatus(ev.Response.Status)
		if ev.Response.Usage != nil {
			u := ev.Response.Usage
			// Responses API input_tokens INCLUDES cached tokens; the cost
			// model bills Input and CacheRead as separate buckets (cache
			// reads are ~10x cheaper), so split them out. Without this the
			// whole prompt is billed at full input price — up to ~10x the
			// real cost on cache-heavy runs, tripping max_budget early.
			cached := 0
			if u.InputTokensDetails != nil {
				cached = u.InputTokensDetails.CachedTokens
			}
			nonCached := u.InputTokens - cached
			if nonCached < 0 {
				nonCached = 0
			}
			s.message.Usage = &core.Usage{
				Input:       nonCached,
				Output:      u.OutputTokens,
				CacheRead:   cached,
				TotalTokens: u.TotalTokens,
			}
		}
		// Response.Model is the actual model used (e.g. "gpt-5.5"); Response.ID
		// is "resp_..." and would poison per-model cost attribution/ResolveModel.
		if ev.Response.Model != "" {
			s.message.Model = ev.Response.Model
		}

		// (1) A response that ran out of output budget (status "incomplete")
		// did NOT finish the turn. Deliver it as Done with stop_reason
		// max_tokens so the agent can persist/account for the partial response,
		// avoid executing incomplete tool calls, and apply its bounded retry.
		if ev.Response.Status == "incomplete" {
			s.ensureStarted(ch)
			final := s.message
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &final}
			return true
		}
	}

	// (2) Any tool call → tool_use. This wins over end_turn: the loop must run
	// the tools and reply with their results (which is itself the continuation),
	// so it must NOT short-circuit to a bare "continue" that skips execution.
	hasToolCall := false
	for _, c := range s.message.Content {
		if c.Type == "tool_call" {
			s.message.StopReason = "tool_use"
			hasToolCall = true
			break
		}
	}

	// (3) end_turn == false (backend explicitly says "not done"): mark the turn
	// as a continuation so the loop resends the conversation as-is and lets the
	// model keep going, whether or not this response carried text. Skips the
	// empty guard below — an empty/reasoning-only response with end_turn:false
	// is a legitimate mid-turn pause, not a stall. (On the current backend
	// end_turn is usually absent, so this rarely fires; it's the correct,
	// forward-compatible behavior and mirrors codex.)
	if !hasToolCall && endTurn != nil && !*endTurn {
		s.message.StopReason = "continue"
		s.ensureStarted(ch) // guarantee Start/End bracketing even with no items
		final := s.message
		ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &final}
		return true
	}

	// (4) A "completed" response with no substantive content (no text and no
	// tool call; reasoning-only counts as empty) and no continue signal is the
	// empty/stalled turn. Surface it as a TYPED error so the loop can re-sample
	// once (a transient empty turn during polling self-corrects) before ending
	// the run — instead of dying in silence or failing on the first occurrence.
	if !hasSubstantiveContent(s.message.Content) {
		ch <- core.AssistantEvent{
			Type:  core.ProviderEventError,
			Error: &core.EmptyResponseError{Provider: "openai", Usage: s.message.Usage},
		}
		return true
	}

	// (5) Normal end of turn.
	final := s.message
	ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &final}
	return true
}

func mapStatus(status string) string {
	switch status {
	case "completed":
		return "end_turn"
	case "cancelled":
		return "cancelled"
	case "failed":
		return "error"
	case "incomplete":
		return "max_tokens"
	default:
		return status
	}
}

// hasSubstantiveContent reports whether an assistant message contains anything
// the agent can act on or show: a non-blank text block or a tool call. A message
// with only empty/whitespace text or only reasoning is NOT substantive — that is
// the empty/stalled turn we must not treat as a legitimate completion.
func hasSubstantiveContent(blocks []core.Content) bool {
	for _, c := range blocks {
		switch c.Type {
		case "text":
			if strings.TrimSpace(c.Text) != "" {
				return true
			}
		case "tool_call":
			return true
		}
	}
	return false
}

// appendOrUpdateText appends text to the last text content block, or creates one.
func appendOrUpdateText(blocks []core.Content, text string) []core.Content {
	for i := len(blocks) - 1; i >= 0; i-- {
		if blocks[i].Type == "text" {
			blocks[i].Text += text
			return blocks
		}
	}
	return append(blocks, core.TextContent(text))
}

// encodeTextSignature packs an output-message item id and phase into a compact
// JSON blob stored on the text block's TextSignature. Returns "" when there is
// nothing worth round-tripping (mirrors pi's encodeTextSignatureV1).
func encodeTextSignature(id, phase string) string {
	if id == "" && phase == "" {
		return ""
	}
	sig := textSignatureV1{V: 1, ID: id}
	if phase == "commentary" || phase == "final_answer" {
		sig.Phase = phase
	}
	raw, err := json.Marshal(sig)
	if err != nil {
		return ""
	}
	return string(raw)
}

// textSignatureV1 is the JSON shape stored in core.Content.TextSignature for
// OpenAI Responses message items.
type textSignatureV1 struct {
	V     int    `json:"v"`
	ID    string `json:"id,omitempty"`
	Phase string `json:"phase,omitempty"`
}

// parseTextSignature decodes a TextSignature blob back into id/phase. Tolerates
// empty/legacy values.
func parseTextSignature(sig string) (id, phase string) {
	if sig == "" {
		return "", ""
	}
	if strings.HasPrefix(sig, "{") {
		var v textSignatureV1
		if json.Unmarshal([]byte(sig), &v) == nil && v.V == 1 {
			// Only echo back a phase OpenAI accepts; a corrupt/foreign phase
			// would make the replayed request invalid.
			if v.Phase != "commentary" && v.Phase != "final_answer" {
				return v.ID, ""
			}
			return v.ID, v.Phase
		}
		return "", ""
	}
	// Legacy: bare id string.
	return sig, ""
}

// readLine reads a single line handling long lines.
func readLine(r *bufio.Reader) (string, error) {
	var sb strings.Builder
	for {
		segment, isPrefix, err := r.ReadLine()
		if err != nil {
			return "", err
		}
		sb.Write(segment)
		if !isPrefix {
			return sb.String(), nil
		}
	}
}
