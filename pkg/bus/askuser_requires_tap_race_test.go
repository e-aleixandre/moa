package bus

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/askuser"
)

// Voice and tap race for a requires_tap request: voice never wins, the tap
// resolves exactly once.
func TestResolveAskUser_RequiresTapVoiceVsTapRace(t *testing.T) {
	am, b := newAskUserPhase1Bus(t)
	response := make(chan askuser.Reply, 1)
	pending := PendingAsk{ID: "ask-rt-race", RequiresTap: true, Questions: []AskQuestion{{Text: "Deploy?"}}, response: response}
	am.mu.Lock()
	am.asks[pending.ID] = &pending
	am.mu.Unlock()
	am.state.ForceState(StatePermission)
	resolved := make(chan AskUserResolved, 32)
	b.Subscribe(func(e AskUserResolved) { resolved <- e })

	const n = 16
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		r := askuser.Responder{Kind: askuser.ResponderVoice, Via: "voice", CallID: fmt.Sprint(i)}
		if i%2 == 1 {
			r = askuser.Responder{Kind: askuser.ResponderUser, Via: fmt.Sprintf("device:%d", i)}
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = b.Execute(ResolveAskUser{AskID: pending.ID, Answers: []string{fmt.Sprint(i)}, Responder: r})
		}()
	}
	close(start)
	wg.Wait()
	winners := 0
	for i, err := range errs {
		if err == nil {
			winners++
			if i%2 == 0 {
				t.Errorf("voice contender %d resolved a requires_tap request", i)
			}
		}
	}
	// Tap replays after the winner are silent no-ops (resolved requests are gone),
	// so only the first consumed the request; verify via delivered reply and events.
	reply := <-response
	if reply.Responder.Kind != askuser.ResponderUser {
		t.Errorf("delivered responder = %+v, want user", reply.Responder)
	}
	b.Drain(time.Second)
	if len(resolved) != 1 {
		t.Errorf("AskUserResolved events = %d, want 1", len(resolved))
	}
}
