package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ealeixandre/moa/pkg/core"
)

// NewApplyPatch creates the apply_patch tool for multi-file patches.
func NewApplyPatch(cfg ToolConfig) core.Tool {
	return core.Tool{
		Name:  "apply_patch",
		Label: "Apply Patch",
		Description: `Apply changes using the *** Begin Patch format. Efficient for large or multi-file changes.

Format:
  *** Begin Patch
  *** Add File: path      (then +lines for content)
  *** Delete File: path   (no body needed)
  *** Update File: path   (then @@ context, +/- /space lines)
  *** End Patch`,
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"patch": {
					"type": "string",
					"description": "The full patch text using the *** Begin Patch format"
				}
			},
			"required": ["patch"]
		}`),
		Effect: core.EffectShell, // multi-file → barrier in scheduler
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			patchText := getString(params, "patch", "")
			if patchText == "" {
				return core.ErrorResult("patch is required"), nil
			}

			hunks, err := ParsePatch(patchText)
			if err != nil {
				return core.ErrorResult(err.Error()), nil
			}

			// Phase A: Validate & compute all changes in memory
			staged, err := validateAndCompute(cfg, hunks)
			if err != nil {
				return core.ErrorResult(err.Error()), nil
			}

			// Phase B: Apply all staged changes
			var summary []string
			for _, s := range staged {
				if cfg.BeforeWrite != nil && s.action != actionDelete {
					if err := cfg.BeforeWrite(s.path); err != nil {
						return core.ErrorResult(fmt.Sprintf("checkpoint %s: %v", s.path, err)), nil
					}
				}

				switch s.action {
				case actionAdd, actionUpdate:
					// Ensure parent directory exists
					dir := filepath.Dir(s.path)
					if err := os.MkdirAll(dir, 0o755); err != nil {
						return core.ErrorResult(fmt.Sprintf("mkdir %s: %v", dir, err)), nil
					}
					// Write to temp file + rename for atomicity
					tmpFile := s.path + ".moa-patch-tmp"
					if err := os.WriteFile(tmpFile, []byte(s.content), 0o644); err != nil {
						return core.ErrorResult(fmt.Sprintf("write %s: %v", s.path, err)), nil
					}
					if err := os.Rename(tmpFile, s.path); err != nil {
						_ = os.Remove(tmpFile) // cleanup
						return core.ErrorResult(fmt.Sprintf("rename %s: %v", s.path, err)), nil
					}

				case actionDelete:
					if cfg.BeforeWrite != nil {
						if err := cfg.BeforeWrite(s.path); err != nil {
							return core.ErrorResult(fmt.Sprintf("checkpoint %s: %v", s.path, err)), nil
						}
					}
					if err := os.Remove(s.path); err != nil {
						return core.ErrorResult(fmt.Sprintf("delete %s: %v", s.path, err)), nil
					}

				case actionMove:
					// Write new file
					dir := filepath.Dir(s.moveTo)
					if err := os.MkdirAll(dir, 0o755); err != nil {
						return core.ErrorResult(fmt.Sprintf("mkdir %s: %v", dir, err)), nil
					}
					tmpFile := s.moveTo + ".moa-patch-tmp"
					if err := os.WriteFile(tmpFile, []byte(s.content), 0o644); err != nil {
						return core.ErrorResult(fmt.Sprintf("write %s: %v", s.moveTo, err)), nil
					}
					if err := os.Rename(tmpFile, s.moveTo); err != nil {
						_ = os.Remove(tmpFile)
						return core.ErrorResult(fmt.Sprintf("rename %s: %v", s.moveTo, err)), nil
					}
					// Remove old file
					if cfg.BeforeWrite != nil {
						if err := cfg.BeforeWrite(s.path); err != nil {
							return core.ErrorResult(fmt.Sprintf("checkpoint %s: %v", s.path, err)), nil
						}
					}
					if err := os.Remove(s.path); err != nil {
						return core.ErrorResult(fmt.Sprintf("delete old %s: %v", s.path, err)), nil
					}
				}

				summary = append(summary, s.label)
			}

			result := strings.Join(summary, "\n")
			if onUpdate != nil {
				onUpdate(core.TextResult(result))
			}
			return core.TextResult(result), nil
		},
	}
}

// stagedAction classifies a staged file change.
type stagedAction int

const (
	actionAdd    stagedAction = iota
	actionDelete
	actionUpdate
	actionMove
)

// stagedChange holds a computed change ready to apply.
type stagedChange struct {
	action  stagedAction
	path    string // resolved absolute path
	moveTo  string // only for actionMove
	content string // new file content (empty for delete)
	label   string // summary line (e.g. "A new.txt")
}

// validateAndCompute validates all hunks and computes results in memory.
// Returns error if any hunk fails — no side effects on disk.
// Maintains an in-memory virtual FS so multiple hunks on the same file
// see each other's changes (sequential semantics).
func validateAndCompute(cfg ToolConfig, hunks []PatchHunk) ([]stagedChange, error) {
	var staged []stagedChange

	// In-memory file state: path → content. Tracks updates from prior hunks
	// so later hunks on the same file see the accumulated changes.
	memFS := make(map[string]string)
	// Track deleted paths so we don't read from disk after a delete.
	deleted := make(map[string]bool)

	for i, h := range hunks {
		resolved, err := safePath(cfg, h.Path)
		if err != nil {
			return nil, fmt.Errorf("hunk %d (%s): %w", i+1, h.Path, err)
		}

		switch h.Type {
		case HunkAdd:
			// Reject if file already exists on disk or was staged
			if _, inMem := memFS[resolved]; inMem {
				return nil, fmt.Errorf("hunk %d: cannot add %s — path already staged", i+1, h.Path)
			}
			if _, err := os.Stat(resolved); err == nil {
				return nil, fmt.Errorf("hunk %d: cannot add %s — file already exists", i+1, h.Path)
			} else if !os.IsNotExist(err) {
				return nil, fmt.Errorf("hunk %d: stat %s: %w", i+1, h.Path, err)
			}

			memFS[resolved] = h.Content
			staged = append(staged, stagedChange{
				action:  actionAdd,
				path:    resolved,
				content: h.Content,
				label:   fmt.Sprintf("A %s", h.Path),
			})

		case HunkDelete:
			if cfg.FileTracker != nil && !cfg.FileTracker.WasRead(resolved) {
				return nil, fmt.Errorf("hunk %d: cannot delete %s — file hasn't been read", i+1, h.Path)
			}
			if deleted[resolved] {
				return nil, fmt.Errorf("hunk %d: cannot delete %s — already deleted by prior hunk", i+1, h.Path)
			}
			if _, err := os.Stat(resolved); os.IsNotExist(err) {
				return nil, fmt.Errorf("hunk %d: cannot delete %s — file does not exist", i+1, h.Path)
			}

			deleted[resolved] = true
			delete(memFS, resolved)
			staged = append(staged, stagedChange{
				action: actionDelete,
				path:   resolved,
				label:  fmt.Sprintf("D %s", h.Path),
			})

		case HunkUpdate:
			if cfg.FileTracker != nil && !cfg.FileTracker.WasRead(resolved) {
				return nil, fmt.Errorf("hunk %d: cannot update %s — file hasn't been read", i+1, h.Path)
			}
			if deleted[resolved] {
				return nil, fmt.Errorf("hunk %d: cannot update %s — deleted by prior hunk", i+1, h.Path)
			}

			// Read from in-memory state (prior hunks) or from disk
			content, inMem := memFS[resolved]
			if !inMem {
				data, err := os.ReadFile(resolved)
				if err != nil {
					return nil, fmt.Errorf("hunk %d: read %s: %w", i+1, h.Path, err)
				}
				content = string(data)
			}

			newContent, err := applyPatchChunks(content, h.Chunks)
			if err != nil {
				return nil, fmt.Errorf("hunk %d (%s): %w", i+1, h.Path, err)
			}

			// Update in-memory state for subsequent hunks
			memFS[resolved] = newContent

			if h.MovePath != "" {
				moveTo, err := safePath(cfg, h.MovePath)
				if err != nil {
					return nil, fmt.Errorf("hunk %d move to %s: %w", i+1, h.MovePath, err)
				}
				staged = append(staged, stagedChange{
					action:  actionMove,
					path:    resolved,
					moveTo:  moveTo,
					content: newContent,
					label:   fmt.Sprintf("R %s → %s", h.Path, h.MovePath),
				})
			} else {
				staged = append(staged, stagedChange{
					action:  actionUpdate,
					path:    resolved,
					content: newContent,
					label:   fmt.Sprintf("M %s", h.Path),
				})
			}
		}
	}

	return staged, nil
}
