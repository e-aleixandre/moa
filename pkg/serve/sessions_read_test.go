package serve

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/owner"
)

// sizedText is a message of exactly n characters that starts with head, ends
// with tail and carries a marker in the middle, so a test can tell which parts
// of it survived a cut.
func sizedText(n int, head, middle, tail string) string {
	fill := n - len([]rune(head)) - len([]rune(middle)) - len([]rune(tail))
	left := fill / 2
	return head + strings.Repeat("é", left) + middle + strings.Repeat("x", fill-left) + tail
}

func ownerWithChild(t *testing.T) (*ManagedSession, *ManagedSession) {
	t.Helper()
	mgr := newOwnerTestManager(t, context.Background())
	root := t.TempDir()
	_, ownerSess := ownerWithSession(t, mgr, root, "Winerim")
	child, err := mgr.CreateSession(CreateOpts{CWD: root, Origin: "owner", Title: "child"})
	if err != nil {
		t.Fatal(err)
	}
	return ownerSess, child
}

func TestSessionsReadAbridgesALongMessageAndSaysWhatIsMissing(t *testing.T) {
	ownerSess, child := ownerWithChild(t)
	long := sizedText(14288, "INICIO ", " MITAD ", " FINAL")
	appendConversationTestMessage(child, "u1", "user", "haz la auditoría", nil)
	appendConversationTestMessage(child, "a1", "assistant", long, nil)

	got := toolText(runSessionsTool(t, ownerSess, map[string]any{"action": "read", "session_id": child.ID}))
	notice := fmt.Sprintf("[... %d of 14288 characters truncated — read it whole with action=read, session_id=%s, message_id=a1 ...]",
		14288-readMessageHead-readMessageTail, child.ID)
	if !strings.Contains(got, notice) {
		t.Fatalf("missing truncation notice %q in:\n%s", notice, got)
	}
	if !strings.Contains(got, "[a1] assistant: INICIO ") || !strings.Contains(got, " FINAL\n") {
		t.Fatalf("abridged read lost its beginning or its end:\n%s", got)
	}
	if strings.Contains(got, "MITAD") {
		t.Fatal("the middle was not cut")
	}
	if !strings.Contains(got, "[u1] user: haz la auditoría\n") || strings.Contains(got, "haz la auditoría\n\n[...") {
		t.Fatalf("a short message must be whole and carry no notice:\n%s", got)
	}

	whole := toolText(runSessionsTool(t, ownerSess, map[string]any{"action": "read", "session_id": child.ID, "message_id": "a1"}))
	if !strings.HasSuffix(whole, "\n\n"+long) || strings.Contains(whole, "truncated") {
		t.Fatalf("message_id did not return the whole message (%d characters)", len([]rune(whole)))
	}
	last := toolText(runSessionsTool(t, ownerSess, map[string]any{"action": "read", "session_id": child.ID, "message_id": "last"}))
	if last != whole {
		t.Fatal("message_id=last did not read the latest assistant message")
	}

	limited := toolText(runSessionsTool(t, ownerSess, map[string]any{"action": "read", "session_id": child.ID, "limit": float64(1)}))
	if !strings.Contains(limited, "[showing the last 1 of 2 messages; 1 earlier ones are not shown") {
		t.Fatalf("limit cut silently:\n%s", limited)
	}
}

func TestSessionsReadPagesAVeryLongMessage(t *testing.T) {
	ownerSess, child := ownerWithChild(t)
	long := sizedText(45000, "A", "B", "Z")
	appendConversationTestMessage(child, "a1", "assistant", long, nil)

	var rebuilt strings.Builder
	offset := 0
	for i := 0; i < 3; i++ {
		got := toolText(runSessionsTool(t, ownerSess, map[string]any{
			"action": "read", "session_id": child.ID, "message_id": "a1", "offset": float64(offset)}))
		body := got[strings.Index(got, "\n\n")+2:]
		end := min(offset+readChunkChars, 45000)
		if end < 45000 {
			notice := fmt.Sprintf("\n\n[truncated — showing characters %d-%d of 45000, %d more. Read on with action=read, session_id=%s, message_id=a1, offset=%d.]",
				offset, end, 45000-end, child.ID, end)
			if !strings.HasSuffix(body, notice) {
				t.Fatalf("chunk %d lacks the notice %q", i, notice)
			}
			body = strings.TrimSuffix(body, notice)
		} else if strings.Contains(body, "truncated") {
			t.Fatal("the last chunk claims to be truncated")
		}
		rebuilt.WriteString(body)
		offset = end
	}
	if rebuilt.String() != long {
		t.Fatal("the chunks do not rebuild the message")
	}
	past := runSessionsTool(t, ownerSess, map[string]any{"action": "read", "session_id": child.ID, "message_id": "a1", "offset": float64(45000)})
	if !past.IsError || !strings.Contains(toolText(past), "past the end") {
		t.Fatalf("offset past the end = %q", toolText(past))
	}
}

func TestSessionsReadListsToolCallsOnlyWhenAsked(t *testing.T) {
	ownerSess, child := ownerWithChild(t)
	command := "go test ./... " + strings.Repeat("-run X ", 200)
	output := sizedText(3000, "PASS-START ", " HIDDEN ", " PASS-END")
	appendConversationTestMessage(child, "a1", "assistant", "voy a probar", nil,
		core.ToolCallContent("call1", "bash", map[string]any{"command": command}))
	appendConversationToolResult(child, "r1", "call1", "bash", output, false, nil)
	appendConversationTestMessage(child, "a2", "assistant", "todo verde", nil)

	plain := toolText(runSessionsTool(t, ownerSess, map[string]any{"action": "read", "session_id": child.ID}))
	if strings.Contains(plain, "tool bash") || strings.Contains(plain, "PASS-START") {
		t.Fatalf("tool calls listed without being asked for:\n%s", plain)
	}

	withTools := toolText(runSessionsTool(t, ownerSess, map[string]any{"action": "read", "session_id": child.ID, "tools": true}))
	toolID := "tool:a1:1"
	for _, want := range []string{
		"[" + toolID + "] tool bash (ok): go test ./...",
		"  result: PASS-START ",
		" PASS-END\n",
		fmt.Sprintf("characters truncated — read it whole with action=read, session_id=%s, message_id=%s ...]", child.ID, toolID),
		fmt.Sprintf("[... %d of 3000 characters truncated", 3000-readToolHead-readToolTail),
	} {
		if !strings.Contains(withTools, want) {
			t.Fatalf("read with tools lacks %q:\n%s", want, withTools)
		}
	}
	if strings.Contains(withTools, "HIDDEN") {
		t.Fatal("the tool result was not abridged")
	}

	whole := toolText(runSessionsTool(t, ownerSess, map[string]any{"action": "read", "session_id": child.ID, "message_id": toolID}))
	if !strings.Contains(whole, "arguments: "+strings.TrimSpace(command)) || !strings.Contains(whole, "result (ok):\n"+output) {
		t.Fatalf("a tool call read whole lost its arguments or result:\n%.400s", whole)
	}
}

func TestReportKeepsBeginningAndEndAndSaysWhatIsMissing(t *testing.T) {
	_, child := ownerWithChild(t)
	long := sizedText(14288, "Resumen: auditoría hecha. ", " DETALLE-INTERMEDIO ", "\n\n## Book delta\n- work/live-preview.md: auditada")
	rep := reportFrom(child, runOutcome{Status: callbackStatusDone, FinalText: long})

	text := reportsMessage(owner.Owner{}, []owner.Report{rep})
	for _, want := range []string{
		"said: Resumen: auditoría hecha. ",
		"- work/live-preview.md: auditada",
		fmt.Sprintf("[... %d of 14288 characters truncated — read the whole message with the sessions tool: action=read, session_id=%s, message_id=last ...]",
			14288-reportHeadChars-reportTailChars, child.ID),
		"book delta: - work/live-preview.md: auditada",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("report lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "DETALLE-INTERMEDIO") {
		t.Fatal("the report carried the middle of the message")
	}
}

func TestReportSaysWhenATurnEndedWithoutAFinalMessage(t *testing.T) {
	_, child := ownerWithChild(t)
	rep := reportFrom(child, runOutcome{Status: callbackStatusDone})
	text := reportsMessage(owner.Owner{}, []owner.Report{rep})
	want := "said: nothing — the turn ended without a final message, so it may have stopped mid-work. " +
		"See where with the sessions tool: action=read, session_id=" + child.ID + ", tools=true"
	if !strings.Contains(text, want) {
		t.Fatalf("report of a turn with no final message:\n%s", text)
	}
}
