package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// PathPolicy controls runtime-mutable path access rules.
// Thread-safe. Shared (via pointer) across all tool closures so that
// /path add and /path rm take effect immediately.
type PathPolicy struct {
	mu            sync.RWMutex
	workspaceRoot string
	realRoot      string   // cached EvalSymlinks(workspaceRoot), resolved once at construction
	allowedPaths  []string
	unrestricted  bool // true = no path checks (was DisableSandbox)
}

// NewPathPolicy creates a PathPolicy. root is the workspace directory.
// allowed are additional directories permitted outside root.
// unrestricted disables all path containment checks.
func NewPathPolicy(root string, allowed []string, unrestricted bool) *PathPolicy {
	cp := make([]string, len(allowed))
	copy(cp, allowed)
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		realRoot = filepath.Clean(root)
	}
	return &PathPolicy{
		workspaceRoot: root,
		realRoot:      realRoot,
		allowedPaths:  cp,
		unrestricted:  unrestricted,
	}
}

// IsAllowed checks whether realPath (already symlink-resolved) is permitted.
// It checks workspace root containment, then allowed paths.
func (p *PathPolicy) IsAllowed(realPath string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.unrestricted || p.workspaceRoot == "" {
		return true
	}

	if realPath == p.realRoot || strings.HasPrefix(realPath, p.realRoot+string(os.PathSeparator)) {
		return true
	}

	for _, ap := range p.allowedPaths {
		realAP, err := filepath.EvalSymlinks(ap)
		if err != nil {
			realAP = filepath.Clean(ap)
		}
		if realPath == realAP || strings.HasPrefix(realPath, realAP+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// AddPath adds a directory to the allowed paths list.
// Returns an error if the path does not exist or is not a directory.
func (p *PathPolicy) AddPath(dir string) error {
	dir = filepath.Clean(dir)
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("path %q does not exist", dir)
	}
	if !info.IsDir() {
		return fmt.Errorf("path %q is not a directory", dir)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	// Avoid duplicates
	for _, ap := range p.allowedPaths {
		if ap == dir {
			return nil
		}
	}
	p.allowedPaths = append(p.allowedPaths, dir)
	return nil
}

// RemovePath removes a directory from the allowed paths list.
// Returns true if the path was found and removed.
func (p *PathPolicy) RemovePath(dir string) bool {
	dir = filepath.Clean(dir)
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, ap := range p.allowedPaths {
		if ap == dir {
			p.allowedPaths = append(p.allowedPaths[:i], p.allowedPaths[i+1:]...)
			return true
		}
	}
	return false
}

// SetUnrestricted toggles unrestricted (sandbox-disabled) mode.
func (p *PathPolicy) SetUnrestricted(v bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.unrestricted = v
}

// Unrestricted returns whether path checks are disabled.
func (p *PathPolicy) Unrestricted() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.unrestricted
}

// AllowedPaths returns a snapshot of the current allowed paths.
func (p *PathPolicy) AllowedPaths() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	cp := make([]string, len(p.allowedPaths))
	copy(cp, p.allowedPaths)
	return cp
}

// WorkspaceRoot returns the workspace root directory.
func (p *PathPolicy) WorkspaceRoot() string {
	return p.workspaceRoot // immutable, no lock needed
}

// Scope returns a human-readable scope description:
// "unrestricted", "workspace", or "ws+N" (N = number of extra allowed paths).
func (p *PathPolicy) Scope() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.unrestricted {
		return "unrestricted"
	}
	n := len(p.allowedPaths)
	if n == 0 {
		return "workspace"
	}
	return fmt.Sprintf("ws+%d", n)
}
