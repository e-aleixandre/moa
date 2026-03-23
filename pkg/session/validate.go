package session

import "fmt"

// ValidateEntries checks the integrity of a session's entry tree.
// Returns an error if:
//   - duplicate entry IDs
//   - missing parent references
//   - leaf ID not found
//   - cycle detected (from leaf to root)
func ValidateEntries(entries []Entry, leafID string) error {
	if len(entries) == 0 && leafID == "" {
		return nil
	}

	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		if seen[e.ID] {
			return fmt.Errorf("validate: duplicate entry ID: %s", e.ID)
		}
		seen[e.ID] = true
	}

	for _, e := range entries {
		if e.ParentID != "" && !seen[e.ParentID] {
			return fmt.Errorf("validate: entry %s references missing parent %s", e.ID, e.ParentID)
		}
	}

	if leafID != "" && !seen[leafID] {
		return fmt.Errorf("validate: leaf %s not found in entries", leafID)
	}

	// Cycle detection: walk from leaf to root
	if leafID != "" {
		index := make(map[string]int, len(entries))
		for i, e := range entries {
			index[e.ID] = i
		}
		visited := make(map[string]bool)
		id := leafID
		for id != "" {
			if visited[id] {
				return fmt.Errorf("validate: cycle detected at entry %s", id)
			}
			visited[id] = true
			idx, ok := index[id]
			if !ok {
				return fmt.Errorf("validate: broken chain at %s", id)
			}
			id = entries[idx].ParentID
		}
	}

	return nil
}
