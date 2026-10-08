package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

func newBackupAgent(t *testing.T, p *snapshotProvider, reg *core.Registry) (*agent.Agent, *session.FileStore, *session.Session) {
	t.Helper()
	a, err := agent.New(agent.AgentConfig{Provider: p, Model: p.model, Tools: reg, MaxTurns: 8, Compaction: &core.CompactionSettings{Enabled: false}})
	if err != nil {
		t.Fatal(err)
	}
	fs, err := session.NewFileStore(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	persisted := fs.Create()
	persisted.Version = 1
	a.SetProviderWaitSave(func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		persisted.Messages = a.Messages()
		return fs.Save(persisted)
	})
	return a, fs, persisted
}

func backupToolResponse(t *testing.T, body []byte, id string) string {
	t.Helper()
	var req struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	name := ""
	for _, tool := range req.Tools {
		if strings.EqualFold(tool.Name, "backup_once") {
			name = tool.Name
		}
	}
	if name == "" {
		t.Fatal("request omitted registered tool")
	}
	return fmt.Sprintf("event: message_start\ndata: {\"message\":{\"model\":\"claude-opus-5-5\",\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":%q,\"name\":%q}}\n\nevent: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{}\"}}\n\nevent: content_block_stop\ndata: {}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":2}}\n\nevent: message_stop\ndata: {}\n\n", id, name)
}

type backupDelayBody struct {
	io.ReadCloser
	delay time.Duration
}

func (b *backupDelayBody) Read(p []byte) (int, error) {
	if b.delay > 0 {
		time.Sleep(b.delay)
		b.delay = 0
	}
	return b.ReadCloser.Read(p)
}

type backupDelayTransport struct {
	wire  *backupWire
	at    int
	delay time.Duration
}

func (tr *backupDelayTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := tr.wire.RoundTrip(r)
	if err == nil && len(tr.wire.headers)-1 == tr.at {
		resp.Body = &backupDelayBody{ReadCloser: resp.Body, delay: tr.delay}
	}
	return resp, err
}

func TestBackupAdmittedWorkPacingAndNaturalRestore(t *testing.T) {
	p, _, tr, _ := backupRuntime(t)
	synctest.Test(t, func(t *testing.T) {
		tools := 0
		reg := core.NewRegistry()
		if err := reg.Register(core.Tool{Name: "backup_once", Parameters: json.RawMessage(`{"type":"object","properties":{}}`), Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
			tools++
			return core.TextResult("executed exactly once per id"), nil
		}}); err != nil {
			t.Fatal(err)
		}
		a, fs, persisted := newBackupAgent(t, p, reg)
		start := time.Now()
		reset := start.Add(time.Second)
		tr.serve = func(r *http.Request, n int) (int, http.Header, string) {
			switch n {
			case 0:
				s, h, b := backupReject("five_hour")
				h.Set("Date", start.Format(http.TimeFormat))
				h.Set("Retry-After", "1")
				h.Set("anthropic-ratelimit-unified-5h-status", "rejected")
				h.Set("anthropic-ratelimit-unified-5h-reset", strconv.FormatInt(reset.Unix(), 10))
				return s, h, b
			case 1, 2:
				return 200, http.Header{}, backupToolResponse(t, tr.bodies[n], fmt.Sprintf("tool-%d", n))
			default:
				return backupOK("end_turn")
			}
		}
		p.httpClient.Transport = &backupDelayTransport{wire: tr, at: 2, delay: 2 * time.Second}
		if _, err := a.SendWithMsgID(context.Background(), "same admitted work", "stable-user-id"); err != nil {
			t.Fatal(err)
		}
		if len(tr.headers) != 4 || tools != 2 {
			t.Fatalf("dispatches=%d tools=%d", len(tr.headers), tools)
		}
		for n, api := range []bool{false, true, true, false} {
			if (tr.headers[n].Get("X-API-Key") != "") != api {
				t.Fatalf("origin at %d: want API=%v", n, api)
			}
		}
		if time.Since(start) != 2*time.Second {
			t.Fatalf("work slept or probed outside its admitted stream: %s", time.Since(start))
		}
		users, results := 0, 0
		for _, m := range a.Messages() {
			if m.Role == "user" {
				users++
				if m.MsgID != "stable-user-id" {
					t.Fatal("user identity changed")
				}
			}
			if m.Role == "tool_result" {
				results++
			}
		}
		if users != 1 || results != 2 {
			t.Fatalf("users=%d results=%d", users, results)
		}
		if _, err := fs.Load(persisted.ID); err != nil {
			t.Fatal(err)
		}
	})
}

func TestBackupToolContinuationRevokeNeverEvadesViaOAuth(t *testing.T) {
	p, s, tr, _ := backupRuntime(t)
	reg := core.NewRegistry()
	tools := 0
	if err := reg.Register(core.Tool{Name: "backup_once", Parameters: json.RawMessage(`{"type":"object","properties":{}}`), Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
		tools++
		st, err := s.AnthropicBackupStatus()
		if err == nil {
			_, err = s.RemoveAnthropicBackup(st.Revision)
		}
		return core.TextResult("executed then revoked"), err
	}}); err != nil {
		t.Fatal(err)
	}
	a, _, _ := newBackupAgent(t, p, reg)
	tr.serve = func(_ *http.Request, n int) (int, http.Header, string) {
		if n == 0 {
			return backupReject("five_hour")
		}
		if n == 1 {
			return 200, http.Header{}, backupToolResponse(t, tr.bodies[n], "one-tool")
		}
		t.Fatal("request escaped revoked same-work origin")
		return backupOK("end_turn")
	}
	if _, err := a.Send(context.Background(), "revoke before follow-up"); err == nil || len(tr.headers) != 2 || tools != 1 {
		t.Fatalf("err=%v requests=%d tools=%d", err, len(tr.headers), tools)
	}
}

func TestBackupPrepareSaveFailurePreventsHTTP(t *testing.T) {
	p, _, tr, _ := backupRuntime(t)
	a, _, _ := newBackupAgent(t, p, core.NewRegistry())
	a.SetProviderWaitSave(func(context.Context) error { return errors.New("offline save failure") })
	tr.serve = func(*http.Request, int) (int, http.Header, string) { return backupReject("five_hour") }
	if _, err := a.Send(context.Background(), "save failure"); !errors.Is(err, core.ErrProviderWaitNotSaved) || len(tr.headers) != 1 {
		t.Fatalf("paid without durable turn: %v requests=%d", err, len(tr.headers))
	}
}
