package tasks

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Parsed is a natural-language "when" resolved in a zone: the canonical
// definition, the first instant it fires and, for a once time whose hour was
// not qualified, the same time in the other half of the day.
type Parsed struct {
	When     When   `json:"when"`
	TZ       string `json:"tz"`
	Next     int64  `json:"next"`
	Alt      int64  `json:"alt,omitempty"`
	Adjusted bool   `json:"adjusted,omitempty"`
}

// ParseError is a "when" that could not be read. Code is one of unknown,
// repeat, invalid or past; At is the instant that was in the past.
type ParseError struct {
	Code string `json:"code"`
	Msg  string `json:"error"`
	At   int64  `json:"at,omitempty"`
}

func (e *ParseError) Error() string { return e.Msg }
func (e *ParseError) Unwrap() error { return ErrInvalid }

func parseErr(code, format string, a ...any) *ParseError {
	return &ParseError{Code: code, Msg: fmt.Sprintf(format, a...)}
}

// maxRelative bounds "in N units": ten years of minutes.
const maxRelativeMinutes = 10 * 366 * 24 * 60

// The grammar (English and Spanish, accents and case folded away first) is a
// port of the scheduling lab's, made strict: every word must be understood,
// and a malformed time or date is an error, never a default.
var (
	relRe       = regexp.MustCompile(`^(?:in|en|dentro de)\s+(?:(-?\d+)|(an?|una?|half an|half|media))\s*(minutes?|mins?|minutos?|m|hours?|hrs?|horas?|h|days?|dias?|d)$`)
	repeatRe    = regexp.MustCompile(`^(?:every|each|cada|todos los|todas las|todos|todas)(?:\s+(.*))?$`)
	keywordRe   = regexp.MustCompile(`^(weekdays|laborables|entre semana|dias laborables|daily|diario|diaria|diariamente)(?:\s+(.*))?$`)
	noonRe      = regexp.MustCompile(`\b(noon|mediodia|midnight|medianoche)\b`)
	atTimeRe    = regexp.MustCompile(`(?:\bat|\ba las?|@)\s*(\d{1,2})(?:[:.h](\d{2}))?\s*(am|pm)?\b`)
	bareTimeRe  = regexp.MustCompile(`\b(\d{1,2})(?::(\d{2})\s*(am|pm)?|\s*(am|pm)|h)\b`)
	pmPhraseRe  = regexp.MustCompile(`\b(de la tarde|por la tarde|in the afternoon|in the evening|de la noche|por la noche|at night|tonight|esta noche)\b`)
	pmPhrasesRe = regexp.MustCompile(`\b(?:de la tarde|por la tarde|in the afternoon|in the evening|de la noche|por la noche|at night)\b`)
	amPhraseRe  = regexp.MustCompile(`\b(de la manana|por la manana|in the morning|de la madrugada)\b`)
	fillerRe    = regexp.MustCompile(`\b(?:at|a las?|a)\b|@`)
	dateFillRe  = regexp.MustCompile(`\b(?:el|on|next|proximo|this|este)\b`)
	isoRe       = regexp.MustCompile(`^(\d{4})-(\d{1,2})-(\d{1,2})$`)
	dmRe        = regexp.MustCompile(`^(\d{1,2})[/-](\d{1,2})(?:[/-](\d{4}))?$`)
	dayMonthRe  = regexp.MustCompile(`^(\d{1,2})(?:st|nd|rd|th|o)? (?:de )?([a-z]+)(?: (?:de )?(\d{4}))?$`)
	monthDayRe  = regexp.MustCompile(`^([a-z]+) (\d{1,2})(?:st|nd|rd|th)?(?: (\d{4}))?$`)
	domRe       = regexp.MustCompile(`^(\d+)(?:st|nd|rd|th|o|ro|er)?$`)
	spacesRe    = regexp.MustCompile(`\s+`)
)

var accents = strings.NewReplacer(
	"á", "a", "à", "a", "â", "a", "ä", "a", "é", "e", "è", "e", "ê", "e", "ë", "e",
	"í", "i", "ì", "i", "î", "i", "ï", "i", "ó", "o", "ò", "o", "ô", "o", "ö", "o",
	"ú", "u", "ù", "u", "û", "u", "ü", "u", "ñ", "n", "ç", "c", ",", " ", ";", " ")

func normalizeWhen(s string) string {
	s = accents.Replace(strings.ToLower(s))
	return strings.TrimSpace(spacesRe.ReplaceAllString(s, " "))
}

var weekdayNames = map[string]int{
	"sunday": 0, "monday": 1, "tuesday": 2, "wednesday": 3, "thursday": 4, "friday": 5, "saturday": 6,
	"domingo": 0, "lunes": 1, "martes": 2, "miercoles": 3, "jueves": 4, "viernes": 5, "sabado": 6,
	"sun": 0, "mon": 1, "tue": 2, "tues": 2, "wed": 3, "thu": 4, "thur": 4, "thurs": 4, "fri": 5, "sat": 6,
	"dom": 0, "lun": 1, "mie": 3, "jue": 4, "vie": 5, "sab": 6,
}

func weekdayOf(word string) (int, bool) {
	if d, ok := weekdayNames[word]; ok {
		return d, true
	}
	d, ok := weekdayNames[strings.TrimSuffix(word, "s")]
	return d, ok
}

var monthNames = map[string]int{
	"january": 1, "february": 2, "march": 3, "april": 4, "may": 5, "june": 6, "july": 7, "august": 8,
	"september": 9, "october": 10, "november": 11, "december": 12,
	"enero": 1, "febrero": 2, "marzo": 3, "abril": 4, "mayo": 5, "junio": 6, "julio": 7, "agosto": 8,
	"septiembre": 9, "setiembre": 9, "octubre": 10, "noviembre": 11, "diciembre": 12,
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "sept": 9, "oct": 10, "nov": 11, "dec": 12,
	"ene": 1, "abr": 4, "ago": 8, "dic": 12,
}

// clock is a time of day read from the text. Ambiguous marks an hour 1–11
// with nothing that says which half of the day.
type clock struct {
	h, mi      int
	ambiguous  bool
	start, end int
}

// readTime finds an explicit time in s: noon/midnight, "at 9", "a las 3:30",
// "14:00", "3pm", "18h". It returns nil when there is none, and an error when
// something that looks like a time is not one (25:00, 9:75, 13pm).
func readTime(s string) (*clock, error) {
	if m := noonRe.FindStringSubmatchIndex(s); m != nil {
		h := 0
		if s[m[2]:m[3]] == "noon" || s[m[2]:m[3]] == "mediodia" {
			h = 12
		}
		return &clock{h: h, start: m[0], end: m[1]}, nil
	}
	var hs, ms, suffix string
	var loc []int
	qualified := false
	switch {
	case atTimeRe.FindStringSubmatchIndex(s) != nil:
		loc = atTimeRe.FindStringSubmatchIndex(s)
		hs, ms, suffix = sub(s, loc, 1), sub(s, loc, 2), sub(s, loc, 3)
	case bareTimeRe.FindStringSubmatchIndex(s) != nil:
		loc = bareTimeRe.FindStringSubmatchIndex(s)
		hs, ms, suffix = sub(s, loc, 1), sub(s, loc, 2), sub(s, loc, 3)+sub(s, loc, 4)
		qualified = strings.HasSuffix(strings.TrimSpace(s[loc[0]:loc[1]]), "h")
	default:
		return nil, nil
	}
	h, _ := strconv.Atoi(hs)
	mi := 0
	if ms != "" {
		mi, _ = strconv.Atoi(ms)
		qualified = true
	}
	if mi > 59 || h > 23 {
		return nil, parseErr("invalid", "%q is not a time of day", strings.TrimSpace(s[loc[0]:loc[1]]))
	}
	if suffix != "" {
		if h < 1 || h > 12 {
			return nil, parseErr("invalid", "%q is not a time of day", strings.TrimSpace(s[loc[0]:loc[1]]))
		}
		if suffix == "pm" && h < 12 {
			h += 12
		}
		if suffix == "am" && h == 12 {
			h = 0
		}
		qualified = true
	}
	if suffix == "" && pmPhraseRe.MatchString(s) {
		if h == 12 && strings.Contains(s, "de la noche") {
			h = 0
		} else if h < 12 {
			h += 12
		}
		qualified = true
	}
	if amPhraseRe.MatchString(s) {
		qualified = true
	}
	return &clock{h: h, mi: mi, ambiguous: !qualified && h >= 1 && h <= 11, start: loc[0], end: loc[1]}, nil
}

func sub(s string, loc []int, i int) string {
	if loc[2*i] < 0 {
		return ""
	}
	return s[loc[2*i]:loc[2*i+1]]
}

// withoutTime removes the time span (if any) and the multi-word day-period
// phrases that only qualify it.
func withoutTime(s string, c *clock) string {
	if c != nil {
		s = s[:c.start] + " " + s[c.end:]
	}
	s = amPhraseRe.ReplaceAllString(s, " ")
	s = pmPhrasesRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(spacesRe.ReplaceAllString(s, " "))
}

type civil struct {
	y int
	m time.Month
	d int
}

func (c civil) plus(days int) civil {
	t := time.Date(c.y, c.m, c.d+days, 0, 0, 0, 0, time.UTC)
	return civil{t.Year(), t.Month(), t.Day()}
}

// ParseWhen reads text in tz, relative to now. Empty text returns nil, nil:
// nothing has been chosen. Relative amounts are elapsed time; every calendar
// choice is resolved with the same civil resolver the planner uses.
func ParseWhen(text string, now time.Time, tz string) (*Parsed, error) {
	loc, err := LoadZone(tz)
	if err != nil {
		return nil, parseErr("invalid", "%v", err)
	}
	s := normalizeWhen(text)
	if s == "" {
		return nil, nil
	}
	if m := relRe.FindStringSubmatch(s); m != nil {
		return parseRelative(m, now, tz)
	}
	if m := repeatRe.FindStringSubmatch(s); m != nil {
		return parseRepeat("", m[1], now, loc, tz)
	}
	if m := keywordRe.FindStringSubmatch(s); m != nil {
		return parseRepeat(m[1], m[2], now, loc, tz)
	}
	return parseOnce(s, now, loc, tz)
}

func parseRelative(m []string, now time.Time, tz string) (*Parsed, error) {
	unit := m[3][:1]
	per := map[string]int64{"m": 1, "h": 60, "d": 24 * 60}[unit]
	var minutes int64
	if m[1] != "" {
		n, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil || n <= 0 || n > maxRelativeMinutes/per {
			return nil, parseErr("invalid", "%q is not an amount of time I can schedule", m[1])
		}
		minutes = n * per
	} else if strings.HasPrefix(m[2], "half") || m[2] == "media" {
		if unit != "h" {
			return nil, parseErr("unknown", "only half an hour is understood")
		}
		minutes = 30
	} else {
		minutes = per
	}
	at := now.Add(time.Duration(minutes) * time.Minute).Round(time.Minute)
	return &Parsed{When: When{Kind: WhenOnce, At: at.UnixMilli()}, TZ: tz, Next: at.UnixMilli()}, nil
}

var (
	dailyWords    = map[string]bool{"day": true, "dia": true, "dias": true, "morning": true, "mornings": true, "manana": true, "mananas": true}
	weekdayWords  = map[string]bool{"weekday": true, "weekdays": true, "dia laborable": true, "dias laborables": true, "laborable": true, "laborables": true, "entre semana": true, "working day": true, "working days": true, "business day": true, "business days": true}
	monthFillerRe = regexp.MustCompile(`\b(?:on|the|el|dia|day|de|of)\b`)
)

func parseRepeat(keyword, rest string, now time.Time, loc *time.Location, tz string) (*Parsed, error) {
	t, err := readTime(rest)
	if err != nil {
		return nil, err
	}
	body := withoutTime(rest, t)
	body = strings.TrimSpace(spacesRe.ReplaceAllString(fillerRe.ReplaceAllString(body, " "), " "))
	h, mi := 9, 0
	if t != nil {
		h, mi = t.h, t.mi
	}
	var rule Rule
	first, tail, _ := strings.Cut(body, " ")
	switch {
	case keyword != "":
		if body != "" {
			return nil, parseErr("repeat", "I did not understand %q after %q", body, keyword)
		}
		rule = Rule{Freq: FreqWeekdays}
		if strings.HasPrefix(keyword, "dai") || strings.HasPrefix(keyword, "diar") {
			rule.Freq = FreqDaily
		}
	case dailyWords[body]:
		rule = Rule{Freq: FreqDaily}
	case weekdayWords[body]:
		rule = Rule{Freq: FreqWeekdays}
	case body == "week" || body == "semana":
		d := int(now.In(loc).Weekday())
		rule = Rule{Freq: FreqWeekly, DOW: &d}
	case first == "month" || first == "mes":
		dom := 1
		if left := strings.TrimSpace(spacesRe.ReplaceAllString(monthFillerRe.ReplaceAllString(tail, " "), " ")); left != "" {
			m := domRe.FindStringSubmatch(left)
			if m == nil {
				return nil, parseErr("repeat", "I did not understand %q after the month", left)
			}
			n, err := strconv.Atoi(m[1])
			if err != nil || n < 1 || n > 31 {
				return nil, parseErr("invalid", "day %s of the month does not exist", m[1])
			}
			dom = n
		}
		rule = Rule{Freq: FreqMonthly, DOM: &dom}
	default:
		d, ok := weekdayOf(body)
		if !ok {
			return nil, parseErr("repeat", "I did not understand how often %q repeats", body)
		}
		rule = Rule{Freq: FreqWeekly, DOW: &d}
	}
	rule.H, rule.Mi = h, mi
	if err := rule.validate(); err != nil {
		return nil, parseErr("invalid", "%v", err)
	}
	next, err := NextSlot(rule, now, loc)
	if err != nil {
		return nil, parseErr("invalid", "%v", err)
	}
	w := next.In(loc)
	return &Parsed{When: When{Kind: WhenRepeat, Rule: &rule}, TZ: tz, Next: next.UnixMilli(),
		Adjusted: w.Hour() != h || w.Minute() != mi}, nil
}

var (
	todayRe     = regexp.MustCompile(`^(today|hoy)$`)
	tonightRe   = regexp.MustCompile(`^(tonight|esta noche)$`)
	tomorrowRe  = regexp.MustCompile(`^(tomorrow|manana|tomorrow morning)$`)
	afterTomRe  = regexp.MustCompile(`^(day after tomorrow|pasado manana)$`)
	yesterdayRe = regexp.MustCompile(`^(yesterday|ayer)$`)
)

func parseOnce(s string, now time.Time, loc *time.Location, tz string) (*Parsed, error) {
	t, err := readTime(s)
	if err != nil {
		return nil, err
	}
	body := withoutTime(s, t)
	body = fillerRe.ReplaceAllString(body, " ")
	body = strings.TrimSpace(spacesRe.ReplaceAllString(dateFillRe.ReplaceAllString(body, " "), " "))
	wall := now.In(loc)
	cur := civil{wall.Year(), wall.Month(), wall.Day()}

	var day civil
	rollable := false // a bare time already past today means tomorrow
	tonight := false
	yesterday := false
	switch {
	case body == "":
		if t == nil {
			return nil, parseErr("unknown", "I did not understand %q", s)
		}
		day, rollable = cur, true
	case todayRe.MatchString(body):
		day = cur
	case tonightRe.MatchString(body):
		day, tonight = cur, true
	case tomorrowRe.MatchString(body):
		day = cur.plus(1)
	case afterTomRe.MatchString(body):
		day = cur.plus(2)
	case yesterdayRe.MatchString(body):
		day, yesterday = cur.plus(-1), true
	default:
		var ok bool
		if day, ok, err = dateOf(body, cur, wall); err != nil {
			return nil, err
		} else if !ok {
			return nil, parseErr("unknown", "I did not understand %q", body)
		}
	}
	h, mi, ambiguous := 9, 0, false
	switch {
	case t != nil:
		h, mi, ambiguous = t.h, t.mi, t.ambiguous
	case tonight:
		h = 21
	}
	at, adjusted, err := ResolveWall(day.y, day.m, day.d, h, mi, loc)
	if err != nil {
		return nil, parseErr("invalid", "%v", err)
	}
	if !at.After(now) && rollable && !yesterday {
		d := day.plus(1)
		if at, adjusted, err = ResolveWall(d.y, d.m, d.d, h, mi, loc); err != nil {
			return nil, parseErr("invalid", "%v", err)
		}
		day = d
	}
	if !at.After(now) {
		return nil, &ParseError{Code: "past", Msg: "that time has already passed", At: at.UnixMilli()}
	}
	p := &Parsed{When: When{Kind: WhenOnce, At: at.UnixMilli()}, TZ: tz, Next: at.UnixMilli(), Adjusted: adjusted}
	if ambiguous {
		p.Alt = otherHalf(day, cur, h, mi, rollable, now, loc)
	}
	return p, nil
}

// otherHalf is the same time twelve hours later: today's when the bare time
// was rolled past it and that is still ahead, else on the day chosen.
func otherHalf(day, cur civil, h, mi int, bare bool, now time.Time, loc *time.Location) int64 {
	if bare {
		if t, _, err := ResolveWall(cur.y, cur.m, cur.d, h+12, mi, loc); err == nil && t.After(now) {
			return t.UnixMilli()
		}
	}
	if t, _, err := ResolveWall(day.y, day.m, day.d, h+12, mi, loc); err == nil {
		return t.UnixMilli()
	}
	return 0
}

// dateOf reads a calendar date: ISO, day/month, "5 oct", "october 5th", or a
// weekday (the next one, never today). ok is false when body is none of them.
func dateOf(body string, cur civil, wall time.Time) (civil, bool, error) {
	atoi := func(s string) int { n, _ := strconv.Atoi(s); return n }
	yearOr := func(s string) int {
		if s == "" {
			return cur.y
		}
		return atoi(s)
	}
	if m := isoRe.FindStringSubmatch(body); m != nil {
		return civil{atoi(m[1]), time.Month(atoi(m[2])), atoi(m[3])}, true, nil
	}
	if m := dmRe.FindStringSubmatch(body); m != nil {
		return civil{yearOr(m[3]), time.Month(atoi(m[2])), atoi(m[1])}, true, nil
	}
	if m := dayMonthRe.FindStringSubmatch(body); m != nil {
		if mo, ok := monthNames[m[2]]; ok {
			return civil{yearOr(m[3]), time.Month(mo), atoi(m[1])}, true, nil
		}
	}
	if m := monthDayRe.FindStringSubmatch(body); m != nil {
		if mo, ok := monthNames[m[1]]; ok {
			return civil{yearOr(m[3]), time.Month(mo), atoi(m[2])}, true, nil
		}
	}
	if d, ok := weekdayOf(body); ok {
		ahead := (d - int(wall.Weekday()) + 7) % 7
		if ahead == 0 {
			ahead = 7
		}
		return cur.plus(ahead), true, nil
	}
	return civil{}, false, nil
}
