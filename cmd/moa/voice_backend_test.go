package main

import (
	"context"
	"net/http"
	"testing"

	"github.com/e-aleixandre/moa/pkg/auth"
	"github.com/e-aleixandre/moa/pkg/core"
)

// The handoff backend of a ChatGPT-plan voice call is bound to the plan
// sign-in the call was opened with: these tests drive the production
// factory, Store and provider transport against a local fake upstream.

func voicePlanPin(t *testing.T, f *runtimeFixture) auth.CredentialSnapshot {
	t.Helper()
	pin, err := f.store.ResolveSnapshot(context.Background(), "openai", auth.SelectPlanOAuth)
	if err != nil {
		t.Fatal(err)
	}
	return pin
}

func voiceBackendFor(t *testing.T, f *runtimeFixture, pin auth.CredentialSnapshot) core.Provider {
	t.Helper()
	p, err := newVoiceBackendFactory(f.store, f.u.client())(openaiModel, pin)
	if err != nil {
		t.Fatalf("voice backend: %v", err)
	}
	return p
}

func TestVoiceBackend_PlanWorksDespiteTextEnvironment(t *testing.T) {
	f := newRuntimeFixture(t)
	access := openaiAccess("acct", "VOICE-PLAN")
	f.commit(t, f.store, "openai", oauthCred(access, "offline-refresh", "acct"))
	t.Setenv("OPENAI_API_KEY", "offline-text-environment-key-0123456789")
	p := voiceBackendFor(t, f, voicePlanPin(t, f))
	if _, err := streamText(context.Background(), p, openaiModel); err != nil {
		t.Fatalf("plan backend with a text API key in the environment: %v", err)
	}
	calls, _ := f.u.snapshot()
	if len(calls) != 1 || calls[0].Origin != "chatgpt.com" || calls[0].Account != "acct" || calls[0].Bearer != access {
		t.Fatalf("calls = %+v, want one plan request for acct", calls)
	}
}

// A replacement of the plan — by an API key, another account, or a new
// sign-in of the same account — fails the handoff closed: no request leaves.
func TestVoiceBackend_PlanReplacementFailsClosed(t *testing.T) {
	const key = "offline-hidden-payment-key-0123456789"
	cases := map[string]auth.Credential{
		"api_key":       apiKeyCred(key),
		"other_account": oauthCred(openaiAccess("acct-B", "VOICE-PLAN-B"), "offline-refresh-B", "acct-B"),
		"new_sign_in":   oauthCred(openaiAccess("acct", "VOICE-PLAN-A2"), "offline-refresh-A2", "acct"),
	}
	for name, replacement := range cases {
		for _, when := range []string{"before_build", "after_build"} {
			t.Run(name+"/"+when, func(t *testing.T) {
				f := newRuntimeFixture(t)
				f.commit(t, f.store, "openai", oauthCred(openaiAccess("acct", "VOICE-PLAN"), "offline-refresh", "acct"))
				pin := voicePlanPin(t, f)
				if when == "before_build" {
					f.commit(t, f.other, "openai", replacement)
				}
				p, err := newVoiceBackendFactory(f.store, f.u.client())(openaiModel, pin)
				if err == nil {
					if when == "after_build" {
						f.commit(t, f.other, "openai", replacement)
					}
					_, err = streamText(context.Background(), p, openaiModel)
				}
				if err == nil {
					t.Fatal("handoff succeeded after the plan was replaced")
				}
				calls, tokens := f.u.snapshot()
				if len(calls) != 0 || len(tokens) != 0 {
					t.Fatalf("requests after replacement: calls=%+v tokens=%+v", calls, tokens)
				}
			})
		}
	}
}

func TestVoiceBackend_DisconnectedPlanFailsClosed(t *testing.T) {
	f := newRuntimeFixture(t)
	f.commit(t, f.store, "openai", oauthCred(openaiAccess("acct", "VOICE-PLAN"), "offline-refresh", "acct"))
	t.Setenv("OPENAI_API_KEY", "offline-text-environment-key-0123456789")
	p := voiceBackendFor(t, f, voicePlanPin(t, f))
	if err := f.other.Remove("openai"); err != nil {
		t.Fatal(err)
	}
	if _, err := streamText(context.Background(), p, openaiModel); err == nil {
		t.Fatal("handoff succeeded after the plan was disconnected")
	}
	if calls, _ := f.u.snapshot(); len(calls) != 0 {
		t.Fatalf("requests after disconnect: %+v", calls)
	}
}

// A rejected plan token is refreshed once through the Store and the request
// retried with the same login and account.
func TestVoiceBackend_RefreshKeepsThePinnedLogin(t *testing.T) {
	f := newRuntimeFixture(t)
	access1 := openaiAccess("acct", "VOICE-PLAN-1")
	access2 := openaiAccess("acct", "VOICE-PLAN-2")
	f.commit(t, f.store, "openai", oauthCred(access1, "offline-refresh-1", "acct"))
	p := voiceBackendFor(t, f, voicePlanPin(t, f))
	f.u.token = func(w http.ResponseWriter, c upstreamCall, _ int) { writeTokens(w, access2, "offline-refresh-2") }
	f.u.inference = func(w http.ResponseWriter, c upstreamCall, _ int) {
		if c.Bearer == access1 {
			writeStatus(w, http.StatusUnauthorized, `{}`)
			return
		}
		writeOK(w, c)
	}
	if _, err := streamText(context.Background(), p, openaiModel); err != nil {
		t.Fatalf("handoff after refresh: %v", err)
	}
	calls, tokens := f.u.snapshot()
	if len(tokens) != 1 || len(calls) != 2 || calls[1].Bearer != access2 || calls[1].Account != "acct" || calls[1].Origin != "chatgpt.com" {
		t.Fatalf("calls=%+v tokens=%d, want one refresh and one retry on the plan", calls, len(tokens))
	}
}

func TestVoiceBackend_RefusesANonPlanPin(t *testing.T) {
	f := newRuntimeFixture(t)
	f.commit(t, f.store, "openai", apiKeyCred("offline-primary-key-0123456789"))
	pin, err := f.store.ResolveSnapshot(context.Background(), "openai")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newVoiceBackendFactory(f.store, f.u.client())(openaiModel, pin); err == nil {
		t.Fatal("voice backend accepted an API-key credential")
	}
}
