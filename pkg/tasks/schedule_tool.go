package tasks

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// unknownTZNote is said whenever a schedule was read in UTC because the
// session does not know where its owner is.
const unknownTZNote = "(UTC — this session's device timezone is unknown)"

// thisSession is the only target an agent can schedule for.
const thisSession = "this session"

// checkScheduleParams keeps scheduling to create: when and target are refused
// on every other action, and a target other than this session is refused
// everywhere. The agent cannot name a session, an owner or a project.
//
// Models with a strict schema send every parameter with its zero value, so an
// empty when, and outside create an empty or own-session target, mean absent.
func checkScheduleParams(action string, params map[string]any) error {
	if w, ok := params["when"].(string); ok && w == "" {
		delete(params, "when")
	}
	if t, ok := params["target"].(string); ok && action != "create" {
		if t = strings.TrimSpace(t); t == "" || strings.EqualFold(t, thisSession) {
			delete(params, "target")
		}
	}
	if _, has := params["when"]; has && action != "create" {
		return invalid("when is only for create: a scheduled task is created, not asked or edited")
	}
	if w, has := params["when"]; has {
		if _, ok := w.(string); !ok {
			return invalid("when must be text such as \"tomorrow at 09:00\"")
		}
	}
	target, has := params["target"]
	if !has {
		return nil
	}
	if action != "create" {
		return invalid("target is only for create with when")
	}
	if s, ok := target.(string); !ok || !strings.EqualFold(strings.TrimSpace(s), thisSession) {
		return invalid("a scheduled task always runs in %s; omit target", thisSession)
	}
	return nil
}

const whenHint = `Try "in 20m", "tomorrow at 09:00", "monday at 09:00", "2026-10-05 15:30" or "every weekday at 08:30".`

// toolSchedule is create with when: read the text in the session's zone
// (UTC when unknown), refuse it if the hour is ambiguous, then write one
// template that runs in this session.
func toolSchedule(ctx context.Context, repo *Repo, actor Actor, in AgentInput, text string) (core.Result, error) {
	if strings.TrimSpace(text) == "" {
		return core.Result{}, invalid("when is empty; omit it for an ordinary task")
	}
	tz, unknown := actor.TZ, false
	if _, err := LoadZone(tz); err != nil {
		tz, unknown = "UTC", true
	}
	p, err := ParseWhen(text, repo.now(), tz)
	if err != nil {
		if pe, ok := err.(*ParseError); ok && (pe.Code == "unknown" || pe.Code == "repeat") {
			return core.Result{}, &ParseError{Code: pe.Code, Msg: pe.Msg + ". " + whenHint}
		}
		return core.Result{}, err
	}
	if p.Alt != 0 {
		a, b := p.When.At, p.Alt
		if b < a {
			a, b = b, a
		}
		return core.Result{}, &ParseError{Code: "ambiguous", Msg: fmt.Sprintf(
			"Nothing was scheduled: %q could be %s or %s. Retry with an explicit time such as 09:00 or 21:00.",
			text, formatInstant(a, tz), formatInstant(b, tz))}
	}
	rec, err := repo.AgentSchedule(ctx, actor, in, p.When, tz)
	if err != nil {
		return core.Result{}, err
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Scheduled task #%d: %s\nSchedule: %s.\nNext run: %s", rec.ID, rec.Title, describeWhen(p.When, tz), formatInstant(rec.Next, tz))
	if unknown {
		sb.WriteString(" " + unknownTZNote)
	}
	sb.WriteString(".")
	if p.Adjusted {
		sb.WriteString("\nNote: that wall time does not exist on that day (clocks go forward), so it runs at the next valid time shown.")
	}
	sb.WriteString("\nEach run arrives in this session as a new task on your checklist. The owner can see, pause and edit the schedule.")
	return core.TextResult(sb.String()), nil
}

// formatInstant is an instant as wall time in its zone: "Thu 1 Oct 2026, 09:00 Europe/Madrid".
func formatInstant(ms int64, tz string) string {
	loc, err := LoadZone(tz)
	if err != nil {
		loc, tz = time.UTC, "UTC"
	}
	return time.UnixMilli(ms).In(loc).Format("Mon 2 Jan 2006, 15:04") + " " + tz
}

var weekdayLabels = [...]string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

// describeWhen says a definition in words.
func describeWhen(w When, tz string) string {
	if w.Kind == WhenOnce {
		return "once, " + formatInstant(w.At, tz)
	}
	r := w.Rule
	at := fmt.Sprintf("%02d:%02d", r.H, r.Mi)
	switch r.Freq {
	case FreqDaily:
		return "every day at " + at + " (" + tz + ")"
	case FreqWeekdays:
		return "every weekday at " + at + " (" + tz + ")"
	case FreqWeekly:
		return "every " + weekdayLabels[*r.DOW] + " at " + at + " (" + tz + ")"
	case FreqMonthly:
		return fmt.Sprintf("every month on day %d at %s (%s)", *r.DOM, at, tz)
	}
	return "repeats"
}

// describeSchedule is a listed template: its rule and when it runs next.
func describeSchedule(t AgentTask) string {
	s := describeWhen(*t.When, t.TZ)
	switch {
	case t.ScheduleState == "paused":
		s += "; paused"
	case t.Next != 0 && t.When.Kind == WhenRepeat:
		s += "; next " + formatInstant(t.Next, t.TZ)
	case t.Status == StatusDone:
		s += "; done"
	}
	return s
}
