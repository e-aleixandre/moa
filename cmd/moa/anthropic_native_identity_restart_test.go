package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/serve"
	"github.com/e-aleixandre/moa/pkg/session"
)

func TestNativeIdentityRealSaveFailureDoesNotPay(t *testing.T) {
	p, _, _, _ := backupRuntime(t)
	tr := newNativeIdentityWire(t, p)
	a, fs, _ := newBackupAgent(t, p, core.NewRegistry())
	if err := os.RemoveAll(fs.Dir()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fs.Dir(), []byte("synthetic blocked FileStore directory"), 0600); err != nil {
		t.Fatal(err)
	}
	tr.serve = func(nativeIdentityCall, int) (int, http.Header, string) { return backupReject("five_hour") }
	if _, err := a.Send(context.Background(), "save failure"); !errors.Is(err, core.ErrProviderWaitNotSaved) || len(tr.snapshot()) != 1 {
		t.Fatalf("real save failure admitted API: err=%v HTTP calls=%d", err, len(tr.snapshot()))
	}
}

func TestNativeIdentityPreparedSnapshotRestartNoPaidAutostart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	p, _, _, _ := backupRuntime(t)
	tr := newNativeIdentityWire(t, p)
	a, fs, persisted := newBackupAgent(t, p, core.NewRegistry())
	cwd := t.TempDir()
	persisted.Metadata = map[string]any{session.MetaCWD: cwd, session.MetaModel: p.model.ID}
	var snapshot *session.Session
	a.SetProviderWaitSave(func(context.Context) error {
		persisted.Messages = a.Messages()
		if err := fs.Save(persisted); err != nil {
			return err
		}
		var err error
		snapshot, err = fs.Load(persisted.ID)
		return err
	})
	tr.serve = func(_ nativeIdentityCall, n int) (int, http.Header, string) {
		if n == 0 {
			return backupReject("seven_day")
		}
		if n == 1 {
			return 400, http.Header{}, `{"error":{"type":"invalid_request_error","message":"synthetic old 400: no automatic replay"}}`
		}
		return backupOK("end_turn")
	}
	if _, err := a.SendWithMsgID(ctx, "original work", "stable-before-restart"); err == nil || snapshot == nil || len(tr.snapshot()) != 2 {
		t.Fatalf("snapshot setup failed: err=%v HTTP calls=%d", err, len(tr.snapshot()))
	}
	users, notes := 0, 0
	tree, err := session.NewTreeFromEntries(snapshot.Entries, snapshot.LeafID)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range tree.AllMessages() {
		if m.Role == "user" && m.MsgID == "stable-before-restart" {
			users++
		}
		if m.Role == "session_event" || m.Custom["type"] == "provider_source_note" {
			notes++
		}
	}
	if users != 1 || notes != 0 || len(tree.AllMessages()) != 1 {
		data, _ := json.Marshal(snapshot)
		t.Logf("synthetic preterminal snapshot: %s", data)
		t.Fatalf("preterminal snapshot must retain the acknowledged user without a chat note: users=%d notes=%d messages=%d", users, notes, len(tree.AllMessages()))
	}
	// Restore the acknowledged preterminal snapshot in a new real FileStore
	// and Manager, not a terminal error saved after the provider returned.
	root := t.TempDir()
	restartBase := filepath.Join(root, "sessions")
	restartStore, err := session.NewFileStore(restartBase, cwd)
	if err != nil {
		t.Fatal(err)
	}
	if err := restartStore.Save(snapshot); err != nil {
		t.Fatal(err)
	}
	cfg := core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off"}
	mgr := serve.NewManager(ctx, serve.ManagerConfig{
		ProviderFactory: func(core.Model) (core.Provider, error) { return p, nil },
		DefaultModel:    p.model, WorkspaceRoot: cwd, SessionBaseDir: restartBase,
		SchedulePath: filepath.Join(root, "schedules.json"), EventsPath: filepath.Join(root, "events.json"),
		MoaCfg: cfg, ConfigLoader: func(string) core.MoaConfig { return cfg },
	})
	defer mgr.Shutdown()
	if _, err := mgr.ResumeSession(snapshot.ID); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(serve.NewServer(mgr))
	defer srv.Close()
	conn, _, err := websocket.Dial(ctx, srv.URL+"/api/sessions/"+snapshot.ID+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseNow() }()
	var wire struct {
		Type string
		Data json.RawMessage
	}
	if err := wsjson.Read(ctx, conn, &wire); err != nil {
		t.Fatal(err)
	}
	var init serve.InitData
	if err := json.Unmarshal(wire.Data, &init); err != nil {
		t.Fatal(err)
	}
	if wire.Type != "init" || init.State == "running" || init.ProviderExecution.Phase != "" {
		t.Fatalf("restart auto-resumed preparation: type=%s state=%s execution=%+v", wire.Type, init.State, init.ProviderExecution)
	}
	if len(init.Messages) != 1 || init.Messages[0].Role != "user" || init.Messages[0].MsgID != "stable-before-restart" || init.Messages[0].Custom["type"] == "provider_source_note" {
		t.Fatal("restart did not serve the original durable user without a chat note")
	}
	if len(tr.snapshot()) != 2 {
		t.Fatal("restoring/reconnecting replayed an old 400 or started API")
	}
}
