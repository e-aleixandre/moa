package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/core"
)

func TestBackupDrainedUserSteerRequiresOwnOAuth(t *testing.T) {
	p, _, tr, _ := backupRuntime(t)
	reg := core.NewRegistry()
	var a *agent.Agent
	queued := false
	if err := reg.Register(core.Tool{Name: "backup_once", Parameters: json.RawMessage(`{"type":"object","properties":{}}`), Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
		queued = a.Steer(core.SteerItem{ID: "explicit-steer", Text: "new explicit user task"})
		return core.TextResult("old task tool finished"), nil
	}}); err != nil {
		t.Fatal(err)
	}
	a, _, _ = newBackupAgent(t, p, reg)
	tr.serve = func(_ *http.Request, n int) (int, http.Header, string) {
		if n == 0 {
			s, h, b := backupReject("five_hour")
			h.Set("Retry-After", "3600")
			return s, h, b
		}
		if n == 1 {
			return 200, http.Header{}, backupToolResponse(t, tr.bodies[n], "old-tool")
		}
		return backupOK("end_turn")
	}
	if _, err := a.SendWithMsgID(context.Background(), "original task", "original-user"); err != nil {
		t.Fatal(err)
	}
	if !queued || len(tr.headers) != 3 || !strings.Contains(string(tr.bodies[2]), "new explicit user task") {
		t.Fatalf("did not deliver steer: queued=%v calls=%d", queued, len(tr.headers))
	}
	if tr.headers[2].Get("Authorization") == "" || tr.headers[2].Get("X-API-Key") != "" {
		t.Fatal("new user task inherited prior API origin")
	}
}
