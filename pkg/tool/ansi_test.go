package tool

import "testing"

func TestStripANSI(t *testing.T) {
	in := []byte("\x1b[33m\x1b[2m✓\x1b[22m\x1b[39m ok \x1b]8;;http://x\x07link\x1b]8;;\x07 \x1b[1A\r")
	got := string(stripANSI(in))
	if got != "✓ ok link \r" {
		t.Fatalf("got %q", got)
	}
}
