package serve

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// The creating device's zone is stored with the session, survives snapshots
// and a resume, and reaches the session's task scope. A bogus zone is refused.
func TestSessionCreatorTimezoneSurvivesResume(t *testing.T) {
	prov := newMockProvider(simpleResponseHandler("ok"))
	mgr := newTestManagerWithConfig(t, context.Background(), prov, t.TempDir(), noticeTestConfig)
	if _, err := mgr.CreateSession(CreateOpts{TZ: "Local"}); !errors.Is(err, ErrInvalidTimezone) {
		t.Fatalf("Local zone: %v", err)
	}
	if _, err := mgr.CreateSession(CreateOpts{TZ: "Mars/Olympus"}); !errors.Is(err, ErrInvalidTimezone) {
		t.Fatalf("bogus zone: %v", err)
	}
	sess, err := mgr.CreateSession(CreateOpts{TZ: "Europe/Madrid"})
	if err != nil {
		t.Fatal(err)
	}
	if got := sess.runtime.Context().TaskStore.Actor().TZ; got != "Europe/Madrid" {
		t.Fatalf("scope tz = %q", got)
	}
	if _, _, _, err := mgr.Send(sess.ID, "hello", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 5*time.Second, "idle", func() bool { return sessState(sess) == StateIdle })
	closeSession(t, mgr, sess.ID)
	saved, _, err := session.FindSessionReadOnly(mgr.sessionBaseDir, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Metadata[session.MetaCreatorTZ] != "Europe/Madrid" {
		t.Fatalf("saved metadata = %+v", saved.Metadata)
	}
	resumed, err := mgr.ResumeSession(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := resumed.runtime.Context().TaskStore.Actor().TZ; got != "Europe/Madrid" {
		t.Fatalf("resumed scope tz = %q", got)
	}
	plain, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if got := plain.runtime.Context().TaskStore.Actor().TZ; got != "" {
		t.Fatalf("unknown zone = %q, want empty", got)
	}
}

func agentSchedule(t *testing.T, sess *ManagedSession, title, when string) string {
	t.Helper()
	res, err := tasks.NewTool(sess.runtime.Context().TaskStore).Execute(context.Background(), map[string]any{"action": "create", "title": title, "when": when}, nil)
	if err != nil || res.IsError {
		t.Fatalf("schedule %q: %+v %v", when, res, err)
	}
	var sb strings.Builder
	for _, c := range res.Content {
		sb.WriteString(c.Text)
	}
	return sb.String()
}

// The agent schedules in the zone of the device that created its session,
// through a save and a resume; the owner's own schedules use the zone of the
// device they were made on, and neither changes the other.
func TestScheduleCreatorTimezoneSurvivesSnapshots(t *testing.T) {
	a := newSchedAPI(t, "2026-09-30T14:40:00Z")
	if code, _ := a.do("POST", "/api/sessions", map[string]any{"tz": "Mars/Olympus"}); code != 400 {
		t.Fatalf("bogus creator zone = %d, want 400", code)
	}
	info := expect[struct {
		ID string `json:"id"`
	}](a, 201, "POST", "/api/sessions", map[string]any{"tz": "Europe/Madrid"})
	sess, ok := a.h.mgr.Get(info.ID)
	if !ok {
		t.Fatal("created session is not loaded")
	}
	out := agentSchedule(t, sess, "before the save", "every monday at 09:00")
	if !strings.Contains(out, "Europe/Madrid") || strings.Contains(out, "unknown") {
		t.Fatalf("first schedule: %s", out)
	}

	if _, _, _, err := a.h.mgr.Send(sess.ID, "hello", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 5*time.Second, "idle", func() bool { return sessState(sess) == StateIdle })
	closeSession(t, a.h.mgr, sess.ID)
	saved, _, err := session.FindSessionReadOnly(a.h.mgr.sessionBaseDir, sess.ID)
	if err != nil || saved.Metadata[session.MetaCreatorTZ] != "Europe/Madrid" {
		t.Fatalf("saved metadata = %+v %v", saved.Metadata, err)
	}
	resumed, err := a.h.mgr.ResumeSession(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	out = agentSchedule(t, resumed, "after the resume", "tomorrow at 09:00")
	if !strings.Contains(out, "Thu 1 Oct 2026, 09:00 Europe/Madrid") {
		t.Fatalf("schedule after resume: %s", out)
	}

	// The owner schedules the same session from Tokyo.
	owned := expect[tasks.Record](a, 201, "POST", "/api/tasks", map[string]any{"title": "from tokyo", "tz": "Asia/Tokyo",
		"when": map[string]any{"kind": "once", "at": a.inMs(2 * time.Hour)}, "target": sessionTarget(sess.ID)})
	if owned.TZ != "Asia/Tokyo" || owned.CreatedBySessionID != "" {
		t.Fatalf("owner schedule = %+v", owned)
	}
	out = agentSchedule(t, resumed, "after tokyo", "in 20m")
	if !strings.Contains(out, "Europe/Madrid") {
		t.Fatalf("the agent's zone followed the owner's device: %s", out)
	}
	zones := map[string]string{}
	list := expect[struct {
		Tasks []tasks.Record `json:"tasks"`
	}](a, 200, "GET", "/api/tasks?include_agents=1", nil)
	for _, r := range list.Tasks {
		if r.When != nil {
			zones[r.Title] = r.TZ
		}
	}
	want := map[string]string{"before the save": "Europe/Madrid", "after the resume": "Europe/Madrid", "from tokyo": "Asia/Tokyo", "after tokyo": "Europe/Madrid"}
	for title, tz := range want {
		if zones[title] != tz {
			t.Errorf("%q zone = %q, want %q (all: %v)", title, zones[title], tz, zones)
		}
	}
}
