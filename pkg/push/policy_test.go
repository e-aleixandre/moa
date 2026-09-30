package push

import (
	"sync"
	"testing"
	"time"
)

type recorder struct {
	mu   sync.Mutex
	sent []Notification
}

func (r *recorder) Notify(n Notification) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, n)
}

func (r *recorder) all() []Notification {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Notification(nil), r.sent...)
}

// fakeClock captures scheduled timers so a test decides when the grace ends.
type fakeClock struct {
	mu     sync.Mutex
	timers []*fakeTimer
}

type fakeTimer struct {
	d       time.Duration
	f       func()
	stopped bool
}

func (c *fakeClock) after(d time.Duration, f func()) func() bool {
	t := &fakeTimer{d: d, f: f}
	c.mu.Lock()
	c.timers = append(c.timers, t)
	c.mu.Unlock()
	return func() bool { c.mu.Lock(); defer c.mu.Unlock(); t.stopped = true; return true }
}

// fire runs every timer that was not stopped, as if its deadline passed.
func (c *fakeClock) fire() {
	c.mu.Lock()
	timers := append([]*fakeTimer(nil), c.timers...)
	c.mu.Unlock()
	for _, t := range timers {
		c.mu.Lock()
		stopped := t.stopped
		c.mu.Unlock()
		if !stopped {
			t.f()
		}
	}
}

func newPolicy(mode Summaries) (*Policy, *recorder, *fakeClock) {
	rec, clock := &recorder{}, &fakeClock{}
	return NewPolicy(rec, PolicyConfig{Summaries: mode, After: clock.after}), rec, clock
}

func TestQuestionNobodyWatchesGoesNowAsUrgent(t *testing.T) {
	p, rec, clock := newPolicy("")
	p.Handle(Signal{Kind: KindAsk, SessionID: "s1", Title: "T", RequestID: "a1"})
	got := rec.all()
	if len(got) != 1 || got[0].Level != LevelUrgent || got[0].SessionID != "s1" || got[0].Kind != KindAsk {
		t.Fatalf("sent = %+v, want one urgent ask for s1", got)
	}
	if len(clock.timers) != 0 {
		t.Fatal("an unwatched question must not wait")
	}
}

func TestPermissionNobodyWatchesGoesNowAsUrgent(t *testing.T) {
	p, rec, _ := newPolicy("")
	p.Handle(Signal{Kind: KindPermission, SessionID: "s1", RequestID: "p1"})
	got := rec.all()
	if len(got) != 1 || got[0].Level != LevelUrgent || got[0].Kind != KindPermission {
		t.Fatalf("sent = %+v, want one urgent permission", got)
	}
}

func TestWatchedQuestionWaitsSixtySecondsAndSendsIfStillOpen(t *testing.T) {
	p, rec, clock := newPolicy("")
	p.Handle(Signal{Kind: KindAsk, SessionID: "s1", RequestID: "a1", Watched: true})
	if len(rec.all()) != 0 {
		t.Fatal("a question somebody is looking at must not go at once")
	}
	if len(clock.timers) != 1 || clock.timers[0].d != 60*time.Second {
		t.Fatalf("timers = %+v, want one of 60s", clock.timers)
	}
	clock.fire() // the viewer may have left long ago: the deadline is what counts
	if got := rec.all(); len(got) != 1 || got[0].Level != LevelUrgent {
		t.Fatalf("after the grace period sent = %+v, want the urgent notification", got)
	}
}

func TestWatchedQuestionAnsweredInTimeNeverSends(t *testing.T) {
	p, rec, clock := newPolicy("")
	p.Handle(Signal{Kind: KindAsk, SessionID: "s1", RequestID: "a1", Watched: true})
	p.Resolved(KindAsk, "s1", "a1")
	clock.fire()
	if got := rec.all(); len(got) != 0 {
		t.Fatalf("answered question sent %+v", got)
	}
}

func TestWatchedQuestionNoLongerPendingNeverSends(t *testing.T) {
	p, rec, clock := newPolicy("")
	p.Handle(Signal{Kind: KindAsk, SessionID: "s1", RequestID: "a1", Watched: true, StillPending: func() bool { return false }})
	clock.fire()
	if got := rec.all(); len(got) != 0 {
		t.Fatalf("a request that is gone sent %+v", got)
	}
}

func TestResolvingOneRequestKeepsTheOther(t *testing.T) {
	p, rec, clock := newPolicy("")
	p.Handle(Signal{Kind: KindAsk, SessionID: "s1", RequestID: "a1", Watched: true})
	p.Handle(Signal{Kind: KindPermission, SessionID: "s1", RequestID: "p1", Watched: true})
	p.Resolved(KindAsk, "s1", "a1")
	clock.fire()
	if got := rec.all(); len(got) != 1 || got[0].Kind != KindPermission {
		t.Fatalf("sent = %+v, want only the permission", got)
	}
}

func TestCancelSessionDropsItsGraceTimers(t *testing.T) {
	p, rec, clock := newPolicy("")
	p.Handle(Signal{Kind: KindAsk, SessionID: "s1", RequestID: "a1", Watched: true})
	p.Handle(Signal{Kind: KindAsk, SessionID: "s2", RequestID: "a1", Watched: true})
	p.CancelSession("s1")
	clock.fire()
	if got := rec.all(); len(got) != 1 || got[0].SessionID != "s2" {
		t.Fatalf("sent = %+v, want only s2", got)
	}
}

func TestFinishedRunOfTheUsersSessionIsAudibleUnlessWatched(t *testing.T) {
	p, rec, _ := newPolicy("")
	p.Handle(Signal{Kind: KindDone, SessionID: "s1"})
	p.Handle(Signal{Kind: KindDone, SessionID: "s2", Watched: true})
	p.Handle(Signal{Kind: KindFailed, SessionID: "s3"})
	p.Handle(Signal{Kind: KindFailed, SessionID: "s4", Watched: true})
	got := rec.all()
	if len(got) != 2 || got[0].SessionID != "s1" || got[0].Level != LevelActive || got[1].SessionID != "s3" || got[1].Level != LevelActive {
		t.Fatalf("sent = %+v, want s1 done and s3 failed, both active", got)
	}
}

func TestDigestsArePassiveAndOnePerProject(t *testing.T) {
	p, rec, _ := newPolicy("")
	p.Handle(Signal{Kind: KindDigest, SessionID: "o1", Project: "/a"})
	p.Handle(Signal{Kind: KindDigest, SessionID: "o2", Project: "/a"}) // another session of the same project
	p.Handle(Signal{Kind: KindDigest, SessionID: "o3", Project: "/b"})
	got := rec.all()
	if len(got) != 3 {
		t.Fatalf("sent %d, want 3", len(got))
	}
	for _, n := range got {
		if n.Level != LevelPassive {
			t.Fatalf("digest level = %q, want passive", n.Level)
		}
	}
	if got[0].Tag != got[1].Tag || got[0].Tag == got[2].Tag {
		t.Fatalf("tags %q %q %q: the same project must share one (it replaces), another project must not", got[0].Tag, got[1].Tag, got[2].Tag)
	}
}

func TestDigestOfAWatchedOwnerIsDropped(t *testing.T) {
	p, rec, _ := newPolicy("")
	p.Handle(Signal{Kind: KindDigest, SessionID: "o1", Project: "/a", Watched: true})
	if got := rec.all(); len(got) != 0 {
		t.Fatalf("sent %+v", got)
	}
}

func TestEventsArePassiveAndOnePerSource(t *testing.T) {
	p, rec, _ := newPolicy("")
	p.Handle(Signal{Kind: KindEvent, Source: "grokbot", Headline: "Event from grokbot", SessionID: "s1"})
	p.Handle(Signal{Kind: KindEvent, Source: "grokbot", Headline: "Event from grokbot", SessionID: "s1"})
	p.Handle(Signal{Kind: KindEvent, Source: "ci", Headline: "Event from ci waiting", Inbox: true})
	got := rec.all()
	if len(got) != 3 || got[0].Level != LevelPassive || got[0].Tag != got[1].Tag || got[0].Tag == got[2].Tag {
		t.Fatalf("sent = %+v", got)
	}
	if got[0].Title != "Event from grokbot" || !got[2].Inbox {
		t.Fatalf("headline or inbox lost: %+v", got)
	}
}

func TestSummariesModeIsTheOneKnob(t *testing.T) {
	for _, tc := range []struct {
		mode Summaries
		want Level
		n    int
	}{{SummariesPassive, LevelPassive, 2}, {SummariesActive, LevelActive, 2}, {SummariesOff, "", 0}} {
		p, rec, _ := newPolicy(tc.mode)
		p.Handle(Signal{Kind: KindDigest, SessionID: "o1", Project: "/a"})
		p.Handle(Signal{Kind: KindEvent, Source: "x"})
		got := rec.all()
		if len(got) != tc.n || (tc.n > 0 && (got[0].Level != tc.want || got[1].Level != tc.want)) {
			t.Fatalf("mode %q: sent = %+v", tc.mode, got)
		}
	}
	if ParseSummaries("bogus") != SummariesPassive || ParseSummaries("off") != SummariesOff {
		t.Fatal("ParseSummaries: unknown must fall back to passive")
	}
}

func TestQuestionsAreNeverSummaries(t *testing.T) {
	p, rec, _ := newPolicy(SummariesOff)
	p.Handle(Signal{Kind: KindAsk, SessionID: "s1", RequestID: "a1"})
	if got := rec.all(); len(got) != 1 || got[0].Level != LevelUrgent {
		t.Fatalf("sent = %+v: the summaries knob must not touch questions", got)
	}
}

func TestAQuestionAndARunShareNoTag(t *testing.T) {
	p, rec, _ := newPolicy("")
	p.Handle(Signal{Kind: KindAsk, SessionID: "s1", RequestID: "a1"})
	p.Handle(Signal{Kind: KindDone, SessionID: "s1"})
	got := rec.all()
	if got[0].Tag == got[1].Tag {
		t.Fatalf("a finished run would replace the open question on the device (tag %q)", got[0].Tag)
	}
}
