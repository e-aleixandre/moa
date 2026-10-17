package openai

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &logs
}

func okAPI(t *testing.T) *OpenAI {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{"text":"ok"}`) }))
	t.Cleanup(srv.Close)
	k := New("synthetic-key")
	k.baseURL = srv.URL
	return k
}

func TestPlanRedirectLoopLeaksNothing(t *testing.T) {
	const token, audio, transcript = "SENTINEL_TOKEN", "SENTINEL_AUDIO", "SENTINEL_TEXT"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/again?token="+token+"&audio="+audio+"&text="+transcript, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	p := NewPlanTranscriber(planCreds(token, "acct"))
	p.url = srv.URL
	logs := captureLogs(t)
	_, err := (&core.FallbackTranscriber{Primary: p}).Transcribe(context.Background(), strings.NewReader(audio), "a.webm", core.TranscribeOptions{})
	if err == nil {
		t.Fatal("want error")
	}
	_, _ = (&core.FallbackTranscriber{Primary: p, Fallback: okAPI(t)}).Transcribe(context.Background(), strings.NewReader(audio), "a.webm", core.TranscribeOptions{})
	for _, v := range []string{token, audio, transcript} {
		if strings.Contains(err.Error(), v) || strings.Contains(logs.String(), v) {
			t.Errorf("%s leaked", v)
		}
	}
}

func TestPlanMalformedHTTPLeaksNothing(t *testing.T) {
	const private = "SENTINEL_PRIVATE_TRANSCRIPT"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close() //nolint:errcheck
		_, _ = rw.WriteString(private + "\r\n\r\n")
		_ = rw.Flush()
	}))
	defer srv.Close()
	p := NewPlanTranscriber(planCreds("synthetic-token", "acct"))
	p.url = srv.URL
	logs := captureLogs(t)
	_, err := p.Transcribe(context.Background(), strings.NewReader("audio"), "a.webm", core.TranscribeOptions{})
	if err == nil || strings.Contains(err.Error(), private) {
		t.Fatalf("err=%v", err)
	}
	_, _ = (&core.FallbackTranscriber{Primary: p, Fallback: okAPI(t)}).Transcribe(context.Background(), strings.NewReader("audio"), "a.webm", core.TranscribeOptions{})
	if strings.Contains(logs.String(), private) {
		t.Error("leaked in log")
	}
}

func TestPlanCredentialErrorLeaksNothing(t *testing.T) {
	p := NewPlanTranscriber(func(context.Context) (string, string, error) {
		return "", "", &credLeakErr{"SENTINEL_CRED_DETAIL"}
	})
	_, err := p.Transcribe(context.Background(), strings.NewReader("a"), "a.webm", core.TranscribeOptions{})
	if err == nil || strings.Contains(err.Error(), "SENTINEL_CRED_DETAIL") {
		t.Fatalf("err=%v", err)
	}
}

type credLeakErr struct{ s string }

func (e *credLeakErr) Error() string { return e.s }

func TestPaidTranscribeErrorLeaksNothing(t *testing.T) {
	const private = "SENTINEL_PAID_BODY"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, private, http.StatusBadRequest) }))
	defer srv.Close()
	k := New("synthetic-key")
	k.baseURL = srv.URL
	_, err := k.Transcribe(context.Background(), strings.NewReader(private), "a.webm", core.TranscribeOptions{})
	if err == nil || strings.Contains(err.Error(), private) || !strings.Contains(err.Error(), "400") {
		t.Fatalf("err=%v", err)
	}
	// Malformed JSON on 200 must not echo the body either.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, private) }))
	defer srv2.Close()
	k.baseURL = srv2.URL
	if _, err := k.Transcribe(context.Background(), strings.NewReader("a"), "a.webm", core.TranscribeOptions{}); err == nil || strings.Contains(err.Error(), private) {
		t.Fatalf("err=%v", err)
	}
	// Transport failure (closed server) carries a classification only.
	srv2.Close()
	if _, err := k.Transcribe(context.Background(), strings.NewReader("a"), "a.webm", core.TranscribeOptions{}); err == nil || strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("err=%v", err)
	}
}

func TestPlanRedirectIsNotFollowed(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	defer target.Close()
	plan := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer plan.Close()
	p := NewPlanTranscriber(planCreds("tok", "acct"))
	p.url = plan.URL
	got, err := (&core.FallbackTranscriber{Primary: p, Fallback: okAPI(t)}).Transcribe(context.Background(), strings.NewReader("a"), "a.webm", core.TranscribeOptions{})
	if err != nil || got != "ok" {
		t.Fatalf("a 3xx must fall back to the key: %q %v", got, err)
	}
	if hits.Load() != 0 {
		t.Fatal("redirect target was contacted")
	}
}

func TestPlanBudgetGrowsWithAudioAndIsCapped(t *testing.T) {
	const mib = 1 << 20
	if got := planBudget(0); got != 15*time.Second {
		t.Fatalf("base %v", got)
	}
	if a, b := planBudget(2*mib), planBudget(10*mib); a <= 15*time.Second || b <= a {
		t.Fatalf("not growing: %v %v", a, b)
	}
	if got := planBudget(25 * mib); got != 60*time.Second {
		t.Fatalf("cap %v", got)
	}
}

func TestHungPlanFallsBackWithinItsBudgetAndKeyKeepsParentContext(t *testing.T) {
	plan := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer plan.Close()
	p := NewPlanTranscriber(planCreds("tok", "acct"))
	p.url = plan.URL
	p.budget = func(int) time.Duration { return 150 * time.Millisecond }
	logs := captureLogs(t)
	start := time.Now()
	got, err := (&core.FallbackTranscriber{Primary: p, Fallback: okAPI(t)}).Transcribe(context.Background(), strings.NewReader("a"), "a.webm", core.TranscribeOptions{})
	if err != nil || got != "ok" || time.Since(start) > 3*time.Second {
		t.Fatalf("%q %v %v", got, err, time.Since(start))
	}
	if !strings.Contains(logs.String(), "timeout") {
		t.Fatalf("log should name the category: %s", logs)
	}
}

func TestSlowCredentialResolutionCountsInsideBudget(t *testing.T) {
	p := NewPlanTranscriber(func(ctx context.Context) (string, string, error) {
		<-ctx.Done()
		return "", "", ctx.Err()
	})
	p.budget = func(int) time.Duration { return 100 * time.Millisecond }
	start := time.Now()
	_, err := p.Transcribe(context.Background(), strings.NewReader("a"), "a.webm", core.TranscribeOptions{})
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("%v %v", err, time.Since(start))
	}
}

func TestParentCancelIsNotATimeoutAndSkipsTheKey(t *testing.T) {
	plan := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer plan.Close()
	p := NewPlanTranscriber(planCreds("tok", "acct"))
	p.url = plan.URL
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	k := okAPI(t)
	if _, err := (&core.FallbackTranscriber{Primary: p, Fallback: k}).Transcribe(ctx, strings.NewReader("a"), "a.webm", core.TranscribeOptions{}); err == nil {
		t.Fatal("key must not run after the client left")
	}
}
