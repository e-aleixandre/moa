package agent

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/e-aleixandre/moa/pkg/core"
)

// NewCacheDiag returns the fingerprint observer for one agent when cache
// diagnostics are on (core.CacheDebug), nil otherwise. It logs, per request,
// how the final provider body relates to the agent's previous one: the first
// block that differs, where the shared prefix ends and which cache marks (with
// their TTL) it still covers. Only hashes, positions and TTLs are logged. The
// previous fingerprint lives as long as the observer, so the link spans runs.
func NewCacheDiag(cacheKey string) func(core.RequestFingerprintFunc) {
	if !core.CacheDebug() {
		return nil
	}
	var mu sync.Mutex
	var prev *core.RequestFingerprint
	return func(f core.RequestFingerprintFunc) {
		fp, err := f()
		if err != nil {
			slog.Warn("request fingerprint failed", "cache_key", cacheKey)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		attrs := []any{"cache_key", cacheKey, "body", short(fp.BodySHA256), "blocks", len(fp.Prefixes), "marks", marksOf(fp.Breakpoints)}
		if prev != nil {
			attrs = append(attrs, diffFingerprints(*prev, fp)...)
		}
		slog.Info("request fingerprint", attrs...)
		prev = &fp
	}
}

func short(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}

func posKey(section string, msg, blk int) string { return fmt.Sprintf("%s:%d:%d", section, msg, blk) }

func marksOf(bps []core.RequestBreakpoint) string {
	if len(bps) == 0 {
		return "none"
	}
	parts := make([]string, len(bps))
	for i, bp := range bps {
		parts[i] = posKey(bp.Section, bp.Message, bp.Block) + "=" + bp.TTL
	}
	return strings.Join(parts, ",")
}

// diffFingerprints compares two consecutive requests of one conversation.
// read_point is the last previous cache mark inside the shared prefix: where a
// cache read can still land; "none" when every previous mark was displaced.
func diffFingerprints(prev, cur core.RequestFingerprint) []any {
	common := 0
	for common < len(prev.Prefixes) && common < len(cur.Prefixes) && prev.Prefixes[common] == cur.Prefixes[common] {
		common++
	}
	lastCommon, firstDiff := "none", "end"
	if common > 0 {
		p := cur.Prefixes[common-1]
		lastCommon = posKey(p.Section, p.Message, p.Block)
	}
	if common < len(cur.Prefixes) {
		p := cur.Prefixes[common]
		firstDiff = posKey(p.Section, p.Message, p.Block)
	}
	inCommon := make(map[string]bool, common)
	for _, p := range cur.Prefixes[:common] {
		inCommon[posKey(p.Section, p.Message, p.Block)] = true
	}
	readPoint := "none"
	for _, bp := range prev.Breakpoints {
		if k := posKey(bp.Section, bp.Message, bp.Block); inCommon[k] {
			readPoint = k + "=" + bp.TTL
		}
	}
	return []any{
		"prev_body", short(prev.BodySHA256),
		"prev_is_prefix", common == len(prev.Prefixes),
		"common_blocks", common, "last_common", lastCommon, "first_diff", firstDiff,
		"read_point", readPoint,
		"tools_same", prev.ToolsSHA256 == cur.ToolsSHA256,
		"system_same", prev.SystemSHA256 == cur.SystemSHA256,
		"options_same", prev.OptionsSHA256 == cur.OptionsSHA256,
	}
}
