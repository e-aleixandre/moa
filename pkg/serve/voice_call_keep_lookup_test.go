package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/voice"
)

func TestVoiceHandoff_SpeechKeepsOriginalDelegation(t *testing.T) {
	f := vpNewFixture(t, "chatgpt-plan")
	entered, release := make(chan context.Context, 2), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var runs atomic.Int32
	f.voiceText.fallback = handoffOnceProvider(func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
		backend, minutes := vpBackendRequest(req)
		if !backend || minutes {
			return f.prov.Stream(ctx, req)
		}
		last := req.Messages[len(req.Messages)-1]
		if last.Role == "tool_result" {
			return simpleResponse(last.Content[0].Text), nil
		}
		n := runs.Add(1)
		entered <- ctx
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return toolCallHandlerFor(fmt.Sprintf("keep-tool-%d", n), "pass_to_session", map[string]any{"summary": "KEEP-LOOKUP-NOTE"})(ctx, req)
	})
	c := f.call()
	p := f.up.peer(t, f.provider)
	p.delegate(t, "original-D1", "Pass this useful lookup")
	var lookup context.Context
	select {
	case lookup = <-entered:
	case <-time.After(vpDeadline):
		t.Fatal("lookup did not enter")
	}
	p.delegate(t, "original-D1", "same request")
	p.delegate(t, "distinct-D2", "another useful request")
	ctx, cancel := context.WithTimeout(context.Background(), vpDeadline)
	defer cancel()
	if err := c.conn.Write(ctx, websocket.MessageBinary, vpPCM()); err != nil {
		t.Fatal(err)
	}
	p.user(t, "rephrase-final", "Let me rephrase that")
	p.emit(t, map[string]any{"type": "input_transcript.added", "item": map[string]any{"text": "Still rephrasing"}})
	c.await("partial processed during blocked lookup", func(v map[string]any) bool {
		return v["type"] == "transcript" && v["text"] == "Still rephrasing" && v["final"] == false
	})
	if lookup.Err() != nil {
		t.Fatalf("speech cancelled lookup: %v", lookup.Err())
	}
	if runs.Load() != 1 {
		t.Fatalf("duplicate or D2 ran concurrently: %d", runs.Load())
	}
	once.Do(func() { close(release) })
	// Do not send another final, permission or response command to release D1.
	pollUntil(t, vpDeadline, "D1 replay and D2 return", func() bool { return countAppends(p, "original-D1") == 2 && countAppends(p, "distinct-D2") == 1 })
	f.waitRecords("relay", 2)
	settle(t, f)
	if runs.Load() != 2 || len(f.records("relay")) != 2 {
		t.Fatalf("backend=%d effects=%d, want 2 distinct once-only jobs", runs.Load(), len(f.records("relay")))
	}
	var targets []string
	var answers []string
	for _, fr := range p.snapshot() {
		if fr["type"] == "response.create" {
			t.Fatal("invented response command")
		}
		if fr["type"] == "delegation.context.append" {
			if fr["channel"] != "speakable" {
				t.Fatalf("wrong result channel: %v", fr)
			}
			targets = append(targets, fr["delegation_item_id"].(string))
			content, _ := fr["content"].([]any)
			if len(content) != 1 {
				t.Fatal("unexpected result content")
			}
			part, _ := content[0].(map[string]any)
			text, _ := part["text"].(string)
			var result map[string]any
			if part["type"] != "input_text" || json.Unmarshal([]byte(text), &result) != nil || result["status"] != "delivered" {
				t.Fatalf("result did not return automatically: %v", fr)
			}
			answers = append(answers, text)
		}
	}
	if fmt.Sprint(targets) != "[original-D1 original-D1 distinct-D2]" {
		t.Fatalf("FIFO original targets lost: %v", targets)
	}
	if answers[0] != answers[1] || answers[0] == answers[2] {
		t.Fatal("cached D1 answer or independent D2 delivery identity changed")
	}
	c.hangup()
	vpClosed(t, p)
}

func TestVoiceHandoff_GenericSpeechDoesNotCancelWorker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &voiceCall{workCtx: ctx, workCancel: cancel, work: make(chan voiceJob, 1), workerDone: make(chan struct{}), minutesCh: make(chan struct{})}
	entered, release := make(chan struct{}), make(chan struct{})
	var active context.Context
	c.work <- voiceJob{do: func(ctx context.Context) { active = ctx; close(entered); <-release }}
	go c.worker()
	<-entered
	c.handle(voice.Event{Kind: voice.EventSpeechStarted}, nil)
	if active.Err() != nil {
		t.Fatal("generic speech cancelled active work")
	}
	close(release)
	cancel()
	<-c.workerDone
}

func TestVoiceHandoff_EndStillCancelsAndJoinsLookup(t *testing.T) {
	for _, reason := range []string{"hangup", "shutdown"} {
		t.Run(reason, func(t *testing.T) {
			f := vpNewFixture(t, "chatgpt-plan")
			entered := make(chan context.Context, 1)
			returned := make(chan struct{})
			f.voiceText.fallback = handoffOnceProvider(func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
				backend, minutes := vpBackendRequest(req)
				if !backend || minutes {
					return f.prov.Stream(ctx, req)
				}
				entered <- ctx
				<-ctx.Done()
				close(returned)
				return nil, ctx.Err()
			})
			c := f.call()
			p := f.up.peer(t, f.provider)
			p.delegate(t, "ending-D1", "a blocked lookup")
			var lookup context.Context
			select {
			case lookup = <-entered:
			case <-time.After(vpDeadline):
				t.Fatal("lookup did not enter")
			}
			f.mgr.voiceCallsMu.Lock()
			registry := f.mgr.voiceCalls[0]
			f.mgr.voiceCallsMu.Unlock()
			call, _, found := registry.lookup(c.c.ID)
			if !found || call == nil {
				t.Fatal("active controller not registered")
			}
			if reason == "hangup" {
				c.hangup()
			} else {
				f.mgr.Shutdown()
			}
			vaWaitDone(t, call)
			select {
			case <-call.workerDone:
			default:
				t.Fatal("call finished before worker joined")
			}
			select {
			case <-returned:
			default:
				t.Fatal("backend not joined")
			}
			if lookup.Err() != context.Canceled {
				t.Fatalf("ending did not cancel lookup: %v", lookup.Err())
			}
			vpClosed(t, p)
			if countAppends(p, "ending-D1") != 0 {
				t.Fatal("ended call resurrected a lookup result")
			}
			_, peers, _ := f.up.snapshot()
			if len(peers) != 1 {
				t.Fatal("ending created another call")
			}
		})
	}
}

type failingHandoffReturn struct {
	voice.Upstream
	sends int
}

func (u *failingHandoffReturn) SendHandoffResult(context.Context, string, string) error {
	u.sends++
	return errors.New("PRIVATE-APPEND-CONTENT 127.0.0.1 token")
}
func TestVoiceHandoff_AppendFailureLoggedWithoutReplay(t *testing.T) {
	for _, full := range []bool{false, true} {
		t.Run(fmt.Sprint("queue_full=", full), func(t *testing.T) {
			var logs vpBuffer
			old := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			defer slog.SetDefault(old)
			up := &failingHandoffReturn{}
			c := &voiceCall{id: "local-call", provider: voice.ChatGPTPlan, up: up, workCtx: context.Background(), work: make(chan voiceJob, 1), limits: VoiceCallLimits{Teardown: time.Second}, handoffs: map[string]*string{}}
			key, _ := c.claimHandoff("PRIVATE-DELEGATION")
			c.storeHandoff(key, "PRIVATE-ANSWER")
			job := voiceJob{handoff: &voice.Event{ItemID: "PRIVATE-DELEGATION"}, handoffKey: key, replay: true}
			if full {
				c.work <- voiceJob{}
				c.submit(job)
			} else {
				c.runJob(job)
			}
			if up.sends != 1 {
				t.Fatalf("sends=%d, want 1 without replay", up.sends)
			}
			got := logs.String()
			if !strings.Contains(got, "voice handoff return failed") || !strings.Contains(got, "reason=append_send_failed") {
				t.Fatalf("missing safe append failure warning: %s", got)
			}
			for _, secret := range []string{"PRIVATE-", "127.0.0.1", "token", key} {
				if strings.Contains(got, secret) {
					t.Errorf("unsafe warning: %s", got)
				}
			}
		})
	}
}
