package openai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func planCreds(token, account string) PlanCredentials {
	return func(context.Context) (string, string, error) { return token, account, nil }
}

func TestPlanTranscribeSendsPlanRequest(t *testing.T) {
	var gotUA, gotAuth, gotAcct, gotOrig, gotLang, gotPrompt, gotName, gotType string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotAuth, gotAcct, gotOrig = r.Header.Get("Authorization"), r.Header.Get("ChatGPT-Account-Id"), r.Header.Get("originator")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
		}
		gotLang, gotPrompt = r.FormValue("language"), r.FormValue("prompt")
		f, h, err := r.FormFile("file")
		if err != nil {
			t.Error(err)
			return
		}
		gotName, gotType = h.Filename, h.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(f)
		_, _ = w.Write([]byte(`{"text":"hola mundo","asset_pointer":"x"}`))
	}))
	defer srv.Close()
	p := NewPlanTranscriber(planCreds("tok", "acct"))
	p.url = srv.URL
	text, err := p.Transcribe(context.Background(), strings.NewReader("AUDIO"), "audio.webm", core.TranscribeOptions{Language: "es", Prompt: "moa, kubectl", Model: "ignored"})
	if err != nil || text != "hola mundo" {
		t.Fatalf("text=%q err=%v", text, err)
	}
	if gotAuth != "Bearer tok" || gotAcct != "acct" || gotOrig != "Codex Desktop" || gotUA != codexUserAgent {
		t.Fatalf("headers: %q %q %q", gotAuth, gotAcct, gotOrig)
	}
	if gotLang != "es" || gotPrompt != "moa, kubectl" || gotName != "audio.webm" || gotType != "audio/webm" || string(gotBody) != "AUDIO" {
		t.Fatalf("form: %q %q %q %q %q", gotLang, gotPrompt, gotName, gotType, gotBody)
	}
}

func TestPlanTranscribeFailuresCarryNoBody(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"500":    func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "secret-body tok", 500) },
		"403":    func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "secret-body tok", 403) },
		"429":    func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "secret-body tok", 429) },
		"format": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"nope":1}`)) },
		"html":   func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`<html>`)) },
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(h)
			defer srv.Close()
			p := NewPlanTranscriber(planCreds("tok", "acct"))
			p.url = srv.URL
			_, err := p.Transcribe(context.Background(), strings.NewReader("a"), "a.wav", core.TranscribeOptions{})
			if err == nil || strings.Contains(err.Error(), "secret-body") || strings.Contains(err.Error(), "tok") && strings.Contains(err.Error(), "Bearer") {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestPlanTranscribeNoLogin(t *testing.T) {
	p := NewPlanTranscriber(planCreds("", ""))
	if _, err := p.Transcribe(context.Background(), strings.NewReader("a"), "a.wav", core.TranscribeOptions{}); err == nil {
		t.Fatal("want error")
	}
	p = NewPlanTranscriber(func(context.Context) (string, string, error) { return "", "", errors.New("boom") })
	if _, err := p.Transcribe(context.Background(), strings.NewReader("a"), "a.wav", core.TranscribeOptions{}); err == nil {
		t.Fatal("want error")
	}
}

func TestPlanThenAPIKeyEndToEnd(t *testing.T) {
	plan := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "no", 429) }))
	defer plan.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-test" || r.URL.Path != "/v1/audio/transcriptions" {
			t.Errorf("bad api request %s %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"text":"desde la key"}`))
	}))
	defer api.Close()
	p := NewPlanTranscriber(planCreds("tok", "acct"))
	p.url = plan.URL
	k := New("sk-test")
	k.baseURL = api.URL
	got, err := (&core.FallbackTranscriber{Primary: p, Fallback: k}).Transcribe(context.Background(), strings.NewReader("a"), "a.webm", core.TranscribeOptions{})
	if err != nil || got != "desde la key" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestPlanRejectsAudioWith400DoesNotSpendTheKey(t *testing.T) {
	plan := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "bad audio", 400) }))
	defer plan.Close()
	apiCalls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		apiCalls++
		_, _ = w.Write([]byte(`{"text":"x"}`))
	}))
	defer api.Close()
	p := NewPlanTranscriber(planCreds("tok", "acct"))
	p.url = plan.URL
	k := New("sk-test")
	k.baseURL = api.URL
	_, err := (&core.FallbackTranscriber{Primary: p, Fallback: k}).Transcribe(context.Background(), strings.NewReader("a"), "a.webm", core.TranscribeOptions{})
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("want the plan's 400 error, got %v", err)
	}
	if apiCalls != 0 {
		t.Fatalf("API key used %d times after a 400", apiCalls)
	}
}

func TestPlanOtherFailuresStillFallBack(t *testing.T) {
	for _, code := range []int{401, 403, 429, 500, 503} {
		plan := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "x", code) }))
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"text":"key"}`)) }))
		p := NewPlanTranscriber(planCreds("tok", "acct"))
		p.url = plan.URL
		k := New("sk-test")
		k.baseURL = api.URL
		got, err := (&core.FallbackTranscriber{Primary: p, Fallback: k}).Transcribe(context.Background(), strings.NewReader("a"), "a.webm", core.TranscribeOptions{})
		plan.Close()
		api.Close()
		if err != nil || got != "key" {
			t.Fatalf("%d: %q %v", code, got, err)
		}
	}
}
