package main

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

// Each Send tries the plan first, gets the quota rejection and falls to the
// API key again. The conversation never records or announces it: the status
// line is the only signal.
func TestAPIBackupAddsNothingToTheConversation(t *testing.T) {
	p, _, tr, _ := backupRuntime(t)
	a, _, _ := newBackupAgent(t, p, core.NewRegistry())
	var mu sync.Mutex
	var live []string
	a.Subscribe(func(ev core.AgentEvent) {
		if ev.Type == core.AgentEventUserMessage && ev.Message.Role == "session_event" {
			mu.Lock()
			live = append(live, ev.Message.Role)
			mu.Unlock()
		}
	})
	apiCalls := 0
	tr.serve = func(r *http.Request, _ int) (int, http.Header, string) {
		if r.Header.Get("X-Api-Key") == "" {
			return backupReject("five_hour")
		}
		apiCalls++
		return backupOK("end_turn")
	}
	for _, text := range []string{"one", "two", "three"} {
		if _, err := a.Send(context.Background(), text); err != nil {
			t.Fatal(err)
		}
	}
	if apiCalls != 3 {
		t.Fatalf("API calls = %d, want 3: the fixture did not use the backup", apiCalls)
	}
	for _, m := range a.Messages() {
		if m.Role == "session_event" {
			t.Fatalf("session_event %v stored, want none", m.Custom)
		}
	}
	if len(live) != 0 {
		t.Fatalf("%d live session_event rows, want 0", len(live))
	}
}
