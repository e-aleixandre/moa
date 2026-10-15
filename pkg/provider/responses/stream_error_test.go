package responses

import (
	"context"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func streamErrorOf(t *testing.T, payload string) string {
	t.Helper()
	body := strings.NewReader("data: " + payload + "\n\n")
	ch := make(chan core.AssistantEvent, 4)
	go func() { ConsumeStream(context.Background(), body, ch, "openai", "gpt-5"); close(ch) }()
	for ev := range ch {
		if ev.Type == core.ProviderEventError {
			return ev.Error.Error()
		}
	}
	t.Fatal("no error event")
	return ""
}

func TestConsumeStream_NestedErrorCodeAndMessage(t *testing.T) {
	got := streamErrorOf(t, `{"type":"error","error":{"code":"cyber_policy","type":"invalid_request","message":"blocked by policy","param":null}}`)
	if got != "openai: cyber_policy: blocked by policy" {
		t.Fatalf("got %q", got)
	}
}

func TestConsumeStream_FlatErrorStillWorks(t *testing.T) {
	if got := streamErrorOf(t, `{"type":"error","code":"rate_limit","message":"slow down"}`); got != "openai: slow down" {
		t.Fatalf("got %q", got)
	}
	if got := streamErrorOf(t, `{"type":"error","code":"rate_limit"}`); got != "openai: rate_limit" {
		t.Fatalf("got %q", got)
	}
	if got := streamErrorOf(t, `{"type":"error"}`); got != "openai: unknown error" {
		t.Fatalf("got %q", got)
	}
}
