package main

import (
	"encoding/json"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

// jsonLineWriter emits agent events as JSON-lines to stdout.
// Used in headless mode with --output json for machine-parseable output.
type jsonLineWriter struct {
	mu  sync.Mutex
	enc *json.Encoder
	// tool_execution_update sends accumulated result, not deltas.
	// Track last length per toolCallId to compute delta.
	toolOutputLens map[string]int

	// Progress tracking
	turnCount      int
	toolsCompleted int             // successful tool_execution_end count
	filesTouched   map[string]bool // paths from edit/write tool args
	startTime      time.Time
}

func newJSONLineWriter() *jsonLineWriter {
	return &jsonLineWriter{
		enc:            json.NewEncoder(os.Stdout),
		toolOutputLens: make(map[string]int),
		filesTouched:   make(map[string]bool),
		startTime:      time.Now(),
	}
}

func (w *jsonLineWriter) handle(e core.AgentEvent) {
	w.mu.Lock()
	defer w.mu.Unlock()

	switch e.Type {
	case core.AgentEventStart:
		w.emit(map[string]any{"type": "agent_start"})

	case core.AgentEventTurnStart:
		w.turnCount++

	case core.AgentEventEnd:
		w.emitSummary()
		w.emit(map[string]any{"type": "agent_end"})

	case core.AgentEventError:
		errMsg := ""
		if e.Error != nil {
			errMsg = e.Error.Error()
		}
		w.emit(map[string]any{"type": "agent_error", "error": errMsg})

	case core.AgentEventMessageUpdate:
		if e.AssistantEvent == nil {
			return
		}
		switch e.AssistantEvent.Type {
		case core.ProviderEventTextDelta:
			w.emit(map[string]any{
				"type":       "message_update",
				"event_type": "text_delta",
				"delta":      e.AssistantEvent.Delta,
			})
		case core.ProviderEventThinkingDelta:
			w.emit(map[string]any{
				"type":       "message_update",
				"event_type": "thinking_delta",
				"delta":      e.AssistantEvent.Delta,
			})
		}

	case core.AgentEventToolExecStart:
		if e.ToolName == "edit" || e.ToolName == "write" {
			if path, ok := e.Args["path"].(string); ok && path != "" {
				w.filesTouched[path] = true
			}
		}
		w.emit(map[string]any{
			"type":         "tool_execution_start",
			"tool_call_id": e.ToolCallID,
			"tool_name":    e.ToolName,
			"args":         e.Args,
		})

	case core.AgentEventToolExecUpdate:
		text := extractResultText(e.Result)
		lastLen := w.toolOutputLens[e.ToolCallID]
		if len(text) > lastLen {
			delta := text[lastLen:]
			w.toolOutputLens[e.ToolCallID] = len(text)
			w.emit(map[string]any{
				"type":         "tool_execution_update",
				"tool_call_id": e.ToolCallID,
				"text":         delta,
			})
		}

	case core.AgentEventToolExecEnd:
		delete(w.toolOutputLens, e.ToolCallID)
		entry := map[string]any{
			"type":         "tool_execution_end",
			"tool_call_id": e.ToolCallID,
			"tool_name":    e.ToolName,
			"is_error":     e.IsError,
		}
		if e.Rejected {
			entry["rejected"] = true
			entry["reason"] = extractResultText(e.Result)
		}
		w.emit(entry)
		if !e.IsError {
			w.toolsCompleted++
		}
		w.emitProgress()

	case core.AgentEventCompactionStart:
		w.emit(map[string]any{"type": "compaction_start"})

	case core.AgentEventCompactionEnd:
		w.emit(map[string]any{"type": "compaction_end"})

		// Ignored: turn_end, message_start, message_end, steer
	}
}

func (w *jsonLineWriter) emitProgress() {
	w.emit(map[string]any{
		"type":            "progress",
		"turns":           w.turnCount,
		"tools_completed": w.toolsCompleted,
		"files_touched":   w.sortedFiles(),
		"elapsed_seconds": int(time.Since(w.startTime).Seconds()),
	})
}

func (w *jsonLineWriter) emitSummary() {
	w.emit(map[string]any{
		"type":            "summary",
		"turns":           w.turnCount,
		"tools_completed": w.toolsCompleted,
		"files_touched":   w.sortedFiles(),
		"elapsed_seconds": int(time.Since(w.startTime).Seconds()),
	})
}

func (w *jsonLineWriter) sortedFiles() []string {
	files := make([]string, 0, len(w.filesTouched))
	for f := range w.filesTouched {
		files = append(files, f)
	}
	sort.Strings(files)
	return files
}

func (w *jsonLineWriter) emit(v map[string]any) {
	w.enc.Encode(v) //nolint:errcheck // stdout write — nothing useful to do on error
}

func extractResultText(r *core.Result) string {
	if r == nil {
		return ""
	}
	var s string
	for _, c := range r.Content {
		if c.Type == "text" {
			s += c.Text
		}
	}
	return s
}
