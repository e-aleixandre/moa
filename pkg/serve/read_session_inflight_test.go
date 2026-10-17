package serve

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

type rsToolOutcome struct {
	offered bool
	isError bool
	text    string
}

// Q01: the very first run that carries the explicit selection can already use
// it: the message is only in the in-flight turn, not yet synced to the tree.
// The model is scripted to call read_session as soon as it sees the message;
// assertions run on the test goroutine, never inside Stream.
func TestReadSessionFirstRunInFlightUsesTheSelection(t *testing.T) {
	for _, selected := range []bool{true, false} {
		name := "unselected"
		if selected {
			name = "selected"
		}
		t.Run(name, func(t *testing.T) {
			var targetID string
			offered := make(chan bool, 1)
			outcome := make(chan rsToolOutcome, 1)
			env := newRSEnv(t, func(req core.Request) <-chan core.AssistantEvent {
				last := lastMessage(req)
				if last.Role == "tool_result" && last.ToolName == rsTool {
					select {
					case outcome <- rsToolOutcome{isError: last.IsError, text: rsContentText(last)}:
					default:
					}
					return simpleResponse("done")
				}
				if last.Role == "user" && strings.Contains(rsContentText(last), "READ_NOW") {
					select {
					case offered <- requestHasTool(req, rsTool):
					default:
					}
					return toolCallResponse("tc-rs", rsTool, map[string]any{"session_id": targetID})
				}
				return simpleResponse("ok")
			})
			caller, target := env.session(env.root), env.target("T_SENT")
			targetID = target.ID

			var mentions any
			if selected {
				mentions = []string{target.ID}
			}
			status, body := env.send(caller.ID, "READ_NOW\n\n"+rsBlock("T", target.ID), mentions, nil)
			if status != http.StatusAccepted {
				t.Fatalf("send = %d %s", status, body)
			}
			if got := rsWait(t, offered); !got {
				t.Error("read_session was not offered to the model on the first request")
			}
			out := rsWait(t, outcome)
			rsSettle(t, caller)
			if selected && (out.isError || !strings.Contains(out.text, "T_SENT")) {
				t.Errorf("selected: tool result error=%v %q", out.isError, out.text)
			}
			if !selected && (!out.isError || strings.Contains(out.text, "T_SENT")) {
				t.Errorf("unselected: tool result error=%v %q", out.isError, out.text)
			}
		})
	}
}

// Q02/Q03: a steer accepted over HTTP grants nothing until it is delivered; one
// cancelled while queued never grants.
func TestReadSessionQueuedSteerGrantsOnlyOnDelivery(t *testing.T) {
	for _, cancelSteer := range []bool{false, true} {
		name := "delivered"
		if cancelSteer {
			name = "cancelled"
		}
		t.Run(name, func(t *testing.T) {
			gate := make(chan struct{})
			started := make(chan bool, 1)
			var once = make(chan struct{}, 1)
			env := newRSEnv(t, func(req core.Request) <-chan core.AssistantEvent {
				last := lastMessage(req)
				if last.Role == "user" && strings.Contains(rsContentText(last), "BLOCK") {
					select {
					case started <- true:
					default:
					}
					select {
					case <-gate:
					case <-time.After(20 * time.Second):
					}
				}
				return simpleResponse("ok")
			})
			caller, target := env.session(env.root), env.target("T_SENT")
			release := func() {
				select {
				case once <- struct{}{}:
					close(gate)
				default:
				}
			}
			// Registered after newRSEnv so it runs before the manager cleanup.
			t.Cleanup(release)

			if status, b := env.send(caller.ID, "BLOCK", nil, nil); status != http.StatusAccepted {
				t.Fatalf("first send = %d %s", status, b)
			}
			rsWait(t, started)
			status, body := env.send(caller.ID, "queued "+rsBlock("T", target.ID), []string{target.ID}, nil)
			var resp struct {
				Action string `json:"action"`
			}
			_ = json.Unmarshal([]byte(body), &resp)
			if status != http.StatusAccepted || resp.Action != "steer" {
				t.Fatalf("second send = %d %s, want an accepted steer", status, body)
			}
			rsDeniedNotMentioned(t, caller, target.ID, "T_SENT") // accepted is not delivered

			if cancelSteer {
				r := apiReq(t, env.srv, "POST", "/api/sessions/"+caller.ID+"/steers/cancel", `{}`)
				r.Body.Close()
			}
			release()
			rsSettle(t, caller)

			if cancelSteer {
				rsCheckReceipts(t, caller)
				rsDeniedNotMentioned(t, caller, target.ID, "T_SENT")
				return
			}
			rsCheckReceipts(t, caller, []string{target.ID})
			rsExpectRead(t, caller, target.ID, "T_SENT")
		})
	}
}

func rsContentText(m core.Message) string {
	var sb strings.Builder
	for _, c := range m.Content {
		sb.WriteString(c.Text)
	}
	return sb.String()
}

func rsWait[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for the scripted model")
		var zero T
		return zero
	}
}
