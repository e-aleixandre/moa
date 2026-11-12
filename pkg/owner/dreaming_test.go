package owner

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDreamingIsOffUnlessEnabled(t *testing.T) {
	now := time.Date(2026, 11, 12, 5, 0, 0, 0, time.UTC)
	for _, raw := range []string{`{}`, `{"dreaming":{}}`, `{"dreaming":{"enabled":false}}`, `{"dreaming":{"enabled":true,"tz":"Mars/Olympus"}}`} {
		var o Owner
		if err := json.Unmarshal([]byte(raw), &o); err != nil {
			t.Fatal(err)
		}
		if d := o.DreamingSettings().DreamingDue(now); d != "" {
			t.Fatalf("%s: due %q, want off", raw, d)
		}
	}
}

func TestDreamingDueUsesTheOwnersCivilDate(t *testing.T) {
	var o Owner
	if err := json.Unmarshal([]byte(`{"dreaming":{"enabled":true,"hour":3,"tz":"Europe/Madrid"}}`), &o); err != nil {
		t.Fatal(err)
	}
	s := o.DreamingSettings()
	// 2026-10-25 is the DST change in Madrid: 01:30Z is 02:30 local, before the hour.
	if d := s.DreamingDue(time.Date(2026, 10, 25, 1, 30, 0, 0, time.UTC)); d != "" {
		t.Fatalf("due before the local hour: %q", d)
	}
	if d := s.DreamingDue(time.Date(2026, 10, 25, 2, 30, 0, 0, time.UTC)); d != "2026-10-25" {
		t.Fatalf("due = %q", d)
	}
	if d := s.DreamingDue(time.Date(2026, 11, 13, 2, 30, 0, 0, time.UTC)); d != "2026-11-13" {
		t.Fatalf("local date after midnight = %q", d)
	}
}
