package tasks

import (
	"errors"
	"testing"
	"time"
)

// The reference clock of the parser tests: Wednesday 30 Sep 2026, 16:40 in
// Madrid (CEST, UTC+2).
var whenNow = time.Date(2026, 9, 30, 14, 40, 0, 0, time.UTC)

const madrid = "Europe/Madrid"

func ms(s string) int64 {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t.UnixMilli()
}

func mustParse(t *testing.T, text string) *Parsed {
	t.Helper()
	p, err := ParseWhen(text, whenNow, madrid)
	if err != nil {
		t.Fatalf("ParseWhen(%q) = %v", text, err)
	}
	if p == nil {
		t.Fatalf("ParseWhen(%q) = nothing", text)
	}
	return p
}

func parseCode(t *testing.T, text string) *ParseError {
	t.Helper()
	p, err := ParseWhen(text, whenNow, madrid)
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("ParseWhen(%q) = %+v, %v; want a ParseError", text, p, err)
	}
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("ParseError %v is not ErrInvalid", err)
	}
	return pe
}

func dow(n int) *int { return &n }

// Equivalent English and Spanish phrases give the same canonical value.
func TestWhenParseEnglishSpanish(t *testing.T) {
	type want struct {
		at   string // once
		rule *Rule  // repeat
		next string
	}
	once := func(at string) want { return want{at: at, next: at} }
	rep := func(r Rule, next string) want { return want{rule: &r, next: next} }
	cases := []struct {
		name  string
		texts []string
		want  want
	}{
		{"in 20 minutes", []string{"in 20m", "in 20 min", "in 20 minutes", "en 20min", "en 20 minutos", "dentro de 20 minutos"}, once("2026-09-30T15:00:00Z")},
		{"half an hour", []string{"en media hora", "in half an hour"}, once("2026-09-30T15:10:00Z")},
		{"an hour", []string{"in an hour", "in 1 hour", "en una hora", "en 1 hora"}, once("2026-09-30T15:40:00Z")},
		{"two hours", []string{"in 2h", "en 2 horas", "dentro de 2 horas"}, once("2026-09-30T16:40:00Z")},
		{"three days elapsed", []string{"in 3 days", "en 3 dias", "en 3 días", "dentro de 3 días"}, once("2026-10-03T14:40:00Z")},
		{"tomorrow at 9:00", []string{"tomorrow at 9:00", "tomorrow at 09:00", "mañana a las 9:00", "manana a las 9:00", "mañana 9:00"}, once("2026-10-01T07:00:00Z")},
		{"tomorrow default 09:00", []string{"tomorrow", "mañana", "manana"}, once("2026-10-01T07:00:00Z")},
		{"tomorrow morning", []string{"tomorrow morning", "mañana por la mañana"}, once("2026-10-01T07:00:00Z")},
		{"day after tomorrow", []string{"day after tomorrow at 9:00", "pasado mañana a las 9:00", "pasado manana 9:00"}, once("2026-10-02T07:00:00Z")},
		{"tonight defaults to 21:00", []string{"tonight", "esta noche"}, once("2026-09-30T19:00:00Z")},
		{"tonight at 10", []string{"tonight at 10", "esta noche a las 10"}, once("2026-09-30T20:00:00Z")},
		{"today at 18:00", []string{"today at 18:00", "hoy a las 18:00", "at 18:00", "18:00", "a las 18h"}, once("2026-09-30T16:00:00Z")},
		{"5pm", []string{"at 5pm", "5pm", "a las 5pm", "5 pm", "17:00"}, once("2026-09-30T15:00:00Z")},
		{"noon rolls to tomorrow", []string{"noon", "mediodía", "mediodia"}, once("2026-10-01T10:00:00Z")},
		{"midnight rolls to tomorrow", []string{"midnight", "medianoche"}, once("2026-09-30T22:00:00Z")},
		{"tomorrow at noon", []string{"tomorrow at noon", "mañana a mediodía"}, once("2026-10-01T10:00:00Z")},
		{"monday default 09:00", []string{"monday", "lunes", "next monday", "el lunes", "próximo lunes"}, once("2026-10-05T07:00:00Z")},
		{"monday at 9:00", []string{"monday at 9:00", "el lunes a las 9:00", "lun 9:00", "mon 9:00"}, once("2026-10-05T07:00:00Z")},
		{"wednesday is next week", []string{"wednesday 9:00", "miércoles 9:00", "miercoles 9:00"}, once("2026-10-07T07:00:00Z")},
		{"saturday", []string{"saturday at 10:30", "sábado a las 10:30", "sabado 10:30"}, once("2026-10-03T08:30:00Z")},
		{"ISO date", []string{"2026-10-05 15:30", "2026-10-05 at 15:30"}, once("2026-10-05T13:30:00Z")},
		{"ISO date in winter", []string{"2026-12-25 at 9:00", "2026-12-25 9:00"}, once("2026-12-25T08:00:00Z")},
		{"day/month", []string{"5/10 at 18:00", "5/10 18:00", "el 5/10 a las 18:00", "5-10 18:00"}, once("2026-10-05T16:00:00Z")},
		{"named month", []string{"5 october 10:00", "october 5 10:00", "oct 5 10:00", "5 oct 10:00", "5 de octubre a las 10:00", "el 5 de octubre 10:00", "October 5th at 10:00"}, once("2026-10-05T08:00:00Z")},
		{"named month Spanish only", []string{"12 dic 10:00", "12 de diciembre a las 10:00", "dec 12 10:00", "12 december 10:00"}, once("2026-12-12T09:00:00Z")},
		{"named month with year", []string{"5 october 2027 10:00", "5 de octubre de 2027 a las 10:00"}, once("2027-10-05T08:00:00Z")},
		{"every day", []string{"every day at 8:30", "cada día a las 8:30", "cada dia a las 8:30", "todos los días a las 8:30", "todos los dias 8:30", "daily at 8:30"}, rep(Rule{Freq: FreqDaily, H: 8, Mi: 30}, "2026-10-01T06:30:00Z")},
		{"every day at 8", []string{"every day at 8", "todos los días a las 8"}, rep(Rule{Freq: FreqDaily, H: 8}, "2026-10-01T06:00:00Z")},
		{"weekdays", []string{"weekdays at 9:00", "every weekday at 9:00", "laborables a las 9:00", "entre semana a las 9:00", "cada día laborable a las 9:00"}, rep(Rule{Freq: FreqWeekdays, H: 9}, "2026-10-01T07:00:00Z")},
		{"weekdays 7:30", []string{"laborables a las 7:30", "weekdays at 7:30"}, rep(Rule{Freq: FreqWeekdays, H: 7, Mi: 30}, "2026-10-01T05:30:00Z")},
		{"every monday", []string{"every monday at 9", "cada lunes a las 9", "every mon 9:00", "todos los lunes a las 9:00", "every mondays at 9"}, rep(Rule{Freq: FreqWeekly, DOW: dow(1), H: 9}, "2026-10-05T07:00:00Z")},
		{"every sunday default time", []string{"every sunday", "cada domingo"}, rep(Rule{Freq: FreqWeekly, DOW: dow(0), H: 9}, "2026-10-04T07:00:00Z")},
		{"every friday 17:30", []string{"every friday at 17:30", "cada viernes a las 17:30"}, rep(Rule{Freq: FreqWeekly, DOW: dow(5), H: 17, Mi: 30}, "2026-10-02T15:30:00Z")},
		{"every week keeps today's weekday", []string{"every week", "cada semana"}, rep(Rule{Freq: FreqWeekly, DOW: dow(3), H: 9}, "2026-10-07T07:00:00Z")},
		{"monthly on the 1st", []string{"every month on the 1st", "every month on the 1", "cada mes el 1", "cada mes el día 1", "every month"}, rep(Rule{Freq: FreqMonthly, DOM: dow(1), H: 9}, "2026-10-01T07:00:00Z")},
		{"monthly on the 15th at 8", []string{"every month on the 15th at 8:00", "cada mes el 15 a las 8:00"}, rep(Rule{Freq: FreqMonthly, DOM: dow(15), H: 8}, "2026-10-15T06:00:00Z")},
	}
	n := 0
	for _, tc := range cases {
		for _, text := range tc.texts {
			n++
			t.Run(tc.name+"/"+text, func(t *testing.T) {
				p := mustParse(t, text)
				if p.TZ != madrid {
					t.Fatalf("tz = %q", p.TZ)
				}
				if tc.want.rule == nil {
					if p.When.Kind != WhenOnce || p.When.At != ms(tc.want.at) || p.When.Rule != nil {
						t.Fatalf("%q = %+v, want once %s", text, p.When, tc.want.at)
					}
				} else {
					r := p.When.Rule
					if p.When.Kind != WhenRepeat || p.When.At != 0 || r == nil || r.Freq != tc.want.rule.Freq || r.H != tc.want.rule.H || r.Mi != tc.want.rule.Mi ||
						!sameInt(r.DOW, tc.want.rule.DOW) || !sameInt(r.DOM, tc.want.rule.DOM) {
						t.Fatalf("%q = %+v %+v, want repeat %+v", text, p.When, r, tc.want.rule)
					}
				}
				if p.Next != ms(tc.want.next) {
					t.Fatalf("%q next = %s, want %s", text, time.UnixMilli(p.Next).UTC().Format(time.RFC3339), tc.want.next)
				}
				if err := p.When.validate(); err != nil {
					t.Fatalf("not canonical: %v", err)
				}
			})
		}
	}
	if n < 100 {
		t.Fatalf("only %d corpus phrases", n)
	}
	t.Logf("%d phrases in %d groups", n, len(cases))
}

func sameInt(a, b *int) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

// A bare hour 1–11 offers the other half of the day; a bare time already
// past today means tomorrow; an explicit past instant is refused.
func TestWhenParseRolloverAndAlternatives(t *testing.T) {
	type c struct {
		text, at string
		alt      string // "" none
	}
	for _, tc := range []c{
		{"at 5", "2026-10-01T03:00:00Z", "2026-09-30T15:00:00Z"},
		{"a las 5", "2026-10-01T03:00:00Z", "2026-09-30T15:00:00Z"},
		{"at 3", "2026-10-01T01:00:00Z", "2026-10-01T13:00:00Z"},
		{"at 9", "2026-10-01T07:00:00Z", "2026-09-30T19:00:00Z"},
		{"at 11", "2026-10-01T09:00:00Z", "2026-09-30T21:00:00Z"},
		{"tomorrow at 9", "2026-10-01T07:00:00Z", "2026-10-01T19:00:00Z"},
		{"mañana a las 9", "2026-10-01T07:00:00Z", "2026-10-01T19:00:00Z"},
		{"monday at 9", "2026-10-05T07:00:00Z", "2026-10-05T19:00:00Z"},
		{"5 oct at 10", "2026-10-05T08:00:00Z", "2026-10-05T20:00:00Z"},
		{"at 9pm", "2026-09-30T19:00:00Z", ""},
		{"at 9am", "2026-10-01T07:00:00Z", ""},
		{"9am", "2026-10-01T07:00:00Z", ""},
		{"at 17:00", "2026-09-30T15:00:00Z", ""},
		{"17:00", "2026-09-30T15:00:00Z", ""},
		{"at 9:00", "2026-10-01T07:00:00Z", ""},
		{"at 9h", "2026-10-01T07:00:00Z", ""},
		{"at 5 in the afternoon", "2026-09-30T15:00:00Z", ""},
		{"a las 5 de la tarde", "2026-09-30T15:00:00Z", ""},
		{"at 9 in the morning", "2026-10-01T07:00:00Z", ""},
		{"a las 9 de la mañana", "2026-10-01T07:00:00Z", ""},
		{"tomorrow at 21:00", "2026-10-01T19:00:00Z", ""},
		{"in 20m", "2026-09-30T15:00:00Z", ""},
		{"tomorrow", "2026-10-01T07:00:00Z", ""},
	} {
		t.Run(tc.text, func(t *testing.T) {
			p := mustParse(t, tc.text)
			if p.When.At != ms(tc.at) || p.Next != ms(tc.at) {
				t.Fatalf("at = %s, want %s", time.UnixMilli(p.When.At).UTC().Format(time.RFC3339), tc.at)
			}
			switch {
			case tc.alt == "" && p.Alt != 0:
				t.Fatalf("alt = %s, want none", time.UnixMilli(p.Alt).UTC().Format(time.RFC3339))
			case tc.alt != "" && p.Alt != ms(tc.alt):
				t.Fatalf("alt = %s, want %s", time.UnixMilli(p.Alt).UTC().Format(time.RFC3339), tc.alt)
			}
		})
	}
	for _, text := range []string{"today at 3", "hoy a las 3", "today at 9:00", "today at 16:00", "2026-09-30 10:00", "2026-01-01", "1 jan 2020 10:00", "ayer", "yesterday at 9:00", "5/9 10:00"} {
		t.Run("past/"+text, func(t *testing.T) {
			if pe := parseCode(t, text); pe.Code != "past" {
				t.Fatalf("%q = %+v, want past", text, pe)
			}
		})
	}
	// A repeating rule never rolls or asks: its next slot is the answer.
	if p := mustParse(t, "every day at 5"); p.Alt != 0 || p.Next != ms("2026-10-01T03:00:00Z") {
		t.Fatalf("repeat at 5 = %+v", p)
	}
}

// Malformed input is an error with a code, never a defaulted or normalized
// schedule.
func TestWhenParseInvalidInputs(t *testing.T) {
	for code, texts := range map[string][]string{
		"invalid": {
			"2026-02-31 at 9:00", "31/02 10:00", "32/1 10:00", "5/13 10:00", "2026-13-01 10:00", "31 feb 10:00", "2026-10-05 25:00",
			"every month on the 0", "every month on the 32", "cada mes el 0", "cada mes el 32", "every month on the 99",
			"tomorrow at 99:90", "at 25", "at 9:75", "mañana a las 24:00", "at 13pm", "at 0am", "at 0pm",
			"in 0 minutes", "en 0 min", "in -5 minutes", "en -3 horas", "in 99999999999999999999 days", "in 100000000 days", "in 0 days",
		},
		"unknown": {
			"banana", "tomorrow banana", "tomorrow at 9 and then feed the cat", "monday tuesday", "at", "the day", "next", "in a while", "en un rato",
			"tomorrow at 9:00 please run the tests", "5 oct 10:00 extra", "in 20m and more", "en media hora luego",
		},
		"repeat": {"every fortnight", "cada quincena", "every", "cada", "every day tuesday", "every monday tuesday", "every 2 days", "every month on banana"},
	} {
		for _, text := range texts {
			t.Run(code+"/"+text, func(t *testing.T) {
				p, err := ParseWhen(text, whenNow, madrid)
				var pe *ParseError
				if !errors.As(err, &pe) || pe.Code != code || p != nil {
					t.Fatalf("%q = %+v, %v; want code %q", text, p, err, code)
				}
			})
		}
	}
	for _, tz := range []string{"Mars/Olympus", "Local", "", " UTC", "+02:00"} {
		if _, err := ParseWhen("tomorrow at 9:00", whenNow, tz); err == nil {
			t.Errorf("tz %q accepted", tz)
		} else if pe := (*ParseError)(nil); !errors.As(err, &pe) || pe.Code != "invalid" {
			t.Errorf("tz %q = %v, want an invalid ParseError", tz, err)
		}
	}
	if p, err := ParseWhen("   ", whenNow, madrid); p != nil || err != nil {
		t.Fatalf("empty text = %+v, %v; want no selection", p, err)
	}
}

// The parser and the planner resolve wall times with the same code: the four
// DESIGN §4 cases, in Madrid and New York, and a zone-independent process.
func TestWhenParseResolvesDST(t *testing.T) {
	for _, tc := range []struct {
		name, now, tz, text string
		at                  string
		adjusted            bool
	}{
		{"madrid gap", "2026-03-28T12:00:00Z", madrid, "2026-03-29 02:30", "2026-03-29T01:30:00Z", true},
		{"madrid overlap", "2026-10-24T12:00:00Z", madrid, "2026-10-25 02:30", "2026-10-25T00:30:00Z", false},
		{"new york gap", "2026-03-07T12:00:00Z", "America/New_York", "2026-03-08 02:30", "2026-03-08T07:30:00Z", true},
		{"new york overlap", "2026-10-31T12:00:00Z", "America/New_York", "2026-11-01 01:30", "2026-11-01T05:30:00Z", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ParseWhen(tc.text, mustTime(tc.now), tc.tz)
			if err != nil || p == nil {
				t.Fatalf("%v %v", p, err)
			}
			if p.When.At != ms(tc.at) || p.Adjusted != tc.adjusted {
				t.Fatalf("at %s adjusted=%v, want %s adjusted=%v", time.UnixMilli(p.When.At).UTC().Format(time.RFC3339), p.Adjusted, tc.at, tc.adjusted)
			}
		})
	}
	// A daily 02:30 rule after the overlap fires once that night.
	p, err := ParseWhen("every day at 02:30", mustTime("2026-10-24T12:00:00Z"), madrid)
	if err != nil || p.Next != ms("2026-10-25T00:30:00Z") {
		t.Fatalf("daily overlap = %+v %v", p, err)
	}
	loc, _ := LoadZone(madrid)
	after, _ := NextSlot(*p.When.Rule, time.UnixMilli(p.Next), loc)
	if after.UnixMilli() != ms("2026-10-26T01:30:00Z") {
		t.Fatalf("slot after the overlap = %s", after)
	}
}

func mustTime(s string) time.Time { return time.UnixMilli(ms(s)).UTC() }
