package session

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/askuser"
	"github.com/e-aleixandre/moa/pkg/core"
)

func roundTripAskUserTranscript(t *testing.T, params map[string]any, result core.AgentMessage) core.AgentMessage {
	t.Helper()
	store := tempStore(t)
	sess := store.Create()
	tree := NewTree()
	tree.Append(Entry{Type: EntryMessage, Message: core.WrapMessage(core.Message{
		Role: "assistant", Content: []core.Content{core.ToolCallContent("ask-record", "ask_user", params)},
	})})
	tree.Append(Entry{Type: EntryMessage, Message: result})
	sess.Entries, sess.LeafID = tree.Snapshot()
	if err := store.Save(sess); err != nil {
		t.Fatalf("save real ask_user transcript: %v", err)
	}
	reopened, err := NewFileStore(store.Dir(), "")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := reopened.Load(sess.ID)
	if err != nil {
		t.Fatalf("load real ask_user transcript: %v", err)
	}
	restored, err := NewTreeFromEntries(loaded.Entries, loaded.LeafID)
	if err != nil {
		t.Fatal(err)
	}
	messages := restored.AllMessages()
	if len(messages) != 2 || messages[0].Role != "assistant" || messages[1].Role != "tool_result" || messages[1].ToolName != "ask_user" {
		t.Fatalf("restored transcript = %+v, want the ask_user call and result", messages)
	}
	if got := messages[0].Content[0].Arguments; !reflect.DeepEqual(got, params) {
		t.Errorf("persisted ask_user input = %#v, want %#v", got, params)
	}
	if !reflect.DeepEqual(messages[1].Content, result.Content) || messages[1].IsError != result.IsError {
		t.Errorf("persisted answer changed: got=%+v want=%+v", messages[1], result)
	}
	t.Log("saved, reopened and reconstructed the real FileStore transcript")
	return messages[1]
}

func TestAskUser_RequiresTapAndVoiceResponderRoundTripLegacy(t *testing.T) {
	for _, tt := range []struct {
		name          string
		input         string
		responderJSON string
		answer        string
	}{
		{
			name:          "requires_tap_user_with_via",
			input:         `{"requires_tap":true,"questions":[{"question":"Deploy?","options":["Yes","No"]}]}`,
			responderJSON: `{"kind":"user","via":"device:tap-fixture"}`,
			answer:        "Yes, by tap",
		},
		{
			name:          "voice_with_literal_evidence",
			input:         `{"requires_tap":false,"questions":[{"question":"¿Hoy o mañana?"}]}`,
			responderJSON: `{"kind":"voice","via":"voice","call_id":"call-roundtrip","quote":"Sí, mañana, no hoy.  Sin cambios."}`,
			answer:        "Mañana",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var params map[string]any
			if err := json.Unmarshal([]byte(tt.input), &params); err != nil {
				t.Fatal(err)
			}
			var responder askuser.Responder
			if err := json.Unmarshal([]byte(tt.responderJSON), &responder); err != nil {
				t.Fatal(err)
			}
			bridge := askuser.NewBridge()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			go func() {
				select {
				case prompt := <-bridge.Prompts():
					prompt.Response <- askuser.Reply{Answers: []string{tt.answer}, Responder: responder}
				case <-ctx.Done():
				}
			}()
			result, err := askuser.NewTool(bridge).Execute(ctx, params, nil)
			if err != nil || result.IsError {
				t.Fatalf("execute ask_user: result=%+v error=%v", result, err)
			}
			message := core.WrapMessage(core.NewToolResultMessage("ask-record", "ask_user", result.Content, result.IsError))
			message.Custom = result.Custom
			persisted := roundTripAskUserTranscript(t, params, message)
			decoded, ok := askuser.ResponderFromCustom(persisted.Custom[askuser.CustomKey])
			if !ok {
				t.Fatalf("persisted responder not recognized: %#v", persisted.Custom)
			}
			encoded, err := json.Marshal(decoded)
			if err != nil {
				t.Fatal(err)
			}
			var got, want map[string]any
			if err := json.Unmarshal(encoded, &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tt.responderJSON), &want); err != nil {
				t.Fatal(err)
			}
			stored, ok := persisted.Custom[askuser.CustomKey].(map[string]any)
			if !ok {
				t.Fatalf("FileStore responder shape = %#v, want a JSON object", persisted.Custom[askuser.CustomKey])
			}
			for key, value := range want {
				if stored[key] != value {
					t.Errorf("tool/FileStore lost responder %s: got %#v, want %#v", key, stored[key], value)
				}
				if got[key] != value {
					t.Errorf("ResponderFromCustom roundtrip lost %s: got %#v, want %#v", key, got[key], value)
				}
			}
		})
	}

	for _, tt := range []struct {
		name      string
		responder string
		text      string
		known     bool
	}{
		{name: "legacy_unknown", text: "blue"},
		{name: "legacy_user", responder: `{"kind":"user","via":"device:legacy"}`, text: "blue", known: true},
		{name: "legacy_owner", responder: `{"kind":"owner","id":"owner-legacy","name":"Serena"}`, text: "[Answered by the owner agent \"Serena\" on the user's behalf, not by the user]\nblue", known: true},
		{name: "legacy_automation", responder: `{"kind":"automation"}`, text: "[Answered by an automation on the user's behalf, not by the user]\nblue", known: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// These are the old message/input shapes, not output synthesized by
			// the new ask_user formatter under test.
			var params map[string]any
			if err := json.Unmarshal([]byte(`{"questions":[{"question":"Color?","options":["blue","green"]}]}`), &params); err != nil {
				t.Fatal(err)
			}
			customJSON := ""
			if tt.known {
				customJSON = fmt.Sprintf(`,"custom":{"ask_responder":%s}`, tt.responder)
			}
			textJSON, err := json.Marshal(tt.text)
			if err != nil {
				t.Fatal(err)
			}
			var old core.AgentMessage
			if err := json.Unmarshal([]byte(fmt.Sprintf(`{"role":"tool_result","tool_call_id":"ask-record","tool_name":"ask_user","content":[{"type":"text","text":%s}]%s}`, textJSON, customJSON)), &old); err != nil {
				t.Fatal(err)
			}
			persisted := roundTripAskUserTranscript(t, params, old)
			if !reflect.DeepEqual(persisted.Custom, old.Custom) {
				t.Errorf("legacy custom changed: got=%#v want=%#v", persisted.Custom, old.Custom)
			}
			got, known := askuser.ResponderFromCustom(persisted.Custom[askuser.CustomKey])
			var want askuser.Responder
			if tt.known {
				if err := json.Unmarshal([]byte(tt.responder), &want); err != nil {
					t.Fatal(err)
				}
			}
			if known != tt.known || !reflect.DeepEqual(got, want) {
				t.Errorf("legacy responder = %+v (known=%v), want %+v (known=%v)", got, known, want, tt.known)
			}
		})
	}
}
