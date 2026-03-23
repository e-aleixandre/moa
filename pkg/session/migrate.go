package session

import (
	"fmt"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

// MigrateV1ToV2 converts a v1 session (flat Messages) to v2 (entry-based tree).
// Idempotent: returns nil immediately if already v2+.
// Each message becomes an Entry with a sequential parent chain.
// If the first message is a compaction_summary, it becomes a CompactionEntry
// followed by the remaining messages (recording that compaction already happened).
func MigrateV1ToV2(sess *Session) error {
	if sess.Version >= SessionVersion {
		return nil
	}
	if len(sess.Messages) == 0 {
		sess.Version = SessionVersion
		return nil
	}

	entries := make([]Entry, 0, len(sess.Messages))
	var lastID string

	for i := range sess.Messages {
		msg := DeepCopyMessage(sess.Messages[i])
		id := fmt.Sprintf("migrated_%d", i)
		ts := time.Unix(msg.Timestamp, 0)
		if msg.Timestamp == 0 {
			ts = sess.Created.Add(time.Duration(i) * time.Second)
		}

		if i == 0 && msg.Role == "compaction_summary" {
			// First message is a compaction summary from a previous compaction.
			// Convert to a CompactionEntry. The "kept" messages start at i+1.
			keptID := ""
			if len(sess.Messages) > 1 {
				keptID = fmt.Sprintf("migrated_%d", 1)
			}
			entries = append(entries, Entry{
				ID:        id,
				ParentID:  lastID,
				Timestamp: ts,
				Type:      EntryCompaction,
				Compaction: CompactionData{
					Summary:          textFromContent(msg.Content),
					FirstKeptEntryID: keptID,
					TokensBefore:     0, // unknown for legacy sessions
				},
			})
		} else {
			entries = append(entries, Entry{
				ID:        id,
				ParentID:  lastID,
				Timestamp: ts,
				Type:      EntryMessage,
				Message:   msg,
			})
		}

		lastID = id
	}

	sess.Entries = entries
	sess.LeafID = lastID
	sess.Version = SessionVersion
	sess.Messages = nil
	sess.CompactionEpoch = 0
	return nil
}

// textFromContent extracts the concatenated text from content blocks.
func textFromContent(cc []core.Content) string {
	var s string
	for _, c := range cc {
		if c.Type == "text" {
			s += c.Text
		}
	}
	return s
}
