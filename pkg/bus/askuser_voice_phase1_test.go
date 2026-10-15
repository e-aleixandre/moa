package bus

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/askuser"
	"github.com/e-aleixandre/moa/pkg/core"
)

func newAskUserPhase1Bus(t *testing.T) (*ApprovalManager, EventBus) {
	t.Helper()
	am, b := newTestApprovalManager(t)
	sctx := &SessionContext{Bus: b, State: am.state, Approvals: am, SessionID: "test"}
	registerPermissionHandlers(sctx)
	registerPermissionQueryHandlers(sctx)
	t.Cleanup(am.Stop)
	return am, b
}

func TestResolveAskUser_RejectsVoiceResponderWhenRequiresTap(t *testing.T) {
	am, b := newAskUserPhase1Bus(t)
	requested := make(chan AskUserRequested, 1)
	resolved := make(chan AskUserResolved, 4)
	b.Subscribe(func(e AskUserRequested) { requested <- e })
	b.Subscribe(func(e AskUserResolved) { resolved <- e })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bridge := askuser.NewBridge()
	am.StartAskBridge(ctx, bridge)

	// The flag belongs to this entire request, not to an individual question.
	var params map[string]any
	if err := json.Unmarshal([]byte(`{"requires_tap":true,"questions":[{"question":"Deploy the release?"},{"question":"Which environment?","options":["staging","production"]}]}`), &params); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		result core.Result
		err    error
	}
	completed := make(chan outcome, 1)
	go func() {
		result, err := askuser.NewTool(bridge).Execute(ctx, params, nil)
		completed <- outcome{result: result, err: err}
	}()
	var ask AskUserRequested
	select {
	case ask = <-requested:
	case out := <-completed:
		t.Fatalf("ask_user completed before asking: result=%+v error=%v", out.result, out.err)
	case <-ctx.Done():
		t.Fatal("ask_user never reached the real bus bridge")
	}
	if len(ask.Questions) != 2 {
		t.Fatalf("request questions = %+v, want both questions", ask.Questions)
	}
	pending, err := QueryTyped[GetPendingApproval, PendingApprovalInfo](b, GetPendingApproval{})
	if err != nil || pending.Ask == nil || pending.Ask.ID != ask.ID {
		t.Fatalf("pending request = %+v, error=%v", pending, err)
	}
	data, err := json.Marshal(pending.Ask)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["requires_tap"] != true {
		t.Errorf("pending request lost root requires_tap=true: %s", data)
	}

	var voice askuser.Responder
	if err := json.Unmarshal([]byte(`{"kind":"voice","via":"voice","call_id":"call-rejected","quote":"Yes, deploy to production."}`), &voice); err != nil {
		t.Fatal(err)
	}
	if err := b.Execute(ResolveAskUser{AskID: ask.ID, Answers: []string{"Yes", "production"}, Responder: voice}); err == nil {
		t.Error("voice responder resolved a requires_tap request; want rejection")
	}
	if !am.AskPending(ask.ID) {
		t.Error("rejecting voice must leave this exact requires_tap request pending")
	}
	pending, err = QueryTyped[GetPendingApproval, PendingApprovalInfo](b, GetPendingApproval{})
	if err != nil || pending.Ask == nil || pending.Ask.ID != ask.ID {
		t.Errorf("request disappeared from pending projection after voice rejection: %+v, error=%v", pending, err)
	} else {
		data, err := json.Marshal(pending.Ask)
		if err != nil {
			t.Fatal(err)
		}
		var preserved map[string]any
		if err := json.Unmarshal(data, &preserved); err != nil {
			t.Fatal(err)
		}
		if preserved["requires_tap"] != true {
			t.Errorf("voice rejection cleared requires_tap: %s", data)
		}
	}
	if state := am.state.Current(); state != StatePermission {
		t.Errorf("state after voice rejection = %q, want the still-blocked permission state", state)
	}
	b.Drain(time.Second)
	select {
	case e := <-resolved:
		t.Errorf("rejected voice published AskUserResolved: %+v", e)
	default:
	}

	user := askuser.Responder{Kind: askuser.ResponderUser, Via: "device:tap-fixture"}
	answers := []string{"Yes, by tap", "staging"}
	if err := b.Execute(ResolveAskUser{AskID: ask.ID, Answers: answers, Responder: user}); err != nil {
		t.Errorf("user/tap could not resolve the request after rejected voice: %v", err)
	}
	select {
	case out := <-completed:
		if out.err != nil || out.result.IsError {
			t.Fatalf("tool completion: result=%+v error=%v", out.result, out.err)
		}
		if got, ok := askuser.ResponderFromCustom(out.result.Custom[askuser.CustomKey]); !ok || !reflect.DeepEqual(got, user) {
			t.Errorf("tool responder = %+v (known=%v), want the user and authenticated Via %+v", got, ok, user)
		}
		const want = "Q: Deploy the release?\nA: Yes, by tap\nQ: Which environment?\nA: staging"
		if got := out.result.Content[0].Text; got != want {
			t.Errorf("tool answer = %q, want user/tap answers %q", got, want)
		}
	case <-ctx.Done():
		t.Fatal("user/tap did not unblock ask_user")
	}
	b.Drain(time.Second)
	select {
	case e := <-resolved:
		if e.ID != ask.ID || e.SessionID != "test" {
			t.Errorf("user/tap resolution event = %+v", e)
		}
	default:
		t.Error("user/tap did not publish AskUserResolved")
	}
	if len(resolved) != 0 || am.AskPending(ask.ID) || am.state.Current() != StateRunning {
		t.Errorf("user/tap did not finish exactly once: extra_events=%d pending=%v state=%q", len(resolved), am.AskPending(ask.ID), am.state.Current())
	}
}

func TestResolveAskUser_SingleWinner(t *testing.T) {
	for _, tt := range []struct {
		name    string
		pending string
		voice   bool
		mixed   bool
	}{
		{name: "legacy_user", pending: `{"id":"ask-single-winner","Questions":[{"question":"Which day?"}]}`},
		{name: "ordinary_voice", pending: `{"id":"ask-single-winner","requires_tap":false,"Questions":[{"question":"Which day?"}]}`, voice: true},
		{name: "voice_and_tap_race", pending: `{"id":"ask-single-winner","requires_tap":false,"Questions":[{"question":"Which day?"}]}`, voice: true, mixed: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			am, b := newAskUserPhase1Bus(t)
			response := make(chan askuser.Reply, 1)
			var pending PendingAsk
			if err := json.Unmarshal([]byte(tt.pending), &pending); err != nil {
				t.Fatal(err)
			}
			pending.response = response
			am.mu.Lock()
			am.asks[pending.ID] = &pending
			am.mu.Unlock()
			am.state.ForceState(StatePermission)
			const contenders = 16
			resolved := make(chan AskUserResolved, contenders)
			b.Subscribe(func(e AskUserResolved) { resolved <- e })
			commands := make([]ResolveAskUser, contenders)
			for i := range commands {
				responder := askuser.Responder{Kind: askuser.ResponderUser, Via: fmt.Sprintf("device:contender-%d", i)}
				if tt.voice && (!tt.mixed || i%2 == 0) {
					if err := json.Unmarshal([]byte(fmt.Sprintf(`{"kind":"voice","via":"voice","call_id":"call-%d","quote":"Tomorrow."}`, i)), &responder); err != nil {
						t.Fatal(err)
					}
				}
				commands[i] = ResolveAskUser{AskID: pending.ID, Answers: []string{fmt.Sprintf("answer-%d", i)}, Responder: responder}
			}
			start := make(chan struct{})
			errs := make([]error, contenders)
			var wg sync.WaitGroup
			for i := range commands {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					errs[i] = b.Execute(commands[i])
				}()
			}
			close(start)
			wg.Wait()
			winners, winner := 0, -1
			for i, err := range errs {
				if err == nil {
					winners++
					winner = i
				}
			}
			if winners != 1 {
				t.Fatalf("successful resolvers = %d, want exactly one; errors=%v", winners, errs)
			}
			select {
			case reply := <-response:
				if !reflect.DeepEqual(reply.Answers, commands[winner].Answers) || !reflect.DeepEqual(reply.Responder, commands[winner].Responder) {
					t.Errorf("delivered reply = %+v, want winner %+v", reply, commands[winner])
				}
			default:
				t.Fatal("winning resolver did not deliver a reply")
			}
			b.Drain(time.Second)
			if len(resolved) != 1 {
				t.Fatalf("AskUserResolved events = %d, want exactly one", len(resolved))
			}
			if e := <-resolved; e.ID != pending.ID || e.SessionID != "test" {
				t.Errorf("resolved event = %+v", e)
			}
			if am.AskPending(pending.ID) || am.state.Current() != StateRunning {
				t.Errorf("winner left pending=%v state=%q", am.AskPending(pending.ID), am.state.Current())
			}
			if err := b.Execute(commands[winner]); err == nil {
				t.Error("resolved request accepted a replay")
			}
		})
	}
}
