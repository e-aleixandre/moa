package serve

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

// saveOutcome writes one terminal transcript to a session's sidecar store,
// together with the launch row that anchors its restored card. Both exist in
// any real session: the subagent tool tags its own tool result with the job id
// (subagent.go#taggedWithJob) before the child can reach a terminal state.
func saveOutcome(t *testing.T, sess *ManagedSession, transcript session.SubagentTranscript) {
	t.Helper()
	store := sess.persister.subagentStore(sess.ID)
	if store == nil {
		t.Fatal("session has no subagent store")
	}
	if err := store.Save(transcript); err != nil {
		t.Fatalf("save transcript %q: %v", transcript.JobID, err)
	}
	appendSubagentLaunch(t, sess, transcript.JobID)
}

// appendSubagentLaunch records the tagged tool result the subagent tool writes
// when it spawns a child. The init payload only sends a card whose launch row
// it is also sending, so a test that skips this gets no outcome at all.
func appendSubagentLaunch(t *testing.T, sess *ManagedSession, jobID string) {
	t.Helper()
	sess.runtime.Context().Tree.Append(session.Entry{Type: session.EntryMessage, Message: core.AgentMessage{
		Message: core.Message{
			MsgID:      "launch-" + jobID,
			Role:       "tool_result",
			ToolName:   "subagent",
			ToolCallID: "call-" + jobID,
			Content:    []core.Content{{Type: "text", Text: "Subagent started in background."}},
			Timestamp:  time.Now().Unix(),
		},
		Custom: map[string]any{"subagent_job_id": jobID},
	}})
}

// TestBuildInitData_TotalPayloadStaysWithinBudget is the aggregate invariant
// behind initPayloadMaxBytes, and the reason it exists: every individual
// section of the init payload had its own bound, yet the payload as a whole had
// none, so each new section (subagent outcomes, live children) silently added
// megabytes until a mobile client needed ~80s on 3G to open a conversation.
//
// This test asserts the only property that survives future changes: whatever
// InitData carries, the ENCODED snapshot fits the budget. A new unbounded field
// fails here even if nobody remembers to test that field specifically.
func TestBuildInitData_TotalPayloadStaysWithinBudget(t *testing.T) {
	mgr := newTestManager(t, t.Context(), newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	// A worst case taken from production: a long-running orchestration session.
	// Tasks are deliberately huge too — they are model-authored and were
	// unbounded, so fifty briefs could blow the budget on their own.
	fatResult := strings.Repeat("subagent report paragraph. ", 4000) // ~108 KB each
	fatTask := strings.Repeat("investigate the payload size regression in detail. ", 2000)
	base := time.Now().Add(-200 * time.Hour)
	for i := range 200 {
		saveOutcome(t, sess, session.SubagentTranscript{
			JobID:      fmt.Sprintf("outcome-%03d", i),
			Task:       fatTask,
			Status:     "completed",
			Async:      true,
			Result:     fatResult,
			FinishedAt: base.Add(time.Duration(i) * time.Hour),
		})
	}

	// Ten live children, each holding a large transcript of its own.
	tree := sess.runtime.Context().Tree
	for i := range 60 {
		tree.Append(session.Entry{Type: session.EntryMessage, Message: core.WrapMessage(core.Message{
			Role: "assistant", MsgID: fmt.Sprintf("parent-%02d", i),
			Content: []core.Content{core.TextContent(strings.Repeat("parent transcript line. ", 500))},
		})})
	}

	data := buildInitData(sess, bus.StreamingAggregate{}, nil, "")
	// buildInitData reads live children from the bus; inject them directly so
	// the assertion covers the projection actually used by the WS handler. The
	// block-heavy shape is the adversarial one: many blocks, each individually
	// under the per-block cap.
	data.Subagents = liveSubagentInitData(blockHeavyLiveSubagents(10, 15))

	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal init: %v", err)
	}
	if len(encoded) > initPayloadMaxBytes {
		t.Fatalf("init payload = %d bytes, budget is %d. A section of InitData is unbounded: "+
			"bound it (excerpt, cap, or shared budget) rather than raising the budget.",
			len(encoded), initPayloadMaxBytes)
	}
	// Guard the guard: a payload that collapsed to nothing would pass the
	// budget while having lost the state a reconnect needs.
	if len(data.SubagentOutcomes) == 0 || len(data.Messages) == 0 || len(data.Subagents) == 0 {
		t.Fatalf("init dropped state entirely: %d outcomes, %d messages, %d subagents",
			len(data.SubagentOutcomes), len(data.Messages), len(data.Subagents))
	}
}

// fatLiveSubagents builds count running children, each with msgs large messages.
func fatLiveSubagents(count, msgs int) []bus.SubagentSnapshot {
	out := make([]bus.SubagentSnapshot, 0, count)
	for i := range count {
		messages := make([]core.AgentMessage, 0, msgs)
		for j := range msgs {
			messages = append(messages, core.WrapMessage(core.Message{
				Role: "assistant", MsgID: fmt.Sprintf("live-%d-%d", i, j),
				Content: []core.Content{core.TextContent(strings.Repeat("child transcript line. ", 800))},
			}))
		}
		out = append(out, bus.SubagentSnapshot{
			JobID: fmt.Sprintf("live-%d", i), Task: "live work", Status: "running",
			Async: true, Messages: messages, ContextPercent: 10,
		})
	}
	return out
}

// blockHeavyLiveSubagents builds count children whose newest message carries
// many text blocks, each just under historyContentMaxBytes. This is the shape
// that defeated the first implementation of the shared budget: every individual
// block was under the per-block cap, the message was never dropped (it is the
// newest one, which must never be omitted), and nothing trimmed it down to the
// quota — ten children shipped 9.4 MiB against a "512 KiB budget".
func blockHeavyLiveSubagents(count, blocks int) []bus.SubagentSnapshot {
	content := make([]core.Content, blocks)
	for i := range content {
		content[i] = core.TextContent(strings.Repeat("x", historyContentMaxBytes-100))
	}
	out := make([]bus.SubagentSnapshot, 0, count)
	for i := range count {
		out = append(out, bus.SubagentSnapshot{
			JobID: fmt.Sprintf("blocky-%d", i), Task: "block heavy", Status: "running", Async: true,
			Messages: []core.AgentMessage{core.WrapMessage(core.Message{
				Role: "assistant", MsgID: fmt.Sprintf("blocky-msg-%d", i), Content: content,
			})},
		})
	}
	return out
}

// TestLiveSubagentInitData_EnforcesBudgetOnBlockHeavyMessages is the regression
// test for that hole. The per-child quota must be ENFORCED on the projected
// message, not merely computed: a single message of many sub-cap blocks has to
// come back trimmed, and it must still come back (dropping the newest message
// of a live child is not an acceptable way to meet a budget).
func TestLiveSubagentInitData_EnforcesBudgetOnBlockHeavyMessages(t *testing.T) {
	for _, children := range []int{10, 20} {
		for _, blocks := range []int{1, 5, 15} {
			t.Run(fmt.Sprintf("%d children/%d blocks", children, blocks), func(t *testing.T) {
				projected := liveSubagentInitData(blockHeavyLiveSubagents(children, blocks))
				total := 0
				for _, sa := range projected {
					size := encodedHistorySize(sa.Messages)
					total += size
					if len(sa.Messages) == 0 {
						t.Fatalf("child %q lost its newest message; degrade it, do not drop it", sa.JobID)
					}
					if size > initSubagentHistoryMinBytes*2 {
						t.Errorf("child %q emitted %d bytes, well over its quota", sa.JobID, size)
					}
				}
				// The floor wins over the target when both cannot hold (see the
				// constants' comment), so the real ceiling is whichever is larger.
				ceiling := max(initSubagentHistoryMaxBytes, children*initSubagentHistoryMinBytes)
				if total > ceiling {
					t.Fatalf("%d children emitted %d bytes, ceiling is %d", children, total, ceiling)
				}
			})
		}
	}
}

// TestSanitizeHistoryRangeDegradesOversizedMessagesToTheBudget covers the
// mechanism directly: the trimming must respect the budget it is GIVEN, not the
// fixed initHistoryMaxBytes, and must preserve the message's identity so the
// client can still address the row.
func TestSanitizeHistoryRangeDegradesOversizedMessagesToTheBudget(t *testing.T) {
	blocks := make([]core.Content, 12)
	for i := range blocks {
		blocks[i] = core.TextContent(strings.Repeat("y", historyContentMaxBytes-100))
	}
	message := core.WrapMessage(core.Message{Role: "assistant", MsgID: "huge", Content: blocks})

	const budget = 40 << 10
	bounded := sanitizeHistoryRange([]core.AgentMessage{message}, budget)
	if len(bounded) != 1 {
		t.Fatalf("got %d messages, want the message degraded rather than dropped", len(bounded))
	}
	if size := encodedMessageSize(bounded[0]); size > budget {
		t.Fatalf("degraded message = %d bytes, budget is %d", size, budget)
	}
	if bounded[0].MsgID != "huge" || bounded[0].Role != "assistant" {
		t.Fatalf("degraded message lost its identity: %+v", bounded[0].Message)
	}
	joined := ""
	for _, block := range bounded[0].Content {
		joined += block.Text
	}
	if !strings.Contains(joined, "truncated on this device") {
		t.Fatalf("degraded message does not tell the user content was omitted: %q", joined[:min(200, len(joined))])
	}
}

func TestLiveSubagentInitData_SharesOneAggregateBudget(t *testing.T) {
	projected := liveSubagentInitData(fatLiveSubagents(10, 40))
	if len(projected) != 10 {
		t.Fatalf("projected %d children, want 10", len(projected))
	}
	total := 0
	for _, sa := range projected {
		total += encodedHistorySize(sa.Messages)
		if len(sa.Messages) == 0 {
			t.Fatalf("child %q lost its whole transcript", sa.JobID)
		}
	}
	// The floor is per child, so the aggregate can exceed the nominal budget by
	// at most one floor's worth on the last child.
	if limit := initSubagentHistoryMaxBytes + initSubagentHistoryMinBytes; total > limit {
		t.Fatalf("live subagent transcripts = %d bytes, shared budget is %d", total, limit)
	}
}

// A terminal SYNC child is gone from the live registry by the time the parent
// reloads, so the init snapshot lists no live subagent for it and the outcome
// is the only carrier of its identity. Without the title there, the restored
// card degrades to the task heading (or the model) even though the sidecar and
// the REST endpoints have the real title.
func TestBuildInitData_OutcomeCarriesPersistedTitle(t *testing.T) {
	mgr := newTestManager(t, t.Context(), newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	saveOutcome(t, sess, session.SubagentTranscript{
		JobID:      "sa-titled",
		Task:       "\n# Delivery review QA\n\nVerify the promised work.",
		Title:      "Review delivered files",
		Status:     "completed",
		Result:     "looks good",
		FinishedAt: time.Now(),
	})

	data := buildInitData(sess, bus.StreamingAggregate{}, nil, "")
	if len(data.Subagents) != 0 {
		t.Fatalf("terminal sync child must not appear as live: %+v", data.Subagents)
	}
	if len(data.SubagentOutcomes) != 1 {
		t.Fatalf("outcomes = %+v", data.SubagentOutcomes)
	}
	if got := data.SubagentOutcomes[0].Title; got != "Review delivered files" {
		t.Fatalf("outcome title = %q, want the persisted title", got)
	}
}

func TestBuildInitData_OutcomeResultsAreExcerpted(t *testing.T) {
	mgr := newTestManager(t, t.Context(), newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("z", subagentOutcomeExcerptMaxBytes*3)
	short := "small enough to travel whole"
	saveOutcome(t, sess, session.SubagentTranscript{
		JobID: "long-result", Status: "completed", Result: long, FinishedAt: time.Now().Add(-2 * time.Hour),
	})
	saveOutcome(t, sess, session.SubagentTranscript{
		JobID: "short-result", Status: "completed", Result: short, FinishedAt: time.Now().Add(-time.Hour),
	})
	saveOutcome(t, sess, session.SubagentTranscript{
		JobID: "long-error", Status: "failed", Error: long, FinishedAt: time.Now(),
	})

	byJob := map[string]SubagentEndData{}
	for _, outcome := range buildInitData(sess, bus.StreamingAggregate{}, nil, "").SubagentOutcomes {
		byJob[outcome.JobID] = outcome
	}

	truncated := byJob["long-result"]
	if len(truncated.Result) >= len(long) {
		t.Fatalf("long result was not excerpted: %d bytes", len(truncated.Result))
	}
	if len(truncated.Result) > subagentOutcomeExcerptMaxBytes+len("\n\n[historic content truncated on this device]") {
		t.Fatalf("excerpt = %d bytes, budget is %d", len(truncated.Result), subagentOutcomeExcerptMaxBytes)
	}
	if !truncated.Excerpt {
		t.Fatal("truncated result must be flagged as an excerpt so the UI offers the full transcript")
	}

	whole := byJob["short-result"]
	if whole.Result != short {
		t.Fatalf("short result = %q, want it verbatim", whole.Result)
	}
	if whole.Excerpt {
		t.Fatal("a result that travels whole must NOT be flagged as an excerpt")
	}

	failed := byJob["long-error"]
	if len(failed.Error) >= len(long) || !failed.Excerpt {
		t.Fatalf("failed outcome = %d error bytes, excerpt=%v", len(failed.Error), failed.Excerpt)
	}
}

// TestSubagentTaskIsBoundedEverywhereItTravels covers the model-authored task
// string, which was unbounded on both the live event and the init snapshot:
// fifty cards each carrying a full delegation brief is a payload section of its
// own.
func TestSubagentTaskIsBoundedEverywhereItTravels(t *testing.T) {
	huge := strings.Repeat("delegate this very long brief. ", 5000)

	live, ok := wsEventFromBus(bus.SubagentEnded{JobID: "sa-1", Task: huge, Status: "completed"})
	if !ok {
		t.Fatal("subagent_end not translated")
	}
	if got := live.Data.(SubagentEndData).Task; len(got) > subagentTaskMaxBytes+128 {
		t.Fatalf("live event task = %d bytes, budget is %d", len(got), subagentTaskMaxBytes)
	}

	mgr := newTestManager(t, t.Context(), newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	saveOutcome(t, sess, session.SubagentTranscript{
		JobID: "sa-1", Status: "completed", Task: huge, Result: "ok", FinishedAt: time.Now(),
	})
	outcomes := buildInitData(sess, bus.StreamingAggregate{}, nil, "").SubagentOutcomes
	if len(outcomes) != 1 {
		t.Fatalf("outcomes = %d, want one", len(outcomes))
	}
	if got := outcomes[0].Task; len(got) > subagentTaskMaxBytes+128 {
		t.Fatalf("init outcome task = %d bytes, budget is %d", len(got), subagentTaskMaxBytes)
	}

	projected := liveSubagentInitData([]bus.SubagentSnapshot{{JobID: "live", Task: huge, Status: "running"}})
	if got := projected[0].Task; len(got) > subagentTaskMaxBytes+128 {
		t.Fatalf("live subagent task = %d bytes, budget is %d", len(got), subagentTaskMaxBytes)
	}
}

// TestLiveAndReloadedOutcomesUseTheSameExcerptBudget pins the consistency the
// user actually perceives: a result seen when the child finished must not shrink
// on the next reload. The live event and the init snapshot therefore share one
// projection instead of each choosing its own budget.
func TestLiveAndReloadedOutcomesUseTheSameExcerptBudget(t *testing.T) {
	// Sized between the old live budget (64 KiB) and the excerpt budget, which
	// is exactly where the two paths used to disagree.
	result := strings.Repeat("m", 10<<10)

	live, ok := wsEventFromBus(bus.SubagentEnded{JobID: "sa-1", Status: "completed", Result: result})
	if !ok {
		t.Fatal("subagent_end not translated")
	}
	liveData := live.Data.(SubagentEndData)

	mgr := newTestManager(t, t.Context(), newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	saveOutcome(t, sess, session.SubagentTranscript{
		JobID: "sa-1", Status: "completed", Result: result, FinishedAt: time.Now(),
	})
	reloaded := buildInitData(sess, bus.StreamingAggregate{}, nil, "").SubagentOutcomes[0]

	if liveData.Result != reloaded.Result {
		t.Fatalf("live result is %d bytes but reload gives %d: the card would shrink on reload",
			len(liveData.Result), len(reloaded.Result))
	}
	if liveData.Excerpt != reloaded.Excerpt {
		t.Fatalf("excerpt flag differs: live=%v reload=%v", liveData.Excerpt, reloaded.Excerpt)
	}
	if !liveData.Excerpt {
		t.Fatal("a 10 KiB result must be flagged as an excerpt in both paths")
	}
}

func TestBuildInitData_OutcomeCapKeepsTheMostRecent(t *testing.T) {
	mgr := newTestManager(t, t.Context(), newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	total := initSubagentOutcomeLimit + 25
	base := time.Now().Add(-time.Duration(total) * time.Hour)
	for i := range total {
		saveOutcome(t, sess, session.SubagentTranscript{
			JobID: fmt.Sprintf("job-%03d", i), Status: "completed", Result: "done",
			FinishedAt: base.Add(time.Duration(i) * time.Hour),
		})
	}

	outcomes := buildInitData(sess, bus.StreamingAggregate{}, nil, "").SubagentOutcomes
	if len(outcomes) != initSubagentOutcomeLimit {
		t.Fatalf("outcomes = %d, want the cap %d", len(outcomes), initSubagentOutcomeLimit)
	}
	// The kept set must be the newest, and it must arrive oldest-first: the
	// frontend inserts each card at its completion point in the parent timeline.
	for i, outcome := range outcomes {
		want := fmt.Sprintf("job-%03d", total-initSubagentOutcomeLimit+i)
		if outcome.JobID != want {
			t.Fatalf("outcome[%d] = %q, want %q (newest %d, chronological)",
				i, outcome.JobID, want, initSubagentOutcomeLimit)
		}
		if i > 0 && outcomes[i-1].FinishedAtMs > outcome.FinishedAtMs {
			t.Fatalf("outcomes are not chronological at %d: %d then %d",
				i, outcomes[i-1].FinishedAtMs, outcome.FinishedAtMs)
		}
	}
}

func TestBuildInitData_DeltaOmitsOutcomesTheClientAlreadyHas(t *testing.T) {
	mgr := newTestManager(t, t.Context(), newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	// The anchor message dates the client's cached transcript. The grace window
	// is subtracted from it, so "old" must sit clearly before it.
	anchorAt := time.Now().Add(-time.Hour)
	tree := sess.runtime.Context().Tree
	tree.Append(session.Entry{Type: session.EntryMessage, Message: core.WrapMessage(core.Message{
		Role: "user", MsgID: "anchor", Timestamp: anchorAt.Unix(),
		Content: []core.Content{core.TextContent("anchor")},
	})})
	tree.Append(session.Entry{Type: session.EntryMessage, Message: core.WrapMessage(core.Message{
		Role: "assistant", MsgID: "suffix", Timestamp: time.Now().Unix(),
		Content: []core.Content{core.TextContent("suffix")},
	})})
	saveOutcome(t, sess, session.SubagentTranscript{
		JobID: "old", Status: "completed", Result: "known", FinishedAt: anchorAt.Add(-time.Hour),
	})
	saveOutcome(t, sess, session.SubagentTranscript{
		JobID: "new", Status: "completed", Result: "fresh", FinishedAt: anchorAt.Add(time.Minute),
	})

	delta := buildInitData(sess, bus.StreamingAggregate{}, nil, "anchor")
	if delta.DeltaBase != "anchor" {
		t.Fatalf("delta_base = %q, want a validated delta", delta.DeltaBase)
	}
	if len(delta.SubagentOutcomes) != 1 || delta.SubagentOutcomes[0].JobID != "new" {
		t.Fatalf("delta outcomes = %+v, want only the one finished after the anchor", delta.SubagentOutcomes)
	}

	// A rejected delta is a full snapshot: it must carry every card, because the
	// client is rebuilding its transcript from nothing.
	full := buildInitData(sess, bus.StreamingAggregate{}, nil, "not-on-this-branch")
	if full.DeltaBase != "" {
		t.Fatalf("delta_base = %q, want the full-snapshot fallback", full.DeltaBase)
	}
	if len(full.SubagentOutcomes) != 2 {
		t.Fatalf("full snapshot outcomes = %d, want both", len(full.SubagentOutcomes))
	}
}

func TestBuildInitData_DeltaWithoutAnchorTimeSendsEveryOutcome(t *testing.T) {
	mgr := newTestManager(t, t.Context(), newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	// A message with no timestamp cannot date the client's cache, so the server
	// must fail CLOSED and re-send everything rather than silently omit a card
	// the client may never have received.
	tree := sess.runtime.Context().Tree
	tree.Append(session.Entry{Type: session.EntryMessage, Message: core.WrapMessage(core.Message{
		Role: "user", MsgID: "undated", Content: []core.Content{core.TextContent("undated")},
	})})
	tree.Append(session.Entry{Type: session.EntryMessage, Message: core.WrapMessage(core.Message{
		Role: "assistant", MsgID: "after", Content: []core.Content{core.TextContent("after")},
	})})
	saveOutcome(t, sess, session.SubagentTranscript{
		JobID: "ancient", Status: "completed", Result: "known", FinishedAt: time.Now().Add(-100 * time.Hour),
	})

	data := buildInitData(sess, bus.StreamingAggregate{}, nil, "undated")
	if data.DeltaBase != "undated" {
		t.Fatalf("delta_base = %q, want a validated delta", data.DeltaBase)
	}
	if len(data.SubagentOutcomes) != 1 {
		t.Fatalf("outcomes = %+v, want every card when the anchor has no timestamp", data.SubagentOutcomes)
	}
}

// TestInitProjectionStripsProviderSignaturesFromTheCopyOnly is the critical
// signature test. ThinkingSignature/TextSignature are opaque provider metadata
// the browser never reads, but Anthropic REQUIRES them to validate replayed
// thinking blocks. Stripping them from the stored transcript instead of from
// the outbound copy would break thinking silently, with no test and no error.
func TestInitProjectionStripsProviderSignaturesFromTheCopyOnly(t *testing.T) {
	original := core.AgentMessage{Message: core.Message{
		Role: "assistant", MsgID: "signed",
		Content: []core.Content{
			{Type: "thinking", Thinking: "reasoning", ThinkingSignature: "sig-thinking"},
			{Type: "text", Text: "answer", TextSignature: `{"id":"msg_1","phase":"final_answer"}`},
		},
	}}

	projected, _ := sanitizeHistoryMessage(original)
	if projected.Content[0].ThinkingSignature != "" || projected.Content[1].TextSignature != "" {
		t.Fatalf("signatures reached the client: %+v", projected.Content)
	}
	if projected.Content[0].Thinking != "reasoning" || projected.Content[1].Text != "answer" {
		t.Fatalf("projection lost the content it must keep: %+v", projected.Content)
	}
	// The source message — the one that stays in memory, gets persisted and is
	// replayed to the provider — must be untouched.
	if original.Content[0].ThinkingSignature != "sig-thinking" {
		t.Fatal("sanitizing the outbound copy mutated the stored thinking signature")
	}
	if original.Content[1].TextSignature == "" {
		t.Fatal("sanitizing the outbound copy mutated the stored text signature")
	}
}

func TestSignaturesAreAbsentFromInitAndHistoryButKeptInTheTranscript(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	sess, err := mgr.CreateSession(CreateOpts{Title: "signed"})
	if err != nil {
		t.Fatal(err)
	}
	tree := sess.runtime.Context().Tree
	tree.Append(session.Entry{Type: session.EntryMessage, Message: core.WrapMessage(core.Message{
		Role: "assistant", MsgID: "signed", Timestamp: time.Now().Unix(),
		Content: []core.Content{
			{Type: "thinking", Thinking: "reasoning", ThinkingSignature: "sig-thinking"},
			{Type: "text", Text: "answer", TextSignature: "sig-text"},
		},
	})})

	initJSON, err := json.Marshal(buildInitData(sess, bus.StreamingAggregate{}, nil, ""))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(initJSON), "sig-thinking") || strings.Contains(string(initJSON), "sig-text") {
		t.Fatal("init payload leaked provider signatures")
	}

	resp := apiReq(t, srv, "GET", "/api/sessions/"+sess.ID+"/history", "")
	defer resp.Body.Close() //nolint:errcheck
	var page historyResponse
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	for _, msg := range page.Messages {
		for _, content := range msg.Content {
			if content.ThinkingSignature != "" || content.TextSignature != "" {
				t.Fatalf("/history leaked provider signatures: %+v", content)
			}
		}
	}

	// The authoritative transcript must still carry them, or the next request to
	// Anthropic replays thinking blocks it cannot validate.
	stored, _ := bus.QueryTyped[bus.GetDisplayMessages, []core.AgentMessage](sess.runtime.Bus, bus.GetDisplayMessages{})
	var found bool
	for _, msg := range stored {
		if msg.MsgID != "signed" {
			continue
		}
		found = true
		if msg.Content[0].ThinkingSignature != "sig-thinking" || msg.Content[1].TextSignature != "sig-text" {
			t.Fatalf("stored transcript lost its provider signatures: %+v", msg.Content)
		}
	}
	if !found {
		t.Fatal("signed message missing from the stored transcript")
	}
}

// TestInitProjectionKeepsTheSubagentJobIDLinkingALaunchToItsChild is the
// duplication test. The subagent tool records the job it spawned under
// custom.subagent_job_id (subagent.go#taggedWithJob), and that annotation is
// the ONLY link from a launch tool call — whose ID belongs to the provider —
// to the child it started. The client's dedup reads it (stream-model.js
// #projectStream) to fold the launch acknowledgement into the completion card.
//
// projectWSMessageCustom used to drop every custom key without a "source"
// field, so the reconnect snapshot arrived with the link erased and the
// launch card was projected as a second, orphan row next to the real
// outcome: the "Subagent started in background." duplicate the owner sees on
// every reload, with a Conversation button pointing at a job that does not
// exist.
func TestInitProjectionKeepsTheSubagentJobIDLinkingALaunchToItsChild(t *testing.T) {
	original := core.AgentMessage{Message: core.Message{
		Role: "tool_result", MsgID: "launch", ToolCallID: "call_provider_1",
		Content: []core.Content{core.TextContent("Subagent started in background.\nJob ID: sa-1\n")},
	}, Custom: map[string]any{"subagent_job_id": "sa-1"}}

	projected, _ := sanitizeHistoryMessage(original)
	if got, _ := projected.Custom["subagent_job_id"].(string); got != "sa-1" {
		t.Fatalf("subagent_job_id = %q, want sa-1 (custom = %#v)", got, projected.Custom)
	}
}

// The projection is an allowlist, so it must not become a passthrough while
// fixing the link above: internal bookkeeping the browser has no business
// reading stays server-side.
func TestInitProjectionStillDropsUnrelatedInternalCustomKeys(t *testing.T) {
	original := core.AgentMessage{Message: core.Message{
		Role: "tool_result", MsgID: "launch", ToolCallID: "call_provider_1",
		Content: []core.Content{core.TextContent("done")},
	}, Custom: map[string]any{"subagent_job_id": "sa-1", "internal_secret": "nope"}}

	projected, _ := sanitizeHistoryMessage(original)
	if _, leaked := projected.Custom["internal_secret"]; leaked {
		t.Fatalf("unrelated internal key reached the client: %#v", projected.Custom)
	}
}

func TestInitProjectionKeepsEventEnvelope(t *testing.T) {
	original := core.AgentMessage{Message: core.Message{
		Role: "user", MsgID: "ev-msg",
		Content: []core.Content{core.TextContent(`{"ok":true}`)},
	}, Custom: map[string]any{
		"source": "event", "id": "ev_1", "source_name": "sentry-tienda",
		"title": "Checkout 500s", "autorun": false, "steer": true,
		"internal_secret": "nope",
	}}
	projected, _ := sanitizeHistoryMessage(original)
	if projected.Custom["source"] != "event" || projected.Custom["id"] != "ev_1" ||
		projected.Custom["source_name"] != "sentry-tienda" || projected.Custom["title"] != "Checkout 500s" ||
		projected.Custom["autorun"] != false || projected.Custom["steer"] != true {
		t.Fatalf("event custom = %#v", projected.Custom)
	}
	if _, leaked := projected.Custom["internal_secret"]; leaked {
		t.Fatalf("internal key leaked: %#v", projected.Custom)
	}
}

// TestBuildInitData_OmitsOutcomesWhoseLaunchIsNotSent covers the report behind
// this filter: a long-running session showed ~50 subagent cards in one block at
// the end of the transcript, hours apart from each other. The cap is applied to
// the newest finished children, which in an active session are spread far wider
// than the messages the init sends, so every card missed its launch row and the
// client appended them all after the last message.
func TestBuildInitData_OmitsOutcomesWhoseLaunchIsNotSent(t *testing.T) {
	mgr := newTestManager(t, t.Context(), newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	store := sess.persister.subagentStore(sess.ID)
	if store == nil {
		t.Fatal("session has no subagent store")
	}
	// Two finished children. Only the second one's launch row is in history,
	// standing in for a launch that has scrolled out of the init window.
	for _, jobID := range []string{"sa-old", "sa-recent"} {
		if err := store.Save(session.SubagentTranscript{
			JobID: jobID, Status: "completed", Task: "t", Result: "ok", FinishedAt: time.Now(),
		}); err != nil {
			t.Fatalf("save %q: %v", jobID, err)
		}
	}
	appendSubagentLaunch(t, sess, "sa-recent")

	outcomes := buildInitData(sess, bus.StreamingAggregate{}, nil, "").SubagentOutcomes
	ids := make([]string, len(outcomes))
	for i, outcome := range outcomes {
		ids[i] = outcome.JobID
	}
	if len(ids) != 1 || ids[0] != "sa-recent" {
		t.Fatalf("outcomes = %v, want only [sa-recent]: a card whose launch row is absent has nothing to attach to", ids)
	}
}
