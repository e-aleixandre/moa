package tool

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/memory"
)

// ToolConfig provides shared configuration for built-in tools.
type ToolConfig struct {
	WorkspaceRoot  string        // Required. All path operations resolve relative to this.
	DisableSandbox bool          // When true, safePath allows any absolute path (YOLO mode). Deprecated: use PathPolicy.
	AllowedPaths   []string      // Additional directories allowed outside WorkspaceRoot. Deprecated: use PathPolicy.
	BashTimeout    time.Duration // Default: 5 minutes.
	BraveAPIKey    string        // Brave Search API key (empty = web_search not registered).
	MemoryStore    *memory.Store // Per-project memory store (nil = memory tool not registered).

	// PathPolicy is the runtime-mutable path access policy. When non-nil,
	// safePath delegates containment checks to this policy instead of using
	// DisableSandbox/AllowedPaths directly. All tool closures share the same
	// pointer, so runtime changes (/path add, /path rm) take effect immediately.
	PathPolicy *PathPolicy

	// BeforeWrite is called before modifying a file (write/edit tools).
	// If it returns an error, the write is aborted. Used by the checkpoint
	// system to capture pre-edit state. nil = no hook.
	BeforeWrite func(path string) error

	// FileTracker records file reads for stale-edit protection. When set,
	// the read tool marks files as read and the edit tool warns when
	// editing files that haven't been read. nil = no tracking.
	FileTracker *FileTracker

	// BashState, when non-nil, makes the bash tool persist cwd and exported
	// env across calls (captured via an EXIT trap, re-applied via cmd.Dir/Env).
	// nil = stateless behavior (previous default).
	BashState *BashState

	// BashJobs owns session-scoped background bash commands. nil leaves the
	// async parameter unavailable (useful for standalone tool registrations).
	BashJobs *BashJobs
}

// Defaults fills in zero-value fields with defaults.
func (c *ToolConfig) Defaults() {
	if c.BashTimeout == 0 {
		c.BashTimeout = 5 * time.Minute
	}
}

// Individual tool registration functions.
// Each calls cfg.Defaults() so they're safe to use standalone.

func RegisterBash(reg *core.Registry, cfg ToolConfig) error {
	cfg.Defaults()
	return reg.Register(NewBash(cfg))
}
func RegisterRead(reg *core.Registry, cfg ToolConfig) error {
	cfg.Defaults()
	return reg.Register(NewRead(cfg))
}
func RegisterWrite(reg *core.Registry, cfg ToolConfig) error {
	cfg.Defaults()
	return reg.Register(NewWrite(cfg))
}
func RegisterEdit(reg *core.Registry, cfg ToolConfig) error {
	cfg.Defaults()
	return reg.Register(NewEdit(cfg))
}
func RegisterMultiEdit(reg *core.Registry, cfg ToolConfig) error {
	cfg.Defaults()
	return reg.Register(NewMultiEdit(cfg))
}
func RegisterApplyPatch(reg *core.Registry, cfg ToolConfig) error {
	cfg.Defaults()
	return reg.Register(NewApplyPatch(cfg))
}
func RegisterGrep(reg *core.Registry, cfg ToolConfig) error {
	cfg.Defaults()
	return reg.Register(NewGrep(cfg))
}
func RegisterFind(reg *core.Registry, cfg ToolConfig) error {
	cfg.Defaults()
	return reg.Register(NewFind(cfg))
}
func RegisterLs(reg *core.Registry, cfg ToolConfig) error {
	cfg.Defaults()
	return reg.Register(NewLs(cfg))
}
func RegisterFetch(reg *core.Registry, cfg ToolConfig) error {
	cfg.Defaults()
	return reg.Register(NewFetch(cfg))
}

func RegisterWebSearch(reg *core.Registry, cfg ToolConfig) error {
	cfg.Defaults()
	if cfg.BraveAPIKey != "" {
		return reg.Register(NewWebSearch(cfg))
	}
	return nil
}

func RegisterMemory(reg *core.Registry, cfg ToolConfig) error {
	cfg.Defaults()
	if cfg.MemoryStore == nil {
		return nil // memory disabled, skip silently
	}
	return reg.Register(NewMemory(cfg))
}

// RegisterBuiltins adds all built-in tools to the registry.
func RegisterBuiltins(reg *core.Registry, cfg ToolConfig) error {
	for _, fn := range []func(*core.Registry, ToolConfig) error{
		RegisterBash, RegisterRead, RegisterWrite, RegisterEdit,
		RegisterMultiEdit, RegisterApplyPatch, RegisterGrep,
		RegisterFind, RegisterLs, RegisterFetch, RegisterWebSearch,
		RegisterMemory,
	} {
		if err := fn(reg, cfg); err != nil {
			return fmt.Errorf("builtin: %w", err)
		}
	}
	if cfg.BashJobs != nil {
		if err := reg.Register(NewBashStatus(cfg)); err != nil {
			return fmt.Errorf("builtin: %w", err)
		}
		if err := reg.Register(NewBashWait(cfg)); err != nil {
			return fmt.Errorf("builtin: %w", err)
		}
		if err := reg.Register(NewBashCancel(cfg)); err != nil {
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

const (
	maxSpillBytes      int64 = 10 << 20  // 10 MiB per command stream
	maxTotalSpillBytes int64 = 100 << 20 // 100 MiB across retained spills
	spillTTL                 = 24 * time.Hour
	spillFilePrefix          = "moa-output-"
	spillFileSuffix          = ".txt"
)

// spillBudget accounts for bytes retained on disk, rather than bytes merely
// observed. Reservations are deliberately retained until TTL cleanup, because
// a spill remains readable after its producing command has completed.
type spillBudget struct {
	mu    sync.Mutex
	limit int64
	used  int64
}

func newSpillBudget(limit int64) *spillBudget { return &spillBudget{limit: limit} }

func (b *spillBudget) reserve(want int64) int64 {
	if want <= 0 {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	available := b.limit - b.used
	if available <= 0 {
		return 0
	}
	if want > available {
		want = available
	}
	b.used += want
	return want
}

func (b *spillBudget) release(n int64) {
	if n <= 0 {
		return
	}
	b.mu.Lock()
	b.used -= n
	if b.used < 0 {
		b.used = 0
	}
	b.mu.Unlock()
}

// account records an existing retained file. Unlike reserve, it may put used
// above limit: inherited spills already consume disk and must block new spills
// until TTL cleanup brings their real total back under the cap.
func (b *spillBudget) account(n int64) {
	if n <= 0 {
		return
	}
	b.mu.Lock()
	b.used += n
	b.mu.Unlock()
}

var (
	defaultSpillBudget = newSpillBudget(maxTotalSpillBytes)
	spillInitOnce      sync.Once
)

// CleanupSpillFiles removes expired regular spill files. It never follows a
// symlink, either for the spill directory or for an entry within it. It is safe
// to call at process/session startup and is also invoked before the first spill
// is created in a process.
func CleanupSpillFiles() error {
	return cleanupSpillFiles(spillOutputDir, time.Now(), spillTTL, defaultSpillBudget)
}

func initializeSpillFiles() {
	spillInitOnce.Do(func() {
		_ = CleanupSpillFiles()
		// Account for surviving files from a previous process so the global cap
		// also applies immediately after a restart.
		_ = accountSpillFiles(spillOutputDir, time.Now(), spillTTL, defaultSpillBudget)
	})
}

func isSpillFile(name string) bool {
	return strings.HasPrefix(name, spillFilePrefix) && strings.HasSuffix(name, spillFileSuffix)
}

// cleanupSpillFiles deliberately uses Lstat/DirEntry.Type rather than Stat so
// cleanup cannot be redirected through an attacker-controlled symlink in /tmp.
func cleanupSpillFiles(dir string, now time.Time, ttl time.Duration, budget *spillBudget) error {
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("spill directory is not a directory")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	cutoff := now.Add(-ttl)
	for _, entry := range entries {
		if !isSpillFile(entry.Name()) || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		fileInfo, err := os.Lstat(path)
		if err != nil || !fileInfo.Mode().IsRegular() || !fileInfo.ModTime().Before(cutoff) {
			continue
		}
		// Removing a pathname does not follow a replacement symlink. We only
		// release the bytes after the unlink succeeds.
		if err := os.Remove(path); err == nil && budget != nil {
			budget.release(fileInfo.Size())
		}
	}
	return nil
}

func accountSpillFiles(dir string, now time.Time, ttl time.Duration, budget *spillBudget) error {
	if budget == nil {
		return nil
	}
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("spill directory is not a directory")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	cutoff := now.Add(-ttl)
	for _, entry := range entries {
		if !isSpillFile(entry.Name()) || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		fileInfo, err := os.Lstat(filepath.Join(dir, entry.Name()))
		if err == nil && fileInfo.Mode().IsRegular() && !fileInfo.ModTime().Before(cutoff) {
			budget.account(fileInfo.Size())
		}
	}
	return nil
}

// suggestDir returns a parent directory suitable for /path add suggestions.
// For a file path like /foo/bar/baz.txt it returns /foo/bar.
// For a directory path it returns the path itself.
func suggestDir(absPath string) string {
	dir := filepath.Dir(absPath)
	if dir == absPath {
		return absPath // already a root
	}
	return dir
}

// SafePath resolves path against cfg's workspace/PathPolicy, exactly as the
// built-in file tools do. Exposed so out-of-package tools (e.g. serve's
// send_file) enforce the same path boundary as read.
func SafePath(cfg ToolConfig, path string) (string, error) { return safePath(cfg, path) }

func safePath(cfg ToolConfig, path string) (string, error) {
	root := cfg.WorkspaceRoot

	// Determine unrestricted mode: PathPolicy takes precedence, then legacy field.
	unrestricted := cfg.DisableSandbox
	if cfg.PathPolicy != nil {
		unrestricted = cfg.PathPolicy.Unrestricted()
	}

	if root == "" || unrestricted {
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
		// Check allowed paths: PathPolicy takes precedence, then legacy field.
		if cfg.PathPolicy != nil {
			if cfg.PathPolicy.IsAllowed(realResolved) {
				return resolved, nil
			}
		} else {
			for _, ap := range cfg.AllowedPaths {
				realAP, err := filepath.EvalSymlinks(ap)
				if err != nil {
					realAP = filepath.Clean(ap)
				}
				if realResolved == realAP || strings.HasPrefix(realResolved, realAP+string(os.PathSeparator)) {
					return resolved, nil
				}
			}
		}
		return "", fmt.Errorf("path %q is outside workspace %q.\nHint: use /path add %s or --allow-path %s to allow access",
			path, root, suggestDir(realResolved), suggestDir(realResolved))
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
	// Trim back to a rune boundary so the cut doesn't emit a broken rune.
	return safeUTF8Truncate(s[:maxBytes]) + "\n\n[output truncated]"
}

// truncateLines truncates text to maxLines lines, appending a notice if truncated.
func truncateLines(s string, maxLines int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= maxLines {
		return s
	}
	return strings.Join(lines[:maxLines], "\n") + "\n\n[output truncated]"
}

// truncateLinesHeadTail truncates text to maxLines lines keeping the first and
// last maxLines/2, with a notice in between. spillIncomplete, when supplied,
// prevents a partial spill from being represented as complete output.
func truncateLinesHeadTail(s string, maxLines int, spillPath string, spillIncomplete ...string) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= maxLines {
		return s
	}
	half := maxLines / 2
	omitted := len(lines) - 2*half
	head := strings.Join(lines[:half], "\n")
	tail := strings.Join(lines[len(lines)-half:], "\n")
	var notice string
	incomplete := ""
	if len(spillIncomplete) > 0 {
		incomplete = spillIncomplete[0]
	}
	if incomplete != "" && spillPath != "" {
		notice = fmt.Sprintf("\n\n[... %d lines truncated — spill is incomplete (%s); partial output at %s ...]\n\n", omitted, incomplete, spillPath)
	} else if incomplete != "" {
		notice = fmt.Sprintf("\n\n[... %d lines truncated — full output was not saved (%s) ...]\n\n", omitted, incomplete)
	} else if spillPath != "" {
		notice = fmt.Sprintf("\n\n[... %d lines truncated — full output at %s ...]\n\n", omitted, spillPath)
	} else {
		notice = fmt.Sprintf("\n\n[... %d lines truncated ...]\n\n", omitted)
	}
	return head + notice + tail
}

const (
	maxOutputBytes = 50 * 1024 // 50KB
	maxOutputLines = 2000
)

// headTailBuffer keeps the first headMax bytes and the last tailMax bytes
// of streamed output. When total input exceeds headMax, excess is routed
// to a circular tail buffer that retains only the most recent tailMax bytes.
// This preserves the beginning (command echo, first results) and end (errors,
// summaries) of large outputs.
//
// When truncation occurs, output is spilled to a temp file when the per-spill
// and process-global storage caps permit it. A notice only calls a spill "full"
// after every byte was written successfully. Set SpillDir before first Write
// to control where the file is created (defaults to SpillOutputDir()).
type headTailBuffer struct {
	head            bytes.Buffer
	tail            []byte // circular buffer
	tailPos         int    // next write position in tail
	tailFull        bool   // tail has wrapped at least once
	headMax         int
	tailMax         int
	totalBytes      int  // total bytes written (for truncation notice)
	truncated       bool // head is full, overflow goes to tail
	spillFile       *os.File
	spillBytes      int64
	spillBudget     *spillBudget
	spillIncomplete string
	SpillDir        string // directory for spill files (empty = SpillOutputDir())
	SpillPath       string // path to spill output (empty if unavailable)
	SpillMaxBytes   int64  // 0 = maxSpillBytes; primarily useful for tests
}

// Append ingests p and returns how many bytes were accepted into the head
// (visible) buffer — used for live streaming. Once truncation kicks in the
// accepted count drops to 0 and overflow is routed to the circular tail buffer
// and the spill file. Never fails.
func (b *headTailBuffer) Append(p []byte) (accepted int) {
	n := len(p)
	b.totalBytes += n

	if !b.truncated {
		remaining := b.headMax - b.head.Len()
		if remaining >= n {
			b.head.Write(p)
			return n
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
		// Spill the head first, then overflow. A capped or failed spill is
		// retained as an explicitly partial diagnostic, never advertised as
		// complete output.
		b.initSpillFile()
	}

	b.writeSpill(p)

	// Write overflow to circular tail buffer
	if b.tailMax <= 0 {
		return accepted
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
	return accepted
}

// Write implements io.Writer. It always consumes all of p — overflow beyond the
// head is routed to the tail buffer and spill file — and reports len(p) so that
// io.Copy-based callers (os/exec's stdout/stderr copy for find/grep) never see a
// short write and abort mid-stream, losing the tail. Use Append when you need
// the count of bytes accepted into the head for live streaming.
func (b *headTailBuffer) Write(p []byte) (int, error) {
	b.Append(p)
	return len(p), nil
}

// initSpillFile creates a temp file and writes the head content to it.
func (b *headTailBuffer) initSpillFile() {
	dir := b.SpillDir
	if dir == "" {
		dir = spillOutputDir
	}
	if dir == spillOutputDir {
		initializeSpillFiles()
	}
	if err := ensureSpillDir(dir); err != nil {
		b.spillIncomplete = "spill could not be created"
		return
	}
	f, err := os.CreateTemp(dir, spillFilePrefix+"*"+spillFileSuffix)
	if err != nil {
		b.spillIncomplete = "spill could not be created"
		return
	}
	b.spillFile = f
	b.SpillPath = f.Name()
	b.writeSpill(b.head.Bytes())
}

func ensureSpillDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("spill directory is not a directory")
	}
	return nil
}

func (b *headTailBuffer) writeSpill(p []byte) {
	if len(p) == 0 || b.spillFile == nil || b.spillIncomplete != "" {
		return
	}
	limit := b.SpillMaxBytes
	if limit == 0 {
		limit = maxSpillBytes
	}
	remaining := limit - b.spillBytes
	if remaining <= 0 {
		b.spillIncomplete = "per-spill size cap reached"
		return
	}
	want := int64(len(p))
	if want > remaining {
		want = remaining
	}
	budget := b.spillBudget
	if budget == nil {
		budget = defaultSpillBudget
		b.spillBudget = budget
	}
	reserved := budget.reserve(want)
	if reserved == 0 {
		b.spillIncomplete = "global spill size cap reached"
		return
	}
	written, err := b.spillFile.Write(p[:reserved])
	b.spillBytes += int64(written)
	if unused := reserved - int64(written); unused > 0 {
		budget.release(unused)
	}
	if err != nil || written != int(reserved) {
		b.spillIncomplete = "spill write failed"
		return
	}
	if reserved < int64(len(p)) {
		if reserved == remaining {
			b.spillIncomplete = "per-spill size cap reached"
		} else {
			b.spillIncomplete = "global spill size cap reached"
		}
	}
}

// Close closes the spill file if open. Must be called when done.
func (b *headTailBuffer) Close() {
	if b.spillFile != nil {
		if err := b.spillFile.Close(); err != nil && b.spillIncomplete == "" {
			b.spillIncomplete = "spill close failed"
		}
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
	// The head may end mid-rune (filled up to headMax bytes) — trim the partial
	// trailing rune so we don't emit a broken rune at the head/notice boundary.
	sb.WriteString(safeUTF8Truncate(b.head.String()))

	tailData := b.tailString()
	// The tail is a circular byte buffer starting at an arbitrary offset, so it
	// may begin mid-rune — drop leading continuation bytes.
	for len(tailData) > 0 && !utf8.RuneStart(tailData[0]) {
		tailData = tailData[1:]
	}
	omitted := b.totalBytes - b.head.Len() - len(tailData)
	if omitted < 0 {
		omitted = 0
	}
	if b.SpillPath != "" && b.spillIncomplete == "" {
		fmt.Fprintf(&sb, "\n\n[... %d bytes truncated — full output at %s ...]\n\n", omitted, b.SpillPath)
	} else if b.SpillPath != "" {
		fmt.Fprintf(&sb, "\n\n[... %d bytes truncated — spill is incomplete (%s); partial output at %s ...]\n\n", omitted, b.spillIncomplete, b.SpillPath)
	} else if b.spillIncomplete != "" {
		fmt.Fprintf(&sb, "\n\n[... %d bytes truncated — full output was not saved (%s) ...]\n\n", omitted, b.spillIncomplete)
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
