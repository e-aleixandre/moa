package tasks

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The escalation of an unanswered question to a call, at the store: two
// handles on one database stand for two processes (or the scheduler and an
// answer), a manual clock moves time, and barriers fix the order of the race.

type manualClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *manualClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *manualClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func callRepos(t *testing.T) (a, b *Repo, clock *manualClock, path string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), DatabaseName)
	clock = &manualClock{t: time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)}
	a, b = openRepo(t, path), openRepo(t, path)
	a.SetClock(clock.now)
	b.SetClock(clock.now)
	return a, b, clock, path
}

func escalated(t *testing.T, r *Repo, clock *manualClock, session string, after time.Duration) Question {
	t.Helper()
	q := newQuestion(session, "tc-"+session)
	q.CallDueAt = clock.now().Add(after).UnixMilli()
	got, _, err := r.InsertQuestion(bg, q)
	if err != nil {
		t.Fatal(err)
	}
	if got.CallState != CallPending {
		t.Fatalf("posted with a call: state %q, want pending", got.CallState)
	}
	return got
}

func callOf(t *testing.T, r *Repo, id string) Question {
	t.Helper()
	q, err := r.GetQuestion(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

// scanAndClaim is the scheduler's step: read what is due, claim each. It
// returns the attempts that won.
func scanAndClaim(t *testing.T, r *Repo, clock *manualClock, attempt string) []string {
	t.Helper()
	due, err := r.DueCallEscalations(bg, clock.now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	var won []string
	for _, q := range due {
		ok, err := r.ClaimCallEscalation(bg, q.ID, attempt)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			won = append(won, attempt)
		}
	}
	return won
}

func TestCallEscalationIsNotDueBeforeItsTime(t *testing.T) {
	a, _, clock, _ := callRepos(t)
	q := escalated(t, a, clock, "s1", 15*time.Minute)
	if won := scanAndClaim(t, a, clock, "att-early"); len(won) != 0 {
		t.Fatalf("claimed before due: %v", won)
	}
	if next, _ := a.NextCallDueAt(bg); next != q.CallDueAt {
		t.Fatalf("next due = %d, want %d", next, q.CallDueAt)
	}
	clock.advance(15 * time.Minute)
	if won := scanAndClaim(t, a, clock, "att-1"); len(won) != 1 {
		t.Fatalf("due claim won %d times, want 1", len(won))
	}
	if got := callOf(t, a, q.ID); got.CallState != CallCalling || got.CallAttempt != "att-1" {
		t.Fatalf("after claim: %q %q", got.CallState, got.CallAttempt)
	}
	if next, _ := a.NextCallDueAt(bg); next != 0 {
		t.Fatalf("a claimed escalation is still due at %d", next)
	}
}

// The answer commits between the scan that saw the question due and the
// claim: zero attempts.
func TestCallEscalationAnswerBetweenScanAndClaimMeansNoCall(t *testing.T) {
	a, b, clock, _ := callRepos(t)
	q := escalated(t, a, clock, "s1", 15*time.Minute)
	clock.advance(16 * time.Minute)
	a.hookBeforeCallClaim = func() {
		if _, err := b.AnswerQuestion(bg, "s1", q.ID, []string{"yes"}, human, renderTest); err != nil {
			t.Errorf("answer: %v", err)
		}
	}
	if won := scanAndClaim(t, a, clock, "att-1"); len(won) != 0 {
		t.Fatalf("a call was claimed for an answered question: %v", won)
	}
	if got := callOf(t, b, q.ID); got.CallState != CallCancelled || got.CallAttempt != "" {
		t.Fatalf("answered question's escalation = %q (attempt %q), want cancelled, no attempt", got.CallState, got.CallAttempt)
	}
}

// The claim commits first: the answer still wins the question, cancels the
// escalation in its own transaction and hands back the attempt to withdraw.
func TestCallEscalationAnswerAfterClaimCancelsAndNamesTheAttempt(t *testing.T) {
	for _, close := range []QuestionState{"", QuestionDismissed, QuestionWithdrawn} {
		name := string(close)
		if name == "" {
			name = "answered"
		}
		t.Run(name, func(t *testing.T) {
			a, b, clock, _ := callRepos(t)
			q := escalated(t, a, clock, "s1", 15*time.Minute)
			clock.advance(15 * time.Minute)
			if won := scanAndClaim(t, a, clock, "att-1"); len(won) != 1 {
				t.Fatal("claim lost")
			}
			var ended Question
			if close == "" {
				res, err := b.AnswerQuestion(bg, "s1", q.ID, []string{"yes"}, human, renderTest)
				if err != nil {
					t.Fatal(err)
				}
				ended = res.Question
			} else {
				var err error
				if ended, _, err = b.CloseQuestion(bg, "s1", q.ID, close, ""); err != nil {
					t.Fatal(err)
				}
			}
			if ended.CallAttempt != "att-1" || ended.CallState != CallCancelled {
				t.Fatalf("the closing change returned attempt %q state %q, want att-1 cancelled", ended.CallAttempt, ended.CallState)
			}
			if got := callOf(t, a, q.ID); got.CallState != CallCancelled {
				t.Fatalf("stored escalation %q, want cancelled", got.CallState)
			}
			// The ringing attempt's own end cannot overwrite the cancel.
			if ok, _ := a.FinishCallEscalation(bg, q.ID, "att-1", CallMissed); ok {
				t.Fatal("a cancelled attempt was marked missed")
			}
		})
	}
}

// Two processes scan at once: exactly one claim wins.
func TestCallEscalationTwoClaimersExactlyOneWins(t *testing.T) {
	a, b, clock, _ := callRepos(t)
	escalated(t, a, clock, "s1", 5*time.Minute)
	clock.advance(5 * time.Minute)
	var wins atomic.Int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, r := range []*Repo{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			due, err := r.DueCallEscalations(bg, clock.now().UnixMilli())
			if err != nil {
				t.Error(err)
				return
			}
			for _, q := range due {
				if ok, err := r.ClaimCallEscalation(bg, q.ID, []string{"att-a", "att-b"}[i]); err != nil {
					t.Error(err)
				} else if ok {
					wins.Add(1)
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("claims won = %d, want exactly 1", wins.Load())
	}
}

// The process that claimed dies before the call ends: the next one marks
// the attempt interrupted and never claims it again.
func TestCallEscalationCrashAfterClaimIsInterruptedNotReplayed(t *testing.T) {
	a, b, clock, _ := callRepos(t)
	q := escalated(t, a, clock, "s1", 15*time.Minute)
	clock.advance(15 * time.Minute)
	if won := scanAndClaim(t, a, clock, "att-1"); len(won) != 1 {
		t.Fatal("claim lost")
	}
	_ = a.Close() // the crash: nothing after the claim reached the row

	ids, err := b.InterruptCallEscalations(bg)
	if err != nil || len(ids) != 1 || ids[0] != q.ID {
		t.Fatalf("interrupted %v (%v), want [%s]", ids, err, q.ID)
	}
	clock.advance(time.Hour)
	if won := scanAndClaim(t, b, clock, "att-2"); len(won) != 0 {
		t.Fatal("an interrupted call was claimed again")
	}
	if got := callOf(t, b, q.ID); got.CallState != CallInterrupted || got.State != QuestionOpen {
		t.Fatalf("after restart: call %q question %q, want interrupted and still open", got.CallState, got.State)
	}
	if ok, _ := b.FinishCallEscalation(bg, q.ID, "att-1", CallDone); ok {
		t.Fatal("the dead attempt's end overwrote interrupted")
	}
}

// A missed call is terminal: not due again, not deferrable, not claimable.
func TestCallEscalationMissedIsNeverRearmed(t *testing.T) {
	a, _, clock, _ := callRepos(t)
	q := escalated(t, a, clock, "s1", 15*time.Minute)
	clock.advance(15 * time.Minute)
	scanAndClaim(t, a, clock, "att-1")
	if ok, err := a.FinishCallEscalation(bg, q.ID, "att-1", CallMissed); !ok || err != nil {
		t.Fatalf("finish missed: %v %v", ok, err)
	}
	if ok, _ := a.DeferCallEscalation(bg, q.ID, "att-1", clock.now().Add(time.Hour).UnixMilli()); ok {
		t.Fatal("a missed call was deferred")
	}
	clock.advance(24 * time.Hour)
	if won := scanAndClaim(t, a, clock, "att-2"); len(won) != 0 {
		t.Fatal("a missed call was claimed again")
	}
	if got := callOf(t, a, q.ID); got.CallState != CallMissed || got.State != QuestionOpen {
		t.Fatalf("call %q question %q, want missed and still open", got.CallState, got.State)
	}
}

// A claimed attempt that could not ring (outside hours) goes back to pending
// at its new time and is claimed again then, once.
func TestCallEscalationDeferredAttemptIsDueAtItsNewTime(t *testing.T) {
	a, _, clock, _ := callRepos(t)
	q := escalated(t, a, clock, "s1", 15*time.Minute)
	clock.advance(15 * time.Minute)
	scanAndClaim(t, a, clock, "att-1")
	at := clock.now().Add(9 * time.Hour).UnixMilli()
	if ok, err := a.DeferCallEscalation(bg, q.ID, "att-1", at); !ok || err != nil {
		t.Fatalf("defer: %v %v", ok, err)
	}
	if got := callOf(t, a, q.ID); got.CallState != CallPending || got.CallDueAt != at {
		t.Fatalf("deferred: %q at %d", got.CallState, got.CallDueAt)
	}
	clock.advance(8 * time.Hour)
	if won := scanAndClaim(t, a, clock, "att-2"); len(won) != 0 {
		t.Fatal("claimed before the deferred time")
	}
	clock.advance(time.Hour)
	if won := scanAndClaim(t, a, clock, "att-3"); len(won) != 1 {
		t.Fatal("not claimed at the deferred time")
	}
}

// Stop cancels the session's live escalations, names the ringing attempts
// and leaves the questions open.
func TestCallEscalationStopCancelsAndKeepsTheQuestionOpen(t *testing.T) {
	a, b, clock, _ := callRepos(t)
	ringing := escalated(t, a, clock, "s1", 5*time.Minute)
	pending := escalated(t, a, clock, "s2", 30*time.Minute)
	other := escalated(t, a, clock, "s3", 30*time.Minute)
	clock.advance(5 * time.Minute)
	scanAndClaim(t, a, clock, "att-1")

	for _, sess := range []string{"s1", "s2"} {
		attempts, _, err := b.CancelSessionCalls(bg, sess)
		if err != nil {
			t.Fatal(err)
		}
		if want := map[string]int{"s1": 1, "s2": 0}[sess]; len(attempts) != want {
			t.Fatalf("%s: attempts %v, want %d", sess, attempts, want)
		}
	}
	for _, id := range []string{ringing.ID, pending.ID} {
		if got := callOf(t, a, id); got.CallState != CallCancelled || got.State != QuestionOpen {
			t.Fatalf("%s: call %q question %q, want cancelled and open", id, got.CallState, got.State)
		}
	}
	if got := callOf(t, a, other.ID); got.CallState != CallPending {
		t.Fatalf("another session's escalation became %q", got.CallState)
	}
	clock.advance(time.Hour)
	if won := scanAndClaim(t, a, clock, "att-2"); len(won) != 1 {
		t.Fatalf("after Stop, claims %d, want only the other session's", len(won))
	}
}

// A question posted without a call has none, and a closed one is never due.
func TestQuestionWithoutCallIsNeverDue(t *testing.T) {
	a, _, clock, _ := callRepos(t)
	q, _, err := a.InsertQuestion(bg, newQuestion("s1", "tc1"))
	if err != nil || q.CallState != CallNone {
		t.Fatalf("plain question call state %q (%v)", q.CallState, err)
	}
	clock.advance(48 * time.Hour)
	if due, _ := a.DueCallEscalations(bg, clock.now().UnixMilli()); len(due) != 0 {
		t.Fatalf("due: %d", len(due))
	}
}
