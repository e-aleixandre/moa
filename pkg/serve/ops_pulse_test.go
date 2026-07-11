package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/bus"
	"github.com/ealeixandre/moa/pkg/ops"
)

func TestOpsPulseEndpointValidatesSinceAndHonorsServeAccessPolicy(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider(simpleResponseHandler("ok")))
	if _, err := mgr.CreateSession(CreateOpts{Title: "release"}); err != nil {
		t.Fatal(err)
	}
	handler := NewServer(mgr, WithAuthToken("token", false))

	request := func(method, path string, authenticated, csrf bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.Host = "localhost"
		if authenticated {
			req.AddCookie(&http.Cookie{Name: authCookieName, Value: "token"})
		}
		if csrf {
			req.Header.Set("X-Moa-Request", "1")
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	if rec := request(http.MethodGet, "/api/ops/pulse", false, false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", rec.Code)
	}
	if rec := request(http.MethodPost, "/api/ops/pulse", true, false); rec.Code != http.StatusForbidden {
		t.Fatalf("POST without CSRF status = %d", rec.Code)
	}

	before, versionBefore := mgr.ops.SnapshotVersion()
	rec := request(http.MethodGet, "/api/ops/pulse", true, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("pulse status = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		GeneratedAt time.Time `json:"generated_at"`
		Changes     struct {
			Requested bool `json:"requested"`
		} `json:"changes"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.GeneratedAt.IsZero() || body.GeneratedAt.Location() != time.UTC || !body.Changes.Requested {
		t.Fatalf("pulse body = %#v", body)
	}
	after, versionAfter := mgr.ops.SnapshotVersion()
	if versionAfter != versionBefore || !reflect.DeepEqual(after, before) {
		t.Fatalf("read-only pulse mutated Ops: version %d -> %d", versionBefore, versionAfter)
	}

	for _, path := range []string{
		"/api/ops/pulse?since=2026-07-10T12:00:00%2B01:00",
		"/api/ops/pulse?since=not-a-time",
		"/api/ops/pulse?since=2999-01-01T00:00:00Z",
		"/api/ops/pulse?until=2026-07-10T12:00:00Z",
	} {
		if rec := request(http.MethodGet, path, true, false); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, body = %s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestOpsPulseEndpointUsesOpaqueCursorAndSignalsReset(t *testing.T) {
	service := ops.New(ops.Config{MaxMilestones: 128})
	if err := service.UpsertSession(ops.SessionInput{ID: "session", Title: "release", CanonicalCWD: "/work/release", Presence: ops.PresenceActive}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for i := 0; i < 130; i++ {
		if err := service.RecordMilestone("session", ops.Milestone{Type: ops.MilestoneRunStarted, At: now, RefID: "run-" + string(rune(1000+i))}); err != nil {
			t.Fatal(err)
		}
	}
	handler := NewServer(&Manager{ops: service})
	request := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "localhost"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	rec := request("/api/ops/pulse")
	if rec.Code != http.StatusOK {
		t.Fatalf("initial status = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Changes struct {
			NextCursor string `json:"next_cursor"`
			HasMore    bool   `json:"has_more"`
		} `json:"changes"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.Changes.HasMore || body.Changes.NextCursor == "" {
		t.Fatalf("initial changes = %#v", body.Changes)
	}
	if rec := request("/api/ops/pulse?cursor=" + url.QueryEscape(body.Changes.NextCursor+"x")); rec.Code != http.StatusBadRequest {
		t.Fatalf("tampered cursor status = %d: %s", rec.Code, rec.Body.String())
	}
	for i := 0; i < 65; i++ {
		if err := service.RecordMilestone("session", ops.Milestone{Type: ops.MilestoneRunStarted, At: now.Add(time.Second), RefID: "later-" + string(rune(2000+i))}); err != nil {
			t.Fatal(err)
		}
	}
	rec = request("/api/ops/pulse?cursor=" + url.QueryEscape(body.Changes.NextCursor))
	if rec.Code != http.StatusGone || !strings.Contains(rec.Body.String(), `"reset":true`) {
		t.Fatalf("retained cursor status = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRunStartedClearsPriorVerificationForOpsPulse(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider(simpleResponseHandler("ok")))
	sess, err := mgr.CreateSession(CreateOpts{Title: "release"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.ops.UpdateVerification(sess.ID, ops.Verification{State: ops.VerificationPassed, At: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	sess.runtime.Bus.Publish(bus.RunStarted{SessionID: sess.ID, RunGen: 99})
	deadline := time.Now().Add(time.Second)
	for {
		status := mgr.OpsSnapshot().Projects[0].Sessions[0]
		if status.Verification.State == ops.VerificationUnknown {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("RunStarted retained prior verification: %#v", status.Verification)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestOpsPulseEndpointReturnsGoneForRetainedJournalGap(t *testing.T) {
	service := ops.New(ops.Config{MaxMilestones: 2})
	if err := service.UpsertSession(ops.SessionInput{ID: "session", Title: "release", CanonicalCWD: "/work/release", Presence: ops.PresenceActive}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 3; i++ {
		if err := service.RecordMilestone("session", ops.Milestone{Type: ops.MilestoneRunStarted, At: now.Add(time.Duration(i) * time.Second), RefID: "run-" + string(rune('a'+i))}); err != nil {
			t.Fatal(err)
		}
	}
	handler := NewServer(&Manager{ops: service})
	since := now.Add(-time.Second).Format(time.RFC3339Nano)
	req := httptest.NewRequest(http.MethodGet, "/api/ops/pulse?since="+url.QueryEscape(since), nil)
	req.Host = "localhost"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusGone {
		t.Fatalf("retention status = %d: %s", rec.Code, rec.Body.String())
	}
}
