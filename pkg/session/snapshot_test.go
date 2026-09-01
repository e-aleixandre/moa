package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

func TestFormatTranscript_ActiveBranchOnly(t *testing.T) {
	tree := NewTree()
	tree.Append(userEntry("hello"))
	id2 := tree.Append(assistantEntry("hi"))
	tree.Append(userEntry("abandoned branch"))
	if err := tree.Branch(id2); err != nil {
		t.Fatal(err)
	}
	tree.Append(userEntry("active branch"))

	text := FormatTranscript(tree.Path())
	if strings.Contains(text, "abandoned branch") {
		t.Fatalf("abandoned branch leaked into snapshot:\n%s", text)
	}
	if !strings.Contains(text, "active branch") {
		t.Fatalf("active branch missing from snapshot:\n%s", text)
	}
	if !strings.Contains(text, "hello") {
		t.Fatalf("shared prefix missing from snapshot:\n%s", text)
	}
}

func TestFormatTranscript_KeepsToolCalls(t *testing.T) {
	tree := NewTree()
	tree.Append(userEntry("look"))
	tree.Append(assistantToolCallEntry("call-1"))
	tree.Append(toolResultEntry("call-1", "bash", "hi from bash"))

	text := FormatTranscript(tree.Path())
	if !strings.Contains(text, "[tool_call id=call-1 name=bash]") {
		t.Fatalf("tool call missing:\n%s", text)
	}
	if !strings.Contains(text, "--- tool_result bash call_id=call-1 ") {
		t.Fatalf("tool result missing:\n%s", text)
	}
	if !strings.Contains(text, "hi from bash") {
		t.Fatalf("tool result body missing:\n%s", text)
	}
}

func TestWriteTranscriptSnapshot_Frozen(t *testing.T) {
	tree := NewTree()
	tree.Append(userEntry("before"))
	dir := t.TempDir()
	path, err := WriteTranscriptSnapshot(dir, tree.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(path) {
		t.Fatalf("path is not absolute: %q", path)
	}

	tree.Append(userEntry("after"))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if strings.Contains(got, "after") {
		t.Fatalf("live tree mutation leaked into frozen snapshot:\n%s", got)
	}
	if !strings.Contains(got, "before") {
		t.Fatalf("original message missing from snapshot:\n%s", got)
	}
}

func TestWriteTranscriptSnapshot_OmitsThinkingAndImageBytes(t *testing.T) {
	tree := NewTree()
	tree.Append(Entry{
		Type: EntryMessage,
		Message: core.AgentMessage{Message: core.Message{
			Role: "assistant",
			Content: []core.Content{
				{Type: "thinking", Thinking: "secret chain of thought"},
				core.TextContent("visible"),
				{Type: "image", MimeType: "image/png", Data: "AAAA"},
			},
		}},
	})
	text := FormatTranscript(tree.Path())
	if strings.Contains(text, "secret chain of thought") {
		t.Fatalf("thinking leaked:\n%s", text)
	}
	if strings.Contains(text, "AAAA") {
		t.Fatalf("image bytes leaked:\n%s", text)
	}
	if !strings.Contains(text, "visible") || !strings.Contains(text, "[image mime=image/png]") {
		t.Fatalf("expected visible text and image marker:\n%s", text)
	}
}

func TestRemoveTranscriptSnapshots(t *testing.T) {
	base := t.TempDir()
	dir := TranscriptSnapshotDir(base, "abc")
	if _, err := WriteTranscriptSnapshot(dir, nil); err != nil {
		t.Fatal(err)
	}
	if err := RemoveTranscriptSnapshots(base, "abc"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("snapshot dir still exists: %v", err)
	}
}

func TestFormatTranscript_KeepsPreCompactionEvidence(t *testing.T) {
	tree := NewTree()
	tree.Append(userEntry("old evidence"))
	tree.Append(assistantEntry("old reply"))
	kept := tree.Append(userEntry("kept after compact"))
	tree.Append(compactionEntry("summary of old stuff", kept, 40000))
	tree.Append(userEntry("new after compact"))

	text := FormatTranscript(tree.Path())
	if !strings.Contains(text, "old evidence") {
		t.Fatalf("pre-compaction evidence dropped from snapshot:\n%s", text)
	}
	if !strings.Contains(text, "summary of old stuff") {
		t.Fatalf("compaction summary missing from snapshot:\n%s", text)
	}
	if !strings.Contains(text, "new after compact") {
		t.Fatalf("post-compaction missing from snapshot:\n%s", text)
	}

	msgs, _ := tree.BuildContext()
	var ctx strings.Builder
	for _, m := range msgs {
		for _, c := range m.Content {
			ctx.WriteString(c.Text)
		}
	}
	if strings.Contains(ctx.String(), "old evidence") {
		t.Fatal("BuildContext still has pre-compaction text; the snapshot contrast is invalid")
	}
}

func TestFormatTranscript_EntryIDTimestampAndToolError(t *testing.T) {
	tree := NewTree()
	userID := tree.Append(userEntry("hello"))
	errID := tree.Append(Entry{
		Type: EntryMessage,
		Message: core.AgentMessage{
			Message: core.NewToolResultMessage("call-err", "bash", []core.Content{core.TextContent("boom")}, true),
		},
	})
	okID := tree.Append(toolResultEntry("call-ok", "bash", "fine"))

	path := tree.Path()
	text := FormatTranscript(path)

	byID := map[string]Entry{}
	for _, e := range path {
		byID[e.ID] = e
	}
	user, ok := byID[userID]
	if !ok {
		t.Fatal("user entry missing from path")
	}
	errE, ok := byID[errID]
	if !ok {
		t.Fatal("error tool_result missing from path")
	}
	okE, ok := byID[okID]
	if !ok {
		t.Fatal("ok tool_result missing from path")
	}

	if !strings.Contains(text, "id="+user.ID) {
		t.Fatalf("entry id missing:\n%s", text)
	}
	if !strings.Contains(text, "ts="+user.Timestamp.UTC().Format(time.RFC3339)) {
		t.Fatalf("entry timestamp missing:\n%s", text)
	}

	errLine := headingLine(text, "call_id=call-err")
	if errLine == "" || !strings.Contains(errLine, "id="+errE.ID) || !strings.Contains(errLine, "is_error") {
		t.Fatalf("error tool_result heading = %q, want id and is_error", errLine)
	}
	okLine := headingLine(text, "call_id=call-ok")
	if okLine == "" || !strings.Contains(okLine, "id="+okE.ID) {
		t.Fatalf("ok tool_result heading = %q", okLine)
	}
	if strings.Contains(okLine, "is_error") {
		t.Fatalf("successful tool_result marked is_error: %q", okLine)
	}
}

func headingLine(text, needle string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}
