package serve

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/owner"
)

func heartbeatText(sess *ManagedSession) []string {
	var out []string
	for _, msg := range sess.History() {
		if msg.Role == "user" && msg.Custom != nil && msg.Custom["source"] == heartbeatSource {
			out = append(out, assistantText(msg))
		}
	}
	return out
}

func defaultHeartbeatSettings() owner.HeartbeatSettings {
	return owner.Owner{}.HeartbeatSettings()
}

// The whole point of a deterministic heartbeat is that a quiet project costs
// nothing: no facts, no prompt, no model.
func TestHeartbeatSaysNothingWhenNothingIsTrue(t *testing.T) {
	now := time.Now()
	settings := defaultHeartbeatSettings()
	sessions := []heartbeatSession{
		{ID: "s1", Title: "working"},
		{ID: "s2", Title: "just asked", Waiting: "asking (ask_id a1): which branch?", PendingID: "a1", Since: now.Add(-2 * time.Minute)},
		{ID: "s3", Title: "failed, and its report is the coordinator's business"},
	}
	work := []heartbeatWork{{Path: "work/fresh.md", Modified: now.Add(-24 * time.Hour)}}
	if facts := heartbeatFacts(now, settings, sessions, work); len(facts) != 0 {
		t.Fatalf("heartbeat invented facts: %+v", facts)
	}
}

// Two rules, and only two: a wait past the threshold, and work that stopped.
// Reports and failures belong to the report coordinator — a heartbeat reading
// the same outbox either duplicates the turn or, once the outbox is emptied,
// announces a failure that was already delivered.
func TestHeartbeatFactsAreTheTwoRules(t *testing.T) {
	now := time.Now()
	settings := defaultHeartbeatSettings()
	sessions := []heartbeatSession{
		{ID: "s1", Title: "blocked", Waiting: "waiting for the user to approve bash", PendingID: "perm_1", Since: now.Add(-2 * time.Hour)},
	}
	work := []heartbeatWork{{Path: "work/parked.md", Modified: now.Add(-9 * 24 * time.Hour)}}

	facts := heartbeatFacts(now, settings, sessions, work)
	if len(facts) != 2 {
		t.Fatalf("facts = %+v", facts)
	}
	joined := heartbeatMessage(facts)
	for _, want := range []string{"s1", "2h", "waiting for the user to approve bash", "work/parked.md", "9 days"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("message missing %q:\n%s", want, joined)
		}
	}
	for _, unwanted := range []string{"report", "failed and left no report", "undelivered"} {
		if strings.Contains(strings.ToLower(joined), unwanted) {
			t.Fatalf("the heartbeat spoke about reports: %q\n%s", unwanted, joined)
		}
	}
}

// The age of a wait is the age of the QUESTION. Reusing the session's last
// update made an old conversation that asks something now look like it had
// been blocked for hours, which is precisely the fact the owner acts on.
func TestHeartbeatAgeIsTheAgeOfTheQuestion(t *testing.T) {
	now := time.Now()
	settings := defaultHeartbeatSettings()
	justAsked := []heartbeatSession{{
		ID: "s1", Title: "old conversation, new question",
		Waiting: "asking (ask_id a9): which branch?", PendingID: "a9", Since: now.Add(-1 * time.Minute),
	}}
	if facts := heartbeatFacts(now, settings, justAsked, nil); len(facts) != 0 {
		t.Fatalf("a question asked a minute ago woke the owner: %+v", facts)
	}

	// The key carries the id of the ask, so answering one question and asking
	// another is news, and the same question an hour later is not.
	old := []heartbeatSession{{
		ID: "s1", Waiting: "asking (ask_id a9): which branch?", PendingID: "a9", Since: now.Add(-2 * time.Hour),
	}}
	first := heartbeatFacts(now, settings, old, nil)
	if len(first) != 1 || !strings.Contains(first[0].Key, "a9") {
		t.Fatalf("fact key does not identify the question: %+v", first)
	}
	next := heartbeatFacts(now, settings, []heartbeatSession{{
		ID: "s1", Waiting: "asking (ask_id a10): and the other one?", PendingID: "a10", Since: now.Add(-2 * time.Hour),
	}}, nil)
	if len(next) != 1 || next[0].Key == first[0].Key {
		t.Fatalf("a different question produced the same fact: %+v vs %+v", next, first)
	}
}

// The same standing fact must wake the owner once: a session that has been
// waiting since yesterday is not news every five minutes.
func TestHeartbeatRepeatsNothingAndWakesOnNewFacts(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	info, ownerSess := ownerWithSession(t, mgr, root, "Winerim")

	store, err := mgr.ownerStore()
	if err != nil {
		t.Fatal(err)
	}
	stageStaleWork(t, store.BookDir(info.CodebaseKey), "parked.md")

	service := newHeartbeatService(mgr)
	if service == nil {
		t.Fatal("heartbeat service unavailable")
	}
	service.beat(time.Now())
	pollUntil(t, 10*time.Second, "the heartbeat reaching the owner", func() bool {
		return len(heartbeatText(ownerSess)) == 1
	})
	if got := heartbeatText(ownerSess)[0]; !strings.Contains(got, "work/parked.md") {
		t.Fatalf("heartbeat body = %q", got)
	}

	// Second beat, same world: silence.
	service.beat(time.Now())
	time.Sleep(200 * time.Millisecond)
	if got := heartbeatText(ownerSess); len(got) != 1 {
		t.Fatalf("the same fact woke the owner twice: %v", got)
	}

	state := store.LoadHeartbeatState(info.CodebaseKey)
	if len(state.Announced) != 1 || state.LastBeat.IsZero() {
		t.Fatalf("heartbeat state = %+v", state)
	}
}

// A quiet project must not produce a run at all: the cost of the heartbeat is
// the promise it makes, so it is asserted at the level where a model would be
// charged, not only on the pure rule.
func TestHeartbeatWithNoFactsSendsNothingToTheOwner(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	_, ownerSess := ownerWithSession(t, mgr, root, "Winerim")

	before := len(ownerSess.History())
	service := newHeartbeatService(mgr)
	if service == nil {
		t.Fatal("heartbeat service unavailable")
	}
	service.beat(time.Now())
	time.Sleep(300 * time.Millisecond)

	if got := heartbeatText(ownerSess); len(got) != 0 {
		t.Fatalf("a quiet project woke its owner: %v", got)
	}
	if after := len(ownerSess.History()); after != before {
		t.Fatalf("a quiet beat added %d messages to the transcript", after-before)
	}
}

// The memory of what was announced is on disk, and it is read by a new
// Manager: a restart must not re-announce a week-old stale branch as news.
func TestHeartbeatDoesNotRepeatAfterAManagerRestart(t *testing.T) {
	ctx := context.Background()
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	// The same session store on both sides: a restart is the same project seen
	// by a new process, not a new project.
	sessionDir := t.TempDir()
	root := t.TempDir()

	mgr := newRestartableManager(t, ctx, sessionDir)
	info, ownerSess := ownerWithSession(t, mgr, root, "Winerim")
	store, err := mgr.ownerStore()
	if err != nil {
		t.Fatal(err)
	}
	stageStaleWork(t, store.BookDir(info.CodebaseKey), "parked.md")

	newHeartbeatService(mgr).beat(time.Now())
	pollUntil(t, 10*time.Second, "the heartbeat reaching the owner", func() bool {
		return len(heartbeatText(ownerSess)) == 1
	})
	mgr.Shutdown()

	// A second process over the same directories: the owner is resumed, the
	// fact is still true, and it must stay silent.
	restarted := newRestartableManager(t, ctx, sessionDir)
	resumed, err := restarted.ResumeSession(info.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	delivered := len(heartbeatText(resumed))
	if delivered != 1 {
		t.Fatalf("the beat did not survive the restart in the transcript: %d", delivered)
	}
	newHeartbeatService(restarted).beat(time.Now())
	time.Sleep(300 * time.Millisecond)
	if got := heartbeatText(resumed); len(got) != delivered {
		t.Fatalf("a restart re-announced an old fact: %v", got)
	}
}

// newRestartableManager builds a Manager over an explicit session directory,
// so two of them in a row are the same installation seen twice.
func newRestartableManager(t *testing.T, ctx context.Context, sessionDir string) *Manager {
	t.Helper()
	moaCfg := core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "haiku"}
	mgr := NewManager(ctx, ManagerConfig{
		ProviderFactory: func(_ core.Model) (core.Provider, error) {
			return newMockProvider(simpleResponseHandler("hello")), nil
		},
		AuxiliaryModelResolver: func(spec string) (core.Model, bool, error) {
			return core.ResolveAuxiliaryModel(spec, func(string) bool { return true })
		},
		DefaultModel:   core.Model{ID: "claude-haiku-4-5-20251001", Provider: "anthropic"},
		WorkspaceRoot:  t.TempDir(),
		MoaCfg:         moaCfg,
		ConfigLoader:   isolatedTestConfigLoader(t, moaCfg),
		SessionBaseDir: sessionDir,
		SchedulePath:   filepath.Join(t.TempDir(), "schedules.json"),
	})
	t.Cleanup(mgr.Shutdown)
	return mgr
}

// A corrupt heartbeat.json is read as an empty memory: the worst case is one
// repeated nudge, which is cheaper than refusing to beat at all.
func TestCorruptHeartbeatStateIsTreatedAsEmpty(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	info, ownerSess := ownerWithSession(t, mgr, root, "Winerim")
	store, err := mgr.ownerStore()
	if err != nil {
		t.Fatal(err)
	}
	stageStaleWork(t, store.BookDir(info.CodebaseKey), "parked.md")

	corrupt := filepath.Join(store.CodebaseDir(info.CodebaseKey), "heartbeat.json")
	if err := os.WriteFile(corrupt, []byte("{ this is not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if state := store.LoadHeartbeatState(info.CodebaseKey); len(state.Announced) != 0 || !state.LastBeat.IsZero() {
		t.Fatalf("a corrupt state was not read as empty: %+v", state)
	}

	newHeartbeatService(mgr).beat(time.Now())
	pollUntil(t, 10*time.Second, "the heartbeat reaching the owner", func() bool {
		return len(heartbeatText(ownerSess)) == 1
	})
	// And the state it writes over the corrupt file is readable again.
	if state := store.LoadHeartbeatState(info.CodebaseKey); len(state.Announced) != 1 {
		t.Fatalf("the rewritten state = %+v", state)
	}
}

// stageStaleWork writes a work file nobody has touched in a month: the
// cheapest fact to stage.
func stageStaleWork(t *testing.T, bookDir, name string) {
	t.Helper()
	workDir := filepath.Join(bookDir, "work")
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	parked := filepath.Join(workDir, name)
	if err := os.WriteFile(parked, []byte("---\ntitle: Parked\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(parked, old, old); err != nil {
		t.Fatal(err)
	}
}

func TestHeartbeatRespectsConfiguration(t *testing.T) {
	now := time.Now()
	enabled := false
	own := owner.Owner{Heartbeat: &owner.Heartbeat{Enabled: &enabled, IdleMinutes: 5, StaleDays: 2}}
	settings := own.HeartbeatSettings()
	if settings.Enabled || settings.IdleMinutes != 5 || settings.StaleDays != 2 {
		t.Fatalf("settings = %+v", settings)
	}
	// The lowered thresholds are what the test instance uses to see a beat
	// without waiting half an hour.
	sessions := []heartbeatSession{{ID: "s1", Waiting: "asking: x", PendingID: "a1", Since: now.Add(-6 * time.Minute)}}
	if facts := heartbeatFacts(now, settings, sessions, nil); len(facts) != 1 {
		t.Fatalf("lowered idle threshold ignored: %+v", facts)
	}
	// Defaults apply to an owner.json written before the field existed.
	def := owner.Owner{}.HeartbeatSettings()
	if !def.Enabled || def.IdleMinutes != owner.DefaultHeartbeatIdleMinutes || def.StaleDays != owner.DefaultHeartbeatStaleDays {
		t.Fatalf("defaults = %+v", def)
	}
}
