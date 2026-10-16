package responses

import (
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func daybreakRequest(program string) core.Request {
	return core.Request{
		Model:    core.Model{ID: "gpt-6-sol", Provider: "openai"},
		Messages: []core.Message{{Role: "user", Content: []core.Content{core.TextContent("hello")}}},
		Options:  core.StreamOptions{AccessProgram: program},
	}
}

func TestBuildRequestBody_AccessPrograms(t *testing.T) {
	body := decodeBody(t, daybreakRequest(core.DaybreakAccessProgram), Dialect{Provider: "openai", SupportsAccessPrograms: true})
	programs, ok := body["access_programs"].(map[string]any)
	if !ok || programs["cyber"] != core.DaybreakAccessProgram || len(programs) != 1 {
		t.Fatalf("access_programs = %#v, want {cyber: daybreak_blue}", body["access_programs"])
	}

	body = decodeBody(t, daybreakRequest(core.DaybreakAccessProgram), Dialect{Provider: "openai"})
	if v, ok := body["access_programs"]; ok {
		t.Fatalf("transport without support sent access_programs: %#v", v)
	}
	body = decodeBody(t, daybreakRequest(""), Dialect{Provider: "openai", SupportsAccessPrograms: true})
	if v, ok := body["access_programs"]; ok {
		t.Fatalf("standard request sent access_programs: %#v", v)
	}
}

// Encrypted reasoning produced under one program is not replayed under
// another until that is shown to be safe.
func TestReasoningNotReplayedAcrossPrograms(t *testing.T) {
	turn := core.Message{
		Role: "assistant", Provider: "openai", Model: "gpt-6-sol", RequestedModel: "gpt-6-sol",
		Content: []core.Content{
			{Type: "text", Text: "ok", TextSignature: "msg_real_1"},
			{Type: "thinking", ThinkingSignature: `{"type":"reasoning","id":"rs_1","encrypted_content":"ENC"}`},
		},
	}
	req := daybreakRequest(core.DaybreakAccessProgram)
	req.Messages = append(req.Messages, turn, core.Message{Role: "user", Content: []core.Content{core.TextContent("hello")}})
	raw, err := BuildRequestBody(req, Dialect{Provider: "openai", Model: "gpt-6-sol", SupportsAccessPrograms: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"encrypted_content":"ENC"`) {
		t.Fatalf("standard reasoning replayed into a Daybreak request: %s", raw)
	}

	turn.AccessProgram = core.DaybreakAccessProgram
	req.Messages[1] = turn
	raw, err = BuildRequestBody(req, Dialect{Provider: "openai", Model: "gpt-6-sol", SupportsAccessPrograms: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"encrypted_content":"ENC"`) {
		t.Fatalf("same-program reasoning was dropped: %s", raw)
	}

	// And the other way round: Daybreak reasoning stays out of a standard request.
	std := daybreakRequest("")
	std.Messages = append(std.Messages, turn)
	raw, err = BuildRequestBody(std, Dialect{Provider: "openai", Model: "gpt-6-sol"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"encrypted_content":"ENC"`) {
		t.Fatalf("Daybreak reasoning replayed into a standard request: %s", raw)
	}
}

// After the migration the alias's history is another model's: gpt-6-sol does
// not adopt it.
func TestMigratedAliasHistoryIsForeign(t *testing.T) {
	msg := daybreakTurn()
	msg.RequestedModel = core.LegacyDaybreakModelID
	req := daybreakRequest(core.DaybreakAccessProgram)
	req.Messages = append(req.Messages, msg, core.Message{Role: "tool_result", ToolCallID: "call_1", ToolName: "bash", Content: []core.Content{core.TextContent("hello")}})
	raw, err := BuildRequestBody(req, Dialect{Provider: "openai", Model: "gpt-6-sol", SupportsAccessPrograms: true})
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if strings.Contains(s, `"encrypted_content":"ENC"`) || strings.Contains(s, "fc_real_1") {
		t.Fatalf("gpt-6-sol replayed the retired alias's model-bound state: %s", s)
	}
	if !strings.Contains(s, "checking") {
		t.Fatalf("history text was lost: %s", s)
	}
}
