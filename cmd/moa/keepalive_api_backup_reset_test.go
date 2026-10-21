package main

import (
	"context"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"github.com/e-aleixandre/moa/pkg/auth"
	"github.com/e-aleixandre/moa/pkg/core"
)

func TestAPIBackupPrewarmFreshNoResetAfterRememberedReset(t *testing.T) {
	h := newAPIPrewarmFixture(t, true)
	synctest.Test(t, func(t *testing.T) {
		// Cross the actual reliable boundary using the provider's clock as
		// well as the runtime's; a new bare retry date is not a new reset.
		time.Sleep(time.Until(h.reset) + time.Second)
		h.wire.serve = func(r *http.Request, _ int) (int, http.Header, string) {
			if r.Header.Get("Authorization") != "" {
				return backupReject("five_hour")
			}
			return backupOK("end_turn")
		}
		before := len(h.wire.headers)
		fresh := finishBackup(t, h.p, context.Background(), h.req)
		if len(h.wire.headers) != before+2 || h.wire.headers[before].Get("Authorization") == "" || fresh.ProviderSource == nil || fresh.ProviderSource.Kind != "api_backup" {
			t.Fatalf("natural turn did not recheck OAuth then use API: source=%+v calls=%d", fresh.ProviderSource, len(h.wire.headers)-before)
		}
		if fresh.ProviderSource.OAuthNotBefore == nil || !fresh.ProviderSource.OAuthNotBefore.After(h.reset) {
			t.Fatalf("new quota observation did not follow old reset: %+v", fresh.ProviderSource)
		}
		if fresh.ProviderSource.OAuthQuotaObservedAt == nil || fresh.ProviderSource.OAuthQuotaObservedAt.Before(h.reset) {
			t.Fatalf("actual fresh quota observation missing: %+v", fresh.ProviderSource)
		}
		freshBody := h.wire.bodies[before+1]
		time.Sleep(4*time.Minute + 30*time.Second)
		h.wire.serve = prewarmOK
		before = len(h.wire.headers)
		if _, err := h.p.Prewarm(context.Background(), h.ping()); err == nil || len(h.wire.headers) != before {
			t.Fatalf("old reliable-reset binding became pingable: err=%v calls=%d", err, len(h.wire.headers)-before)
		}
		req := h.req
		req.Options.ProviderBinding = fresh.ProviderSource
		legacy := *fresh.ProviderSource
		legacy.OAuthQuotaObservedAt = nil
		req.Options.ProviderBinding = &legacy
		if _, err := h.p.Prewarm(context.Background(), req); err == nil || len(h.wire.headers) != before {
			t.Fatalf("legacy binding inferred observation from retry date: err=%v calls=%d", err, len(h.wire.headers)-before)
		}
		req.Options.ProviderBinding = fresh.ProviderSource
		usage, err := h.p.Prewarm(context.Background(), req)
		if err != nil || len(h.wire.headers) != before+1 || usage.CacheRead != 250000 {
			t.Fatalf("fresh no-reset API response blocked by expired memo: err=%v calls=%d usage=%+v", err, len(h.wire.headers)-before, usage)
		}
		assertAPIPrewarmBody(t, freshBody, h.wire.bodies[before])
		prepared, dispatched := false, false
		checkObservation := func(src core.ProviderSource) bool {
			return src.OAuthQuotaObservedAt != nil && src.OAuthQuotaObservedAt.Equal(*fresh.ProviderSource.OAuthQuotaObservedAt)
		}
		req.Options.OnProviderPrepare = func(_ context.Context, src core.ProviderSource) error {
			prepared = checkObservation(src)
			return nil
		}
		req.Options.OnProviderDispatch = func(_ context.Context, src core.ProviderSource) error {
			dispatched = checkObservation(src)
			return nil
		}
		h.wire.serve = func(*http.Request, int) (int, http.Header, string) { return backupOK("end_turn") }
		continued := finishBackup(t, h.p, context.Background(), req)
		if !prepared || !dispatched || continued.ProviderSource == nil || !checkObservation(*continued.ProviderSource) || len(h.wire.headers) != before+2 || h.wire.headers[before+1].Get("Authorization") != "" {
			t.Fatalf("continuation lost quota observation or probed OAuth: prepared=%v dispatched=%v source=%+v calls=%d", prepared, dispatched, continued.ProviderSource, len(h.wire.headers)-before)
		}
		h.wire.serve = prewarmOK
		req.Options.ProviderBinding = continued.ProviderSource
		if _, err := h.p.Prewarm(context.Background(), req); err != nil || len(h.wire.headers) != before+3 {
			t.Fatalf("continued fresh snapshot cannot ping: err=%v calls=%d", err, len(h.wire.headers)-before)
		}
		assertAPIPrewarmBody(t, h.wire.bodies[before+1], h.wire.bodies[before+2])
		// A later reliable account-wide reset remains authoritative over
		// this earlier no-reset observation; no old map entry is cleared.
		nextReset := time.Now().Add(time.Hour)
		h.f.store.RememberAnthropicPlanLimit(auth.BackupRevision{Primary: fresh.ProviderSource.PrimaryGeneration, Key: fresh.ProviderSource.BackupGeneration}, nextReset)
		h.p.now = func() time.Time { return nextReset }
		if _, err := h.p.Prewarm(context.Background(), req); err == nil || len(h.wire.headers) != before+3 {
			t.Fatalf("later reliable reset ignored: err=%v calls=%d", err, len(h.wire.headers)-before)
		}
	})
}
