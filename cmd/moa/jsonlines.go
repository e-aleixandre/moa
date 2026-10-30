package main

import (
	"encoding/json"
	"math"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
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
	usage          usageTotals
	byModel        map[modelUsageKey]*modelUsage
	costUSD        float64 // authoritative RunEnded plus SubagentEnded costs
}

type usageTotals struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cache_read"`
	CacheWrite int `json:"cache_write"`
}

type modelUsageKey struct {
	Provider string
	Model    string
	Role     string
}

type modelUsage struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Role     string `json:"role"`
	Messages int    `json:"messages"`
	usageTotals
	CostUSD float64 `json:"cost_usd"`
}

// roundUSD keeps JSONL costs readable: micro-dollar precision is far below any
// provider's billing granularity.
func roundUSD(v float64) float64 { return math.Round(v*1e6) / 1e6 }

func newJSONLineWriter() *jsonLineWriter {
	return &jsonLineWriter{
		enc:          json.NewEncoder(os.Stdout),
		filesTouched: make(map[string]bool),
		byModel:      make(map[modelUsageKey]*modelUsage),
		startTime:    time.Now(),
	}
}

// subscribeAll subscribes to all bus events via SubscribeAll for guaranteed
// publication order (single goroutine). When done is non-nil, RunEnded is
// delivered via a separate typed subscriber to avoid backpressure from
// high-volume stream events dropping the completion signal.
func (w *jsonLineWriter) subscribeAll(b bus.EventBus, done chan bus.RunEnded) {
	// Dedicated completion subscriber — isolated from streaming backpressure.
	if done != nil {
		b.Subscribe(func(e bus.RunEnded) {
			select {
			case done <- e:
			default:
				select {
				case <-done:
				default:
				}
				select {
				case done <- e:
				default:
				}
			}
		})
	}

	// Ordered rendering of all stream events.
	b.SubscribeAll(func(event any) {
		w.mu.Lock()
		defer w.mu.Unlock()

		switch e := event.(type) {
		case bus.AgentStarted:
			// "telemetry" lets a consumer tell "no child tool calls" from a CLI
			// that predates child tool events.
			w.emit(map[string]any{"type": "agent_start", "telemetry": []string{"subagent_tools", "compaction_usage"}})

		case bus.TurnStarted:
			w.turnCount++

		case bus.AgentEnded:
			w.emit(map[string]any{"type": "agent_end"})

		case bus.RunEnded:
			// RunEnded includes all main-agent provider calls for this run,
			// including calls such as compaction which have no MessageEnded event.
			// Do not add per-message costs here: they are only for message_usage
			// and by_model, and adding them would charge the main run twice.
			w.costUSD += e.Cost
			w.emitSummary()

		case bus.MessageEnded:
			w.recordMessageUsage("main", "", e.Message)

		case bus.SubagentEvent:
			switch inner := e.Inner.(type) {
			case bus.MessageEnded:
				w.recordMessageUsage("subagent", e.JobID, inner.Message)
			case bus.ToolExecStarted:
				// Children's tool calls carry subagent_id so a consumer can
				// tell them from the main agent's, which have none.
				w.emit(map[string]any{
					"type":         "tool_execution_start",
					"subagent_id":  e.JobID,
					"tool_call_id": inner.ToolCallID,
					"tool_name":    inner.ToolName,
					"args":         inner.Args,
				})
			case bus.ToolExecEnded:
				entry := map[string]any{
					"type":         "tool_execution_end",
					"subagent_id":  e.JobID,
					"tool_call_id": inner.ToolCallID,
					"tool_name":    inner.ToolName,
					"is_error":     inner.IsError,
				}
				if inner.Rejected {
					entry["rejected"] = true
					entry["reason"] = inner.Result
				}
				w.emit(entry)
			}

		case bus.SubagentEnded:
			// A subagent's message events provide its model breakdown; its
			// terminal cost is authoritative for the overall ledger. In
			// particular, never add SubagentUsage, which is a running aggregate.
			// A resumed child's cost_usd covers its whole history; only what this
			// run spent enters the ledger.
			w.costUSD += e.NewCostUSD()
			end := map[string]any{
				"type":        "subagent_end",
				"subagent_id": e.JobID,
				"status":      e.Status,
				"cost_usd":    roundUSD(e.CostUSD),
			}
			if e.RunCostUSD != nil && roundUSD(*e.RunCostUSD) != roundUSD(e.CostUSD) {
				end["run_cost_usd"] = roundUSD(*e.RunCostUSD)
			}
			w.emit(end)

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
			end := map[string]any{"type": "compaction_end"}
			if e.Background {
				end["background"] = true
				end["job_id"] = e.JobID
			}
			if s := compactionSummarizerFields(e.Payload); s != nil {
				end["summarizer"] = s
			}
			w.emit(end)

		case bus.CompactionUsage:
			// A background summary call is settled apart from its
			// compaction_end (and is billed even when its result is dropped).
			w.emit(map[string]any{
				"type":        "compaction_usage",
				"job_id":      e.JobID,
				"input":       e.Usage.Input,
				"output":      e.Usage.Output,
				"cache_read":  e.Usage.CacheRead,
				"cache_write": e.Usage.CacheWrite,
			})
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
		"cost_usd":        roundUSD(w.costUSD),
		"usage":           w.usage,
		"by_model":        w.sortedModelUsage(),
	})
}

func (w *jsonLineWriter) recordMessageUsage(role, subagentID string, message core.AgentMessage) {
	if message.Role != "assistant" || message.Usage == nil {
		return
	}

	usage := *message.Usage
	cost := messageCost(message)
	w.usage.add(usage)
	key := modelUsageKey{Provider: message.Provider, Model: message.Model, Role: role}
	aggregate := w.byModel[key]
	if aggregate == nil {
		aggregate = &modelUsage{Provider: message.Provider, Model: message.Model, Role: role}
		w.byModel[key] = aggregate
	}
	aggregate.Messages++
	aggregate.add(usage)
	aggregate.CostUSD += cost

	entry := map[string]any{
		"type":        "message_usage",
		"role":        role,
		"provider":    message.Provider,
		"model":       message.Model,
		"input":       usage.Input,
		"output":      usage.Output,
		"cache_read":  usage.CacheRead,
		"cache_write": usage.CacheWrite,
		"cost_usd":    roundUSD(cost),
	}
	if subagentID != "" {
		entry["subagent_id"] = subagentID
	}
	w.emit(entry)
}

func (u *usageTotals) add(usage core.Usage) {
	u.Input += usage.Input
	u.Output += usage.Output
	u.CacheRead += usage.CacheRead
	u.CacheWrite += usage.CacheWrite
}

func messageCost(message core.AgentMessage) float64 {
	if message.Usage == nil {
		return 0
	}
	model, ok := core.ResolveModel(message.Provider + "/" + message.Model)
	if !ok {
		model, ok = core.ResolveModel(message.Model)
	}
	if !ok || model.Pricing == nil {
		return 0
	}
	return model.Pricing.Cost(*message.Usage)
}

func (w *jsonLineWriter) sortedModelUsage() []*modelUsage {
	models := make([]*modelUsage, 0, len(w.byModel))
	for _, aggregate := range w.byModel {
		copy := *aggregate
		copy.CostUSD = roundUSD(copy.CostUSD)
		models = append(models, &copy)
	}
	sort.Slice(models, func(i, j int) bool {
		if models[i].Provider != models[j].Provider {
			return models[i].Provider < models[j].Provider
		}
		if models[i].Model != models[j].Model {
			return models[i].Model < models[j].Model
		}
		return models[i].Role < models[j].Role
	})
	return models
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

// compactionSummarizerFields renders the summarizer telemetry of a compaction
// for the JSON-lines stream; nil when the payload carries none.
func compactionSummarizerFields(p *core.CompactionPayload) map[string]any {
	if p == nil || p.Summarizer == nil {
		return nil
	}
	s := p.Summarizer
	out := map[string]any{
		"provider":    s.Provider,
		"model":       s.Model,
		"stop_reason": s.StopReason,
		"output_cap":  s.OutputCap,
	}
	if s.OutputCap == core.SummarizerCapApplied {
		out["max_output_tokens"] = s.MaxOutputTokens
	}
	if s.Usage != nil {
		out["input"] = s.Usage.Input
		out["output"] = s.Usage.Output
		out["cache_read"] = s.Usage.CacheRead
		out["cache_write"] = s.Usage.CacheWrite
	}
	return out
}
