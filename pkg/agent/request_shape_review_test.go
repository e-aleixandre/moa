package agent

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"reflect"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func TestReviewRequestShapeDetectsEarlyPrefixRewrite(t *testing.T) {
	t.Setenv("MOA_DEBUG_CACHE", "1")
	previous := slog.Default()
	defer slog.SetDefault(previous)
	capture := func(text string) map[string]any {
		var buf bytes.Buffer
		slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
		eph := core.NewUserMessage("inventory")
		eph.Ephemeral = true
		var shape requestShape
		shape.log(core.Request{Model: core.Model{ID: "same-model", Provider: "openai"}, System: "same system", Messages: []core.Message{core.NewUserMessage(text), eph}, Options: core.StreamOptions{PromptCacheKey: "same-key"}})
		var got map[string]any
		if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		delete(got, "time")
		return got
	}
	before, after := capture("original prefix"), capture("rewritten prefix")
	if reflect.DeepEqual(before, after) {
		t.Fatalf("early input rewrite has identical request shape: %v", before)
	}
}
