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
	WorkspaceRoot  string   // Required. All path operations resolve relative to this.
	DisableSandbox bool     // When true, safePath allows any absolute path (YOLO mode).
	AllowedPaths   []string // Additional directories allowed outside WorkspaceRoot.
	BashTimeout   time.Duration // Default: 5 minutes.
	BraveAPIKey   string        // Brave Search API key (empty = web_search not registered).

	// BeforeWrite is called before modifying a file (write/edit tools).
	// If it returns an error, the write is aborted. Used by the checkpoint
	// system to capture pre-edit state. nil = no hook.
	BeforeWrite func(path string) error
}

// Defaults fills in zero-value fields with defaults.
func (c *ToolConfig) Defaults() {
	if c.BashTimeout == 0 {
		c.BashTimeout = 5 * time.Minute
	}
}

// Individual tool registration functions.
// Each calls cfg.Defaults() so they're safe to use standalone.

func RegisterBash(reg *core.Registry, cfg ToolConfig) error  { cfg.Defaults(); return reg.Register(NewBash(cfg)) }
func RegisterRead(reg *core.Registry, cfg ToolConfig) error  { cfg.Defaults(); return reg.Register(NewRead(cfg)) }
func RegisterWrite(reg *core.Registry, cfg ToolConfig) error { cfg.Defaults(); return reg.Register(NewWrite(cfg)) }
func RegisterEdit(reg *core.Registry, cfg ToolConfig) error  { cfg.Defaults(); return reg.Register(NewEdit(cfg)) }
func RegisterGrep(reg *core.Registry, cfg ToolConfig) error  { cfg.Defaults(); return reg.Register(NewGrep(cfg)) }
func RegisterFind(reg *core.Registry, cfg ToolConfig) error  { cfg.Defaults(); return reg.Register(NewFind(cfg)) }
func RegisterLs(reg *core.Registry, cfg ToolConfig) error    { cfg.Defaults(); return reg.Register(NewLs(cfg)) }
func RegisterFetch(reg *core.Registry, cfg ToolConfig) error { cfg.Defaults(); return reg.Register(NewFetch(cfg)) }

func RegisterWebSearch(reg *core.Registry, cfg ToolConfig) error {
	cfg.Defaults()
	if cfg.BraveAPIKey != "" {
		return reg.Register(NewWebSearch(cfg))
	}
	return nil
}

// RegisterBuiltins adds all built-in tools to the registry.
func RegisterBuiltins(reg *core.Registry, cfg ToolConfig) error {
	for _, fn := range []func(*core.Registry, ToolConfig) error{
		RegisterBash, RegisterRead, RegisterWrite, RegisterEdit,
		RegisterGrep, RegisterFind, RegisterLs, RegisterFetch,
		RegisterWebSearch,
	} {
		if err := fn(reg, cfg); err != nil {
			return fmt.Errorf("builtin: %w", err)
		}
	}
	return nil
}

// safePath resolves a path relative to WorkspaceRoot.
// If the path is absolute, it's used as-is but still checked for escapes.
// Returns error if the resolved path escapes the workspace via .. or symlinks.
// spillOutputDir is the directory under /tmp where tool output spill files
// are created. Exported via SpillOutputDir() for safePath whitelisting.
var spillOutputDir = filepath.Join(os.TempDir(), "moa-output")

// SpillOutputDir returns the directory where tool output spill files are stored.
func SpillOutputDir() string { return spillOutputDir }

func safePath(cfg ToolConfig, path string) (string, error) {
	root := cfg.WorkspaceRoot

	if root == "" || cfg.DisableSandbox {
		// No workspace restriction (YOLO mode)
		if filepath.IsAbs(path) {
			return filepath.Clean(path), nil
		}
		if root != "" {
			return filepath.Clean(filepath.Join(root, path)), nil
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
		// Check AllowedPaths before rejecting
		for _, ap := range cfg.AllowedPaths {
			realAP, err := filepath.EvalSymlinks(ap)
			if err != nil {
				realAP = filepath.Clean(ap)
			}
			if realResolved == realAP || strings.HasPrefix(realResolved, realAP+string(os.PathSeparator)) {
				return resolved, nil
			}
		}
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

// headTailBuffer keeps the first headMax bytes and the last tailMax bytes
// of streamed output. When total input exceeds headMax, excess is routed
// to a circular tail buffer that retains only the most recent tailMax bytes.
// This preserves the beginning (command echo, first results) and end (errors,
// summaries) of large outputs.
//
// When truncation occurs, the full output is also spilled to a temp file so
// the model can explore it with read (offset/limit), grep, etc. without
// re-executing the command. Set SpillDir before first Write to control where
// the file is created (defaults to os.TempDir).
type headTailBuffer struct {
	head       bytes.Buffer
	tail       []byte // circular buffer
	tailPos    int    // next write position in tail
	tailFull   bool   // tail has wrapped at least once
	headMax    int
	tailMax    int
	totalBytes int    // total bytes written (for truncation notice)
	truncated  bool   // head is full, overflow goes to tail
	spillFile  *os.File // temp file for full output (created lazily on truncation)
	SpillDir   string   // directory for spill files (empty = os.TempDir)
	SpillPath  string   // path to temp file (empty if no truncation)
}

// Write appends data to the buffer. Returns the number of bytes accepted into
// the head buffer (for streaming display) via the first return value. Once
// truncation kicks in, accepted is 0 — callers should stop sending live updates.
func (b *headTailBuffer) Write(p []byte) (accepted int, err error) {
	n := len(p)
	b.totalBytes += n

	if !b.truncated {
		remaining := b.headMax - b.head.Len()
		if remaining >= n {
			b.head.Write(p)
			return n, nil
		}
		// Partially fill head, overflow to tail
		accepted = remaining
		if remaining > 0 {
			b.head.Write(p[:remaining])
			p = p[remaining:]
		}
		b.truncated = true
		if b.tailMax > 0 {
			b.tail = make([]byte, b.tailMax)
		}
		// Spill full output to temp file: write head first, then overflow
		b.initSpillFile()
	}

	// Write overflow to spill file
	if b.spillFile != nil {
		_, _ = b.spillFile.Write(p) // best effort — truncation still works without complete spill
	}

	// Write overflow to circular tail buffer
	if b.tailMax <= 0 {
		return accepted, nil
	}
	for len(p) > 0 {
		space := b.tailMax - b.tailPos
		if space >= len(p) {
			copy(b.tail[b.tailPos:], p)
			b.tailPos += len(p)
			if b.tailPos == b.tailMax {
				b.tailPos = 0
				b.tailFull = true
			}
			break
		}
		copy(b.tail[b.tailPos:], p[:space])
		p = p[space:]
		b.tailPos = 0
		b.tailFull = true
	}
	return accepted, nil
}

// initSpillFile creates a temp file and writes the head content to it.
func (b *headTailBuffer) initSpillFile() {
	dir := b.SpillDir
	if dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return // best effort — truncation still works without spill
		}
	}
	f, err := os.CreateTemp(dir, "moa-output-*.txt")
	if err != nil {
		return // best effort — truncation still works without spill
	}
	b.spillFile = f
	b.SpillPath = f.Name()
	// Write the head bytes already captured
	_, _ = f.Write(b.head.Bytes()) // best effort — truncation still works without complete spill
}

// Close closes the spill file if open. Must be called when done.
func (b *headTailBuffer) Close() {
	if b.spillFile != nil {
		_ = b.spillFile.Close()
		b.spillFile = nil
	}
}

// String returns the buffered output. If truncated, includes a notice
// between head and tail with the spill file path.
func (b *headTailBuffer) String() string {
	if !b.truncated {
		return b.head.String()
	}
	var sb strings.Builder
	sb.Write(b.head.Bytes())

	tailData := b.tailString()
	omitted := b.totalBytes - b.head.Len() - len(tailData)
	if omitted < 0 {
		omitted = 0
	}
	if b.SpillPath != "" {
		fmt.Fprintf(&sb, "\n\n[... %d bytes truncated — full output at %s ...]\n\n", omitted, b.SpillPath)
	} else {
		fmt.Fprintf(&sb, "\n\n[... %d bytes truncated ...]\n\n", omitted)
	}
	sb.WriteString(tailData)
	return sb.String()
}

// tailString returns the tail buffer contents in order.
func (b *headTailBuffer) tailString() string {
	if !b.truncated || b.tailMax <= 0 {
		return ""
	}
	if !b.tailFull {
		return string(b.tail[:b.tailPos])
	}
	// Circular: data from tailPos..end + 0..tailPos
	var sb strings.Builder
	sb.Write(b.tail[b.tailPos:])
	sb.Write(b.tail[:b.tailPos])
	return sb.String()
}
