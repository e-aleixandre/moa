package serve

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/owner"
)

// The Cellaria call of 17/10: the session's first answer said the API stays
// aggregated, "sin [...] identidades", yet a later handoff reasoned without
// it. The ChatGPT-plan backend starts every delegation from scratch, so what
// the call already learned (the brief, the session's answers, the book files
// read) must reach each handoff as data, even when the voice never said it
// aloud.
func TestVoiceHandoff_LaterHandoffKeepsBriefAndSessionAnswersAsData(t *testing.T) {
	f := vpNewFixture(t, "chatgpt-plan")
	f.voiceText.fallback = handoffOnceProvider(func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
		if backend, _ := vpBackendRequest(req); backend {
			return simpleResponse("Cubrimos lo agregado; lo nominal no."), nil
		}
		last := vpLastUser(req)
		switch {
		case strings.Contains(last, "<voice_brief>"):
			return simpleResponse("BRIEF-CELLARIA: mapa de las 78 entregado."), nil
		case strings.Contains(last, "<voice_question>"):
			return simpleResponse("API de consultas fijas y agregadas, SIN-IDENTIDADES-NI-DISTRIBUIDORES."), nil
		}
		return f.prov.Stream(ctx, req)
	})
	c := f.call()
	p := f.up.peer(t, f.provider)
	p.await(t, "brief injected", func(v map[string]any) bool { return strings.Contains(vpJSON(v), "BRIEF-CELLARIA") })

	p.tool(t, "ask-topic", "ask_session", map[string]any{"question": "Topic context for Cellaria"})
	if v := p.result(t, "ask-topic"); vpError(v) {
		t.Fatalf("ask_session failed: %v", v)
	}
	p.await(t, "answer injected", func(v map[string]any) bool { return strings.Contains(vpJSON(v), "SIN-IDENTIDADES") })

	p.user(t, "u-cover", "¿Y cuánto de las 78 cubrimos con la API?")
	p.delegate(t, "item-cover", "COVER-REQUEST ¿Cuánto de las 78 cubrimos con la API?")
	p.result(t, "item-cover")
	c.hangup()

	inputs := f.voiceText.backendInputs()
	var second string
	for _, in := range inputs {
		if strings.Contains(in, "Latest request") && strings.Contains(in, "COVER-REQUEST") {
			second = in
		}
	}
	if second == "" {
		t.Fatalf("second handoff not seen in %d backend inputs", len(inputs))
	}
	data, ok := between(second, "<voice_call_data>", "</voice_call_data>")
	if !ok {
		t.Fatalf("second handoff carries no call data block:\n%s", second)
	}
	for _, want := range []string{"BRIEF-CELLARIA", "Topic context for Cellaria", "SIN-IDENTIDADES-NI-DISTRIBUIDORES"} {
		if !strings.Contains(data, want) {
			t.Errorf("call data lost %q:\n%s", want, data)
		}
	}
	if !strings.Contains(data, "not the owner's words") {
		t.Errorf("call data is not marked as data:\n%s", data)
	}
	if transcript, _ := between(second, "<voice_call_transcript>", "</voice_call_transcript>"); strings.Contains(transcript, "SIN-IDENTIDADES") {
		t.Errorf("session answer presented inside the owner's call transcript")
	}
	if !strings.Contains(f.log.String(), "voice handoff") || !strings.Contains(f.log.String(), "voice brief injected") {
		t.Errorf("handoff/brief traces missing:\n%s", f.log.String())
	}
	if strings.Contains(f.log.String(), "SIN-IDENTIDADES") {
		t.Errorf("trace leaked call content")
	}
}

// A book file the backend read in one handoff is the topic context the next
// handoff reasons with.
func TestVoiceHandoff_LaterHandoffKeepsBookReadAsData(t *testing.T) {
	f := vpNewFixture(t, "chatgpt-plan")
	vpSeedOwnerBook(t, f, "work/cellaria.md", "# Cellaria\nDECISION-SIN-NOMBRES-DE-RESTAURANTES")
	c := f.call()
	p := f.up.peer(t, f.provider)
	p.tool(t, "read-ficha", "book_read", map[string]any{"path": "work/cellaria.md"})
	if v := p.result(t, "read-ficha"); vpError(v) {
		t.Fatalf("book_read failed: %v", v)
	}
	p.delegate(t, "item-next", "PROPOSE-NEXT-STEP")
	p.result(t, "item-next")
	c.hangup()
	var next string
	for _, in := range f.voiceText.backendInputs() {
		if strings.Contains(in, "Latest request") && strings.Contains(in, "PROPOSE-NEXT-STEP") {
			next = in
		}
	}
	data, _ := between(next, "<voice_call_data>", "</voice_call_data>")
	if !strings.Contains(data, "DECISION-SIN-NOMBRES-DE-RESTAURANTES") || !strings.Contains(data, "work/cellaria.md") {
		t.Fatalf("next handoff lost the book file read before:\n%s", next)
	}
}

func between(s, open, close string) (string, bool) {
	i := strings.Index(s, open)
	if i < 0 {
		return "", false
	}
	rest := s[i+len(open):]
	j := strings.Index(rest, close)
	if j < 0 {
		return "", false
	}
	return rest[:j], true
}

// vpSeedOwnerBook makes the called session an owner whose book holds path
// with content.
func vpSeedOwnerBook(t *testing.T, f *vpFixture, path, content string) {
	t.Helper()
	info, err := f.mgr.CreateOwner(CreateOwnerOpts{Root: t.TempDir(), Name: "Winerim"})
	if err != nil {
		t.Fatal(err)
	}
	f.sess, _ = f.mgr.Get(info.SessionID)
	store, err := owner.Default()
	if err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(store.BookDir(info.CodebaseKey), path)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
