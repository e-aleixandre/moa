package serve

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

func manualNoticeManager(t *testing.T) *Manager {
	t.Helper()
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	m := newNoticeManagerAt(t, t.TempDir(), newMockProvider())
	m.tasksCancel()
	<-m.notices.done
	t.Cleanup(m.Shutdown)
	return m
}

func createManualNotice(t *testing.T, m *Manager, s *ManagedSession) tasks.Notice {
	t.Helper()
	rec, err := m.tasks.Create(context.Background(), tasks.CreateInput{Title: "review", Place: tasks.PlaceAgent, AssigneeSessionID: s.ID})
	if err != nil {
		t.Fatal(err)
	}
	ns, err := m.tasks.TaskNotices(context.Background(), rec.ID, 1)
	if err != nil || len(ns) != 1 {
		t.Fatalf("notices = %v, %v", ns, err)
	}
	return ns[0]
}

func TestNoticeAdmissionRequiresDurableOutboxState(t *testing.T) {
	m := manualNoticeManager(t)
	s, err := m.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	n := createManualNotice(t, m, s)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m.notices.mu.Lock()
	m.notices.attempt(ctx, n, false)
	m.notices.mu.Unlock()
	pollUntil(t, time.Second, "idle", func() bool { return sessState(s) == StateIdle })
	if err := s.runtime.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := len(noticeMessages(s.History(), n.ID)); got != 0 {
		t.Fatalf("notice admitted %d times although its outbox transition failed", got)
	}
	m.notices.pass(context.Background())
	if err := s.runtime.Flush(); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, time.Second, "one notice", func() bool { return len(noticeMessages(s.History(), n.ID)) > 0 })
	if got := len(noticeMessages(s.History(), n.ID)); got != 1 {
		t.Fatalf("retry duplicated notice: %d copies", got)
	}
}

func TestNoticePendingAlreadyOnDiskIsNotInjectedAgain(t *testing.T) {
	m := manualNoticeManager(t)
	s, err := m.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	n := createManualNotice(t, m, s)
	m.notices.pass(context.Background())
	pollUntil(t, time.Second, "idle after notice", func() bool { return sessState(s) == StateIdle && len(noticeMessages(s.History(), n.ID)) == 1 })
	if err := s.runtime.Flush(); err != nil {
		t.Fatal(err)
	}
	setNoticeRow(t, m.tasks.Path(), n.ID, tasks.NoticePending, tasks.DeliverHold)
	m.notices.pass(context.Background())
	pollUntil(t, time.Second, "idle after retry", func() bool { return sessState(s) == StateIdle })
	if err := s.runtime.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := savedNoticeCount(savedTranscript(t, m, s.ID), n.ID); got != 1 {
		t.Fatalf("already persisted pending notice duplicated: %d copies", got)
	}
	got, err := m.tasks.Notice(context.Background(), n.ID)
	if err != nil || got.State != tasks.NoticeDelivered {
		t.Fatalf("reconciled notice = %+v, %v", got, err)
	}
}

func TestPersisterHasOnlySuccessfullySavedNotices(t *testing.T) {
	for _, tree := range []bool{false, true} {
		name := "flat"
		if tree {
			name = "tree"
		}
		t.Run(name, func(t *testing.T) {
			store, err := session.NewFileStore(filepath.Join(t.TempDir(), "sessions"), "")
			if err != nil {
				t.Fatal(err)
			}
			saved := store.Create()
			if err := store.Save(saved); err != nil {
				t.Fatal(err)
			}
			sp := newServePersister(saved, store, func() (string, string, bool) { return "", "", false })
			msg := core.WrapMessage(core.NewUserMessage("notice"))
			msg.Custom = noticeCustom(tasks.Notice{ID: "tn_test"}, false, false)
			backup := store.Dir() + ".saved"
			if err := os.Rename(store.Dir(), backup); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(store.Dir(), []byte("not a directory"), 0600); err != nil {
				t.Fatal(err)
			}
			if tree {
				tr := session.NewTree()
				tr.Append(session.Entry{Type: session.EntryMessage, Message: msg})
				err = sp.SnapshotTree(tr.Entries(), tr.LeafID(), nil)
			} else {
				err = sp.Snapshot([]core.AgentMessage{msg}, 0, nil)
			}
			if err == nil {
				t.Fatal("filesystem obstruction did not fail save")
			}
			if sp.has(func(s *session.Session) bool { return transcriptHasNotice(s, "tn_test") }) {
				t.Fatal("failed save presented as durable notice")
			}
			if err := os.Remove(store.Dir()); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(backup, store.Dir()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNoticeMissingForTwoMinutesIsRetriedOnce(t *testing.T) {
	for _, age := range []time.Duration{119 * time.Second, 121 * time.Second} {
		t.Run(age.String(), func(t *testing.T) {
			m := manualNoticeManager(t)
			s, err := m.CreateSession(CreateOpts{})
			if err != nil {
				t.Fatal(err)
			}
			n := createManualNotice(t, m, s)
			m.tasks.SetClock(func() time.Time { return time.Now().Add(-age) })
			ok, err := m.tasks.SetNoticeState(context.Background(), n.ID, tasks.NoticeChange{From: []string{tasks.NoticePending}, State: tasks.NoticeSent})
			m.tasks.SetClock(time.Now)
			if err != nil || !ok {
				t.Fatalf("sent = %v, %v", ok, err)
			}
			m.notices.trigger(func() { m.notices.reconcileAll = true })
			m.notices.pass(context.Background())
			got, err := m.tasks.Notice(context.Background(), n.ID)
			if err != nil {
				t.Fatal(err)
			}
			if age < 2*time.Minute {
				if got.State != tasks.NoticeSent || len(noticeMessages(s.History(), n.ID)) != 0 {
					t.Fatalf("early retry: %+v", got)
				}
				return
			}
			if got.State != tasks.NoticePending {
				t.Fatalf("missing notice not made retryable: %+v", got)
			}
			m.notices.pass(context.Background())
			pollUntil(t, time.Second, "one notice and idle", func() bool { return sessState(s) == StateIdle && len(noticeMessages(s.History(), n.ID)) == 1 })
			if err := s.runtime.Flush(); err != nil {
				t.Fatal(err)
			}
			m.notices.trigger(func() { m.notices.reconcileAll = true })
			m.notices.pass(context.Background())
			m.notices.pass(context.Background())
			if got := savedNoticeCount(savedTranscript(t, m, s.ID), n.ID); got != 1 {
				t.Fatalf("retry persisted %d copies", got)
			}
			got, _ = m.tasks.Notice(context.Background(), n.ID)
			if got.State != tasks.NoticeDelivered {
				t.Fatalf("retry did not settle: %+v", got)
			}
		})
	}
}
