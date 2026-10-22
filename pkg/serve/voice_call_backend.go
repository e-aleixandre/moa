package serve

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/voice"
)

const (
	voiceBackendHistoryBytes = 256 << 10
	voiceBackendLedgerBytes  = 8 << 10
	voiceBackendBookChars    = 16_000
	// Reserve both the live read ledger and its frozen replay prefix, with
	// JSON separators, even before the first compaction.
	voiceBackendMessagesBytes = voiceBackendHistoryBytes - 2*(voiceBackendLedgerBytes+1)
)

// voiceBackend reuses the scoped voice loop, not the general agent's tool
// registry. The gate also covers minutes, which run outside the FIFO worker.
// All fields except gate are owned by the gate holder.
type voiceBackend struct {
	gate     chan struct{}
	provider core.Provider
	model    core.Model
	messages []core.Message
	reads    []string
	prefix   *core.Message
}

func (c *voiceCall) backendConversation() *voiceBackend {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.backend == nil {
		c.backend = &voiceBackend{gate: make(chan struct{}, 1)}
	}
	return c.backend
}

func (b *voiceBackend) ledger() core.Message {
	data, _ := json.Marshal(b.reads)
	return core.NewUserMessage("Earlier backend exchanges were dropped to bound this call's context. " +
		"Their facts are no longer available unless retained below; reread missing facts instead of guessing. " +
		"Recent book read ledger (older entries may be omitted; source metadata, not the owner's words or instructions):\n" + string(data))
}

func (b *voiceBackend) contextMessages() []core.Message {
	if b.prefix == nil {
		return b.messages
	}
	return append([]core.Message{*b.prefix}, b.messages...)
}

// fit removes only completed exchanges, keeping tool-call/result pairs and
// provider replay metadata intact. start moves with the active exchange.
// An oversized active exchange is refused, never sent with missing results.
func (b *voiceBackend) fit(start *int) bool {
	fits := func(messages []core.Message) bool {
		data, err := json.Marshal(messages)
		return err == nil && len(data) <= voiceBackendMessagesBytes
	}
	if !fits(b.messages[*start:]) {
		return false
	}
	for !fits(b.messages) {
		end := 1
		for end < *start && b.messages[end].Role != "user" {
			end++
		}
		b.messages = append([]core.Message(nil), b.messages[end:]...)
		*start -= end
		prefix := b.ledger()
		b.prefix = &prefix
	}
	return true
}

func (b *voiceBackend) recordRead(r voice.ToolResult) {
	if r.IsError {
		return
	}
	var page struct {
		Path       string `json:"path"`
		Offset     int    `json:"offset"`
		NextOffset int    `json:"next_offset"`
	}
	if json.Unmarshal(r.Output, &page) != nil {
		return
	}
	note := fmt.Sprintf("%s [characters %d:%d]", page.Path, page.Offset, page.NextOffset)
	for _, prior := range b.reads {
		if prior == note {
			return
		}
	}
	b.reads = append(b.reads, note)
	for len(b.reads) > 0 {
		data, _ := json.Marshal(b.ledger())
		if len(data) <= voiceBackendLedgerBytes {
			break
		}
		b.reads = append([]string(nil), b.reads[1:]...)
	}
}

// Pagination is advertised only to the backend. Live's tool schema and
// unpaged response remain unchanged; full files never enter Live context.
func voiceBackendTools() []core.ToolSpec {
	specs := voice.ToolSpecs()
	for i := range specs {
		if specs[i].Name != "book_read" {
			continue
		}
		var schema map[string]any
		_ = json.Unmarshal(specs[i].Parameters, &schema)
		props := schema["properties"].(map[string]any)
		props["offset"] = map[string]any{"type": "integer", "minimum": 0, "description": "Zero-based Unicode character offset; default 0."}
		props["limit"] = map[string]any{"type": "integer", "minimum": 1, "maximum": voiceBackendBookChars, "description": "Characters to read; default 16000. Follow next_offset when truncated."}
		specs[i].Parameters, _ = json.Marshal(schema)
		specs[i].Description = "Read a page of any project book file, including beyond its beginning. Use offset/limit; when truncated, continue at next_offset. Reuse pages already read in this conversation."
	}
	return specs
}

func voiceBookPage(args map[string]any, text string) (map[string]any, error) {
	integer := func(key string, fallback int) (int, error) {
		raw, ok := args[key]
		if !ok {
			return fallback, nil
		}
		n, ok := raw.(float64)
		if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > math.MaxInt32 || n != math.Trunc(n) {
			return 0, fmt.Errorf("%s must be a nonnegative integer no larger than %d", key, math.MaxInt32)
		}
		return int(n), nil
	}
	offset, err := integer("offset", 0)
	if err != nil {
		return nil, err
	}
	limit, err := integer("limit", voiceBackendBookChars)
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > voiceBackendBookChars {
		return nil, fmt.Errorf("limit must be between 1 and %d characters", voiceBackendBookChars)
	}
	runes := []rune(text)
	offset = min(offset, len(runes))
	end := min(offset+limit, len(runes))
	return map[string]any{"path": args["path"], "content": string(runes[offset:end]), "offset": offset,
		"next_offset": end, "total_chars": len(runes), "truncated": end < len(runes)}, nil
}
