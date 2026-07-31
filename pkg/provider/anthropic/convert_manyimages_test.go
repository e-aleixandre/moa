package anthropic

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

// imageTurns builds a chronological conversation carrying one image per user
// turn, so message order is age order: image 0 is the oldest.
func imageTurns(t *testing.T, images []string) []core.Message {
	t.Helper()
	msgs := make([]core.Message, 0, len(images)*2)
	for _, img := range images {
		msgs = append(msgs, core.Message{
			Role: "user",
			Content: []core.Content{
				core.TextContent("look"),
				core.ImageContent(img, "image/jpeg"),
			},
		})
		msgs = append(msgs, core.Message{
			Role:    "assistant",
			Content: []core.Content{core.TextContent("seen")},
		})
	}
	return msgs
}

func smallImages(t *testing.T, n int) []string {
	t.Helper()
	img := jpegB64(t, 100, 200)
	out := make([]string, n)
	for i := range out {
		out[i] = img
	}
	return out
}

// imageOutcomes flattens a converted request into the per-image outcome, in
// conversion order: "image" for a block that stayed an image, "text" otherwise.
func imageOutcomes(msgs []map[string]any) []string {
	var out []string
	for _, m := range msgs {
		if m["role"] != "user" {
			continue
		}
		content, _ := m["content"].([]any)
		out = append(out, outcomesInContent(content)...)
	}
	return out
}

func outcomesInContent(content []any) []string {
	var out []string
	for _, raw := range content {
		block, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch block["type"] {
		case "image":
			out = append(out, "image")
		case "text":
			if text, _ := block["text"].(string); strings.HasPrefix(text, "[image omitted") {
				out = append(out, "note:"+text)
			}
		case "tool_result":
			inner, _ := block["content"].([]any)
			out = append(out, outcomesInContent(inner)...)
		}
	}
	return out
}

func countImages(outcomes []string) (images, notes int) {
	for _, o := range outcomes {
		if o == "image" {
			images++
		} else {
			notes++
		}
	}
	return
}

// At the threshold the stricter cap never applies, so nothing is degraded.
func TestConvertMessages_TwentyImagesUntouched(t *testing.T) {
	result := convertMessages(imageTurns(t, smallImages(t, 20)), false)

	outcomes := imageOutcomes(result)
	images, notes := countImages(outcomes)
	if images != 20 || notes != 0 {
		t.Fatalf("got %d images and %d notes, want 20 and 0", images, notes)
	}
}

// One image over the threshold retires a whole batch of the oldest, regardless
// of how small those images are: the rule is count and age, not size.
func TestConvertMessages_TwentyOneRetiresOldestBatch(t *testing.T) {
	result := convertMessages(imageTurns(t, smallImages(t, 21)), false)

	outcomes := imageOutcomes(result)
	if len(outcomes) != 21 {
		t.Fatalf("expected 21 image slots, got %d", len(outcomes))
	}
	for i, o := range outcomes {
		if i < 8 && o == "image" {
			t.Fatalf("image %d should be retired", i)
		}
		if i >= 8 && o != "image" {
			t.Fatalf("image %d should survive, got %q", i, o)
		}
	}
	if note := outcomes[0]; !strings.Contains(note, "100x200") || !strings.Contains(note, "2000 px") {
		t.Fatalf("note should name the size and the cap: %q", note)
	}
}

// The images that survive keep their full resolution even when they are far
// above the many-image cap: retiring the oldest brings the count back to 20,
// where the 2000 px cap does not apply.
func TestConvertMessages_OversizedNewestSurvive(t *testing.T) {
	images := smallImages(t, 25)
	tall := jpegB64(t, 1170, 2532)
	images = append(images, tall, tall)

	result := convertMessages(imageTurns(t, images), false)

	outcomes := imageOutcomes(result)
	if len(outcomes) != 27 {
		t.Fatalf("expected 27 image slots, got %d", len(outcomes))
	}
	images27, notes := countImages(outcomes)
	if images27 != 19 || notes != 8 {
		t.Fatalf("got %d images and %d notes, want 19 and 8", images27, notes)
	}
	for i := 0; i < 8; i++ {
		if outcomes[i] == "image" {
			t.Fatalf("image %d should be retired", i)
		}
	}
	if outcomes[25] != "image" || outcomes[26] != "image" {
		t.Fatalf("newest 1170x2532 images must survive: %v", outcomes[25:])
	}
}

// The retired set only moves when the count crosses a batch boundary, so the
// request bytes stay stable turn to turn and the prompt cache survives.
func TestConvertMessages_RetiredSetIsBatched(t *testing.T) {
	for _, tc := range []struct {
		n, wantRetired int
	}{
		{21, 8}, {22, 8}, {28, 8}, {29, 16}, {36, 16}, {37, 24},
	} {
		result := convertMessages(imageTurns(t, smallImages(t, tc.n)), false)
		outcomes := imageOutcomes(result)
		images, notes := countImages(outcomes)
		if notes != tc.wantRetired {
			t.Errorf("n=%d: retired %d, want %d", tc.n, notes, tc.wantRetired)
		}
		if images != tc.n-tc.wantRetired {
			t.Errorf("n=%d: kept %d, want %d", tc.n, images, tc.n-tc.wantRetired)
		}
		for i := 0; i < tc.wantRetired; i++ {
			if outcomes[i] == "image" {
				t.Errorf("n=%d: image %d should be retired", tc.n, i)
			}
		}
	}
}

// An image above the 8000 px cap is replaced by its own note and never reaches
// the wire, so it must not count toward the many-image threshold.
func TestConvertMessages_OversizedImageExcludedFromCount(t *testing.T) {
	images := smallImages(t, 20)
	huge := jpegB64(t, 100, core.MaxImageDimension+1)
	// Oldest is the huge one: if it counted, N would be 21 and a batch would
	// be retired.
	msgs := imageTurns(t, append([]string{huge}, images...))

	result := convertMessages(msgs, false)

	outcomes := imageOutcomes(result)
	if len(outcomes) != 21 {
		t.Fatalf("expected 21 image slots, got %d", len(outcomes))
	}
	if !strings.Contains(outcomes[0], "exceeds the 8000 px per-side limit") {
		t.Fatalf("oversized image should keep its own note: %q", outcomes[0])
	}
	kept, notes := countImages(outcomes)
	if kept != 20 || notes != 1 {
		t.Fatalf("got %d images and %d notes, want 20 and 1", kept, notes)
	}

	// One more small image tips the countable total to 21 and retires a batch,
	// and the oversized one still carries the oversize note.
	msgs = imageTurns(t, append([]string{huge}, smallImages(t, 21)...))
	outcomes = imageOutcomes(convertMessages(msgs, false))
	kept, notes = countImages(outcomes)
	if kept != 13 || notes != 9 {
		t.Fatalf("got %d images and %d notes, want 13 and 9", kept, notes)
	}
	if !strings.Contains(outcomes[0], "exceeds the 8000 px per-side limit") {
		t.Fatalf("oversized note lost: %q", outcomes[0])
	}
	if !strings.Contains(outcomes[1], "read the file again") {
		t.Fatalf("oldest small image should be retired: %q", outcomes[1])
	}
}

// Images arrive through the read tool far more often than through attachments,
// so tool_result content has to be counted and retired like any other.
func TestConvertMessages_ToolResultImagesCountAndRetire(t *testing.T) {
	img := jpegB64(t, 100, 200)
	var msgs []core.Message
	for i := 0; i < 21; i++ {
		msgs = append(msgs,
			core.Message{Role: "user", Content: []core.Content{core.TextContent("read it")}},
			core.Message{Role: "assistant", Content: []core.Content{
				core.ToolCallContent("t", "read", map[string]any{"path": "a.png"}),
			}},
			core.NewToolResultMessage("t", "read", []core.Content{
				core.ImageContent(img, "image/jpeg"),
			}, false),
		)
	}

	outcomes := imageOutcomes(convertMessages(msgs, false))
	images, notes := countImages(outcomes)
	if images != 13 || notes != 8 {
		t.Fatalf("got %d images and %d notes, want 13 and 8", images, notes)
	}
	for i := 0; i < 8; i++ {
		if outcomes[i] == "image" {
			t.Fatalf("tool_result image %d should be retired", i)
		}
	}
}

// Documents do not count toward the threshold on the direct API, so a
// conversation full of PDFs must not push images into retirement.
func TestConvertMessages_DocumentsDoNotCount(t *testing.T) {
	var msgs []core.Message
	img := jpegB64(t, 100, 200)
	for i := 0; i < 20; i++ {
		msgs = append(msgs, core.Message{Role: "user", Content: []core.Content{
			core.ImageContent(img, "image/jpeg"),
		}})
		msgs = append(msgs, core.Message{Role: "assistant", Content: []core.Content{
			core.TextContent("ok"),
		}})
	}
	for i := 0; i < 10; i++ {
		msgs = append(msgs, core.Message{Role: "user", Content: []core.Content{
			core.DocumentContent("ZGF0YQ==", "application/pdf", "report.pdf"),
		}})
		msgs = append(msgs, core.Message{Role: "assistant", Content: []core.Content{
			core.TextContent("ok"),
		}})
	}

	outcomes := imageOutcomes(convertMessages(msgs, false))
	images, notes := countImages(outcomes)
	if images != 20 || notes != 0 {
		t.Fatalf("got %d images and %d notes, want 20 and 0", images, notes)
	}
}

// An image whose header cannot be read still counts toward N and still
// consumes a retirement slot, and its note uses the dimension-less wording.
// A huge image hiding behind an unreadable header therefore cannot keep the
// wire request above the threshold.
func TestConvertMessages_UnreadableHeaderCountsAndRetires(t *testing.T) {
	garbage := base64.StdEncoding.EncodeToString([]byte("not an image at all"))
	images := append([]string{garbage}, smallImages(t, 21)...)
	msgs := convertMessages(imageTurns(t, images), false)

	outcomes := imageOutcomes(msgs)
	imgs, notes := countImages(outcomes)
	if imgs != 14 || notes != 8 {
		t.Fatalf("images = %d, notes = %d, want 14 kept and 8 retired", imgs, notes)
	}
	// The unreadable image is the oldest, so it must be among the retired,
	// with the dimension-less note.
	if !strings.HasPrefix(outcomes[0], "note:[image omitted: an older image was retired") {
		t.Fatalf("oldest outcome = %q, want dimension-less retirement note", outcomes[0])
	}
}
