package responses

import (
	"context"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func TestConsumeStream_MalformedToolArgumentsAreTerminal(t *testing.T) {
	body := strings.NewReader("data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"bash\"}}\n\ndata: {\"type\":\"response.function_call_arguments.done\",\"output_index\":0,\"arguments\":\"{bad\"}\n\n")
	ch := make(chan core.AssistantEvent, 8)
	go func() { ConsumeStream(context.Background(), body, ch, "xai", "grok-4.5"); close(ch) }()
	var ends, terminals int
	for ev := range ch {
		if ev.Type == core.ProviderEventToolCallEnd {
			ends++
		}
		if ev.IsTerminal() {
			terminals++
			if ev.Type != core.ProviderEventError {
				t.Errorf("terminal = %s", ev.Type)
			}
		}
	}
	if ends != 0 || terminals != 1 {
		t.Fatalf("tool ends=%d terminals=%d", ends, terminals)
	}
}

func TestConsumeStream_FunctionCallDoneRequiresIdentity(t *testing.T) {
	for _, item := range []string{
		`{"type":"function_call","call_id":"call_1","arguments":"{}"}`,
		`{"type":"function_call","name":"bash","arguments":"{}"}`,
	} {
		t.Run(item, func(t *testing.T) {
			body := strings.NewReader(`data: {"type":"response.output_item.done","output_index":0,"item":` + item + "}\n\n")
			ch := make(chan core.AssistantEvent, 4)
			go func() { ConsumeStream(context.Background(), body, ch, "xai", "grok-4.5"); close(ch) }()
			var ends, terminals int
			for ev := range ch {
				if ev.Type == core.ProviderEventToolCallEnd {
					ends++
				}
				if ev.IsTerminal() {
					terminals++
				}
			}
			if ends != 0 || terminals != 1 {
				t.Fatalf("tool ends=%d terminals=%d", ends, terminals)
			}
		})
	}
}
