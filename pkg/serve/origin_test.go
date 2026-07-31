package serve

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/session"
)

func TestCreateSessionOrigin(t *testing.T) {
	tests := []struct {
		name       string
		origin     string
		wantMeta   string // persisted metadata value ("" = key absent)
		wantAPI    string // SessionInfo.Origin
		wantOrigin string // session.Session.Origin()
	}{
		{"default is user", "", "", "", session.OriginUser},
		{"automation", "automation", "automation", "automation", "automation"},
		{"caller label", "linear-webhook", "linear-webhook", "linear-webhook", "linear-webhook"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, mgr, cancel := newTestServer(t)
			defer cancel()

			sess, err := mgr.CreateSession(CreateOpts{Title: "t", Origin: tt.origin})
			if err != nil {
				t.Fatal(err)
			}
			if got := mgr.sessionInfo(sess).Origin; got != tt.wantAPI {
				t.Errorf("SessionInfo.Origin = %q, want %q", got, tt.wantAPI)
			}

			saved, _, err := session.FindSession(mgr.sessionBaseDir, sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			gotMeta, _ := saved.Metadata[session.MetaOrigin].(string)
			if gotMeta != tt.wantMeta {
				t.Errorf("metadata origin = %q, want %q", gotMeta, tt.wantMeta)
			}
			if got := saved.Origin(); got != tt.wantOrigin {
				t.Errorf("saved.Origin() = %q, want %q", got, tt.wantOrigin)
			}
		})
	}
}

// The persistence reactor rebuilds Metadata from scratch on every snapshot, so
// origin must survive the first save after creation (and a resume after it).
func TestOriginSurvivesSnapshotAndResume(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()

	sess, err := mgr.CreateSession(CreateOpts{Title: "auto", Origin: "automation"})
	if err != nil {
		t.Fatal(err)
	}
	resp := apiReq(t, srv, "POST", "/api/sessions/"+sess.ID+"/send", `{"text":"hello"}`)
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != 202 {
		t.Fatalf("expected 202, got %d", resp.StatusCode)
	}
	pollUntil(t, 5*time.Second, "origin persisted after snapshot", func() bool {
		saved, _, err := session.FindSession(mgr.sessionBaseDir, sess.ID)
		if err != nil {
			return false
		}
		return len(saved.Entries) > 0 && saved.Origin() == "automation"
	})

	if err := mgr.Delete(sess.ID); err != nil {
		t.Fatal(err)
	}
}

func TestListSessionsExposesOrigin(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()

	auto, err := mgr.CreateSession(CreateOpts{Title: "auto", Origin: "automation"})
	if err != nil {
		t.Fatal(err)
	}
	user, err := mgr.CreateSession(CreateOpts{Title: "human"})
	if err != nil {
		t.Fatal(err)
	}

	resp := apiReq(t, srv, "GET", "/api/sessions", "")
	defer resp.Body.Close() //nolint:errcheck
	var list []SessionInfo
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]SessionInfo, len(list))
	for _, info := range list {
		byID[info.ID] = info
	}
	if got := byID[auto.ID].Origin; got != "automation" {
		t.Errorf("automation session origin = %q, want automation", got)
	}
	if got := byID[user.ID].Origin; got != "" {
		t.Errorf("user session origin = %q, want empty", got)
	}
}
