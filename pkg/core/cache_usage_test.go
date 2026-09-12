package core

import (
	"encoding/json"
	"math"
	"testing"
)

func cacheUsageTurn(input, read, written int) AgentMessage {
	return AgentMessage{Message: Message{
		Role:  "assistant",
		Usage: &Usage{Input: input, CacheRead: read, CacheWrite: written},
	}}
}

func TestSummarizeCacheUsage_AggregatesValidAssistantTurns(t *testing.T) {
	messages := []AgentMessage{
		WrapMessage(NewUserMessage("ignored")),
		cacheUsageTurn(100, 300, 50),
		{Message: Message{Role: "assistant", StopReason: "error", Usage: &Usage{Input: 1, CacheRead: 999, CacheWrite: 999}}},
		cacheUsageTurn(200, 100, 25),
	}

	got := SummarizeCacheUsage(messages)
	if !got.Available {
		t.Fatal("expected usage to be available")
	}
	if got.Read != 400 || got.Written != 75 {
		t.Fatalf("expected read=400 and written=75, got %+v", got)
	}
	if want := 400.0 / 775.0; math.Abs(got.Ratio-want) > 1e-12 {
		t.Fatalf("expected ratio %v, got %v", want, got.Ratio)
	}
}

func TestSummarizeCacheUsage_TwoWritesWithoutReadsDoesNotAlert(t *testing.T) {
	got := SummarizeCacheUsage([]AgentMessage{
		cacheUsageTurn(100, 0, 20),
		cacheUsageTurn(100, 0, 20),
	})
	if got.Alert {
		t.Fatal("two consecutive writes without reads must not alert")
	}
	if got.Streak != 2 {
		t.Fatalf("expected streak 2, got %d", got.Streak)
	}
}

func TestSummarizeCacheUsage_ThreeWritesWithoutReadsAtEndAlerts(t *testing.T) {
	got := SummarizeCacheUsage([]AgentMessage{
		cacheUsageTurn(100, 0, 20),
		cacheUsageTurn(100, 0, 20),
		cacheUsageTurn(100, 0, 20),
	})
	if !got.Alert {
		t.Fatal("three trailing writes without reads must alert")
	}
	if got.Streak != 3 {
		t.Fatalf("expected streak 3, got %d", got.Streak)
	}
}

func TestSummarizeCacheUsage_ReadAfterThreeWritesClearsAlert(t *testing.T) {
	got := SummarizeCacheUsage([]AgentMessage{
		cacheUsageTurn(100, 0, 20),
		cacheUsageTurn(100, 0, 20),
		cacheUsageTurn(100, 0, 20),
		cacheUsageTurn(100, 10, 20),
	})
	if got.Alert {
		t.Fatal("a cache read after three writes must clear the alert")
	}
	if got.Streak != 0 {
		t.Fatalf("expected streak 0 after cache read, got %d", got.Streak)
	}
}

func TestSummarizeCacheUsage_NoData(t *testing.T) {
	got := SummarizeCacheUsage([]AgentMessage{
		WrapMessage(NewUserMessage("no provider usage")),
		{Message: Message{Role: "assistant", Usage: &Usage{Output: 10}}},
	})
	if got.Available {
		t.Fatalf("expected no data, got %+v", got)
	}
}

func TestSummarizeCacheUsage_UsageJSONTags(t *testing.T) {
	var message AgentMessage
	if err := json.Unmarshal([]byte(`{"role":"assistant","content":[],"usage":{"input":10,"cache_read":30,"cache_write":5}}`), &message); err != nil {
		t.Fatal(err)
	}

	got := SummarizeCacheUsage([]AgentMessage{message})
	if !got.Available || got.Read != 30 || got.Written != 5 {
		t.Fatalf("cache usage JSON fields were not preserved: %+v", got)
	}
}
