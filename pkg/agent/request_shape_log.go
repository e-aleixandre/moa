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

// logRequestShape records, without any content, what decides a provider's
// implicit prefix cache: sizes and short hashes of the instructions, the tools
// and the ephemeral block (its position and hash), so a cache read in the
// usage can be matched to the request shape that produced it.
func logRequestShape(req core.Request) {
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
	slog.Info("request shape",
		"cache_key", req.Options.PromptCacheKey,
		"model", req.Model.ID,
		"messages", len(req.Messages),
		"system_bytes", len(req.System), "system_hash", shortHash([]byte(req.System)),
		"tools_bytes", len(tools), "tools_hash", shortHash(tools),
		"ephemeral_at", ephAt, "ephemeral_hash", ephHash,
	)
}
