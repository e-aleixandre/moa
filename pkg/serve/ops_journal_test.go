package serve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOpsChangesQueryRequiresExplicitUTCWindow(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider(simpleResponseHandler("ok")))
	server := NewServer(mgr)
	for _, path := range []string{
		"/api/ops?view=changes-since&since=2026-07-10T12:00:00Z",
		"/api/ops?view=changes-since&since=2026-07-10T12:00:00%2B01:00&until=2026-07-10T13:00:00Z",
		"/api/ops?view=changes-since&since=2026-07-10T12:00:00Z&until=2026-08-12T12:00:00Z",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "localhost"
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d", path, rec.Code)
		}
	}
}

func TestOpsCheckpointsAreReadOnlyView(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider(simpleResponseHandler("ok")))
	at := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	if _, err := mgr.CreateOpsCheckpoint("shift-1", at); err != nil {
		t.Fatal(err)
	}
	server := NewServer(mgr)
	req := httptest.NewRequest(http.MethodGet, "/api/ops?view=checkpoints", nil)
	req.Host = "localhost"
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "[{\"name\":\"shift-1\",\"at\":\"2026-07-10T12:00:00Z\"}]\n" {
		t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
	}
}
