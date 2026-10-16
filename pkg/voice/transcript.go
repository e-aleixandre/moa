package voice

import (
	"errors"
	"fmt"
	"strings"
)

// ErrTranscriptFull means the observed record exceeded MaxTranscriptBytes.
// The call must end: dropping part of the record to keep acting is not
// allowed.
var ErrTranscriptFull = errors.New("voice transcript limit reached")

// TranscriptEntry is one observed caption: a provider item (xAI/ChatGPT,
// updated until final) or one timed Live fragment.
type TranscriptEntry struct {
	Role           string
	ItemID         string
	Text           string
	Final          bool
	StartMS, EndMS *int64
}

// Transcript is the observed record of a call. It holds exactly what the
// provider transcribed; it never fabricates finals or turns.
type Transcript struct {
	entries []TranscriptEntry
	size    int
}

// Add records one transcript event.
func (t *Transcript) Add(ev Event) error {
	if ev.Text == "" && !ev.Final {
		return nil
	}
	if ev.StartMS == nil {
		if i := t.open(ev); i >= 0 {
			e := &t.entries[i]
			next := e.Text + ev.Text
			if ev.Final {
				next = ev.Text
			}
			if err := t.grow(len(next) - len(e.Text)); err != nil {
				return err
			}
			e.Text, e.Final = next, ev.Final
			if ev.ItemID != "" {
				e.ItemID = ev.ItemID
			}
			return nil
		}
	}
	if err := t.grow(len(ev.Text)); err != nil {
		return err
	}
	t.entries = append(t.entries, TranscriptEntry{Role: ev.Role, ItemID: ev.ItemID, Text: ev.Text, Final: ev.Final, StartMS: ev.StartMS, EndMS: ev.EndMS})
	return nil
}

// open finds the unfinished entry an event continues: the same provider
// item, or else the latest unfinished entry of that role without an item.
func (t *Transcript) open(ev Event) int {
	for i := len(t.entries) - 1; i >= 0; i-- {
		e := t.entries[i]
		if e.Final || e.Role != ev.Role || e.StartMS != nil {
			continue
		}
		if ev.ItemID != "" && e.ItemID == ev.ItemID {
			return i
		}
		if e.ItemID == "" {
			return i
		}
	}
	return -1
}

func (t *Transcript) grow(n int) error {
	if t.size+n > MaxTranscriptBytes {
		return ErrTranscriptFull
	}
	t.size += n
	return nil
}

// Entries returns a copy of the record.
func (t *Transcript) Entries() []TranscriptEntry {
	return append([]TranscriptEntry(nil), t.entries...)
}

// Tail renders the newest part of the record as plain "User:"/"Voice:" lines,
// oldest first, within maxChars characters. What does not fit is the oldest
// part, replaced by one omission line.
func (t *Transcript) Tail(maxChars int) string {
	var lines []string
	used, omitted := 0, false
	for i := len(t.entries) - 1; i >= 0; i-- {
		e := t.entries[i]
		if e.Text == "" {
			continue
		}
		who := "User"
		if e.Role == "assistant" {
			who = "Voice"
		}
		line := []rune(who + ": " + e.Text)
		if used+len(line)+1 > maxChars {
			omitted = true
			if len(lines) == 0 && maxChars > 0 {
				lines = append(lines, string(line[len(line)-min(len(line), maxChars):]))
			}
			break
		}
		lines = append(lines, string(line))
		used += len(line) + 1
	}
	if omitted {
		lines = append(lines, "[earlier transcript omitted]")
	}
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return strings.Join(lines, "\n")
}

// Markdown renders the record literally, in arrival order, with Live
// intervals when the provider gave them.
func (t *Transcript) Markdown() string {
	var b strings.Builder
	b.WriteString("# Voice call transcript\n\n")
	if len(t.entries) == 0 {
		b.WriteString("_Nothing was transcribed._\n")
	}
	for _, e := range t.entries {
		who := "User"
		if e.Role == "assistant" {
			who = "Voice"
		}
		when := ""
		if e.StartMS != nil && e.EndMS != nil {
			when = fmt.Sprintf(" [%d–%d ms]", *e.StartMS, *e.EndMS)
		}
		partial := ""
		if !e.Final && e.StartMS == nil {
			partial = " (partial)"
		}
		fmt.Fprintf(&b, "**%s**%s%s: %s\n\n", who, when, partial, e.Text)
	}
	return b.String()
}
