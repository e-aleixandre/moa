package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestParseOpsAskIntentExactNormalizedGrammar(t *testing.T) {
	tests := []struct {
		text       string
		wantKind   opsAskKind
		wantTarget string
		ok         bool
	}{
		{"how is everything", opsAskSitrep, "", true},
		{"  HOW   ARE things ", opsAskSitrep, "", true},
		{"Cómo va todo", opsAskSitrep, "", true},
		{"situación general", opsAskSitrep, "", true},
		{"what are the blockers", opsAskBlockers, "", true},
		{"Qué está bloqueado", opsAskBlockers, "", true},
		{"status Deploy API", opsAskStatus, "Deploy API", true},
		{"estado release", opsAskStatus, "release", true},
		{"  CÓMO  VA   mi proyecto ", opsAskStatus, "mi proyecto", true},
		{"status", "", "", false},
		{"como va", "", "", false},
		{"status deploy api now", opsAskStatus, "deploy api now", true},
		{"how is deploy", "", "", false},
		{"", "", "", false},
	}
	for _, test := range tests {
		kind, target, ok := parseOpsAskIntent(test.text)
		if kind != test.wantKind || target != test.wantTarget || ok != test.ok {
			t.Errorf("parseOpsAskIntent(%q) = (%q, %q, %v), want (%q, %q, %v)", test.text, kind, target, ok, test.wantKind, test.wantTarget, test.ok)
		}
	}
}

func opsAskRequest(t *testing.T, handler http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/ops/ask", strings.NewReader(body))
	req.Host = "localhost"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Moa-Request", "1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func decodeOpsAskResponse(t *testing.T, rec *httptest.ResponseRecorder) opsAskResponse {
	t.Helper()
	var response opsAskResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	return response
}

func TestOpsAskEndpointReturnsOnlySafeOpsBriefings(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider(simpleResponseHandler("ok")))
	if _, err := mgr.CreateSession(CreateOpts{Title: "Deploy API"}); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.CreateSession(CreateOpts{Title: "duplicate"}); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.CreateSession(CreateOpts{Title: "duplicate"}); err != nil {
		t.Fatal(err)
	}
	handler := NewServer(mgr)

	for _, test := range []struct {
		text string
		kind opsAskKind
	}{
		{"how is everything", opsAskSitrep},
		{"como va todo", opsAskSitrep},
		{"what are the blockers", opsAskBlockers},
		{"qué está bloqueado", opsAskBlockers},
	} {
		rec := opsAskRequest(t, handler, `{"text":`+quoteJSON(t, test.text)+`}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("ask %q status = %d: %s", test.text, rec.Code, rec.Body.String())
		}
		response := decodeOpsAskResponse(t, rec)
		if response.Kind != test.kind || response.Briefing == nil || response.Resolution != nil {
			t.Fatalf("ask %q response = %#v", test.text, response)
		}
	}

	for _, text := range []string{"status Deploy API", "estado Deploy API", "Cómo va Deploy API"} {
		rec := opsAskRequest(t, handler, `{"text":`+quoteJSON(t, text)+`}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("ask %q status = %d: %s", text, rec.Code, rec.Body.String())
		}
		response := decodeOpsAskResponse(t, rec)
		if response.Kind != opsAskStatus || response.Resolution == nil || len(response.Resolution.Candidates) != 1 || response.Briefing == nil || len(response.Briefing.Sessions) != 1 || response.Briefing.Sessions[0].Title != "Deploy API" {
			t.Fatalf("focused ask %q response = %#v", text, response)
		}
	}
}

func TestOpsAskEndpointRequiresExactResolutionWithoutBriefing(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider(simpleResponseHandler("ok")))
	if _, err := mgr.CreateSession(CreateOpts{Title: "duplicate"}); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.CreateSession(CreateOpts{Title: "duplicate"}); err != nil {
		t.Fatal(err)
	}
	handler := NewServer(mgr)

	for _, test := range []struct {
		text       string
		candidates int
	}{
		{"status missing", 0},
		{"estado duplicate", 2},
		{"status duplic", 0},
	} {
		rec := opsAskRequest(t, handler, `{"text":`+quoteJSON(t, test.text)+`}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("ask %q status = %d: %s", test.text, rec.Code, rec.Body.String())
		}
		response := decodeOpsAskResponse(t, rec)
		if response.Kind != opsAskStatus || response.Resolution == nil || len(response.Resolution.Candidates) != test.candidates || response.Briefing != nil {
			t.Fatalf("ask %q response = %#v", test.text, response)
		}
	}
}

func TestOpsAskEndpointRejectsInvalidAndBoundedBodiesWithoutEchoingInput(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider(simpleResponseHandler("ok")))
	handler := NewServer(mgr)
	sentinel := "do-not-echo-this-input"

	tests := []struct {
		name      string
		body      string
		status    int
		wantError string
	}{
		{"empty", `{"text":"   "}`, http.StatusBadRequest, "invalid_input"},
		{"unsupported", `{"text":"please start a deployment ` + sentinel + `"}`, http.StatusBadRequest, "unsupported_input"},
		{"unknown field", `{"text":"sitrep","extra":true}`, http.StatusBadRequest, "invalid_json"},
		{"malformed", `{"text":`, http.StatusBadRequest, "invalid_json"},
		{"multiple values", `{"text":"sitrep"} {}`, http.StatusBadRequest, "invalid_json"},
		{"too many runes", `{"text":` + quoteJSON(t, strings.Repeat("é", opsAskTextLimit+1)) + `}`, http.StatusBadRequest, "invalid_input"},
		{"body too large", `{"text":"` + strings.Repeat("x", opsAskBodyLimit) + `"}`, http.StatusRequestEntityTooLarge, "body_too_large"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rec := opsAskRequest(t, handler, test.body)
			wire := rec.Body.String()
			if rec.Code != test.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, test.status, wire)
			}
			var response opsAskError
			if err := json.Unmarshal([]byte(wire), &response); err != nil {
				t.Fatal(err)
			}
			if response.Kind != "error" || response.Error != test.wantError {
				t.Fatalf("error response = %#v", response)
			}
			if strings.Contains(wire, sentinel) {
				t.Fatalf("error echoed submitted text: %s", wire)
			}
		})
	}
}

func TestOpsAskEndpointHonorsAuthCSRFAndDoesNotMutateOps(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider(simpleResponseHandler("ok")))
	if _, err := mgr.CreateSession(CreateOpts{Title: "release"}); err != nil {
		t.Fatal(err)
	}
	handler := NewServer(mgr, WithAuthToken("token", false))

	newRequest := func(withAuth, withCSRF bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/ops/ask", strings.NewReader(`{"text":"status release"}`))
		req.Host = "localhost"
		req.Header.Set("Content-Type", "application/json")
		if withAuth {
			req.AddCookie(&http.Cookie{Name: authCookieName, Value: "token"})
		}
		if withCSRF {
			req.Header.Set("X-Moa-Request", "1")
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	if rec := newRequest(true, false); rec.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	if rec := newRequest(false, true); rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing auth status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	before, versionBefore := mgr.ops.SnapshotVersion()
	rec := newRequest(true, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("authenticated ask status = %d: %s", rec.Code, rec.Body.String())
	}
	after, versionAfter := mgr.ops.SnapshotVersion()
	if versionAfter != versionBefore || !reflect.DeepEqual(after, before) {
		t.Fatalf("read-only ask mutated Ops: version %d -> %d, snapshot %#v -> %#v", versionBefore, versionAfter, before, after)
	}
	response := decodeOpsAskResponse(t, rec)
	if response.Kind != opsAskStatus || response.Resolution == nil || response.Briefing == nil {
		t.Fatalf("authenticated response = %#v", response)
	}
}

func quoteJSON(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
