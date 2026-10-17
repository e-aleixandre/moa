package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/auth"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/voice"
)

// Real-model probe of the ChatGPT-plan handoff backend (3 plan calls). It
// only runs with MOA_VOICE_RELAY_PROBE=1 and uses the owner's real sign-in.
func TestVoiceRelayProbe(t *testing.T) {
	if os.Getenv("MOA_VOICE_RELAY_PROBE") != "1" {
		t.Skip("set MOA_VOICE_RELAY_PROBE=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	store := auth.NewStore(os.Getenv("MOA_VOICE_RELAY_PROBE_AUTH"))
	model, ok := core.ResolveModel(core.DefaultVoiceBackendModel)
	if !ok {
		t.Fatalf("model %q", core.DefaultVoiceBackendModel)
	}
	pin, err := store.ResolveSnapshot(ctx, "openai", auth.SelectPlanOAuth)
	if err != nil {
		t.Fatal(err)
	}
	prov, err := newVoiceBackendFactory(store, nil)(model, pin)
	if err != nil {
		t.Fatal(err)
	}
	base := "**User**: Dime las dudas de Celaria\n\n**Voice**: Hay un catálogo de 78 preguntas...\n\n" +
		"**User**: Mi jefe siempre tiene ideas raras como lo de Celaria: quiere que le permitamos hacer queries, pero yo le propuse una API que respondiera esas preguntas sin queries libres. ¿Me explico?\n"
	cases := []struct {
		name, transcript, request string
		wantPass                  bool
	}{
		{"explains", base, "El dueño explica que su jefe quiere queries libres y él propuso una API sin queries libres. ¿Sí, te explicas?", false},
		{"asks_to_pass", base + "\n**Voice**: Sí, te explicas.\n\n**User**: Vale, díselo a la sesión.\n", "díselo a la sesión", true},
		{"pass_nothing", base + "\n**Voice**: Sí.\n\n**User**: No pases nada a la sesión en toda la llamada.\n\n**Voice**: Entendido.\n\n**User**: Mi jefe quiere queries libres y le dije que no daríamos datos de restaurantes concretos.\n", "El dueño dice que su jefe quiere queries libres y que no deberíamos dar datos de restaurantes concretos.", false},
	}
	for _, c := range cases {
		input := "Called session: \"Celaria\" (session_id s1).\n\n<voice_call_transcript>\n" + c.transcript + "</voice_call_transcript>\n\nVoice model request: " + c.request
		msg, err := streamMessageProbe(ctx, prov, core.Request{Model: model, System: voice.HandoffInstructions, Messages: []core.Message{core.NewUserMessage(input)}, Tools: voice.ToolSpecs()})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		var passed bool
		var tools, text []string
		for _, part := range msg.Content {
			switch part.Type {
			case "tool_call":
				tools = append(tools, part.ToolName)
				passed = passed || part.ToolName == "pass_to_session"
			case "text":
				text = append(text, part.Text)
			}
		}
		t.Logf("%s: tools=%v text=%q", c.name, tools, strings.Join(text, " "))
		if passed != c.wantPass {
			t.Errorf("%s: pass_to_session called=%v, want %v", c.name, passed, c.wantPass)
		}
	}
}

func streamMessageProbe(ctx context.Context, prov core.Provider, req core.Request) (core.Message, error) {
	ch, err := prov.Stream(ctx, req)
	if err != nil {
		return core.Message{}, err
	}
	var final *core.Message
	var failure error
	for ev := range ch {
		switch ev.Type {
		case core.ProviderEventDone:
			final = ev.Message
		case core.ProviderEventError:
			failure = ev.Error
		}
	}
	if failure != nil {
		return core.Message{}, failure
	}
	if final == nil {
		return core.Message{}, context.Canceled
	}
	return *final, nil
}
