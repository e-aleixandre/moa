package serve

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The minutes the UI draws come from structured lists, not from re-parsing
// the text the model reads: an item with line breaks must arrive whole.
func TestVoiceMinutes_StructuredItemsReachMessagesAndHistory(t *testing.T) {
	f := vpNewFixture(t, "grok-plan", vpBriefHandlers()...)
	c := f.call()
	p := f.up.peer(t, f.provider)
	vpAwaitBrief(t, f, p)
	multi := "MULTI-FIRST: prepare\nMULTI-SECOND: - never deploy\nStill open:"
	p.tool(t, "pass", "pass_to_session", map[string]any{"summary": multi})
	if vpError(p.result(t, "pass")) {
		t.Fatal("pass failed")
	}
	relay := f.waitRecords("relay", 1)[0]
	deliveryID, _ := relay.Custom["delivery_id"].(string)
	p.tool(t, "end", "end_call", map[string]any{"decided": []string{"D1\nD1-more"}, "passed": []string{}, "open": []string{"O1\nO1-more"}})
	f.waitRecords("minutes", 1)
	c.hangup()

	type minutes struct {
		Decided []string `json:"decided"`
		Passed  []struct {
			DeliveryID string `json:"delivery_id"`
			Summary    string `json:"summary"`
		} `json:"passed"`
		Open []string `json:"open"`
	}
	check := func(path string, got minutes) {
		if len(got.Decided) != 1 || got.Decided[0] != "D1\nD1-more" ||
			len(got.Open) != 1 || got.Open[0] != "O1\nO1-more" ||
			len(got.Passed) != 1 || got.Passed[0].Summary != multi || got.Passed[0].DeliveryID != deliveryID {
			t.Errorf("%s structured minutes=%+v", path, got)
		}
	}
	raw := f.mustRequest("GET", "/api/sessions/"+f.sess.ID+"/messages", nil, f.owner(), 200)
	var msgs struct {
		Messages []struct {
			Kind    string   `json:"kind"`
			Minutes *minutes `json:"minutes"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &msgs); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range msgs.Messages {
		if m.Kind == "minutes" {
			found = true
			if m.Minutes == nil {
				t.Fatalf("/messages minutes has no structured field: %s", raw)
			}
			check("/messages", *m.Minutes)
		}
	}
	if !found {
		t.Fatal("no minutes in /messages")
	}
	raw = f.mustRequest("GET", "/api/sessions/"+f.sess.ID+"/history", nil, f.owner(), 200)
	if !strings.Contains(string(raw), `"minutes":{`) {
		t.Fatalf("/history minutes has no structured field")
	}
	var hist struct {
		Messages []struct {
			Custom map[string]json.RawMessage `json:"custom"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &hist); err != nil {
		t.Fatal(err)
	}
	for _, m := range hist.Messages {
		if string(m.Custom["kind"]) == `"minutes"` {
			var got minutes
			if err := json.Unmarshal(m.Custom["minutes"], &got); err != nil {
				t.Fatalf("history minutes: %v", err)
			}
			check("/history", got)
			return
		}
	}
	t.Fatalf("no minutes in /history: %s", raw)
}

// The live announcement and the history replay share one projection; the
// structured items must survive it typed (live) and decoded (from disk), and
// an older record without them must project without the field.
func TestVoiceMinutes_WSProjectionKeepsStructuredItems(t *testing.T) {
	live := map[string]any{"source": "voice_call", "kind": "minutes", "call_id": "c", "minutes": voiceMinutesItems{Decided: []string{"a\nb"}, Passed: []voiceMinutesPassed{{DeliveryID: "d", Summary: "s\nt"}}}}
	disk := map[string]any{"source": "voice_call", "kind": "minutes", "call_id": "c", "minutes": map[string]any{"decided": []any{"a\nb"}, "passed": []any{map[string]any{"delivery_id": "d", "summary": "s\nt"}}, "open": []any{}}}
	for name, custom := range map[string]map[string]any{"live": live, "disk": disk} {
		raw, err := json.Marshal(projectWSMessageCustom(custom)["minutes"])
		if err != nil || string(raw) != `{"decided":["a\nb"],"passed":[{"delivery_id":"d","summary":"s\nt"}],"open":[]}` {
			t.Errorf("%s projection=%s err=%v", name, raw, err)
		}
	}
	if _, has := projectWSMessageCustom(map[string]any{"source": "voice_call", "kind": "minutes", "call_id": "c"})["minutes"]; has {
		t.Error("old record gained a minutes field")
	}
}

// The live user_message frame goes through the bus bridge before serve
// projects it: the structured lists must survive that hop, or the UI re-parses
// text right after the call and shows part of Passed as Still open.
func TestVoiceMinutes_StructuredItemsReachLiveSocket(t *testing.T) {
	f := vpNewFixture(t, "grok-plan", vpBriefHandlers()...)
	sock := f.sessionSocket()
	c := f.call()
	p := f.up.peer(t, f.provider)
	vpAwaitBrief(t, f, p)
	p.tool(t, "multiline", "pass_to_session", map[string]any{"summary": "First line\nStill open:\n- not a separate section"})
	if vpError(p.result(t, "multiline")) {
		t.Fatal("control: pass failed")
	}
	p.tool(t, "end", "end_call", map[string]any{"decided": []string{"D1\nD2"}, "passed": []string{}, "open": []string{}})
	f.waitRecords("minutes", 1)
	c.hangup()
	var frame map[string]any
	pollUntil(t, 3*time.Second, "live minutes event", func() bool {
		frame = sock.find(func(v map[string]any) bool {
			data, _ := v["data"].(map[string]any)
			custom, _ := data["custom"].(map[string]any)
			return v["type"] == "user_message" && custom["kind"] == "minutes"
		})
		return frame != nil
	})
	custom := frame["data"].(map[string]any)["custom"].(map[string]any)
	minutes, _ := custom["minutes"].(map[string]any)
	passed, _ := minutes["passed"].([]any)
	open, _ := minutes["open"].([]any)
	decided, _ := minutes["decided"].([]any)
	if len(passed) != 1 || len(open) != 0 || len(decided) != 1 || decided[0] != "D1\nD2" {
		t.Fatalf("live minutes frame lost structured lists: %s", vpJSON(frame))
	}
	if !strings.Contains(vpJSON(passed[0]), "Still open:") {
		t.Fatalf("passed item not whole: %s", vpJSON(passed[0]))
	}
}
