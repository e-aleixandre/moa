package serve

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/tasks"
)

type parseBody struct {
	Text string `json:"text"`
	TZ   string `json:"tz"`
}

type parseErrBody struct {
	Code  string `json:"code"`
	Error string `json:"error"`
	At    int64  `json:"at"`
}

func taskCount(t *testing.T, a *schedAPI) int {
	// The database is created by the first write; make sure it exists.
	if _, err := a.h.mgr.tasks.Revision(bgc); err != nil {
		t.Fatal(err)
	}
	return sqlCount(t, a.h.dbPath(), "SELECT COUNT(*) FROM tasks")
}

// The preview asks the server, with the server's clock: a bare hour offers
// its other half, a bare past time rolls to tomorrow, and a preview never
// authorizes an instant that has passed by the time it is created.
func TestWhenParseRolloverAndAlternatives(t *testing.T) {
	a := newSchedAPI(t, "2026-09-30T14:40:00Z")
	sid := a.h.session().ID
	expect[tasks.Record](a, 201, "POST", "/api/tasks", map[string]any{"title": "seed", "place": "you"})
	parse := func(text string) tasks.Parsed {
		return expect[tasks.Parsed](a, 200, "POST", "/api/tasks/when/parse", parseBody{text, "Europe/Madrid"})
	}
	p := parse("at 5")
	if p.When.Kind != "once" || p.When.At != ms("2026-10-01T03:00:00Z") || p.Next != p.When.At || p.Alt != ms("2026-09-30T15:00:00Z") || p.TZ != "Europe/Madrid" {
		t.Fatalf("at 5 = %+v", p)
	}
	if p = parse("at 17:00"); p.Alt != 0 || p.When.At != ms("2026-09-30T15:00:00Z") {
		t.Fatalf("17:00 = %+v", p)
	}
	raw := expect[map[string]json.RawMessage](a, 200, "POST", "/api/tasks/when/parse", parseBody{"at 17:00", "Europe/Madrid"})
	if _, has := raw["alt"]; has {
		t.Fatalf("a qualified time carries alt: %s", raw["alt"])
	}
	if _, has := raw["adjusted"]; has {
		t.Fatalf("plain time is adjusted: %s", raw["adjusted"])
	}
	e := expect[parseErrBody](a, 400, "POST", "/api/tasks/when/parse", parseBody{"today at 3", "Europe/Madrid"})
	if e.Code != "past" || e.Error == "" {
		t.Fatalf("today at 3 = %+v", e)
	}
	// Stale preview: "in 20m" is valid now and past 21 minutes later.
	soon := parse("in 20m")
	a.h.clock.Advance(21 * time.Minute)
	before := taskCount(t, a)
	body := a.onceBody("stale", soon.When.At, sessionTarget(sid))
	if code, raw := a.do("POST", "/api/tasks", body); code != 400 {
		t.Fatalf("creating the stale preview = %d %s, want 400", code, raw)
	}
	if taskCount(t, a) != before {
		t.Fatal("the stale creation stored a task")
	}
	// The same parse against the new clock is a fresh instant.
	if again := parse("in 20m"); again.When.At <= soon.When.At {
		t.Fatalf("the clock did not move the preview: %+v", again)
	}
}

// Anything the grammar does not understand is an error with a code, never a
// defaulted schedule; the endpoint is read-only and takes no clock of its own.
func TestWhenParseInvalidInputs(t *testing.T) {
	a := newSchedAPI(t, "2026-09-30T14:40:00Z")
	expect[tasks.Record](a, 201, "POST", "/api/tasks", map[string]any{"title": "seed", "place": "you"})
	before := taskCount(t, a)
	revBefore, _ := a.h.mgr.tasks.Revision(bgc)
	for code, texts := range map[string][]string{
		"invalid": {"2026-02-31 at 9:00", "every month on the 0", "every month on the 32", "tomorrow at 99:90", "in -5 minutes", "in 0 minutes", "in 99999999999999999999 days"},
		"unknown": {"banana", "tomorrow at 9 and then feed the cat"},
		"repeat":  {"every fortnight"},
		"past":    {"yesterday at 9:00", "2026-01-01 10:00"},
	} {
		for _, text := range texts {
			e := expect[parseErrBody](a, 400, "POST", "/api/tasks/when/parse", parseBody{text, "Europe/Madrid"})
			if e.Code != code || e.Error == "" {
				t.Errorf("%q = %+v, want code %s", text, e, code)
			}
		}
	}
	for name, body := range map[string]string{
		"bogus tz":     `{"text":"tomorrow at 9:00","tz":"Mars/Olympus"}`,
		"local tz":     `{"text":"tomorrow at 9:00","tz":"Local"}`,
		"missing tz":   `{"text":"tomorrow at 9:00"}`,
		"now override": `{"text":"tomorrow at 9:00","tz":"UTC","now":"2020-01-01T00:00:00Z"}`,
		"not json":     `tomorrow`,
	} {
		code, raw := a.do("POST", "/api/tasks/when/parse", body)
		if code != 400 {
			t.Errorf("%s = %d %s, want 400", name, code, raw)
		}
	}
	// Empty text is no selection: OK, with nothing chosen.
	got := expect[map[string]json.RawMessage](a, 200, "POST", "/api/tasks/when/parse", parseBody{"   ", "Europe/Madrid"})
	if _, has := got["when"]; has || string(got["tz"]) != `"Europe/Madrid"` {
		t.Fatalf("empty text = %v", got)
	}
	if taskCount(t, a) != before {
		t.Fatal("parsing stored a task")
	}
	if revAfter, _ := a.h.mgr.tasks.Revision(bgc); revAfter != revBefore {
		t.Fatalf("parsing wrote to the database: revision %d -> %d", revBefore, revAfter)
	}
}

// What the preview shows is what the planner fires: the four DESIGN §4 wall
// times go through parse, create and the real planner and land on the same
// UTC instant, once, in both directions of the clock change.
func TestWhenParseAndPlannerShareDST(t *testing.T) {
	type c struct {
		name, now, tz, text, wall, at string
		adjusted                      bool
		following                     string // the next daily slot
	}
	for _, tc := range []c{
		{"madrid gap", "2026-03-28T12:00:00Z", "Europe/Madrid", "2026-03-29 02:30", "02:30", "2026-03-29T01:30:00Z", true, "2026-03-30T00:30:00Z"},
		{"madrid overlap", "2026-10-24T12:00:00Z", "Europe/Madrid", "2026-10-25 02:30", "02:30", "2026-10-25T00:30:00Z", false, "2026-10-26T01:30:00Z"},
		{"new york gap", "2026-03-07T12:00:00Z", "America/New_York", "2026-03-08 02:30", "02:30", "2026-03-08T07:30:00Z", true, "2026-03-09T06:30:00Z"},
		{"new york overlap", "2026-10-31T12:00:00Z", "America/New_York", "2026-11-01 01:30", "01:30", "2026-11-01T05:30:00Z", false, "2026-11-02T06:30:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newSchedAPI(t, tc.now)
			saved := a.h.savedSession()
			hold := map[string]any{"saved": "hold"}
			parse := func(text string) tasks.Parsed {
				return expect[tasks.Parsed](a, 200, "POST", "/api/tasks/when/parse", parseBody{text, tc.tz})
			}
			create := func(p tasks.Parsed) tasks.Record {
				return expect[tasks.Record](a, 201, "POST", "/api/tasks", map[string]any{"title": "dst", "when": p.When, "tz": p.TZ, "target": sessionTarget(saved), "delivery": hold})
			}

			once := parse(tc.text)
			if once.When.At != ms(tc.at) || once.Adjusted != tc.adjusted || once.Next != once.When.At {
				t.Fatalf("parse = %+v, want %s adjusted=%v", once, tc.at, tc.adjusted)
			}
			rec := create(once)
			if rec.Next != ms(tc.at) {
				t.Fatalf("stored next = %d", rec.Next)
			}
			a.h.clock.Set(time.UnixMilli(rec.Next))
			a.h.pass()
			if runs := a.runs(rec.ID); len(runs) != 1 || runs[0].DueAt != ms(tc.at) {
				t.Fatalf("planner runs = %+v, want one at %s", runs, tc.at)
			}
			// An hour later nothing fires a second time (the repeated hour).
			a.h.clock.Advance(time.Hour)
			a.h.pass()
			if runs := a.runs(rec.ID); len(runs) != 1 {
				t.Fatalf("%d runs after the overlap hour", len(runs))
			}

			// The same wall time as a daily rule, parsed from a day before.
			a.h.clock.Set(mustUTC(tc.now))
			rule := parse("every day at " + tc.wall)
			if rule.Next != ms(tc.at) {
				t.Fatalf("daily next = %s, want %s", time.UnixMilli(rule.Next).UTC().Format(time.RFC3339), tc.at)
			}
			daily := create(rule)
			a.h.clock.Set(time.UnixMilli(daily.Next))
			a.h.pass()
			runs := a.runs(daily.ID)
			if len(runs) != 1 || runs[0].DueAt != ms(tc.at) {
				t.Fatalf("daily runs = %+v", runs)
			}
			if got := a.rec(daily.ID).Next; got != ms(tc.following) {
				t.Fatalf("following slot = %s, want %s", time.UnixMilli(got).UTC().Format(time.RFC3339), tc.following)
			}
			a.h.clock.Advance(time.Hour)
			a.h.pass()
			if n := len(a.runs(daily.ID)); n != 1 {
				t.Fatalf("daily fired %d times around the change", n)
			}
		})
	}
}
