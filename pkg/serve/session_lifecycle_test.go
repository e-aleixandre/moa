package serve

import (
	"sync"
	"testing"
)

func TestDiscardCancelledNotificationReleasesSteerText(t *testing.T) {
	texts := &sync.Map{}
	text := "[subagent cancelled] Job job-1 was cancelled"
	texts.Store(text, struct{}{})

	if !discardCancelledNotification(texts, "cancelled", text) {
		t.Fatal("cancelled notification was not discarded")
	}
	if _, retained := texts.Load(text); retained {
		t.Fatal("cancelled notification remains in subagentTexts")
	}
}
