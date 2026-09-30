package tasks

import (
	"testing"
	"time"
)

func mustZone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func utc(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func daily(h, mi int) Rule { return Rule{Freq: FreqDaily, H: h, Mi: mi} }

func TestScheduleCivilResolution(t *testing.T) {
	cases := []struct {
		zone          string
		y             int
		m             time.Month
		d, h, mi      int
		want, next    string
		wantAdjusted  bool
		wantLocalHour int
	}{
		{"Europe/Madrid", 2026, time.March, 29, 2, 30, "2026-03-29T01:30:00Z", "2026-03-30T00:30:00Z", true, 3},
		{"Europe/Madrid", 2026, time.October, 25, 2, 30, "2026-10-25T00:30:00Z", "2026-10-26T01:30:00Z", false, 2},
		{"America/New_York", 2026, time.March, 8, 2, 30, "2026-03-08T07:30:00Z", "2026-03-09T06:30:00Z", true, 3},
		{"America/New_York", 2026, time.November, 1, 1, 30, "2026-11-01T05:30:00Z", "2026-11-02T06:30:00Z", false, 1},
	}
	for _, c := range cases {
		t.Run(c.zone+"/"+c.want, func(t *testing.T) {
			loc := mustZone(t, c.zone)
			got, adjusted, err := ResolveWall(c.y, c.m, c.d, c.h, c.mi, loc)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Equal(utc(c.want)) || adjusted != c.wantAdjusted {
				t.Fatalf("resolve = %s adjusted=%v, want %s adjusted=%v", got.UTC().Format(time.RFC3339), adjusted, c.want, c.wantAdjusted)
			}
			if lh := got.In(loc).Hour(); lh != c.wantLocalHour || got.In(loc).Minute() != c.mi {
				t.Fatalf("local wall = %s", got.In(loc))
			}
			next, err := NextSlot(daily(c.h, c.mi), got, loc)
			if err != nil {
				t.Fatal(err)
			}
			if !next.Equal(utc(c.next)) {
				t.Fatalf("next daily = %s, want %s", next.UTC().Format(time.RFC3339), c.next)
			}
		})
	}
	if _, _, err := ResolveWall(2026, time.February, 31, 9, 0, time.UTC); err == nil {
		t.Fatal("31 February was normalized instead of rejected")
	}
}

func TestScheduleNextSkipsSecondOverlapInstance(t *testing.T) {
	for _, c := range []struct {
		zone         string
		first, after string
		rule         Rule
		want         string
	}{
		// First 02:30 CEST is 00:30Z; the repeated 02:30 CET is 01:30Z.
		{"Europe/Madrid", "2026-10-25T00:30:00Z", "2026-10-25T01:00:00Z", daily(2, 30), "2026-10-26T01:30:00Z"},
		// First 01:30 EDT is 05:30Z; the repeated 01:30 EST is 06:30Z.
		{"America/New_York", "2026-11-01T05:30:00Z", "2026-11-01T06:00:00Z", daily(1, 30), "2026-11-02T06:30:00Z"},
	} {
		loc := mustZone(t, c.zone)
		for _, after := range []string{c.first, c.after} {
			got, err := NextSlot(c.rule, utc(after), loc)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Equal(utc(c.want)) {
				t.Fatalf("%s: next after %s = %s, want %s (the repeated hour is not a second slot)",
					c.zone, after, got.UTC().Format(time.RFC3339), c.want)
			}
		}
	}
}

func TestScheduleCalendarRules(t *testing.T) {
	madrid := mustZone(t, "Europe/Madrid")
	step := func(r Rule, after time.Time, loc *time.Location) time.Time {
		t.Helper()
		got, err := NextSlot(r, after, loc)
		if err != nil {
			t.Fatal(err)
		}
		if !got.After(after) {
			t.Fatalf("next %s is not strictly after %s", got, after)
		}
		return got
	}
	// Daily 09:00 Madrid across both transitions keeps the wall clock: 23h
	// then 25h between the slots around each change.
	for _, c := range []struct {
		from    string
		spacing time.Duration
	}{{"2026-03-28T08:00:00Z", 23 * time.Hour}, {"2026-10-24T07:00:00Z", 25 * time.Hour}} {
		a := step(daily(9, 0), utc(c.from).Add(-time.Minute), madrid)
		b := step(daily(9, 0), a, madrid)
		if a.In(madrid).Hour() != 9 || b.In(madrid).Hour() != 9 || b.Sub(a) != c.spacing {
			t.Fatalf("daily across DST: %s -> %s (%s)", a.In(madrid), b.In(madrid), b.Sub(a))
		}
	}
	monday, dom31 := 1, 31
	// Weekly Monday from Wednesday 30 Sep 2026.
	if got := step(Rule{Freq: FreqWeekly, DOW: &monday, H: 9}, utc("2026-09-30T12:00:00Z"), time.UTC); !got.Equal(utc("2026-10-05T09:00:00Z")) {
		t.Fatalf("weekly monday = %s", got)
	}
	// Weekdays: Friday's slot is followed by Monday's.
	if got := step(Rule{Freq: FreqWeekdays, H: 9}, utc("2026-10-02T09:00:00Z"), time.UTC); !got.Equal(utc("2026-10-05T09:00:00Z")) {
		t.Fatalf("weekdays after friday = %s", got)
	}
	// Monthly on the 31st skips February instead of clamping.
	if got := step(Rule{Freq: FreqMonthly, DOM: &dom31, H: 9}, utc("2027-01-31T09:00:00Z"), time.UTC); !got.Equal(utc("2027-03-31T09:00:00Z")) {
		t.Fatalf("monthly 31 after Jan 31 = %s", got)
	}
}
