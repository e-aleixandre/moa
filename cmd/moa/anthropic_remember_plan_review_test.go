package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/e-aleixandre/moa/pkg/auth"
	"github.com/e-aleixandre/moa/pkg/core"
)

func reviewDrain(p *snapshotProvider, req core.Request) error {
	ch, err := p.Stream(context.Background(), req)
	if err != nil {
		return err
	}
	for ev := range ch {
		if ev.Error != nil {
			return ev.Error
		}
	}
	return nil
}

func TestReviewMemoExpiryBeforePaidAdmission(t *testing.T) {
	p, _, tr, req := backupRuntime(t)
	synctest.Test(t, func(t *testing.T) {
		reset := time.Now().Add(time.Hour)
		tr.serve = func(_ *http.Request, n int) (int, http.Header, string) {
			if n == 0 {
				return backupRejectUntil("five_hour", reset)
			}
			return backupOK("end_turn")
		}
		finishBackup(t, p, context.Background(), req)
		time.Sleep(time.Hour - time.Second)
		req.Options.OnProviderPrepare = func(context.Context, core.ProviderSource) error {
			time.Sleep(2 * time.Second)
			return nil
		}
		finishBackup(t, p, context.Background(), req)
		plan, api := planCalls(tr.headers[2:])
		t.Logf("at admission T+%s: second ordinary request plan=%d api=%d", time.Since(reset), plan, api)
		if plan != 1 || api != 0 {
			t.Fatalf("expired memo admitted a new paid request; want plan=1 api=0, got plan=%d api=%d", plan, api)
		}
	})
}

func TestReviewLateConfirmationCannotEraseNewMemo(t *testing.T) {
	for _, changeKey := range []bool{false, true} {
		name := "same_pair_shorter_reset"
		if changeKey {
			name = "replaced_key_old_reply"
		}
		t.Run(name, func(t *testing.T) {
			p, s, slow, req := backupRuntime(t)
			first := time.Now().Add(time.Hour)
			later := time.Now().Add(5 * time.Hour)
			arrived, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			slow.serve = func(_ *http.Request, n int) (int, http.Header, string) {
				if n == 0 {
					close(arrived)
					<-release
					return backupRejectUntil("five_hour", first)
				}
				return backupOK("end_turn")
			}
			go func() { done <- reviewDrain(p, req) }()
			<-arrived
			if changeKey {
				status, err := s.AnthropicBackupStatus()
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.SaveAnthropicAPIKey(status.Revision.Primary, "fake-new-review-key"); err != nil {
					t.Fatal(err)
				}
			}
			fast := &backupWire{serve: func(_ *http.Request, n int) (int, http.Header, string) {
				if n == 0 {
					return backupRejectUntil("seven_day", later)
				}
				return backupOK("end_turn")
			}}
			q := &snapshotProvider{model: p.model, authStore: s, httpClient: &http.Client{Transport: fast}}
			finishBackup(t, q, context.Background(), req)
			close(release)
			err := <-done
			if changeKey && err == nil {
				t.Fatal("old key request was admitted")
			}
			if !changeKey && err != nil {
				t.Fatal(err)
			}
			if !changeKey {
				q.now = func() time.Time { return first.Add(time.Minute) }
			}
			finishBackup(t, q, context.Background(), req)
			plan, api := planCalls(fast.headers[2:])
			t.Logf("after newer seven_day confirmation and delayed old reply: next request plan=%d api=%d", plan, api)
			if plan != 0 || api != 1 {
				t.Fatalf("late reply erased the live memo; want plan=0 api=1, got plan=%d api=%d", plan, api)
			}
		})
	}
}

func TestReviewMemoReadToAdmissionInvalidation(t *testing.T) {
	for _, action := range []string{"stop", "reconfigure", "remove", "replace_key", "relogin", "env_override"} {
		t.Run(action, func(t *testing.T) {
			p, s, tr, req := backupRuntime(t)
			tr.serve = func(_ *http.Request, n int) (int, http.Header, string) {
				if n == 0 {
					return backupRejectUntil("seven_day", time.Now().Add(time.Hour))
				}
				return backupOK("end_turn")
			}
			finishBackup(t, p, context.Background(), req)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			prepared := false
			req.Options.OnProviderPrepare = func(context.Context, core.ProviderSource) error {
				prepared = true
				if action == "stop" {
					cancel()
					return ctx.Err()
				}
				if action == "reconfigure" {
					return core.ErrProviderReconfigured
				}
				if action == "env_override" {
					t.Setenv("ANTHROPIC_API_KEY", "fake-env-key")
					return nil
				}
				st, err := s.AnthropicBackupStatus()
				if err != nil {
					return err
				}
				switch action {
				case "remove":
					return s.RemoveAnthropicAPIKey(st.Revision.Key)
				case "replace_key":
					_, err = s.SaveAnthropicAPIKey(st.Revision.Primary, "fake-new-key")
				case "relogin":
					_, err = s.CommitLogin("anthropic", st.Revision.Primary, auth.Credential{Type: "oauth", Access: "fake-new-access", Refresh: "fake-new-refresh", Expires: 4102444800000})
				}
				return err
			}
			_, err := p.Stream(ctx, req)
			if !prepared || err == nil || len(tr.headers) != 2 {
				t.Fatalf("action=%s prepared=%v err=%v wire calls=%d, want no new HTTP", action, prepared, err, len(tr.headers))
			}
		})
	}
}

type reviewConcurrentWire struct {
	mu      sync.Mutex
	headers []http.Header
	reset   time.Time
	gate    chan struct{}
	target  int
	arrived int
}

func (tr *reviewConcurrentWire) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.String() != "https://api.anthropic.com/v1/messages" {
		return nil, errors.New("network forbidden")
	}
	_, _ = io.Copy(io.Discard, r.Body)
	_ = r.Body.Close()
	tr.mu.Lock()
	tr.headers = append(tr.headers, r.Header.Clone())
	plan := r.Header.Get("Authorization") != ""
	reset, gate := tr.reset, tr.gate
	if plan && gate != nil {
		tr.arrived++
		if tr.arrived == tr.target {
			close(gate)
		}
	}
	tr.mu.Unlock()
	if plan && gate != nil {
		<-gate
	}
	status, h, body := backupOK("end_turn")
	if plan {
		status, h, body = backupRejectUntil("seven_day", reset)
	}
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}
func TestReviewConcurrentExpiryAndReconfirmation(t *testing.T) {
	const n = 12
	p, s, _, req := backupRuntime(t)
	first := time.Now().Add(time.Hour)
	tr := &reviewConcurrentWire{reset: first}
	p.httpClient = &http.Client{Transport: tr}
	finishBackup(t, p, context.Background(), req)
	tr.mu.Lock()
	tr.reset = first.Add(5 * time.Hour)
	tr.gate = make(chan struct{})
	tr.target = n
	tr.mu.Unlock()
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			q := &snapshotProvider{model: p.model, authStore: s, httpClient: p.httpClient, now: func() time.Time { return first }}
			errs <- reviewDrain(q, req)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	q := &snapshotProvider{model: p.model, authStore: s, httpClient: p.httpClient, now: func() time.Time { return first.Add(time.Minute) }}
	finishBackup(t, q, context.Background(), req)
	plan, api := planCalls(tr.headers)
	t.Logf("seed + %d simultaneous sessions at T + one after reconfirmation: plan=%d api=%d", n, plan, api)
	if plan != n+1 || api != n+2 {
		t.Fatalf("unexpected counts plan=%d api=%d", plan, api)
	}
}

func TestReviewRememberedTimingBoundaries(t *testing.T) {
	for _, name := range []string{"exactly_8d", "beyond_8d", "origin_3h_ahead", "origin_3h_behind", "conflicting_global", "duplicate_global_conflict", "other_rejected_missing_reset", "duplicate_reset", "past_reset"} {
		t.Run(name, func(t *testing.T) {
			p, s, tr, req := backupRuntime(t)
			synctest.Test(t, func(t *testing.T) {
				observed := time.Now()
				until := observed.Add(2 * time.Hour)
				remember := true
				tr.serve = func(_ *http.Request, n int) (int, http.Header, string) {
					if n > 0 {
						return backupOK("end_turn")
					}
					reset, origin := until, observed
					switch name {
					case "exactly_8d":
						reset = observed.Add(8 * 24 * time.Hour)
						until = reset
					case "beyond_8d":
						reset = observed.Add(8*24*time.Hour + time.Second)
						remember = false
					case "origin_3h_ahead":
						origin = observed.Add(3 * time.Hour)
						reset = origin.Add(2 * time.Hour)
					case "origin_3h_behind":
						origin = observed.Add(-3 * time.Hour)
						reset = origin.Add(2 * time.Hour)
					case "past_reset":
						reset = observed.Add(-time.Hour)
						remember = false
					}
					status, h, body := backupRejectUntil("five_hour", reset)
					h.Set("Date", origin.UTC().Format(http.TimeFormat))
					switch name {
					case "conflicting_global":
						h.Set("anthropic-ratelimit-unified-reset", strconv.FormatInt(reset.Add(time.Hour).Unix(), 10))
						remember = false
					case "duplicate_global_conflict":
						h.Add("anthropic-ratelimit-unified-reset", strconv.FormatInt(reset.Add(time.Hour).Unix(), 10))
						remember = false
					case "other_rejected_missing_reset":
						h.Set("anthropic-ratelimit-unified-7d-status", "rejected")
						remember = false
					case "duplicate_reset":
						h.Add("anthropic-ratelimit-unified-5h-reset", strconv.FormatInt(reset.Unix(), 10))
						remember = false
					}
					return status, h, body
				}
				finishBackup(t, p, context.Background(), req)
				st, err := s.AnthropicBackupStatus()
				if err != nil {
					t.Fatal(err)
				}
				got, ok := s.AnthropicPlanLimit(st.Revision, observed)
				if ok != remember || (ok && !got.Equal(until)) {
					t.Errorf("remember=%v want %v, until=%s want %s", ok, remember, got, until)
				}
				finishBackup(t, p, context.Background(), req)
				plan, api := planCalls(tr.headers[2:])
				if (remember && (plan != 0 || api != 1)) || (!remember && (plan != 1 || api != 0)) {
					t.Fatalf("remember=%v next request plan=%d api=%d", remember, plan, api)
				}
			})
		})
	}
}

func TestReviewNewStoreDoesNotInheritMemo(t *testing.T) {
	f := newRuntimeFixture(t)
	f.commit(t, f.store, "anthropic", auth.Credential{Type: "oauth", Access: "fake-access", Refresh: "fake-refresh", Expires: 4102444800000})
	snap, err := f.store.PeekSnapshot("anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.SaveAnthropicAPIKey(snap.Generation, "fake-key"); err != nil {
		t.Fatal(err)
	}
	st, err := f.store.AnthropicBackupStatus()
	if err != nil {
		t.Fatal(err)
	}
	f.store.RememberAnthropicPlanLimit(st.Revision, time.Now().Add(time.Hour))
	fresh := auth.NewStore(f.path)
	if _, ok := fresh.AnthropicPlanLimit(st.Revision, time.Now()); ok {
		t.Fatal("memo persisted")
	}
	tr := &backupWire{serve: func(*http.Request, int) (int, http.Header, string) { return backupOK("end_turn") }}
	p := &snapshotProvider{model: core.Model{Provider: "anthropic", ID: "claude-opus-5-5"}, authStore: fresh, httpClient: &http.Client{Transport: tr}}
	req := core.Request{Model: p.model, Messages: []core.Message{core.NewUserMessage("explicit next send")}}
	finishBackup(t, p, context.Background(), req)
	plan, api := planCalls(tr.headers)
	if plan != 1 || api != 0 {
		t.Fatalf("new store's explicit send plan=%d api=%d", plan, api)
	}
}
