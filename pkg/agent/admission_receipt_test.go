package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// recordingSave is an acknowledged save that remembers when it ran relative to
// the provider and can be made to fail.
type recordingSave struct {
	mu     sync.Mutex
	events *[]string
	fail   error
}

func (s *recordingSave) save(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	*s.events = append(*s.events, "save")
	return s.fail
}

func durableCustom() map[string]any {
	return map[string]any{"source": "question_answer", core.CustomDurableAdmission: true}
}

func receiptAgent(events *[]string, saveErr error, handlers ...func(core.Request) (<-chan core.AssistantEvent, error)) (*Agent, *MockProvider) {
	wrapped := make([]func(core.Request) (<-chan core.AssistantEvent, error), len(handlers))
	for i, h := range handlers {
		wrapped[i] = func(req core.Request) (<-chan core.AssistantEvent, error) {
			*events = append(*events, "provider")
			return h(req)
		}
	}
	provider := NewMockProvider(wrapped...)
	ag := newTestAgent(provider)
	ag.SetProviderWaitSave((&recordingSave{events: events, fail: saveErr}).save)
	return ag, provider
}

// An admitted answer is on disk before the request that carries it, and a save
// that fails means the model never reads it.
func TestDurableAdmissionPromptIsSavedBeforeInference(t *testing.T) {
	var events []string
	ag, provider := receiptAgent(&events, nil, simpleTextResponse("ok"))
	if _, err := ag.SendWithCustomAnnounced(context.Background(), "answer", durableCustom()); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0] != "save" || events[1] != "provider" {
		t.Fatalf("order = %v, want the save before the provider call", events)
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.calls)
	}
}

func TestDurableAdmissionSaveFailureNeverInfers(t *testing.T) {
	var events []string
	boom := errors.New("disk full")
	ag, provider := receiptAgent(&events, boom, simpleTextResponse("must not happen"))
	_, err := ag.SendWithCustomAnnounced(context.Background(), "answer", durableCustom())
	if !errors.Is(err, ErrAdmissionNotSaved) || !errors.Is(err, boom) {
		t.Fatalf("err = %v, want ErrAdmissionNotSaved wrapping the save error", err)
	}
	if provider.calls != 0 {
		t.Fatalf("provider calls = %d after a failed save, want 0", provider.calls)
	}
}

// With no acknowledged save wired there is no receipt to give: refuse rather
// than infer on an unsaved answer.
func TestDurableAdmissionWithoutASaveNeverInfers(t *testing.T) {
	provider := NewMockProvider(simpleTextResponse("must not happen"))
	ag := newTestAgent(provider)
	if _, err := ag.SendWithCustomAnnounced(context.Background(), "answer", durableCustom()); !errors.Is(err, ErrAdmissionNotSaved) {
		t.Fatalf("err = %v, want ErrAdmissionNotSaved", err)
	}
	if provider.calls != 0 {
		t.Fatalf("provider calls = %d, want 0", provider.calls)
	}
}

// A steer admitted at a tool boundary gets the same receipt: the second request
// of the run is not made when its save fails, and the first already was.
func TestDurableAdmissionSteerIsSavedBeforeTheNextRequest(t *testing.T) {
	for _, tc := range []struct {
		name    string
		saveErr error
		want    []string
	}{
		{"saved", nil, []string{"provider", "save", "provider"}},
		{"save fails", errors.New("disk full"), []string{"provider", "save"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			blockTool := core.Tool{
				Name:       "block",
				Parameters: json.RawMessage(`{"type":"object"}`),
				Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
					close(started)
					<-release
					return core.TextResult("done"), nil
				},
			}
			var events []string
			provider := NewMockProvider(
				func(req core.Request) (<-chan core.AssistantEvent, error) {
					events = append(events, "provider")
					return toolCallResponse("tc-1", "block", nil)(req)
				},
				func(req core.Request) (<-chan core.AssistantEvent, error) {
					events = append(events, "provider")
					return simpleTextResponse("after")(req)
				},
			)
			ag := newTestAgent(provider, blockTool)
			ag.SetProviderWaitSave((&recordingSave{events: &events, fail: tc.saveErr}).save)

			done := make(chan error, 1)
			go func() { _, err := ag.Run(context.Background(), "go"); done <- err }()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("tool never started")
			}
			ag.Steer(core.SteerItem{ID: "s1", Text: "answer", Custom: durableCustom()})
			close(release)
			var err error
			select {
			case err = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("run never finished")
			}
			if tc.saveErr != nil && !errors.Is(err, ErrAdmissionNotSaved) {
				t.Fatalf("err = %v, want ErrAdmissionNotSaved", err)
			}
			if tc.saveErr == nil && err != nil {
				t.Fatal(err)
			}
			if len(events) != len(tc.want) {
				t.Fatalf("events = %v, want %v", events, tc.want)
			}
			for i := range tc.want {
				if events[i] != tc.want[i] {
					t.Fatalf("events = %v, want %v", events, tc.want)
				}
			}
		})
	}
}

// Ordinary messages never ask for a save: the receipt is only for what says so.
func TestOrdinaryMessageDoesNotForceASave(t *testing.T) {
	var events []string
	ag, _ := receiptAgent(&events, nil, simpleTextResponse("ok"))
	if _, err := ag.SendWithCustomAnnounced(context.Background(), "hello", map[string]any{"source": "event"}); err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e == "save" {
			t.Fatalf("an ordinary message forced a save: %v", events)
		}
	}
}
