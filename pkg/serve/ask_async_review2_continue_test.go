package serve

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

type reviewPausedCrashProvider struct {
	f1Provider
	t       *testing.T
	session *ManagedSession
	qid     string
	answers int
	reason  string
}

func (p *reviewPausedCrashProvider) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	if !isTestAutoTitleRequest(req) && f1LastUserIsAnswer(req) {
		p.answers++
		if p.answers == 1 {
			ch := make(chan core.AssistantEvent, 2)
			msg := core.Message{Role: "assistant", Provider: "anthropic", Content: []core.Content{core.TextContent("unfinished reply")}, StopReason: p.reason, Timestamp: time.Now().Unix()}
			ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
			close(ch)
			return ch, nil
		}
		if err := p.session.runtime.CheckpointAcknowledged(ctx); err != nil {
			p.t.Fatalf("checkpoint paused reply: %v", err)
		}
		f1CrashNow(p.t, p.session.ID, p.qid)
	}
	return p.f1Provider.Stream(ctx, req)
}

func TestReviewCrashAfterUnfinishedAssistantStillOwesWork(t *testing.T) {
	if mode := os.Getenv(f1CrashChildEnv); mode != "" {
		base, root, cfgDir := os.Getenv(f1CrashBaseEnv), os.Getenv(f1CrashWorkspaceEnv), os.Getenv(f1CrashConfigEnv)
		t.Setenv("MOA_CONFIG_DIR", cfgDir)
		p := &reviewPausedCrashProvider{f1Provider: f1Provider{script: f1Script(false)}, t: t, reason: mode}
		m := f1CrashManager(base, root, cfgDir, p)
		e := &f1Env{t: t, mgr: m, prov: &p.f1Provider, root: root, srv: newF1ChildServer(m)}
		s := e.session(f1KindHuman)
		p.session = s
		e.send(s, "F1-TRIGGER please ask")
		qid := waitPendingAsk(t, s).ID
		p.qid = qid
		f1Settle(t, s)
		if code, body := f1PostAsk(t, e, s.ID, qid, "yes"); code != 204 {
			t.Fatalf("answer=%d %s", code, body)
		}
		<-time.After(30 * time.Second)
		t.Fatal("did not reach crash")
		return
	}
	for _, reason := range []string{"pause_turn", "continue"} {
		t.Run(reason, func(t *testing.T) {
			d := f1NewCrashDirs(t)
			t.Setenv("MOA_CONFIG_DIR", d.config)
			manifest := f1RunCrashChild(t, "^TestReviewCrashAfterUnfinishedAssistantStillOwesWork$", reason, d)
			saved, _, err := session.FindSessionReadOnly(manifest.Base, manifest.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			last := ""
			for _, entry := range saved.Entries {
				if entry.Type == session.EntryMessage {
					last = entry.Message.StopReason
				}
			}
			if last != reason {
				t.Fatalf("invalid fixture: saved last stop_reason=%q want %q", last, reason)
			}
			p := &f1Provider{script: f1Script(false)}
			e := f1NewEnv(t, manifest.Base, manifest.Workspace, p)
			if !f1Eventually(f1Guard, func() bool {
				return p.countWhere(func(req core.Request) bool {
					return req.Options.PromptCacheKey == core.PromptCacheKey(manifest.SessionID)
				}) > 0
			}) {
				q, err := e.mgr.tasks.GetQuestion(context.Background(), manifest.QuestionID)
				t.Fatalf("unfinished %s answer lost across restart: target provider calls=%d work=%s (err=%v, expected %s until continuation)", reason, p.countWhere(func(req core.Request) bool {
					return req.Options.PromptCacheKey == core.PromptCacheKey(manifest.SessionID)
				}), q.Work, err, tasks.WorkDue)
			}
		})
	}
}
