package core

import (
	"strings"
	"testing"
)

func toolResult(id, text string, isError bool) AgentMessage {
	m := WrapMessage(NewToolResultMessage("call-"+id, "read", []Content{TextContent(text)}, isError))
	m.MsgID = id
	return m
}

func assistantWithCall(id string, callIDs ...string) AgentMessage {
	content := make([]Content, 0, len(callIDs))
	for _, c := range callIDs {
		content = append(content, ToolCallContent("call-"+c, "read", map[string]any{"path": "/x"}))
	}
	m := WrapMessage(Message{Role: "assistant", Content: content})
	m.MsgID = id
	return m
}

func userMsg(id, text string) AgentMessage {
	m := WrapMessage(NewUserMessage(text))
	m.MsgID = id
	return m
}

func bigText(prefix string, n int) string {
	return prefix + strings.Repeat("x", n)
}

// A trim must keep the message, its identity and its tool_call pairing: the
// provider rejects an unanswered tool_call, and the tree repairs one by
// injecting a synthetic error result on reload.
func TestPlanTrimPreservesIdentityAndPairing(t *testing.T) {
	msgs := []AgentMessage{
		userMsg("u1", "go"),
		assistantWithCall("a1", "r1"),
		toolResult("r1", bigText("HEAD\n", 8000)+"\nFAIL pkg/agent\nExit code: 1", false),
		assistantWithCall("a2", "r2"),
		toolResult("r2", bigText("recent\n", 200), false),
	}
	plan, ok := PlanTrim(msgs, 10, "")
	if !ok {
		t.Fatal("expected a trim")
	}
	got := plan.Messages[2]
	if got.MsgID != "r1" || got.ToolCallID != "call-r1" || got.ToolName != "read" || got.IsError {
		t.Fatalf("identity lost: %+v", got.Message)
	}
	text := messageText(got)
	if !strings.HasPrefix(text, "[Output elided") {
		t.Fatalf("placeholder header missing: %q", text)
	}
	if !strings.Contains(text, "Exit code: 1") {
		t.Fatalf("tail with the outcome not preserved: %q", text)
	}
	if plan.Results != 1 {
		t.Fatalf("Results = %d, want 1", plan.Results)
	}
	if plan.TokensRemoved <= 0 {
		t.Fatalf("TokensRemoved = %d", plan.TokensRemoved)
	}
}

// The protected tail is a promise: a large, very recent result must survive
// even when the watermark walk lands inside the group that owns it. Snapping
// forward (what compaction's FindCutPoint does) would elide it.
func TestPlanTrimProtectsRecentGroupBySnappingBackward(t *testing.T) {
	msgs := []AgentMessage{
		userMsg("u1", "go"),
		assistantWithCall("a1", "r1"),
		toolResult("r1", bigText("old\n", 8000), false),
		assistantWithCall("a2", "r2"),
		toolResult("r2", bigText("huge recent\n", 80000), false),
	}
	plan, ok := PlanTrim(msgs, 2000, "")
	if !ok {
		t.Fatal("expected a trim")
	}
	if plan.WatermarkMsgID != "a2" {
		t.Fatalf("watermark = %q, want a2 (the assistant owning the recent group)", plan.WatermarkMsgID)
	}
	if messageText(plan.Messages[4]) != messageText(msgs[4]) {
		t.Fatal("the recent result inside the protected tail was elided")
	}
	if messageText(plan.Messages[2]) == messageText(msgs[2]) {
		t.Fatal("the old result was not elided")
	}
}

func TestPlanTrimKeepsErrorsAndSmallResults(t *testing.T) {
	msgs := []AgentMessage{
		userMsg("u1", "go"),
		assistantWithCall("a1", "r1", "r2", "r3"),
		toolResult("r1", bigText("failed\n", 8000), true),
		toolResult("r2", "tiny output", false),
		toolResult("r3", bigText("big\n", 8000), false),
		assistantWithCall("a2", "r4"),
		toolResult("r4", "recent", false),
	}
	plan, ok := PlanTrim(msgs, 10, "")
	if !ok {
		t.Fatal("expected a trim")
	}
	if messageText(plan.Messages[2]) != messageText(msgs[2]) {
		t.Fatal("an error result was elided")
	}
	if messageText(plan.Messages[3]) != messageText(msgs[3]) {
		t.Fatal("a sub-threshold result was elided")
	}
	if messageText(plan.Messages[4]) == messageText(msgs[4]) {
		t.Fatal("a large result was not elided")
	}
	if plan.Results != 1 {
		t.Fatalf("Results = %d, want 1", plan.Results)
	}
}

// Non-textual blocks stay whole in v1: EstimateTokens prices them with a
// constant, so their "saving" would be a number nobody measured, and the
// AttachmentID in history is what keeps the blob reachable.
func TestPlanTrimSkipsNonTextualResults(t *testing.T) {
	img := WrapMessage(NewToolResultMessage("call-i", "screenshot",
		[]Content{TextContent(bigText("shot\n", 4000)), {Type: "image", AttachmentID: "att-1", MimeType: "image/png"}}, false))
	img.MsgID = "i1"
	msgs := []AgentMessage{
		userMsg("u1", "go"),
		assistantWithCall("a1", "i"),
		img,
		assistantWithCall("a2", "r2"),
		toolResult("r2", "recent", false),
	}
	if _, ok := PlanTrim(msgs, 10, ""); ok {
		t.Fatal("a result carrying an image must not be elided in v1")
	}
}

// An async subagent completion arrives as a user message, not a tool result.
// It is the same kind of payload and must be elidable — but only with a job ID
// in the placeholder text, since the provider request drops Custom entirely.
func TestPlanTrimElidesSubagentNotificationWithJobID(t *testing.T) {
	note := userMsg("n1", bigText("[subagent completed] Job j-42 finished.\n", 8000)+"\nverdict: ok")
	note.Custom = map[string]any{
		"source":          "subagent",
		"subagent_job_id": "j-42",
		"subagent_task":   "review the diff",
	}
	msgs := []AgentMessage{
		userMsg("u1", "go"),
		note,
		assistantWithCall("a1", "r1"),
		toolResult("r1", "recent", false),
	}
	plan, ok := PlanTrim(msgs, 10, "")
	if !ok {
		t.Fatal("expected a trim")
	}
	text := messageText(plan.Messages[1])
	if !strings.Contains(text, "j-42") {
		t.Fatalf("job ID missing from the provider-visible placeholder: %q", text)
	}
	if !strings.Contains(text, "subagent_status") {
		t.Fatalf("placeholder does not say how to recover the result: %q", text)
	}
	if !strings.Contains(text, "review the diff") {
		t.Fatalf("task missing from the placeholder: %q", text)
	}
}

func TestPlanTrimLeavesOrdinaryUserMessagesAlone(t *testing.T) {
	msgs := []AgentMessage{
		userMsg("u1", bigText("a long human message\n", 8000)),
		assistantWithCall("a1", "r1"),
		toolResult("r1", "recent", false),
	}
	if _, ok := PlanTrim(msgs, 10, ""); ok {
		t.Fatal("an ordinary user message must never be elided")
	}
}

// Monotonicity: the second trim only touches the region after the first
// watermark, and the bytes before it are unchanged — that identity is what
// keeps the provider's prefix cache warm across trims.
func TestPlanTrimIsMonotonic(t *testing.T) {
	msgs := []AgentMessage{
		userMsg("u1", "go"),
		assistantWithCall("a1", "r1"),
		toolResult("r1", bigText("first\n", 8000), false),
		assistantWithCall("a2", "r2"),
		toolResult("r2", bigText("second\n", 8000), false),
		assistantWithCall("a3", "r3"),
		toolResult("r3", "recent", false),
	}
	first, ok := PlanTrim(msgs, 10, "")
	if !ok {
		t.Fatal("expected a first trim")
	}
	// New work arrives after the first watermark.
	grown := append(append([]AgentMessage(nil), first.Messages...),
		assistantWithCall("a4", "r4"),
		toolResult("r4", bigText("third\n", 8000), false),
		assistantWithCall("a5", "r5"),
		toolResult("r5", "newest", false),
	)
	second, ok := PlanTrim(grown, 10, first.WatermarkMsgID)
	if !ok {
		t.Fatal("expected a second trim")
	}
	wm1 := indexOfMsgID(grown, first.WatermarkMsgID)
	wm2 := indexOfMsgID(grown, second.WatermarkMsgID)
	if wm2 <= wm1 {
		t.Fatalf("watermark moved backwards: %d -> %d", wm1, wm2)
	}
	for i := 0; i < wm1; i++ {
		if messageText(second.Messages[i]) != messageText(grown[i]) {
			t.Fatalf("message %d before the previous watermark was rewritten", i)
		}
	}
}

func TestPlanTrimRefusesWhenNothingNewIsEligible(t *testing.T) {
	msgs := []AgentMessage{
		userMsg("u1", "go"),
		assistantWithCall("a1", "r1"),
		toolResult("r1", bigText("old\n", 8000), false),
		assistantWithCall("a2", "r2"),
		toolResult("r2", "recent", false),
	}
	first, ok := PlanTrim(msgs, 10, "")
	if !ok {
		t.Fatal("expected a first trim")
	}
	if _, ok := PlanTrim(first.Messages, 10, first.WatermarkMsgID); ok {
		t.Fatal("a second trim with no new region must be refused")
	}
}

// Replaying persisted spans must produce exactly what the live plan produced;
// two implementations would drift and a restored session would send a context
// the provider has never cached.
func TestApplyTrimsMatchesPlanTrim(t *testing.T) {
	msgs := []AgentMessage{
		userMsg("u1", "go"),
		assistantWithCall("a1", "r1"),
		toolResult("r1", bigText("old\n", 8000)+"\ntail line", false),
		assistantWithCall("a2", "r2"),
		toolResult("r2", bigText("mid\n", 8000), false),
		assistantWithCall("a3", "r3"),
		toolResult("r3", "recent", false),
	}
	plan, ok := PlanTrim(msgs, 10, "")
	if !ok {
		t.Fatal("expected a trim")
	}
	replayed := ApplyTrims(msgs, []TrimSpan{{WatermarkMsgID: plan.WatermarkMsgID, Version: plan.Version}})
	for i := range replayed {
		if messageText(replayed[i]) != messageText(plan.Messages[i]) {
			t.Fatalf("message %d differs between live trim and replay:\nlive:   %q\nreplay: %q",
				i, messageText(plan.Messages[i]), messageText(replayed[i]))
		}
	}
}

func TestApplyTrimsIgnoresBackwardAndMissingWatermarks(t *testing.T) {
	msgs := []AgentMessage{
		userMsg("u1", "go"),
		assistantWithCall("a1", "r1"),
		toolResult("r1", bigText("old\n", 8000), false),
		assistantWithCall("a2", "r2"),
		toolResult("r2", bigText("mid\n", 8000), false),
		assistantWithCall("a3", "r3"),
		toolResult("r3", "recent", false),
	}
	spans := []TrimSpan{
		{WatermarkMsgID: "a3", Version: TrimProjectionVersion},
		{WatermarkMsgID: "a2", Version: TrimProjectionVersion}, // backwards: ignored
		{WatermarkMsgID: "gone", Version: TrimProjectionVersion},
	}
	out := ApplyTrims(msgs, spans)
	forward := ApplyTrims(msgs, []TrimSpan{{WatermarkMsgID: "a3", Version: TrimProjectionVersion}})
	for i := range out {
		if messageText(out[i]) != messageText(forward[i]) {
			t.Fatalf("message %d: a backwards or missing span changed the projection", i)
		}
	}
}

func TestApplyTrimsDoesNotMutateInput(t *testing.T) {
	original := bigText("old\n", 8000)
	msgs := []AgentMessage{
		userMsg("u1", "go"),
		assistantWithCall("a1", "r1"),
		toolResult("r1", original, false),
		assistantWithCall("a2", "r2"),
		toolResult("r2", "recent", false),
	}
	_ = ApplyTrims(msgs, []TrimSpan{{WatermarkMsgID: "a2", Version: TrimProjectionVersion}})
	if messageText(msgs[2]) != original {
		t.Fatal("ApplyTrims mutated the caller's messages")
	}
}

// A trim that merely wins minGain is not enough: the threshold check says the
// context is above T, not by how much, so a big overshoot can still leave the
// request over the line.
func TestAcceptTrimRequiresLandingBelowTheLowWaterMark(t *testing.T) {
	settings := CompactionSettings{Enabled: true, ReserveTokens: 16384, KeepRecent: 20000, CompactAt: 350000}
	window := settings.EffectiveWindow(1_000_000)
	effective := window - settings.ReserveTokens
	minGain := TrimMinGain(window, settings)

	if AcceptTrim(effective+120_000, minGain+1, window, settings) {
		t.Fatal("accepted a trim that leaves the context above the threshold")
	}
	if !AcceptTrim(effective+1000, minGain+2000, window, settings) {
		t.Fatal("rejected a trim that lands well below the low-water mark")
	}
	if AcceptTrim(effective+1000, minGain-1, window, settings) {
		t.Fatal("accepted a trim below the minimum gain")
	}
}

func TestTrimMinGainFloor(t *testing.T) {
	settings := CompactionSettings{Enabled: true, ReserveTokens: 1000, KeepRecent: 1000}
	if got := TrimMinGain(30_000, settings); got != minWarnBandTokens {
		t.Fatalf("TrimMinGain = %d, want the %d floor", got, minWarnBandTokens)
	}
}

// A human typed this answer. Re-running the tool means asking them the same
// question again, which is the one thing in a transcript that cannot be
// reproduced from the worktree — so ask_user is never elided, however large.
func TestPlanTrimNeverElidesAskUser(t *testing.T) {
	ask := WrapMessage(NewToolResultMessage("call-ask", "ask_user",
		[]Content{TextContent(bigText("the user's considered answer\n", 8000))}, false))
	ask.MsgID = "ask"
	msgs := []AgentMessage{
		userMsg("u1", "go"),
		assistantWithCall("a1", "ask"),
		ask,
		assistantWithCall("a2", "r2"),
		toolResult("r2", "recent", false),
	}

	plan, ok := PlanTrim(msgs, 10, "")
	if ok && messageText(plan.Messages[2]) != messageText(msgs[2]) {
		t.Fatal("an ask_user answer was elided; it cannot be re-run")
	}
}

// A trimmed subagent_status has to keep pointing at the job it described. The
// tool tags its result with the job id precisely so the placeholder can carry
// it: without it the model is told to re-run a tool whose only argument has
// just been elided along with the output.
func TestTrimPlaceholderKeepsSubagentJobIDFromAStatusResult(t *testing.T) {
	status := WrapMessage(NewToolResultMessage("call-s", "subagent_status",
		[]Content{TextContent(bigText("status line\n", 8000))}, false))
	status.MsgID = "s"
	status.Custom = map[string]any{"subagent_job_id": "sa-abc123"}
	msgs := []AgentMessage{
		userMsg("u1", "go"),
		assistantWithCall("a1", "s"),
		status,
		assistantWithCall("a2", "r2"),
		toolResult("r2", "recent", false),
	}

	plan, ok := PlanTrim(msgs, 10, "")
	if !ok {
		t.Fatal("expected a trim")
	}
	got := messageText(plan.Messages[2])
	if got == messageText(msgs[2]) {
		t.Fatal("the status output was not elided")
	}
	if !strings.Contains(got, "sa-abc123") || !strings.Contains(got, "subagent_status") {
		t.Fatalf("placeholder lost the way back to the job: %q", got)
	}
}
