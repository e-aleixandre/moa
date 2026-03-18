// Package memory provides cross-session project memory persistence.
//
// Memory is stored per-project in ~/.config/moa/projects/<hash>/MEMORY.md,
// where <hash> is SHA256(CanonicalizePath(workspaceRoot))[:16].
package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ealeixandre/moa/pkg/core"
)

// MaxSize is the hard limit for memory content (50KB).
const MaxSize = 50 * 1024

// Store manages per-project memory files.
type Store struct {
	baseDir string // e.g. ~/.config/moa/projects/
}

// New creates a Store rooted at baseDir (typically ~/.config/moa/projects/).
func New(baseDir string) *Store {
	return &Store{baseDir: baseDir}
}

// ProjectDir returns the directory for a project given its workspace root.
func (s *Store) ProjectDir(workspaceRoot string) string {
	return filepath.Join(s.baseDir, projectHash(workspaceRoot))
}

// FilePath returns the full path to MEMORY.md for a project.
func (s *Store) FilePath(workspaceRoot string) string {
	return filepath.Join(s.ProjectDir(workspaceRoot), "MEMORY.md")
}

// Load reads MEMORY.md for the given project. Returns "" if not found.
func (s *Store) Load(workspaceRoot string) (string, error) {
	data, err := os.ReadFile(s.FilePath(workspaceRoot))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

// Save writes MEMORY.md atomically (temp file → rename).
// If content is empty, the file is deleted.
// Directories are created with 0700, files with 0600.
func (s *Store) Save(workspaceRoot string, content string) error {
	path := s.FilePath(workspaceRoot)

	if content == "" {
		err := os.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	if len(content) > MaxSize {
		return fmt.Errorf("memory content exceeds %dKB limit (%d bytes)", MaxSize/1024, len(content))
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating memory dir: %w", err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		return fmt.Errorf("writing memory: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("saving memory: %w", err)
	}
	return nil
}

// Truncate returns the first maxLines lines of content.
// If content has fewer lines, it's returned unchanged.
func Truncate(content string, maxLines int) string {
	lines := strings.SplitN(content, "\n", maxLines+1)
	if len(lines) <= maxLines {
		return content
	}
	return strings.Join(lines[:maxLines], "\n")
}

// projectHash returns a 16-char hex hash of the canonical workspace path.
func projectHash(workspaceRoot string) string {
	canonical, err := core.CanonicalizePath(workspaceRoot)
	if err != nil {
		canonical = filepath.Clean(workspaceRoot)
	}
	h := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(h[:8])
}
