package main

import (
	"encoding/json"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/ealeixandre/moa/pkg/bus"
)

// jsonLineWriter emits agent events as JSON-lines to stdout.
// Used in headless mode with --output json for machine-parseable output.
type jsonLineWriter struct {
	mu  sync.Mutex
	enc *json.Encoder

	// Progress tracking
	turnCount      int
	toolsCompleted int             // successful tool_execution_end count
	filesTouched   map[string]bool // paths from edit/write tool args
	startTime      time.Time
}

func newJSONLineWriter() *jsonLineWriter {
	return &jsonLineWriter{
		enc:          json.NewEncoder(os.Stdout),
		filesTouched: make(map[string]bool),
		startTime:    time.Now(),
	}
}

// subscribeAll subscribes to all bus events via SubscribeAll for guaranteed
// publication order (single goroutine). When done is non-nil, RunEnded is
// delivered via a separate typed subscriber to avoid backpressure from
// high-volume stream events dropping the completion signal.
func (w *jsonLineWriter) subscribeAll(b bus.EventBus, done chan<- bus.RunEnded) {
	// Dedicated completion subscriber — isolated from streaming backpressure.
	if done != nil {
		b.Subscribe(func(e bus.RunEnded) { done <- e })
	}

	// Ordered rendering of all stream events.
	b.SubscribeAll(func(event any) {
		w.mu.Lock()
		defer w.mu.Unlock()

		switch e := event.(type) {
		case bus.AgentStarted:
			w.emit(map[string]any{"type": "agent_start"})

		case bus.TurnStarted:
			w.turnCount++

		case bus.AgentEnded:
			w.emitSummary()
			w.emit(map[string]any{"type": "agent_end"})

		case bus.AgentError:
			errMsg := ""
			if e.Err != nil {
				errMsg = e.Err.Error()
			}
			w.emit(map[string]any{"type": "agent_error", "error": errMsg})

		case bus.TextDelta:
			w.emit(map[string]any{
				"type":       "message_update",
				"event_type": "text_delta",
				"delta":      e.Delta,
			})

		case bus.ThinkingDelta:
			w.emit(map[string]any{
				"type":       "message_update",
				"event_type": "thinking_delta",
				"delta":      e.Delta,
			})

		case bus.ToolExecStarted:
			w.trackFile(e.ToolName, e.Args)
			w.emit(map[string]any{
				"type":         "tool_execution_start",
				"tool_call_id": e.ToolCallID,
				"tool_name":    e.ToolName,
				"args":         e.Args,
			})

		case bus.ToolExecUpdate:
			w.emit(map[string]any{
				"type":         "tool_execution_update",
				"tool_call_id": e.ToolCallID,
				"text":         e.Delta,
			})

		case bus.ToolExecEnded:
			entry := map[string]any{
				"type":         "tool_execution_end",
				"tool_call_id": e.ToolCallID,
				"tool_name":    e.ToolName,
				"is_error":     e.IsError,
			}
			if e.Rejected {
				entry["rejected"] = true
				entry["reason"] = e.Result
			}
			w.emit(entry)
			if !e.IsError {
				w.toolsCompleted++
			}
			w.emitProgress()

		case bus.CompactionStarted:
			w.emit(map[string]any{"type": "compaction_start"})

		case bus.CompactionEnded:
			w.emit(map[string]any{"type": "compaction_end"})
		}
	})
}

func (w *jsonLineWriter) trackFile(toolName string, args map[string]any) {
	if toolName == "edit" || toolName == "write" {
		if path, ok := args["path"].(string); ok && path != "" {
			w.filesTouched[path] = true
		}
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
