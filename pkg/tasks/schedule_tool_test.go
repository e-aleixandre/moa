package tasks

import (
	"strings"
	"testing"
	"time"
)

func toolRepo(t *testing.T) *Repo {
	t.Helper()
	r := newRepo(t)
	r.SetClock(func() time.Time { return whenNow })
	return r
}

func templates(t *testing.T, r *Repo) []Record {
	t.Helper()
	res, err := r.List(bg, Filter{IncludeAgents: true})
	if err != nil {
		t.Fatal(err)
	}
	var out []Record
	for _, rec := range res.Tasks {
		if rec.When != nil {
			out = append(out, rec)
		}
	}
	return out
}

func allTasks(t *testing.T, r *Repo) int {
	t.Helper()
	res, err := r.List(bg, Filter{IncludeAgents: true})
	if err != nil {
		t.Fatal(err)
	}
	return len(res.Tasks)
}

// A session schedules for itself only: the target, the creator and the zone
// come from the scope, an ambiguous or refused call writes nothing, and the
// explicit retry creates exactly one template.
func TestScheduleToolIdentityAndAmbiguity(t *testing.T) {
	r := toolRepo(t)
	sc := scopeFor(r, "A", "p").WithTZ("Europe/Madrid")
	other := scopeFor(r, "B", "p").WithTZ("Europe/Madrid")

	out, isErr := run(t, sc, map[string]any{"action": "create", "title": "check the build", "description": "look at CI", "when": "tomorrow at 09:00"})
	if isErr {
		t.Fatalf("schedule: %s", out)
	}
	tmpl := templates(t, r)
	if len(tmpl) != 1 {
		t.Fatalf("%d templates", len(tmpl))
	}
	got := tmpl[0]
	if got.Target == nil || got.Target.Kind != TargetSession || got.Target.ID != "A" || got.CreatedBySessionID != "A" || got.TZ != "Europe/Madrid" ||
		got.Place != PlaceYou || got.RequesterSessionID != "" || got.AssigneeSessionID != "" || got.When.At != ms("2026-10-01T07:00:00Z") ||
		*got.Delivery != (Delivery{Busy: BusySteer, Saved: DeliverWake, Late: LateAsk}) {
		t.Fatalf("template = %+v", got)
	}
	for _, want := range []string{"#1", "check the build", "Thu 1 Oct 2026, 09:00 Europe/Madrid", "Next run"} {
		if !strings.Contains(out, want) {
			t.Errorf("result lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "device timezone is unknown") {
		t.Errorf("a known zone is reported unknown:\n%s", out)
	}

	// It never enters the checklist or the requests; the session lists it
	// apart and reads it; another session cannot see it at all.
	view, err := r.AgentList(bg, sc.Actor())
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Checklist) != 0 || len(view.Requests) != 0 || len(view.Scheduled) != 1 {
		t.Fatalf("view = %+v", view)
	}
	if list, _ := run(t, sc, map[string]any{"action": "list"}); !strings.Contains(list, "Your scheduled tasks") || !strings.Contains(list, "#1: check the build") || !strings.Contains(list, "09:00") {
		t.Fatalf("list:\n%s", list)
	}
	if get, isErr := run(t, sc, map[string]any{"action": "get", "id": float64(1)}); isErr || !strings.Contains(get, "Schedule: once") {
		t.Fatalf("get: %q", get)
	}
	if _, isErr := run(t, other, map[string]any{"action": "get", "id": float64(1)}); !isErr {
		t.Fatal("another session read the template")
	}
	if list, _ := run(t, other, map[string]any{"action": "list"}); strings.Contains(list, "check the build") {
		t.Fatalf("another session lists it:\n%s", list)
	}
	if _, isErr := run(t, sc, map[string]any{"action": "update", "id": float64(1), "title": "hijack"}); !isErr {
		t.Fatal("the agent edited its template")
	}
	if _, isErr := run(t, sc, map[string]any{"action": "done", "id": float64(1)}); !isErr {
		t.Fatal("the agent completed its template")
	}

	// Injection: nothing the model says can name another target, actor, zone
	// or project; refused calls write nothing.
	before := allTasks(t, r)
	for name, params := range map[string]map[string]any{
		"other target":     {"action": "create", "title": "x", "when": "in 20m", "target": "B"},
		"owner target":     {"action": "create", "title": "x", "when": "in 20m", "target": "owner"},
		"object target":    {"action": "create", "title": "x", "when": "in 20m", "target": map[string]any{"kind": "session", "id": "B"}},
		"ask with when":    {"action": "ask", "title": "x", "when": "in 20m"},
		"update with when": {"action": "update", "id": float64(1), "when": "in 20m"},
		"done with when":   {"action": "done", "id": float64(1), "when": "in 20m"},
		"list with when":   {"action": "list", "when": "in 20m"},
		"get with when":    {"action": "get", "id": float64(1), "when": "in 20m"},
		"claim with when":  {"action": "claim", "id": float64(1), "when": "in 20m"},
		"when not string":  {"action": "create", "title": "x", "when": float64(5)},
		"blank when":       {"action": "create", "title": "x", "when": "  "},
		"unreadable":       {"action": "create", "title": "x", "when": "whenever you like"},
		"past":             {"action": "create", "title": "x", "when": "yesterday at 9:00"},
		"malformed":        {"action": "create", "title": "x", "when": "at 25:99"},
		"no title":         {"action": "create", "when": "in 20m"},
	} {
		if out, isErr := run(t, sc, params); !isErr {
			t.Errorf("%s was accepted: %s", name, out)
		}
	}
	if after := allTasks(t, r); after != before {
		t.Fatalf("refused calls wrote %d tasks", after-before)
	}
	// A target that only repeats the default is fine, and the extra
	// parameters a model might invent are not identity.
	if out, isErr := run(t, sc, map[string]any{"action": "create", "title": "explicit", "when": "in 20m", "target": "this session",
		"tz": "Asia/Tokyo", "session_id": "B", "project": "elsewhere", "actor": "B", "created_by_session_id": "B"}); isErr {
		t.Fatalf("explicit this-session target: %s", out)
	}
	last := templates(t, r)[1]
	if last.TZ != "Europe/Madrid" || last.Target.ID != "A" || last.CreatedBySessionID != "A" || last.ProjectKey != "p" {
		t.Fatalf("injected parameters changed the identity: %+v", last)
	}

	// An unqualified hour is ambiguous: both readings come back, nothing is
	// written, and the explicit retry creates once.
	before = allTasks(t, r)
	out, isErr = run(t, sc, map[string]any{"action": "create", "title": "standup", "when": "tomorrow at 9"})
	if !isErr {
		t.Fatalf("an ambiguous hour was scheduled: %s", out)
	}
	for _, want := range []string{"09:00", "21:00", "Europe/Madrid", "nothing was scheduled"} {
		if !strings.Contains(strings.ToLower(out), strings.ToLower(want)) {
			t.Errorf("ambiguity answer lacks %q:\n%s", want, out)
		}
	}
	if allTasks(t, r) != before {
		t.Fatal("the ambiguous call wrote a task")
	}
	if out, isErr = run(t, sc, map[string]any{"action": "create", "title": "standup", "when": "tomorrow at 09:00"}); isErr {
		t.Fatalf("retry: %s", out)
	}
	if allTasks(t, r) != before+1 {
		t.Fatalf("the retry wrote %d tasks", allTasks(t, r)-before)
	}

	// A repeat is one template with a rule.
	out, isErr = run(t, sc, map[string]any{"action": "create", "title": "weekly review", "when": "every monday at 9:00"})
	if isErr || !strings.Contains(out, "every Monday at 09:00") || !strings.Contains(out, "Mon 5 Oct 2026, 09:00 Europe/Madrid") {
		t.Fatalf("repeat: %q err=%v", out, isErr)
	}
	// A DST gap is said out loud.
	r.SetClock(func() time.Time { return mustTime("2026-03-28T12:00:00Z") })
	out, isErr = run(t, sc, map[string]any{"action": "create", "title": "gap", "when": "2026-03-29 02:30"})
	if isErr || !strings.Contains(out, "03:30") || !strings.Contains(strings.ToLower(out), "clocks") {
		t.Fatalf("gap: %q err=%v", out, isErr)
	}
	// Ordinary creates are unchanged.
	if out, isErr = run(t, sc, map[string]any{"action": "create", "title": "plain"}); isErr || !strings.HasPrefix(out, "Created task #") {
		t.Fatalf("plain create: %q", out)
	}
}

// Without a creator zone the agent schedules in UTC and says so; nothing is
// guessed from the server.
func TestScheduleAgentUnknownTimezone(t *testing.T) {
	t.Setenv("TZ", "Asia/Tokyo")
	r := toolRepo(t)
	sc := scopeFor(r, "A", "p") // no WithTZ: an old or headless session
	const note = "(UTC — this session's device timezone is unknown)"

	out, isErr := run(t, sc, map[string]any{"action": "create", "title": "soon", "when": "in 20m"})
	if isErr || !strings.Contains(out, note) || !strings.Contains(out, "15:00 UTC") {
		t.Fatalf("relative: %q err=%v", out, isErr)
	}
	out, isErr = run(t, sc, map[string]any{"action": "create", "title": "daily", "when": "every day at 08:30"})
	if isErr || !strings.Contains(out, note) || !strings.Contains(out, "every day at 08:30") {
		t.Fatalf("repeat: %q err=%v", out, isErr)
	}
	out, isErr = run(t, sc, map[string]any{"action": "create", "title": "tomorrow", "when": "tomorrow at 09:00"})
	if isErr || !strings.Contains(out, note) || !strings.Contains(out, "Thu 1 Oct 2026, 09:00 UTC") {
		t.Fatalf("tomorrow: %q err=%v", out, isErr)
	}
	tmpl := templates(t, r)
	if len(tmpl) != 3 {
		t.Fatalf("%d templates", len(tmpl))
	}
	for _, rec := range tmpl {
		if rec.TZ != "UTC" {
			t.Errorf("template #%d stored zone %q, want UTC", rec.ID, rec.TZ)
		}
	}
	if tmpl[0].When.At != ms("2026-09-30T15:00:00Z") || tmpl[2].When.At != ms("2026-10-01T09:00:00Z") || tmpl[1].Next != ms("2026-10-01T08:30:00Z") {
		t.Fatalf("times = %+v", tmpl)
	}
	// A session with a known zone does not carry the note.
	known := scopeFor(r, "K", "p").WithTZ("America/New_York")
	if out, _ = run(t, known, map[string]any{"action": "create", "title": "k", "when": "in 20m"}); strings.Contains(out, "unknown") || !strings.Contains(out, "America/New_York") {
		t.Fatalf("known zone: %q", out)
	}
}
