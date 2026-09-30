package tasks

import (
	"fmt"
	"time"
)

// Recurrence frequencies.
const (
	FreqDaily    = "daily"
	FreqWeekdays = "weekdays"
	FreqWeekly   = "weekly"
	FreqMonthly  = "monthly"
)

// Rule is a recurring wall-clock rule, interpreted in its schedule's IANA zone.
// DOW (0 = Sunday) is only for weekly, DOM (1–31) only for monthly.
type Rule struct {
	Freq string `json:"freq"`
	DOW  *int   `json:"dow,omitempty"`
	DOM  *int   `json:"dom,omitempty"`
	H    int    `json:"h"`
	Mi   int    `json:"mi"`
}

// ResolveWall returns the instant of the civil date and wall time y-m-d h:mi
// in loc. The civil date must exist (31 February is an error, not 3 March).
// When the wall time happens twice (clocks going back) the earlier instant
// wins; when it does not happen at all (clocks going forward) it is moved
// forward by the length of the gap, keeping its minutes, and adjusted is true.
// time.Date is not used to pick: its choice in both cases is unspecified.
func ResolveWall(y int, m time.Month, d, h, mi int, loc *time.Location) (time.Time, bool, error) {
	if h < 0 || h > 23 || mi < 0 || mi > 59 {
		return time.Time{}, false, invalid("time %02d:%02d does not exist", h, mi)
	}
	naive := time.Date(y, m, d, h, mi, 0, 0, time.UTC)
	if naive.Year() != y || naive.Month() != m || naive.Day() != d {
		return time.Time{}, false, invalid("date %04d-%02d-%02d does not exist", y, int(m), d)
	}
	periods := zonePeriods(naive, loc)
	if t, ok := matchWall(naive, periods, loc); ok {
		return t, false, nil
	}
	// A gap: the wall time falls between the end of one offset and the start
	// of a larger one. Shift by the difference and resolve again.
	for i := 0; i+1 < len(periods); i++ {
		a, b := periods[i], periods[i+1]
		if b.offset <= a.offset {
			continue
		}
		from := b.start.Add(time.Duration(a.offset) * time.Second)
		to := b.start.Add(time.Duration(b.offset) * time.Second)
		if naive.Before(from) || !naive.Before(to) {
			continue
		}
		shifted := naive.Add(time.Duration(b.offset-a.offset) * time.Second)
		if t, ok := matchWall(shifted, periods, loc); ok {
			return t, true, nil
		}
	}
	return time.Time{}, false, fmt.Errorf("cannot resolve %s in %s", naive.Format("2006-01-02 15:04"), loc)
}

type zonePeriod struct {
	start  time.Time // zero for "since forever"
	offset int
}

// zonePeriods lists the offsets in force from two days before to two days
// after the naive instant, in order: every transition that could touch it.
func zonePeriods(naive time.Time, loc *time.Location) []zonePeriod {
	end := naive.Add(48 * time.Hour)
	t := naive.Add(-48 * time.Hour).In(loc)
	var out []zonePeriod
	for i := 0; i < 16; i++ {
		_, off := t.Zone()
		start, next := t.ZoneBounds()
		out = append(out, zonePeriod{start: start, offset: off})
		if next.IsZero() || next.After(end) {
			break
		}
		t = next.In(loc)
	}
	return out
}

// matchWall returns the earliest instant whose wall clock in loc is naive.
func matchWall(naive time.Time, periods []zonePeriod, loc *time.Location) (time.Time, bool) {
	var best time.Time
	found := false
	seen := map[int]bool{}
	for _, p := range periods {
		if seen[p.offset] {
			continue
		}
		seen[p.offset] = true
		c := naive.Add(-time.Duration(p.offset) * time.Second).In(loc)
		w := c
		if w.Year() == naive.Year() && w.Month() == naive.Month() && w.Day() == naive.Day() &&
			w.Hour() == naive.Hour() && w.Minute() == naive.Minute() && w.Second() == 0 {
			if !found || c.Before(best) {
				best, found = c, true
			}
		}
	}
	return best, found
}

// ruleMatches says whether the civil date (a UTC midnight) is one of rule's days.
func ruleMatches(r Rule, day time.Time) bool {
	switch r.Freq {
	case FreqDaily:
		return true
	case FreqWeekdays:
		wd := day.Weekday()
		return wd >= time.Monday && wd <= time.Friday
	case FreqWeekly:
		return r.DOW != nil && int(day.Weekday()) == *r.DOW
	case FreqMonthly:
		return r.DOM != nil && day.Day() == *r.DOM
	}
	return false
}

// NextSlot is the first instant of rule strictly after after, in loc. Days are
// walked in civil-date space, never by adding 24h to an instant, and each is
// resolved with ResolveWall, so the repeated hour of a DST overlap is never a
// second slot and a gap slot moves forward instead of disappearing.
func NextSlot(rule Rule, after time.Time, loc *time.Location) (time.Time, error) {
	if err := rule.validate(); err != nil {
		return time.Time{}, err
	}
	local := after.In(loc)
	// Start the day before: a slot resolved forward out of a gap can land on
	// the next civil date.
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
	for i := 0; i < 400; i++ {
		d := day.AddDate(0, 0, i)
		if !ruleMatches(rule, d) {
			continue
		}
		t, _, err := ResolveWall(d.Year(), d.Month(), d.Day(), rule.H, rule.Mi, loc)
		if err != nil {
			return time.Time{}, err
		}
		if t.After(after) {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("no %s slot within a year", rule.Freq)
}

func (r Rule) validate() error {
	if r.H < 0 || r.H > 23 {
		return invalid("hour must be 0–23")
	}
	if r.Mi < 0 || r.Mi > 59 {
		return invalid("minute must be 0–59")
	}
	switch r.Freq {
	case FreqDaily, FreqWeekdays:
		if r.DOW != nil || r.DOM != nil {
			return invalid("a %s rule has no day", r.Freq)
		}
	case FreqWeekly:
		if r.DOW == nil || *r.DOW < 0 || *r.DOW > 6 || r.DOM != nil {
			return invalid("a weekly rule needs dow 0–6 and no dom")
		}
	case FreqMonthly:
		if r.DOM == nil || *r.DOM < 1 || *r.DOM > 31 || r.DOW != nil {
			return invalid("a monthly rule needs dom 1–31 and no dow")
		}
	default:
		return invalid("unknown frequency %q", r.Freq)
	}
	return nil
}
