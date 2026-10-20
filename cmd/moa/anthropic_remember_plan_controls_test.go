package main

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestReviewMemoPreservesWireAndUnsupportedSuccessDoesNotClearIt(t *testing.T) {
	for _, model := range []string{"claude-opus-5-5", "claude-sonnet-5-5"} {
		t.Run(model, func(t *testing.T) {
			p, _, tr, req := backupRuntime(t)
			p.model.ID = model
			req.Model = p.model
			tr.serve = func(_ *http.Request, n int) (int, http.Header, string) {
				if n == 0 {
					return backupRejectUntil("five_hour", time.Now().Add(time.Hour))
				}
				return backupOK("end_turn")
			}
			finishBackup(t, p, context.Background(), req)
			finishBackup(t, p, context.Background(), req)
			if len(tr.headers) != 3 || tr.headers[2].Get("Authorization") != "" || tr.headers[2].Get("X-API-Key") == "" {
				t.Fatal("memo did not choose API")
			}
			assertBackupIdentityDelta(t, tr.bodies[0], tr.bodies[2])
			if !bytes.Equal(tr.bodies[1], tr.bodies[2]) || !strings.Contains(string(tr.bodies[2]), `"model":"`+model+`"`) {
				t.Fatal("remembered API changed the established API body/model")
			}
			fast := req
			fast.Options.Fast = true
			finishBackup(t, p, context.Background(), fast)
			unsupported := req
			unsupported.Model.ID = "claude-haiku-4-5"
			finishBackup(t, p, context.Background(), unsupported)
			finishBackup(t, p, context.Background(), req)
			if len(tr.headers) != 6 || tr.headers[3].Get("Authorization") == "" || tr.headers[4].Get("Authorization") == "" || tr.headers[5].Get("X-API-Key") == "" {
				t.Fatal("unsupported success erased memo or used backup")
			}
		})
	}
}

func TestReviewEnvironmentKeepsItsNormalPriority(t *testing.T) {
	p, _, tr, req := backupRuntime(t)
	tr.serve = func(_ *http.Request, n int) (int, http.Header, string) {
		if n == 0 {
			return backupRejectUntil("seven_day", time.Now().Add(time.Hour))
		}
		return backupOK("end_turn")
	}
	finishBackup(t, p, context.Background(), req)
	t.Setenv("ANTHROPIC_API_KEY", "fake-env-overrides-memo")
	finishBackup(t, p, context.Background(), req)
	if tr.headers[2].Get("X-API-Key") != "fake-env-overrides-memo" || tr.headers[2].Get("Authorization") != "" {
		t.Fatal("memo displaced env key")
	}
	t.Setenv("ANTHROPIC_API_KEY", "")
	finishBackup(t, p, context.Background(), req)
	if tr.headers[3].Get("X-API-Key") != "fake-api-backup" {
		t.Fatal("unchanged stored pair was not restored")
	}
	t.Log("env override selected its own primary API path; removing it restored the unchanged stored pair and its memo")
}
