package subagent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
)

// The delegated task is the child's opening turn. A client watching a live
// child from before its first message_end used to see activity with no
// encargo: the task was appended silently (SendWithCustom) and the job's
// transcript was only refreshed on message_end, so both the live stream and
// the REST snapshot were empty until the first turn ended.
//
// These tests pin the two halves of the fix together: the announcement
// reaches subscribers, and the job's transcript ALREADY contains the message
// at the moment that announcement is forwarded — so an immediate GET can
// never miss what the event carries.
func TestParentTaskIsAnnouncedBeforeTheChildAnswers(t *testing.T) {
	release := make(chan struct{})
	provider := newMockProvider(gateResponse(nil, release, "child done"))

	var mu sync.Mutex
	announcements := 0
	var announcedMsgID string
	var messagesAtAnnounce []core.AgentMessage
	announced := make(chan struct{}, 1)

	var jobs *jobStore
	sub, _, _, jobs := newSubagentToolsWithStore(t, Config{
		DefaultModel:    core.Model{ID: "default", Provider: "mock"},
		ProviderFactory: func(model core.Model) (core.Provider, error) { return provider, nil },
		OnChildEvent: func(jobID string, inner any) {
			ev, ok := inner.(bus.UserMessageAppended)
			if !ok {
				return
			}
			mu.Lock()
			announcements++
			announcedMsgID = ev.MsgID
			// Read the job transcript from INSIDE the forwarding path: this is
			// exactly what a client that fetches the moment it sees the event
			// would observe.
			messagesAtAnnounce = jobs.messages(jobID)
			if announcements == 1 {
				announced <- struct{}{}
			}
			mu.Unlock()
		},
	})

	res, err := sub.Execute(context.Background(), map[string]any{"task": "investiga el bug", "async": true}, nil)
	if err != nil {
		t.Fatalf("start child = %+v, %v", res, err)
	}
	jobID := jobIDFromResult(t, res)

	select {
	case <-announced:
	case <-time.After(2 * time.Second):
		t.Fatal("the delegated task was never announced while the child was still running")
	}

	mu.Lock()
	msgID, snapshot := announcedMsgID, messagesAtAnnounce
	mu.Unlock()

	if msgID == "" {
		t.Fatal("announcement carries no msg_id: clients cannot dedup it against the REST copy")
	}
	// The child is still blocked in the provider, so nothing else can have
	// written this transcript: the announcement path did.
	task := findParentTask(t, snapshot)
	if task.MsgID != msgID {
		t.Fatalf("job transcript msg id = %q, announced %q", task.MsgID, msgID)
	}
	if got := textOf(core.Result{Content: task.Content}); got != "investiga el bug" {
		t.Fatalf("announced task = %q, want %q", got, "investiga el bug")
	}

	close(release)
	waitFor(t, 2*time.Second, func() bool {
		snap, ok := jobs.snapshot(jobID)
		return ok && snap.Status == statusCompleted
	})

	// The completed transcript still holds exactly one copy of the task: the
	// terminal setMessages overwrites with the child's own history rather than
	// appending a second row.
	final := jobs.messages(jobID)
	if got := countParentTasks(final); got != 1 {
		t.Fatalf("parent tasks in final transcript = %d, want 1", got)
	}
	mu.Lock()
	total := announcements
	mu.Unlock()
	if total != 1 {
		t.Fatalf("user_message announcements = %d, want 1", total)
	}
}

// A sync child streams into the parent's blocking tool call, but it is the
// same job and the same live view (it can be promoted mid-run), so its task is
// announced and recorded on the same terms.
func TestSyncParentTaskIsAnnouncedAndRecorded(t *testing.T) {
	provider := newMockProvider(textResponse("child done"))
	var mu sync.Mutex
	seen := 0
	var snapshotAtAnnounce []core.AgentMessage
	var jobs *jobStore

	sub, _, _, jobs := newSubagentToolsWithStore(t, Config{
		DefaultModel:    core.Model{ID: "default", Provider: "mock"},
		ProviderFactory: func(model core.Model) (core.Provider, error) { return provider, nil },
		OnChildEvent: func(jobID string, inner any) {
			if _, ok := inner.(bus.UserMessageAppended); !ok {
				return
			}
			mu.Lock()
			seen++
			snapshotAtAnnounce = jobs.messages(jobID)
			mu.Unlock()
		},
	})

	if _, err := sub.Execute(context.Background(), map[string]any{"task": "haz lo tuyo"}, nil); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	count, snapshot := seen, snapshotAtAnnounce
	mu.Unlock()
	if count != 1 {
		t.Fatalf("user_message announcements = %d, want 1", count)
	}
	if got := textOf(core.Result{Content: findParentTask(t, snapshot).Content}); got != "haz lo tuyo" {
		t.Fatalf("recorded task = %q, want %q", got, "haz lo tuyo")
	}
}

// A resumed child appends its new task after the replayed history, and that
// append is announced too — a resume is as live as a fresh start.
func TestResumedParentTaskIsAnnounced(t *testing.T) {
	child, err := newChildAgent(
		Config{}, newMockProvider(textResponse("done")), core.Model{ID: "m", Provider: "mock"},
		"medium", 0, "sys", core.NewRegistry(), "job-resume",
	)
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var announced []core.AgentMessage
	var order []string
	unsub := child.Subscribe(func(e core.AgentEvent) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, e.Type)
		if e.Type == core.AgentEventUserMessage {
			announced = append(announced, e.Message)
		}
	})
	defer unsub()

	seed := []core.AgentMessage{core.WrapMessage(core.NewUserMessage("previous task"))}
	if _, err := runChild(context.Background(), child, "resumed task", seed); err != nil {
		t.Fatal(err)
	}
	child.Drain(time.Second)

	mu.Lock()
	defer mu.Unlock()
	if len(announced) != 1 {
		t.Fatalf("user_message announcements = %d, want 1", len(announced))
	}
	if got := announced[0].Custom["source"]; got != "subagent_parent" {
		t.Fatalf("announced source = %#v, want subagent_parent", got)
	}
	if got := textOf(core.Result{Content: announced[0].Content}); got != "resumed task" {
		t.Fatalf("announced task = %q, want %q", got, "resumed task")
	}
	// The encargo precedes the run's own events, so a client never renders
	// child activity above the message that caused it.
	if len(order) == 0 || order[0] != core.AgentEventUserMessage {
		t.Fatalf("event order = %v, want user_message first", order)
	}
}

func findParentTask(t *testing.T, msgs []core.AgentMessage) core.AgentMessage {
	t.Helper()
	for _, m := range msgs {
		if m.Role == "user" && m.Custom["source"] == "subagent_parent" {
			return m
		}
	}
	t.Fatalf("no subagent_parent message in %#v", msgs)
	return core.AgentMessage{}
}

func countParentTasks(msgs []core.AgentMessage) int {
	n := 0
	for _, m := range msgs {
		if m.Role == "user" && m.Custom["source"] == "subagent_parent" {
			n++
		}
	}
	return n
}
