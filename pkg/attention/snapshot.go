package attention

import (
	"strings"
	"time"

	"github.com/ealeixandre/moa/pkg/bus"
)

// snapshot.go — per-session aggregated state owned by Service.loop.
//
// The snapshot is the durable truth the service keeps about each session,
// updated from whitelisted bus events. It intentionally records only what the
// voice layer needs and avoids asserting fragile "facts": we track tool CALL
// counts, not "files edited", because the latter can't be inferred reliably
// from the event stream (design §2.3, review point 11).

// sessionSnapshot is the live state of one attached session. Only Service.loop
// reads or writes it.
type sessionSnapshot struct {
	id    string
	gen   uint64           // generation token; stale-event guard
	alias string           // pronounceable name for speech
	title string           // human title (may change via auto-title)
	state bus.SessionState // real state type from the bus, not a raw string

	// LLM-generated status prose. Unlike state and pending counts, this may
	// age; callers keep the actionable fields derived from live bus state.
	attempting   string
	progress     string
	briefUpdated time.Time

	// Pending requests, keyed by moa's real ref id (perm_%d / ask_%d). Maps,
	// not single values: ApprovalManager can hold more than one (verified in
	// pkg/bus/approvals.go — PendingInfo only returns the first, but the manager
	// stores many). Each maps to the attention item id created for it, so a
	// resolution event can find and resolve the right item.
	pendingPerm map[string]string // refID -> attentionItemID
	pendingAsk  map[string]string // refID -> attentionItemID

	// lastError is the most recent error message seen via StateChanged(error),
	// used to dedupe RunEnded{Err} against it within a short window.
	lastError string

	// Live activity is tracked by IDs because executions can overlap. sequence
	// selects the newest remaining action deterministically.
	subagents   map[string]subInfo
	tools       map[string]toolInfo
	activitySeq uint64

	// Phase 2 novelty filter: signatures of the last progress briefing emitted
	// per kind, so a repeated identical verdict (e.g. the same goal-iteration
	// feedback) isn't narrated twice. Keyed by Kind.
	lastBriefSig map[Kind]string

	// terminations is a bounded FIFO of successful runs awaiting the guardian's
	// explicit spoken acknowledgement. It is deliberately not an attention
	// item: run completion requires no user action.
	terminations          []RunTermination
	terminationSignatures map[string]struct{}
	terminationSigOrder   []string
}

type subInfo struct {
	task  string
	model string
	seq   uint64
}

type toolInfo struct {
	name   string
	detail string
	seq    uint64
}

func newSessionSnapshot(id, alias, title string, gen uint64) *sessionSnapshot {
	return &sessionSnapshot{
		id:                    id,
		gen:                   gen,
		alias:                 alias,
		title:                 title,
		state:                 bus.StateIdle,
		pendingPerm:           make(map[string]string),
		pendingAsk:            make(map[string]string),
		subagents:             make(map[string]subInfo),
		tools:                 make(map[string]toolInfo),
		lastBriefSig:          make(map[Kind]string),
		terminationSignatures: make(map[string]struct{}),
	}
}

func (s *sessionSnapshot) startSubagent(jobID, task, model string) {
	s.activitySeq++
	s.subagents[jobID] = subInfo{task: task, model: model, seq: s.activitySeq}
}

func (s *sessionSnapshot) endSubagent(jobID string) bool {
	if _, ok := s.subagents[jobID]; !ok {
		return false
	}
	delete(s.subagents, jobID)
	return true
}

func (s *sessionSnapshot) startTool(toolCallID, name string, args map[string]any) {
	s.activitySeq++
	s.tools[toolCallID] = toolInfo{name: name, detail: toolActivityTarget(name, args), seq: s.activitySeq}
}

func (s *sessionSnapshot) endTool(toolCallID string) bool {
	if _, ok := s.tools[toolCallID]; !ok {
		return false
	}
	delete(s.tools, toolCallID)
	return true
}

// clearRunTools drops in-flight tool activity when a run ends. Tools are bound
// to the run, but an async subagent can outlive its parent run and is cleared
// by its own SubagentEnded, so subagents are intentionally left untouched here.
func (s *sessionSnapshot) clearRunTools() bool {
	if len(s.tools) == 0 {
		return false
	}
	clear(s.tools)
	return true
}

func (s *sessionSnapshot) activity() *SessionActivity {
	if len(s.subagents) > 0 {
		var newest subInfo
		for _, subagent := range s.subagents {
			if subagent.seq > newest.seq {
				newest = subagent
			}
		}
		return &SessionActivity{
			Kind:   "subagent",
			Detail: boundedActivityDetail(newest.task),
			Model:  boundedActivityLabel(newest.model),
			Count:  len(s.subagents),
		}
	}

	var newest toolInfo
	for _, tool := range s.tools {
		if tool.seq > newest.seq {
			newest = tool
		}
	}
	if newest.seq == 0 {
		return nil
	}
	return &SessionActivity{
		Kind:   "tool",
		Tool:   boundedActivityLabel(newest.name),
		Detail: boundedActivityDetail(newest.detail),
	}
}

func toolActivityTarget(name string, args map[string]any) string {
	switch name {
	case "bash":
		value, _ := args["command"].(string)
		return value
	case "fetch_content":
		value, _ := args["url"].(string)
		return value
	default:
		// A file path (edit/read/write/multiedit) is a useful "what now"
		// detail; anything else has no concise target, so leave it empty
		// rather than echo the tool name back as its own detail.
		value, _ := args["path"].(string)
		return value
	}
}

func boundedActivityDetail(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > 140 {
		return strings.TrimSpace(string(runes[:140]))
	}
	return value
}

// boundedActivityLabel caps short identifier fields (model, tool name) so a
// pathological value can't bloat the prompt. These are normally a few runes.
func boundedActivityLabel(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > 64 {
		return strings.TrimSpace(string(runes[:64]))
	}
	return value
}
