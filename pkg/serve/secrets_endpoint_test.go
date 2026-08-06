package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
)

func secretRequest(t *testing.T, srv *httptest.Server, sessionID, body string) *http.Response {
	t.Helper()
	return apiReq(t, srv, http.MethodPost, "/api/sessions/"+sessionID+"/secrets", body)
}

func readAllAndClose(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	defer resp.Body.Close() //nolint:errcheck // test cleanup
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func messageText(message core.AgentMessage) string {
	var text strings.Builder
	for _, content := range message.Content {
		text.WriteString(content.Text)
	}
	return text.String()
}

func TestSecretsEndpointAuthAndCSRF(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("ok")))
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"secrets":[{"name":"token","value":"not-in-chat"}]}`

	auth := NewServer(mgr, WithAuthToken("owner", false))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sess.ID+"/secrets", strings.NewReader(body))
	req.Host = "localhost"
	req.Header.Set("Content-Type", "application/json")
	auth.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", rec.Code)
	}

	csrf := NewServer(mgr)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/sessions/"+sess.ID+"/secrets", strings.NewReader(body))
	req.Host = "localhost"
	req.Header.Set("Content-Type", "application/json")
	csrf.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status = %d, want 403", rec.Code)
	}
}

func TestSecretsEndpointStrictDecodingAndValidation(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"secrets":[]} {}`,
		`{"secrets":[{"name":"token","value":"safe"}],"extra":true}`,
		`{"secrets":[{"name":"../x","value":"must-not-leak"}]}`,
	} {
		resp := secretRequest(t, srv, sess.ID, body)
		data := readAllAndClose(t, resp)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("body %q: status = %d, want 400 (%s)", body, resp.StatusCode, data)
		}
		if strings.Contains(string(data), "must-not-leak") {
			t.Fatalf("validation response leaked value: %q", data)
		}
	}
}

func TestSecretsEndpointStashesAndDeliversOnlyNote(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	var logs bytes.Buffer
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })

	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	userMessages := make(chan bus.UserMessageAppended, 1)
	unsub := sess.runtime.Bus.Subscribe(func(event bus.UserMessageAppended) {
		userMessages <- event
	})
	defer unsub()
	value := "ultra-private-credential"
	resp := secretRequest(t, srv, sess.ID, `{"secrets":[{"name":"db-produccion","value":"`+value+`"},{"name":"netrc","value":"other"}]}`)
	data := readAllAndClose(t, resp)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, data)
	}
	if strings.Contains(string(data), value) {
		t.Fatalf("response leaked value: %q", data)
	}
	var got struct {
		Directory string   `json:"directory"`
		Aliases   []string `json:"aliases"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Aliases) != 2 || got.Aliases[0] != "db-produccion" || got.Aliases[1] != "netrc" {
		t.Fatalf("unexpected aliases: %#v", got.Aliases)
	}
	stored, err := os.ReadFile(filepath.Join(got.Directory, "db-produccion"))
	if err != nil || string(stored) != value {
		t.Fatalf("stored secret = %q, err = %v", stored, err)
	}
	pollUntil(t, time.Second, "secret note in history", func() bool {
		messages, err := bus.QueryTyped[bus.GetMessages, []core.AgentMessage](sess.runtime.Bus, bus.GetMessages{})
		if err != nil {
			return false
		}
		for _, message := range messages {
			if message.Custom["source"] == "secret_batch" {
				return strings.Contains(messageText(message), got.Directory) && !strings.Contains(messageText(message), value)
			}
		}
		return false
	})
	if strings.Contains(logs.String(), value) {
		t.Fatalf("logs leaked value: %q", logs.String())
	}
	select {
	case event := <-userMessages:
		if event.Custom["source"] != "secret_batch" {
			t.Fatalf("live event custom = %#v, want secret_batch", event.Custom)
		}
		aliases, ok := event.Custom["secret_aliases"].([]string)
		if !ok || len(aliases) != 2 || aliases[0] != "db-produccion" {
			t.Fatalf("live event aliases = %#v", event.Custom["secret_aliases"])
		}
	case <-time.After(time.Second):
		t.Fatal("secret batch did not emit a live user message event")
	}
	if err := mgr.Delete(sess.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(got.Directory); !os.IsNotExist(err) {
		t.Fatalf("delete did not wipe secret batch: %v", err)
	}
}

func TestSecretsEndpointQueuesWhenSessionBusy(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	slow := func(context.Context, core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 2)
		go func() {
			defer close(ch)
			time.Sleep(150 * time.Millisecond)
			message := core.Message{Role: "assistant", Content: []core.Content{core.TextContent("done")}, StopReason: "end_turn", Timestamp: time.Now().Unix()}
			ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &message}
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &message}
		}()
		return ch, nil
	}
	mgr := newTestManager(t, ctx, newMockProvider(slow))
	srv := httptest.NewServer(NewServer(mgr))
	defer srv.Close()
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	first := apiReq(t, srv, http.MethodPost, "/api/sessions/"+sess.ID+"/send", `{"text":"start"}`)
	_ = first.Body.Close()
	if first.StatusCode != http.StatusAccepted {
		t.Fatalf("first send status = %d", first.StatusCode)
	}
	pollUntil(t, time.Second, "session running", func() bool { return sess.runtime.State.Current() == bus.StateRunning })
	resp := secretRequest(t, srv, sess.ID, `{"secrets":[{"name":"token","value":"queued-private-value"}]}`)
	data := readAllAndClose(t, resp)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, data)
	}
	if strings.Contains(string(data), "queued-private-value") {
		t.Fatalf("response leaked value: %s", data)
	}
	pollUntil(t, 2*time.Second, "queued secret note", func() bool {
		messages, err := bus.QueryTyped[bus.GetMessages, []core.AgentMessage](sess.runtime.Bus, bus.GetMessages{})
		if err != nil {
			return false
		}
		for _, message := range messages {
			if message.Custom["source"] == "secret_batch" {
				return !strings.Contains(messageText(message), "queued-private-value")
			}
		}
		return false
	})
}
