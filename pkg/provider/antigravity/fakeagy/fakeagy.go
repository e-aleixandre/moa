// Package fakeagy is a stand-in for `agy acp` in tests. A test binary runs it
// from TestMain when FAKE_AGY=1 and points MOA_AGY_BIN at itself.
package fakeagy

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

type message struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}

// Active reports whether this process was started as the fake agy.
func Active() bool { return os.Getenv("FAKE_AGY") == "1" }

// Serve speaks the agent side of ACP on stdin/stdout: one turn per prompt
// with a tool call that needs permission. Every line it receives is appended
// to the file named by FAKE_AGY_LOG, so tests can check the wire.
func Serve() {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 1<<20), 1<<24)
	send := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Println(string(b))
	}
	update := func(u map[string]any) {
		send(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "s1", "update": u}})
	}
	var promptID json.RawMessage
	logPath := os.Getenv("FAKE_AGY_LOG")
	if logPath == "" {
		logPath = "wire.log" // one log per process, in the session's cwd
	}
	log, _ := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if log != nil {
		b, _ := json.Marshal(os.Args[1:])
		fmt.Fprintf(log, "{\"argv\":%s}\n", b)
	}
	mode := os.Getenv("FAKE_AGY_MODE")
	prompts := 0
	for in.Scan() {
		var m message
		_ = json.Unmarshal(in.Bytes(), &m)
		if log != nil {
			fmt.Fprintln(log, in.Text())
		}
		// FAKE_AGY_MODE scripts the first prompt of a process.
		if mode != "" && m.Method == "session/prompt" {
			prompts++
			switch {
			case mode == "stream" && prompts == 1:
				// A long turn: one chunk, then wait for session/cancel.
				promptID = m.ID
				update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "first"}})
				continue
			case mode == "silent" && prompts == 1:
				// A long turn that has produced nothing yet.
				promptID = m.ID
				continue
			case mode == "gate" && prompts == 1:
				// REVIEW2: two mirrored calls, each released by a file.
				id := m.ID
				go func() {
					for i, gate := range []string{"gate1", "gate2"} {
						tid := fmt.Sprintf("g%d", i)
						update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": tid, "title": "Read " + tid, "kind": "read", "status": "in_progress"})
						update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": tid, "status": "completed", "content": []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "ok"}}}})
						for {
							if _, err := os.Stat(os.Getenv("FAKE_GATE_DIR") + "/" + gate); err == nil {
								break
							}
							time.Sleep(10 * time.Millisecond)
						}
					}
					send(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"stopReason": "end_turn"}})
				}()
				continue
			case mode == "second-silent" && prompts == 2:
				// REVIEW3: the second prompt runs until session/cancel.
				promptID = m.ID
				continue
			case mode == "crash":
				os.Exit(3)
			case mode == "unknown" && prompts == 1:
				send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32603, "message": "unknown error"}})
				continue
			case mode == "tool429" && prompts == 1:
				update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "t1", "title": "Read a.go", "kind": "read", "status": "in_progress"})
				time.Sleep(300 * time.Millisecond)
				update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "t1", "status": "completed", "content": []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "package a"}}}})
				time.Sleep(200 * time.Millisecond)
				update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "partial"}})
				send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": 429, "message": "Resource exhausted"}})
				continue
			case mode == "repeat" && prompts == 1:
				for i := 0; i < 4; i++ {
					id := fmt.Sprintf("r%d", i)
					update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": id, "title": "Run git status", "kind": "execute", "status": "in_progress", "rawInput": map[string]any{"command": "git status"}})
					update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": id, "status": "completed", "content": []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "clean"}}}})
					time.Sleep(100 * time.Millisecond)
				}
				send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"stopReason": "end_turn"}})
				continue
			case mode != "":
				update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "reply"}})
				send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"stopReason": "end_turn"}})
				continue
			}
		}
		if mode != "" && m.Method == "session/cancel" && promptID != nil {
			update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "late"}})
			send(map[string]any{"jsonrpc": "2.0", "id": promptID, "result": map[string]any{"stopReason": "cancelled"}})
			promptID = nil
			continue
		}
		switch m.Method {
		case "initialize":
			send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"protocolVersion": 1}})
		case "session/resume", "session/new":
			if m.Method == "session/resume" && os.Getenv("FAKE_AGY_NORESUME") == "1" { // REVIEW2
				send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32002, "message": "conversation not found"}})
				continue
			}
			send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"sessionId": "s1", "configOptions": []any{
				map[string]any{"id": "model", "category": "model", "currentValue": "flash", "options": []any{map[string]any{"value": "flash"}, map[string]any{"value": "pro"}}},
				map[string]any{"id": "thought", "category": "thought_level", "currentValue": "low", "options": []any{map[string]any{"value": "low", "name": "Low"}, map[string]any{"value": "high", "name": "High"}, map[string]any{"value": "max", "name": "Max"}}},
			}}})
		case "session/set_config_option":
			send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{}})
		case "session/prompt":
			promptID = m.ID
			update(map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]any{"type": "text", "text": "thinking"}})
			update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "hello"}})
			update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "t1", "title": "Run ls", "kind": "execute", "status": "pending", "rawInput": map[string]any{"command": "ls"}})
			send(map[string]any{"jsonrpc": "2.0", "id": 100, "method": "session/request_permission", "params": map[string]any{
				"sessionId": "s1", "toolCall": map[string]any{"toolCallId": "t1"},
				"options": []any{map[string]any{"optionId": "yes", "kind": "allow_once"}, map[string]any{"optionId": "no", "kind": "reject_once"}},
			}})
		case "":
			// The answer to our permission request.
			var r struct {
				Outcome struct {
					OptionID string `json:"optionId"`
				} `json:"outcome"`
			}
			_ = json.Unmarshal(m.Result, &r)
			if r.Outcome.OptionID == "yes" {
				update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "t1", "status": "completed", "content": []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "file.txt"}}}})
				update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "done"}})
			} else {
				update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "t1", "status": "failed"})
				update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "denied"}})
			}
			send(map[string]any{"jsonrpc": "2.0", "id": promptID, "result": map[string]any{"stopReason": "end_turn"}})
		}
	}
}
