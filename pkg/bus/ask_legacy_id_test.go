package bus

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/askuser"
)

// With the asynchronous pilot off a blocking ask keeps the ID it always had:
// the session's own counter, not a random one.
func TestBlockingAskKeepsItsSequentialID(t *testing.T) {
	am, b := newAskUserPhase1Bus(t)
	requested := make(chan AskUserRequested, 2)
	b.Subscribe(func(e AskUserRequested) { requested <- e })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bridge := askuser.NewBridge()
	am.StartAskBridge(ctx, bridge)
	params := map[string]any{"questions": []any{map[string]any{"question": "Which?"}}}
	for _, want := range []string{"ask_1", "ask_2"} {
		go func() { _, _ = askuser.NewTool(bridge).Execute(ctx, params, nil) }()
		select {
		case ask := <-requested:
			if !regexp.MustCompile(`^ask_\d+$`).MatchString(ask.ID) || ask.ID != want {
				t.Fatalf("blocking ask ID = %q, want %q", ask.ID, want)
			}
			if err := am.ResolveAskUser(ask.ID, []string{"x"}, askuser.Responder{Kind: askuser.ResponderUser}); err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("ask never reached the bus")
		}
	}
}
