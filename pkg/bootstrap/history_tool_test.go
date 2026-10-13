package bootstrap

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
)

type historyCaptureProvider struct{ requests chan core.Request }

func (p historyCaptureProvider) Stream(_ context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	select {
	case p.requests <- req:
	default:
	}
	msg := core.Message{Role: "assistant", Content: []core.Content{core.TextContent("done")}, StopReason: "end_turn"}
	ch := make(chan core.AssistantEvent, 1)
	ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
	close(ch)
	return ch, nil
}

func TestHeadlessMainSessionOffersHistory(t *testing.T) {
	// cmd/moa/main.go uses BuildSession(Headless: true) and RuntimeConfig
	// directly; it never goes through serve.buildManagedSession.
	cfg := minimalConfig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg.Ctx = ctx
	cfg.Headless = true
	cfg.DisableSandbox = true
	cfg.PermissionMode = "yolo"
	cfg.MoaCfg = &core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off"}
	requests := make(chan core.Request, 1)
	cfg.Provider = historyCaptureProvider{requests: requests}
	bs, err := BuildSession(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if bs.MCPManager != nil {
		defer bs.MCPManager.Close()
	}
	rcfg := bs.RuntimeConfig()
	rcfg.SessionID, rcfg.Ctx = "headless", ctx
	rt, err := bus.NewSessionRuntime(rcfg)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if err := rt.Bus.Execute(bus.SendPrompt{Text: "Look up a fact earlier in this conversation"}); err != nil {
		t.Fatal(err)
	}
	select {
	case req := <-requests:
		var names []string
		for _, tool := range req.Tools {
			names = append(names, tool.Name)
			if tool.Name == "history" {
				return
			}
		}
		t.Fatalf("main headless request has no history tool: tools=%v", names)
	case <-time.After(5 * time.Second):
		t.Fatal("headless provider was not called")
	}
}

func TestHistoryToolReadsTheBoundTranscript(t *testing.T) {
	cfg := minimalConfig(t)
	cfg.MoaCfg = &core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off"}
	bs, err := BuildSession(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if bs.MCPManager != nil {
		defer bs.MCPManager.Close()
	}
	tool, ok := bs.ToolReg.Get("history")
	if !ok {
		t.Fatal("history is not registered")
	}
	params := map[string]any{"action": "search", "query": "canary"}
	if res, _ := tool.Execute(context.Background(), params, nil); !res.IsError {
		t.Fatalf("unbound history = %+v, want not available", res)
	}
	bs.BindTranscript(func(fn func(*core.AgentMessage)) error {
		m := core.WrapMessage(core.Message{Role: "user", MsgID: "m1", Content: []core.Content{core.TextContent("the canary port is 7413")}})
		fn(&m)
		return nil
	})
	res, err := tool.Execute(context.Background(), params, nil)
	if err != nil || res.IsError || !strings.Contains(res.Content[0].Text, "id=m1") {
		t.Fatalf("bound history = %+v, %v", res, err)
	}
}
