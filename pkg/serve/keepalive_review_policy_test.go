package serve

import (
	"context"
	"encoding/json"
	"github.com/e-aleixandre/moa/pkg/tasks"
	"math"
	"testing"
	"time"
)

func TestReviewPingCostPersistsAndResumeDoesNotPing(t *testing.T) {
	h := newKAServe(t, allKA())
	before := sessionCostOf(t, h.sess)
	msgsBefore, _ := json.Marshal(h.sess.runtime.Context().Agent.Messages())
	h.advance(56 * time.Minute)
	pollUntil(t, 3*time.Second, "ping saved to metadata", func() bool {
		v, ok := diskSessionCost(t, h.mgr, h.sess.ID)
		f, isNum := v.(float64)
		return ok && isNum && f > before
	})
	after := sessionCostOf(t, h.sess)
	if got := h.sess.info().KeepAlive; got == nil || math.Abs(after-before-got.CostUSD) > 1e-9 {
		t.Fatalf("cost not isolated: before=%v after=%v keepalive=%+v", before, after, got)
	}
	msgsAfter, _ := json.Marshal(h.sess.runtime.Context().Agent.Messages())
	if string(msgsBefore) != string(msgsAfter) {
		t.Fatal("ping modified history")
	}
	id := h.sess.ID
	closeWhenQuiet(t, h.mgr, id)
	h.advance(3 * time.Hour)
	if n := h.prov.pingCount(); n != 1 {
		t.Fatalf("pings after close=%d want 1", n)
	}
	resumed, err := h.mgr.ResumeSession(id)
	if err != nil {
		t.Fatal(err)
	}
	if got := sessionCostOf(t, resumed); math.Abs(got-after) > 1e-9 {
		t.Fatalf("resume cost=%v want %v", got, after)
	}
	h.advance(3 * time.Hour)
	if n := h.prov.pingCount(); n != 1 {
		t.Fatalf("pings after resume without real send=%d want 1", n)
	}
	t.Logf("ping increment=%0.8f durable; history unchanged; close/resume adds no pings", after-before)
}

func TestReviewKnownScheduledSessionMustNotPing(t *testing.T) {
	h := newKAServe(t, allKA())
	mkTemplate(t, h.mgr.tasks, "known future wake", onceAt(time.Now().Add(15*time.Hour), toSession(h.sess.ID), tasks.Delivery{}))
	records, err := h.mgr.sessionScheduled(context.Background(), h.sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("setup: known schedules=%d want 1", len(records))
	}
	h.advance(56 * time.Minute)
	if got := h.prov.pingCount(); got != 0 {
		t.Fatalf("known-scheduled session sent %d ping(s), policy requires none", got)
	}
}
