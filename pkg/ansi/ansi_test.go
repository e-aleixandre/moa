package ansi

import "testing"

func TestStripRemovesTerminalControls(t *testing.T) {
	in := "safe\x1b]52;c;clipboard\a\x1b[2J\x1b[Htext\x1b[31mred\x1b[0m"
	if got, want := Strip(in), "safetextred"; got != want {
		t.Fatalf("Strip() = %q, want %q", got, want)
	}
}

func TestAllowSGRRetainsOnlyStyling(t *testing.T) {
	in := "\x1b[31mred\x1b[0m\x1b]0;title\a\x1b[2J"
	if got, want := AllowSGR(in), "\x1b[31mred\x1b[0m"; got != want {
		t.Fatalf("AllowSGR() = %q, want %q", got, want)
	}
}
