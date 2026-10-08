package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func TestBackupPartialAPIRetainsSource(t *testing.T) {
	p, _, tr, _ := backupRuntime(t)
	a, _, _ := newBackupAgent(t, p, core.NewRegistry())
	tr.serve = func(_ *http.Request, n int) (int, http.Header, string) {
		if n == 0 {
			return backupReject("five_hour")
		}
		s, h, b := backupOK("end_turn")
		return s, h, strings.Split(b, "event: message_delta")[0]
	}
	msgs, err := a.Send(context.Background(), "offline partial paid response")
	if !errors.Is(err, core.ErrAPIBackupUncertain) || len(tr.headers) != 2 || tr.headers[1].Get("X-API-Key") == "" {
		t.Fatalf("witness did not reach uncertain API: calls=%d err=%v", len(tr.headers), err)
	}
	var partial *core.ProviderSource
	found := false
	for _, m := range msgs {
		if m.Role == "assistant" && strings.Contains(core.ExtractAssistantText(m), "ok") {
			found = true
			partial = m.ProviderSource
		}
	}
	if !found {
		t.Fatal("no preserved partial assistant")
	}
	t.Logf("actual paid request retained partial but source=%+v", partial)
	if partial == nil || partial.Kind != "api_backup" {
		t.Fatal("partial paid assistant lost API provenance")
	}
	if partial.UsageComplete || partial.EstimatedCost != nil {
		t.Fatal("incomplete paid response fabricated complete usage")
	}
}
