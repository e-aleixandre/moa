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
	return NewPolicy(rec, PolicyConfig{Summaries: mode, After: clock.after, Deliver: func(f func()) { f() }}), rec, clock
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
	p, rec, clock := newPolicy("")
	p.Handle(Signal{Kind: KindDigest, SessionID: "o1", Project: "/a", Title: "first"})
	p.Handle(Signal{Kind: KindDigest, SessionID: "o2", Project: "/a", Title: "last"}) // another session of the same project
	p.Handle(Signal{Kind: KindDigest, SessionID: "o3", Project: "/b", Title: "other"})
	if len(rec.all()) != 0 {
		t.Fatalf("digests went before their window ended: %+v", rec.all())
	}
	clock.fire()
	got := rec.all()
	if len(got) != 2 {
		t.Fatalf("sent %d, want 2: one per project", len(got))
	}
	for _, n := range got {
		if n.Level != LevelPassive {
			t.Fatalf("digest level = %q, want passive", n.Level)
		}
	}
	if got[0].Body != "last" || got[0].Tag == got[1].Tag {
		t.Fatalf("sent %+v: the project must send its latest digest, under its own tag", got)
	}
}

func TestSummariesWaitForTheWindowAndEachNewOneRestartsIt(t *testing.T) {
	p, rec, clock := newPolicy("")
	p.Handle(Signal{Kind: KindDigest, SessionID: "o1", Project: "/a"})
	p.Handle(Signal{Kind: KindDigest, SessionID: "o1", Project: "/a"})
	if len(clock.timers) != 2 || clock.timers[0].d != DefaultWindow || !clock.timers[0].stopped || clock.timers[1].stopped {
		t.Fatalf("timers = %+v, want the first stopped by the second, both %s", clock.timers, DefaultWindow)
	}
	clock.fire()
	if len(rec.all()) != 1 {
		t.Fatalf("sent %d, want exactly the last digest", len(rec.all()))
	}
}

func TestSummaryTimerThatLostTheRaceSendsNothing(t *testing.T) {
	p, rec, clock := newPolicy("")
	p.Handle(Signal{Kind: KindDigest, SessionID: "o1", Project: "/a"})
	stale := clock.timers[0].f
	p.Handle(Signal{Kind: KindDigest, SessionID: "o1", Project: "/a"})
	stale() // already firing when it was superseded
	if len(rec.all()) != 0 {
		t.Fatalf("a superseded timer sent %+v", rec.all())
	}
}

func TestDailyLimitCapsQuietAndAudibleButNeverQuestions(t *testing.T) {
	rec, clock := &recorder{}, &fakeClock{}
	now := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	p := NewPolicy(rec, PolicyConfig{After: clock.after, Deliver: func(f func()) { f() }, DailyLimit: 3, Now: func() time.Time { return now }})
	for i := 0; i < 5; i++ {
		p.Handle(Signal{Kind: KindDone, SessionID: "s"})
	}
	p.Handle(Signal{Kind: KindAsk, SessionID: "s", RequestID: "a1"})
	p.Handle(Signal{Kind: KindPermission, SessionID: "s", RequestID: "p1"})
	got := rec.all()
	if len(got) != 5 || got[3].Kind != KindAsk || got[4].Kind != KindPermission {
		t.Fatalf("sent %d: %+v, want 3 dones then the ask and the permission", len(got), got)
	}
	p.Handle(Signal{Kind: KindDigest, SessionID: "o", Project: "/a"})
	clock.fire()
	if len(rec.all()) != 5 {
		t.Fatalf("a summary went over the daily limit: %+v", rec.all())
	}
	now = now.Add(24 * time.Hour)
	p.Handle(Signal{Kind: KindDone, SessionID: "s"})
	if len(rec.all()) != 6 {
		t.Fatal("the limit must reset the next day")
	}
}

func TestImmediateRequestThatIsAlreadyGoneSendsNothing(t *testing.T) {
	p, rec, _ := newPolicy("")
	p.Handle(Signal{Kind: KindPermission, SessionID: "s1", RequestID: "p1", StillPending: func() bool { return false }})
	if got := rec.all(); len(got) != 0 {
		t.Fatalf("sent %+v for a request nobody can answer any more", got)
	}
}

func TestClosedPolicyIsInert(t *testing.T) {
	p, rec, clock := newPolicy("")
	p.Handle(Signal{Kind: KindDigest, SessionID: "o", Project: "/a"})
	p.Handle(Signal{Kind: KindAsk, SessionID: "s", RequestID: "a", Watched: true})
	p.Close()
	clock.fire()
	p.Handle(Signal{Kind: KindDone, SessionID: "s"})
	p.Handle(Signal{Kind: KindDigest, SessionID: "o", Project: "/a"})
	if got := rec.all(); len(got) != 0 {
		t.Fatalf("a closed policy sent %+v", got)
	}
	if len(clock.timers) != 2 || !clock.timers[0].stopped || !clock.timers[1].stopped {
		t.Fatalf("Close must stop its timers and arm no new ones: %+v", clock.timers)
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
	p, rec, clock := newPolicy("")
	p.Handle(Signal{Kind: KindEvent, Source: "grokbot", Headline: "Event from grokbot", SessionID: "s1"})
	p.Handle(Signal{Kind: KindEvent, Source: "grokbot", Headline: "Event from grokbot", SessionID: "s1"})
	p.Handle(Signal{Kind: KindEvent, Source: "ci", Headline: "Event from ci waiting", Inbox: true})
	clock.fire()
	got := rec.all()
	if len(got) != 2 || got[0].Level != LevelPassive || got[0].Tag == got[1].Tag {
		t.Fatalf("sent = %+v, want one passive notification per source", got)
	}
	if got[0].Title != "Event from grokbot" || !got[1].Inbox {
		t.Fatalf("headline or inbox lost: %+v", got)
	}
}

func TestSummariesModeIsTheOneKnob(t *testing.T) {
	for _, tc := range []struct {
		mode Summaries
		want Level
		n    int
	}{{SummariesPassive, LevelPassive, 2}, {SummariesActive, LevelActive, 2}, {SummariesOff, "", 0}} {
		p, rec, clock := newPolicy(tc.mode)
		p.Handle(Signal{Kind: KindDigest, SessionID: "o1", Project: "/a"})
		p.Handle(Signal{Kind: KindEvent, Source: "x"})
		clock.fire()
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

// A transport that hangs must not stop the policy from deciding, nor a question
// from going out behind it.
type blockingSender struct {
	release chan struct{}
	entered chan struct{}
	rec     recorder
}

func (b *blockingSender) Notify(n Notification) {
	if n.Kind == KindDone {
		close(b.entered)
		<-b.release
	}
	b.rec.Notify(n)
}

func TestSlowTransportDoesNotBlockDecidingOrUrgentDelivery(t *testing.T) {
	snd := &blockingSender{release: make(chan struct{}), entered: make(chan struct{})}
	defer close(snd.release)
	p := NewPolicy(snd, PolicyConfig{})
	returned := make(chan struct{})
	go func() {
		p.Handle(Signal{Kind: KindDone, SessionID: "s1"})
		p.Handle(Signal{Kind: KindAsk, SessionID: "s1", RequestID: "a1"})
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("Handle waited for a delivery that was hanging")
	}
	<-snd.entered
	deadline := time.After(2 * time.Second)
	for {
		if got := snd.rec.all(); len(got) == 1 && got[0].Kind == KindAsk {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("the question did not go out behind a hanging delivery: %+v", snd.rec.all())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestCancelSessionDropsItsWaitingSummaryButNotOthers(t *testing.T) {
	p, rec, clock := newPolicy("")
	p.Handle(Signal{Kind: KindDigest, SessionID: "gone", Project: "/a"})
	p.Handle(Signal{Kind: KindDigest, SessionID: "alive", Project: "/b"})
	p.Handle(Signal{Kind: KindEvent, Source: "x", Inbox: true}) // no session: nothing to cancel
	p.CancelSession("gone")
	clock.fire()
	got := rec.all()
	if len(got) != 2 {
		t.Fatalf("sent %+v, want the other project's digest and the inbox event", got)
	}
	for _, n := range got {
		if n.SessionID == "gone" {
			t.Fatalf("a summary for a deleted session was sent: %+v", n)
		}
	}
}
