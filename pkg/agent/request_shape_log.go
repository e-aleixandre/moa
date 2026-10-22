package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"

	"github.com/e-aleixandre/moa/pkg/core"
)

func shortHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:4])
}

// messagesChain hashes msgs in order. It returns the hash of the first n of
// them (n <= 0 or beyond len: none/all) and of all of them, hex encoded.
func messagesChain(msgs []core.Message, n int) (atN, all string) {
	h := sha256.New()
	for i, m := range msgs {
		if i == n {
			atN = hex.EncodeToString(h.Sum(nil))
		}
		b, _ := json.Marshal(m)
		h.Write(b)
	}
	all = hex.EncodeToString(h.Sum(nil))
	if n == len(msgs) {
		atN = all
	}
	return atN, all
}

// requestShape logs, without any content, what decides a provider's implicit
// prefix cache: sizes and short hashes of the instructions, the tools and the
// ephemeral block (position and hash), the number of input items, and whether
// the previous request of this run is still an exact prefix of this one
// (prev_is_prefix=false: something earlier was rewritten or displaced). It is
// a partial signal: it is taken before the provider's own transformations.
type requestShape struct {
	items int
	chain string
}

func (s *requestShape) log(req core.Request) {
	if !slog.Default().Enabled(context.Background(), slog.LevelInfo) {
		return
	}
	tools, _ := json.Marshal(req.Tools)
	ephAt, ephHash := -1, ""
	for i, m := range req.Messages {
		if m.Ephemeral {
			body, _ := json.Marshal(m.Content)
			ephAt, ephHash = i, shortHash(body)
		}
	}
	atPrev, all := messagesChain(req.Messages, s.items)
	attrs := []any{
		"cache_key", req.Options.PromptCacheKey,
		"model", req.Model.ID,
		"items", len(req.Messages), "chain_hash", all[:8],
		"system_bytes", len(req.System), "system_hash", shortHash([]byte(req.System)),
		"tools_bytes", len(tools), "tools_hash", shortHash(tools),
		"ephemeral_at", ephAt, "ephemeral_hash", ephHash,
	}
	if s.items > 0 {
		attrs = append(attrs, "prev_items", s.items, "prev_is_prefix", s.items <= len(req.Messages) && atPrev == s.chain)
	}
	s.items, s.chain = len(req.Messages), all
	slog.Info("request shape", attrs...)
}
