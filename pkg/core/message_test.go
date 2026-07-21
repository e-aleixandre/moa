package core

import "testing"

func TestNativeDocBytes(t *testing.T) {
	img := "0123456789ABCDEF" // 16 base64 chars → DecodedLen 12
	content := []Content{
		TextContent("hi"),
		ImageContent(img, "image/png"),
		DocumentContent(img, "application/pdf", "x.pdf"),
		ThinkingContent("nope"),
		ToolCallContent("tc", "edit", nil),
	}
	if got := NativeDocBytes(content); got != 24 {
		t.Fatalf("NativeDocBytes = %d, want 24", got)
	}
	if got := NativeDocBytes(nil); got != 0 {
		t.Fatalf("NativeDocBytes(nil) = %d, want 0", got)
	}
}

func TestNativeDocBytesAttachmentReference(t *testing.T) {
	inline := "0123456789ABCDEF" // 16 base64 chars → DecodedLen 12
	content := []Content{
		{Type: "image", AttachmentID: "att_aaaaaaaaaaaaaaaaaaaaaaaa", AttachmentSize: 42},
		{Type: "document", Data: inline},
	}
	if got := NativeDocBytes(content); got != 54 {
		t.Fatalf("NativeDocBytes = %d, want 54", got)
	}
}
