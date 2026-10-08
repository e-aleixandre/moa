package main

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/auth"
	"github.com/e-aleixandre/moa/pkg/core"
)

func TestBackupFailedRevokeDoesNotResumePaidDispatch(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Fatal("test requires real filesystem permission denial; do not run as root")
	}
	f := newRuntimeFixture(t)
	const access = "sk-ant-oat01-REVIEW-PRIMARY-ACCESS-SENTINEL"
	const refresh = "REVIEW-PRIMARY-REFRESH-SENTINEL"
	const key = "sk-ant-api03-REVIEW-BACKUP-KEY-SENTINEL"
	f.commit(t, f.store, "anthropic", auth.Credential{Type: "oauth", Access: access, Refresh: refresh, Expires: 4102444800000})
	st, err := f.store.AnthropicBackupStatus()
	if err != nil {
		t.Fatal(err)
	}
	st, err = f.store.SaveAnthropicBackup(st.Revision, key)
	if err != nil {
		t.Fatal(err)
	}
	st, err = f.store.SetAnthropicBackupEnabled(st.Revision, true)
	if err != nil || !st.Enabled {
		t.Fatalf("fixture enable: enabled=%t err=%v", st.Enabled, err)
	}
	lockPath := f.path + ".lock"
	if err := os.Chmod(lockPath, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(lockPath, 0600) })
	_, revokeErr := f.store.RemoveAnthropicBackup(st.Revision)
	pe, classified := core.AsProviderCredentialError(revokeErr)
	if !classified || pe.Class != core.CredentialStoreUnavailable {
		t.Fatalf("fixture did not fail at lock open: %v", revokeErr)
	}
	if err := os.Chmod(lockPath, 0600); err != nil {
		t.Fatal(err)
	}
	stopLogs := captureOutput(t)
	tr := &backupWire{serve: func(_ *http.Request, n int) (int, http.Header, string) {
		if n == 0 {
			return backupReject("five_hour")
		}
		return backupOK("end_turn")
	}}
	model := core.Model{ID: "claude-opus-5-5", Provider: "anthropic"}
	p := &snapshotProvider{model: model, authStore: f.store, httpClient: &http.Client{Transport: tr}}
	req := core.Request{Model: model, Messages: []core.Message{core.NewUserMessage("offline review")}, Options: core.StreamOptions{OnProviderRetry: func(_ context.Context, w core.ProviderWait) error {
		return &core.ProviderRetryReady{Attempt: w.Attempt, Wait: &w}
	}}}
	ch, streamErr := p.Stream(context.Background(), req)
	if ch != nil {
		for ev := range ch {
			if ev.Error != nil {
				streamErr = ev.Error
			}
		}
	}
	logs := stopLogs()
	for _, secret := range []string{access, refresh, key} {
		if strings.Contains(logs, secret) {
			t.Error("logs leaked a fixture credential")
		}
	}
	oauthHTTP, paidHTTP := 0, 0
	for _, h := range tr.headers {
		if h.Get("Authorization") != "" {
			oauthHTTP++
		}
		if h.Get("X-API-Key") != "" {
			paidHTTP++
		}
	}
	t.Logf("revoke_class=%s later_current_quota=five_hour OAuth_HTTP=%d paid_HTTP=%d stream_error=%v explicit_backup_mutations_after_failure=0", pe.Class, oauthHTTP, paidHTTP, streamErr)
	if oauthHTTP != 1 {
		t.Fatalf("proof did not reach the current primary request: OAuth_HTTP=%d", oauthHTTP)
	}
	if paidHTTP != 0 {
		t.Error("failed local remove resumed a paid HTTP dispatch after recovery")
	}
}
