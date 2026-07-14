package bus

import "testing"

func TestClassifyCommand_Policies(t *testing.T) {
	cases := []struct {
		raw  string
		want QueuePolicy
	}{
		// Barrier commands (must wait for idle).
		{"/compact", PolicyQueue},
		{"compact", PolicyQueue},
		{"  /compact  ", PolicyQueue},
		{"/clear", PolicyQueue},
		{"/model sonnet", PolicyQueue},
		{"/model", PolicyQueue}, // picker still needs a settled run
		{"/thinking high", PolicyQueue},
		{"/verify", PolicyQueue},

		// Reject while busy.
		{"/undo", PolicyReject},
		{"/branch", PolicyReject},
		{"/back", PolicyReject},
		{"/plan", PolicyReject},
		{"/plan exit", PolicyReject},

		// Instant.
		{"/rename new title", PolicyInstant},
		{"/permissions", PolicyInstant},
		{"/path add x", PolicyInstant},
		{"/tasks", PolicyInstant},
		{"/schedule list", PolicyInstant},

		// goal is argument-dependent.
		{"/goal", PolicyInstant},
		{"/goal status", PolicyInstant},
		{"/goal stop", PolicyInstant},
		{"/goal ship the feature", PolicyQueue},
		{"/goal start", PolicyQueue},

		// Malformed / unknown.
		{"", PolicyInstant},
		{"/", PolicyInstant},
		{"   ", PolicyInstant},
		{"/bogus", PolicyInstant},
		{"notacommand", PolicyInstant},
	}
	for _, c := range cases {
		if got := ClassifyCommand(c.raw); got != c.want {
			t.Errorf("ClassifyCommand(%q) = %v, want %v", c.raw, got, c.want)
		}
	}
}

func TestClassifyCommand_CaseInsensitiveName(t *testing.T) {
	if got := ClassifyCommand("/Compact"); got != PolicyQueue {
		t.Errorf("ClassifyCommand(/Compact) = %v, want queue", got)
	}
	if got := ClassifyCommand("/GOAL status"); got != PolicyInstant {
		t.Errorf("ClassifyCommand(/GOAL status) = %v, want instant", got)
	}
}

func TestSplitCommand(t *testing.T) {
	cases := []struct {
		in, name, rest string
	}{
		{"/model sonnet", "model", "sonnet"},
		{"compact", "compact", ""},
		{"  /goal  ship it  ", "goal", "ship it"},
		{"", "", ""},
		{"/", "", ""},
		{"/GOAL Status", "goal", "Status"}, // rest keeps original case
	}
	for _, c := range cases {
		name, rest := splitCommand(c.in)
		if name != c.name || rest != c.rest {
			t.Errorf("splitCommand(%q) = (%q,%q), want (%q,%q)", c.in, name, rest, c.name, c.rest)
		}
	}
}

func TestQueuePolicyString(t *testing.T) {
	if PolicyInstant.String() != "instant" || PolicyQueue.String() != "queue" || PolicyReject.String() != "reject" {
		t.Fatal("QueuePolicy.String mismatch")
	}
}
