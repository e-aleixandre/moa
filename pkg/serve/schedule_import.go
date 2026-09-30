package serve

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/e-aleixandre/moa/pkg/schedule"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// importLegacySchedules moves the pending records of the retired /schedule
// store into the task database, once per installation, before the planner
// or the dispatcher can deliver anything.
//
// The SQLite flag set in the import's own transaction is the authority: a
// crash between the commit and retiring the file never imports twice, nor
// brings back a template the owner deleted since. An unreadable file or a
// failed import leaves the file untouched for the next start; nothing of it
// is delivered in the meantime (the old scheduler no longer runs).
func (m *Manager) importLegacySchedules(ctx context.Context) {
	path := m.schedulePath
	if path == "" {
		return
	}
	if _, err := os.Stat(path); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("legacy schedules: cannot read the file; not imported", "path", path, "error", err)
		}
		return
	}
	done, err := m.tasks.LegacySchedulesImported(ctx)
	if err != nil {
		slog.Warn("legacy schedules: reading the import flag failed; not imported", "error", err)
		return
	}
	if !done {
		store, err := schedule.Open(path)
		if err != nil {
			slog.Warn("legacy schedules: the file cannot be decoded; kept, not imported", "path", path, "error", err)
			return
		}
		var items []tasks.LegacySchedule
		transcripts := map[string]*session.Session{}
		for _, rec := range store.List() {
			if rec.Status != schedule.StatusPending {
				continue
			}
			items = append(items, tasks.LegacySchedule{ID: rec.ID, SessionID: rec.SessionID, Text: rec.Text,
				DueAt: rec.DueAt.UnixMilli(), TZ: rec.TimeZone, CreatedAt: rec.CreatedAt.UnixMilli(),
				Delivered: m.legacyPromptSaved(transcripts, rec.SessionID, rec.OccurrenceID)})
		}
		imported, err := m.tasks.ImportLegacySchedules(ctx, items)
		if err != nil {
			slog.Warn("legacy schedules: import failed; the file is kept for the next start", "path", path, "error", err)
			return
		}
		if imported {
			slog.Info("legacy schedules imported as scheduled tasks", "count", len(items))
		}
	}
	rename := os.Rename
	if m.planner != nil && m.planner.hooks.legacyRename != nil {
		rename = m.planner.hooks.legacyRename
	}
	if err := rename(path, path+".migrated"); err != nil {
		slog.Warn("legacy schedules: retiring the imported file failed; retried at the next start", "path", path, "error", err)
		return
	}
	if dir, err := os.Open(filepath.Dir(path)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
}

// legacyPromptSaved reports whether the old scheduler already put a record's
// prompt in its session: it marked each prompt with the record's occurrence
// ID and deduplicated by it after a crash. Transcripts are read once per
// session (cache). An unreadable one proves nothing: the record is imported
// as a run that waits for the owner's OK.
func (m *Manager) legacyPromptSaved(cache map[string]*session.Session, sessionID, occurrenceID string) bool {
	if occurrenceID == "" || sessionID == "" {
		return false
	}
	s, seen := cache[sessionID]
	if !seen {
		saved, _, err := session.FindSessionReadOnly(m.sessionBaseDir, sessionID)
		if err != nil && !errors.Is(err, session.ErrNotFound) {
			slog.Warn("legacy schedules: cannot read the session to check for a delivered prompt", "session", sessionID, "error", err)
		}
		s = saved
		cache[sessionID] = s
	}
	if s == nil {
		return false
	}
	marked := func(c map[string]any) bool {
		src, _ := c["source"].(string)
		occ, _ := c["occurrence_id"].(string)
		return src == "schedule" && occ == occurrenceID
	}
	for _, e := range s.Entries {
		if marked(e.Message.Custom) {
			return true
		}
	}
	for _, msg := range s.Messages {
		if marked(msg.Custom) {
			return true
		}
	}
	return false
}
