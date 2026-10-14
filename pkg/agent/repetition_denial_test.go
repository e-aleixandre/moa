package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/extension"
	"github.com/e-aleixandre/moa/pkg/secretaction"
	"github.com/e-aleixandre/moa/pkg/tool"
)

func TestLoop_RealSecretDenialsTransparent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	for _, tc := range []struct {
		name       string
		batches    []string
		allows     int
		wantCards  int32
		wantErrors int32
		wantCut    bool
		warnings   []string
		browser    bool
	}{
		{name: "four_browser_denials", batches: []string{"secret", "secret", "secret", "secret"}, wantCards: 4, browser: true},
		{name: "success_then_denial", batches: []string{"secret", "secret"}, allows: 1, wantCards: 2},
		{name: "four_denials", batches: []string{"secret", "secret", "secret", "secret"}, wantCards: 4},
		{name: "four_denials_with_exempt_status", batches: []string{"secret_wait", "secret_wait", "secret_wait", "secret_wait"}, wantCards: 4},
		{name: "three_successes_still_cut_before_action", batches: []string{"secret", "secret", "secret"}, allows: 3, wantCards: 2, wantCut: true},
		{name: "errors_across_denial", batches: []string{"fail", "secret", "fail", "fail"}, wantCards: 1, wantErrors: 2, wantCut: true, warnings: []string{"fail-3"}},
		{name: "mixed_denial_and_error_still_cut", batches: []string{"mixed", "mixed", "mixed"}, wantCards: 2, wantErrors: 2, wantCut: true, warnings: []string{"fail-2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			var cards, failures atomic.Int32
			denialReported := make(chan struct{}, len(tc.batches))
			broker := secretaction.NewBroker("denial-fixture")
			broker.SetHooks(func(req secretaction.Request) {
				n := int(cards.Add(1))
				var values [][]byte
				if n <= tc.allows {
					values = [][]byte{[]byte("synthetic-unused-value")}
				}
				if err := broker.Resolve(req.ID, n <= tc.allows, values); err != nil {
					t.Errorf("resolve synthetic card: %v", err)
				}
			}, nil)
			reg := core.NewRegistry()
			secret := tool.NewRunWithSecret(tool.ToolConfig{WorkspaceRoot: dir, BashTimeout: time.Second}, broker)
			if err := reg.Register(secret); err != nil {
				t.Fatal(err)
			}
			if err := reg.Register(core.Tool{Name: "read", Parameters: []byte(`{"type":"object"}`), Effect: core.EffectReadOnly,
				Execute: func(ctx context.Context, _ map[string]any, _ func(core.Result)) (core.Result, error) {
					if tc.name == "mixed_denial_and_error_still_cut" {
						select {
						case <-denialReported:
						case <-ctx.Done():
							return core.ErrorResult(ctx.Err().Error()), nil
						}
					}
					failures.Add(1)
					return core.ErrorResult("missing file"), nil
				}}); err != nil {
				t.Fatal(err)
			}
			if err := reg.Register(readOnlyTool("bash_status")); err != nil {
				t.Fatal(err)
			}
			params := map[string]any{
				"command": "printf x >> action-count", "fields": []any{map[string]any{"label": "Synthetic", "kind": "password"}},
			}
			denialBody := "The user denied the request; the command did not run."
			if tc.browser {
				for _, name := range []string{"mcp__fixture__browser_evaluate", "mcp__fixture__browser_type"} {
					if err := reg.Register(core.Tool{Name: name, Parameters: []byte(`{"type":"object"}`), Execute: func(_ context.Context, _ map[string]any, _ func(core.Result)) (core.Result, error) {
						if name == "mcp__fixture__browser_type" {
							t.Error("browser typed after denial")
						}
						return core.TextResult(`### Result
{"origin":"https://example.invalid","type":"password","label":"Synthetic"}`), nil
					}}); err != nil {
						t.Fatal(err)
					}
				}
				secretaction.SyncBrowserFillTool(reg, broker)
				secret, _ = reg.Get(secretaction.BrowserFillToolName)
				params = map[string]any{"fields": []any{map[string]any{"target": "e1", "element": "Synthetic field", "label": "Synthetic", "kind": "password"}}}
				denialBody = "The user denied the request; nothing was filled."
			}
			var handlers []func(core.Request) (<-chan core.AssistantEvent, error)
			for i, batch := range tc.batches {
				var calls []core.Content
				if batch != "fail" {
					calls = append(calls, core.ToolCallContent(fmt.Sprintf("secret-%d", i+1), secret.Name, params))
				}
				if batch == "fail" || batch == "mixed" {
					calls = append(calls, core.ToolCallContent(fmt.Sprintf("fail-%d", i+1), "read", map[string]any{"path": "missing"}))
				}
				if batch == "secret_wait" {
					calls = append(calls, core.ToolCallContent(fmt.Sprintf("status-%d", i+1), "bash_status", nil))
				}
				handlers = append(handlers, toolBatchResponse(calls...))
			}
			handlers = append(handlers, simpleTextResponse("done"))
			provider := NewMockProvider(handlers...)
			ag, err := New(AgentConfig{Provider: provider, Model: core.Model{ID: "test"}, Tools: reg, MaxTurns: 10,
				Extensions: []extension.Extension{&testExtension{initFunc: func(api extension.API) error {
					api.OnToolResult(func(_ context.Context, name string, result core.Result, isError bool) (*core.Result, error) {
						if repeatedWarningCount(result.Content) != 0 {
							t.Error("hook saw decorated result")
						}
						if name == secret.Name && isError {
							if result.Custom["rejected"] != true {
								t.Error("hook must receive structured human rejection")
							}
							denialReported <- struct{}{}
						}
						// Hooks may replace Custom; the observed rejection must remain sticky.
						result.Custom = map[string]any{"hook_seen": true}
						return &result, nil
					})
					return nil
				}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			events := collectEvents(ag)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err = ag.Run(ctx, "exercise synthetic consent")
			if errors.Is(err, ErrDoomLoop) != tc.wantCut || (err != nil && !errors.Is(err, ErrDoomLoop)) {
				t.Errorf("run error=%v, want cutoff=%v", err, tc.wantCut)
			}
			if tc.wantCards != 0 && cards.Load() != tc.wantCards {
				t.Errorf("synthetic consent requests=%d, want %d", cards.Load(), tc.wantCards)
			}
			if failures.Load() != tc.wantErrors {
				t.Errorf("ordinary error executions=%d, want %d", failures.Load(), tc.wantErrors)
			}
			actions, readErr := os.ReadFile(filepath.Join(dir, "action-count"))
			if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
				t.Fatal(readErr)
			}
			wantActions := tc.allows
			if tc.wantCut && wantActions > 2 {
				wantActions = 2
			}
			if len(actions) != wantActions {
				t.Errorf("actual command executions=%d, want %d", len(actions), wantActions)
			}
			assertRepeatedWarningIDs(t, ag.Messages(), tc.warnings...)
			if tc.wantCut {
				for _, msg := range ag.Messages() {
					if msg.Role == "tool_result" && strings.HasSuffix(msg.ToolCallID, fmt.Sprintf("-%d", len(tc.batches))) {
						if !msg.IsError || msg.Custom["rejected"] == true || !strings.Contains(messageText(msg), "doom loop detected") {
							t.Errorf("third unexecuted call must remain synthetic doom, not observed denial: %+v", msg)
						}
					}
				}
			}
			ag.Drain(2 * time.Second)
			var endIDs []string
			for _, e := range events.snapshot() {
				if e.Type != core.AgentEventToolExecEnd {
					continue
				}
				endIDs = append(endIDs, e.ToolCallID)
				if e.Result.Custom["hook_seen"] != true {
					t.Errorf("%s lost result hook metadata", e.ToolCallID)
				}
				if e.ToolName == secret.Name && e.IsError {
					if e.Result.Custom["rejected"] != true || !e.Rejected {
						t.Errorf("%s denial missing structured result/UI rejection", e.ToolCallID)
					}
					assertRepeatedWarningContent(t, core.WrapMessage(core.Message{ToolCallID: e.ToolCallID, Content: e.Result.Content}),
						core.ErrorResult(denialBody).Content, false)
				}
			}
			assertRepeatedWarningEndEvents(t, ag, events, endIDs...)
		})
	}
}
