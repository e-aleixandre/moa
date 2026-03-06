package tool

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

// ToolConfig provides shared configuration for built-in tools.
type ToolConfig struct {
	WorkspaceRoot string        // Required. All path operations resolve relative to this.
	BashTimeout   time.Duration // Default: 5 minutes.
}

// Defaults fills in zero-value fields with defaults.
func (c *ToolConfig) Defaults() {
	if c.BashTimeout == 0 {
		c.BashTimeout = 5 * time.Minute
	}
}

// RegisterBuiltins adds all built-in tools to the registry.
func RegisterBuiltins(reg *core.Registry, cfg ToolConfig) {
	cfg.Defaults()
	reg.Register(NewBash(cfg))
	reg.Register(NewRead(cfg))
	reg.Register(NewWrite(cfg))
	reg.Register(NewEdit(cfg))
	reg.Register(NewGrep(cfg))
	reg.Register(NewFind(cfg))
	reg.Register(NewLs(cfg))
}

// safePath resolves a path relative to WorkspaceRoot.
// If the path is absolute, it's used as-is but still checked for escapes.
// Returns error if the resolved path escapes the workspace via .. or symlinks.
func safePath(root, path string) (string, error) {
	if root == "" {
		// No workspace restriction
		if filepath.IsAbs(path) {
			return filepath.Clean(path), nil
		}
		return filepath.Abs(path)
	}

	var resolved string
	if filepath.IsAbs(path) {
		resolved = filepath.Clean(path)
	} else {
		resolved = filepath.Clean(filepath.Join(root, path))
	}

	// Evaluate symlinks to get the real root
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("workspace root: %w", err)
	}

	// Walk up from resolved path to find the deepest existing ancestor.
	// Evaluate its symlinks, then check containment.
	existing := resolved
	for {
		_, err := os.Lstat(existing)
		if err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			// Reached filesystem root without finding anything
			break
		}
		existing = parent
	}

	realExisting, err := filepath.EvalSymlinks(existing)
	if err != nil {
		realExisting = existing
	}

	// Rebuild the full real path: real ancestor + rest of the path
	rest, _ := filepath.Rel(existing, resolved)
	var realResolved string
	if rest == "." {
		realResolved = realExisting
	} else {
		realResolved = filepath.Join(realExisting, rest)
	}

	if !strings.HasPrefix(realResolved, realRoot+string(os.PathSeparator)) && realResolved != realRoot {
		return "", fmt.Errorf("path %q escapes workspace %q", path, root)
	}

	return resolved, nil
}

// getString extracts a string parameter or returns a default.
func getString(params map[string]any, key, def string) string {
	v, ok := params[key]
	if !ok {
		return def
	}
	s, ok := v.(string)
	if !ok {
		return def
	}
	return s
}

// getInt extracts an integer parameter or returns a default.
// Fractional float64 values (e.g. 2.6) are rejected — returns default.
func getInt(params map[string]any, key string, def int) int {
	v, ok := params[key]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case float64:
		if n != math.Trunc(n) {
			return def
		}
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	default:
		return def
	}
}

// getBool extracts a boolean parameter or returns a default.
func getBool(params map[string]any, key string, def bool) bool {
	v, ok := params[key]
	if !ok {
		return def
	}
	b, ok := v.(bool)
	if !ok {
		return def
	}
	return b
}

// truncateOutput truncates text to maxBytes, appending a notice if truncated.
func truncateOutput(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	return s[:maxBytes] + "\n\n[output truncated]"
}

// truncateLines truncates text to maxLines lines, appending a notice if truncated.
func truncateLines(s string, maxLines int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= maxLines {
		return s
	}
	return strings.Join(lines[:maxLines], "\n") + "\n\n[output truncated]"
}

const (
	maxOutputBytes = 50 * 1024  // 50KB
	maxOutputLines = 2000
)

// cappedBuffer accumulates up to max bytes. Once full, further writes are
// silently discarded. This prevents OOM when commands produce huge output
// (e.g., cat /dev/urandom). The truncated flag tells the caller to append a notice.
type cappedBuffer struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.truncated {
		return len(p), nil // accept but discard
	}
	remaining := b.max - b.buf.Len()
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		b.buf.Write(p[:remaining])
		b.truncated = true
		return len(p), nil // report full write to caller
	}
	return b.buf.Write(p)
}

func (b *cappedBuffer) String() string {
	return b.buf.String()
}

func (b *cappedBuffer) Len() int {
	return b.buf.Len()
}
