package permission

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// The request a consumer retains must not share maps with the working args the
// gate later writes the approved feedback into.
func TestAskRequestArgsAreASnapshot(t *testing.T) {
	g := New(ModeAsk, Config{})
	args := map[string]any{"command": "make", "nested": map[string]any{"k": []any{"v"}}}
	want := map[string]any{"command": "make", "nested": map[string]any{"k": []any{"v"}}}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	decided := make(chan struct{})
	go func() {
		defer close(decided)
		if d := g.Check(ctx, "bash", args); d != nil {
			t.Errorf("approved check blocked: %+v", d)
		}
	}()

	var req Request
	select {
	case req = <-g.Requests():
	case <-time.After(5 * time.Second):
		t.Fatal("no permission request")
	}
	req.Response <- Response{Approved: true, Feedback: "go ahead"}

	// Unordered with the feedback insertion on purpose: no signal says it is done.
	marshaled := make(chan error, 1)
	go func() {
		_, err := json.Marshal(req.Args)
		marshaled <- err
	}()
	select {
	case err := <-marshaled:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("marshal timed out")
	}
	select {
	case <-decided:
	case <-time.After(5 * time.Second):
		t.Fatal("Check did not return")
	}

	if !reflect.DeepEqual(req.Args, want) {
		t.Fatalf("retained request args changed: %#v", req.Args)
	}
	if got := PopApprovedFeedback(args); got != "go ahead" {
		t.Fatalf("working args lost the feedback: %q", got)
	}
	req.Args["nested"].(map[string]any)["k"].([]any)[0] = "changed"
	if args["nested"].(map[string]any)["k"].([]any)[0] != "v" {
		t.Fatal("request args share nested state with the working args")
	}
}
