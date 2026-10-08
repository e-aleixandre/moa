package agent

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/provider/anthropic"
)

type reviewNonPersistentTransport struct{ calls int }

func (tr *reviewNonPersistentTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	tr.calls++
	_ = r.Body.Close()
	status, body := 429, `{"error":{"type":"rate_limit_error","message":"offline busy"}}`
	if tr.calls > 1 {
		status = 200
		body = "event: message_start\ndata: {\"message\":{\"id\":\"offline\",\"model\":\"claude-opus-5-5\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"done\"}}\n\nevent: content_block_stop\ndata: {\"index\":0}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {}\n\n"
	}
	return &http.Response{StatusCode: status, Header: http.Header{}, Request: r, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func TestReviewGenericRetryWithoutPersistentRuntime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tr := &reviewNonPersistentTransport{}
		p := anthropic.NewWithKind("offline-not-a-real-credential", true).WithHTTPClient(&http.Client{Transport: tr})
		model, _ := core.ResolveModel("opus")
		a, err := New(AgentConfig{Provider: p, Model: model, Tools: core.NewRegistry(), Compaction: &core.CompactionSettings{Enabled: false}})
		if err != nil {
			t.Fatal(err)
		}
		msgs, err := a.SendWithMsgID(context.Background(), "offline headless-style task", "U1")
		t.Logf("generic 429 then 200, no persistence runtime: HTTP=%d error=%v", tr.calls, err)
		if err != nil || tr.calls != 2 || core.ExtractFinalAssistantText(msgs) != "done" {
			t.Fatalf("non-quota transient no longer follows existing finite retry: requests=%d err=%v", tr.calls, err)
		}
	})
}
