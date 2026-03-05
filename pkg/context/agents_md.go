package agentcontext

import (
	"os"
	"path/filepath"
	"strings"
)

const agentsMDFile = "AGENTS.md"

// LoadAgentsMD discovers and concatenates AGENTS.md files in priority order.
//
// Priority (lowest first, content concatenated):
//  1. <agentHome>/AGENTS.md       (global)
//  2. Walk: root → ... → cwd/..   (ancestor directories, root first, deepest last)
//  3. cwd/AGENTS.md               (project-local, highest priority)
//
// agentHome defaults to $AGENT_HOME, then ~/.config/agent.
// Duplicate paths are deduplicated.
func LoadAgentsMD(cwd, agentHome string) (string, error) {
	if agentHome == "" {
		agentHome = os.Getenv("AGENT_HOME")
	}
	if agentHome == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			agentHome = filepath.Join(home, ".config", "agent")
		}
	}

	seen := make(map[string]bool)
	var sections []string

	// 1. Global
	if agentHome != "" {
		globalPath := filepath.Join(agentHome, agentsMDFile)
		if content := readIfExists(globalPath); content != "" {
			absPath, _ := filepath.Abs(globalPath)
			if !seen[absPath] {
				seen[absPath] = true
				sections = append(sections, content)
			}
		}
	}

	// 2. Walk from filesystem root to cwd (ancestors)
	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		absCwd = cwd
	}

	ancestors := collectAncestors(absCwd)
	for _, dir := range ancestors {
		p := filepath.Join(dir, agentsMDFile)
		absP, _ := filepath.Abs(p)
		if seen[absP] {
			continue
		}
		if content := readIfExists(p); content != "" {
			seen[absP] = true
			sections = append(sections, content)
		}
	}

	// 3. CWD itself (highest priority — last)
	cwdPath := filepath.Join(absCwd, agentsMDFile)
	if !seen[cwdPath] {
		if content := readIfExists(cwdPath); content != "" {
			sections = append(sections, content)
		}
	}

	return strings.Join(sections, "\n\n---\n\n"), nil
}

// collectAncestors returns directories from the filesystem root down to (but not including) dir.
// Example: /a/b/c → ["/", "/a", "/a/b"]
func collectAncestors(dir string) []string {
	var ancestors []string
	current := filepath.Dir(dir) // parent of dir
	var stack []string

	for current != dir {
		stack = append(stack, current)
		dir = current
		current = filepath.Dir(current)
	}

	// Reverse so root comes first
	for i := len(stack) - 1; i >= 0; i-- {
		ancestors = append(ancestors, stack[i])
	}
	return ancestors
}

func readIfExists(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
