package agent

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"reflect"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func TestRequestShapeReportsPrefixBreak(t *testing.T) {
	t.Setenv("MOA_DEBUG_CACHE", "1")
	var s requestShape
	a, b, c, x := core.NewUserMessage("a"), core.NewUserMessage("b"), core.NewUserMessage("c"), core.NewUserMessage("X")
	s.log(core.Request{Messages: []core.Message{a, b}})
	if s.items != 2 {
		t.Fatalf("items = %d", s.items)
	}
	if atPrev, _ := messagesChain([]core.Message{a, b, c}, s.items); atPrev != s.chain {
		t.Fatal("an appended request must have the previous one as prefix")
	}
	if atPrev, _ := messagesChain([]core.Message{a, x, c}, s.items); atPrev == s.chain {
		t.Fatal("a rewrite must break the prefix")
	}
}

func TestMessagesChainFailsClosedOnUnserializable(t *testing.T) {
	bad := core.NewUserMessage("x")
	bad.Content = append(bad.Content, core.Content{Type: "tool_call", Arguments: map[string]any{"f": func() {}}})
	if at, all := messagesChain([]core.Message{bad}, 0); at != "" || all != "" {
		t.Fatalf("got %q %q, want empty", at, all)
	}
}

func TestRequestShapeSurvivesUnserializableHistory(t *testing.T) {
	t.Setenv("MOA_DEBUG_CACHE", "1")
	previous := slog.Default()
	defer slog.SetDefault(previous)
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("request-shape logging must survive the fail-closed result: %v", r)
		}
	}()
	var s requestShape
	s.log(core.Request{Messages: []core.Message{unserializableMessage()}})
	s.log(core.Request{Messages: []core.Message{unserializableMessage()}})
	if s.chain != "" {
		t.Fatalf("an invalid fingerprint was kept as the previous chain: %q", s.chain)
	}
}

func unserializableMessage() core.Message {
	bad := core.NewUserMessage("x")
	bad.Content = append(bad.Content, core.Content{Type: "tool_call", Arguments: map[string]any{"f": func() {}}})
	return bad
}

func TestReviewRequestShapeDetectsEarlyPrefixRewrite(t *testing.T) {
	t.Setenv("MOA_DEBUG_CACHE", "1")
	previous := slog.Default()
	defer slog.SetDefault(previous)
	capture := func(text string) map[string]any {
		var buf bytes.Buffer
		slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
		var shape requestShape
		shape.log(core.Request{Model: core.Model{ID: "same-model", Provider: "openai"}, System: "same system", Messages: []core.Message{core.NewUserMessage(text), core.NewUserMessage("tail")}, Options: core.StreamOptions{PromptCacheKey: "same-key"}})
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
