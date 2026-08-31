package bus

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

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
		{"/handoff", PolicyReject},
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
		// A newline between name and argument (a multiline composer allows it)
		// must split the same as a space, so a busy-session /compact still
		// classifies as a command rather than one long unknown name.
		{"/compact\nkeep phase 3", "compact", "keep phase 3"},
		{"/compact\tkeep phase 3", "compact", "keep phase 3"},
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

// The frontend mirrors this table in command-policy.js to decide whether a
// command typed mid-run is queued or refused. They drifted before: /reload was
// added here and the mirror had to follow. This pins the set so a future
// addition fails here instead of silently disagreeing with the UI.
func TestQueuePolicy_MirrorsTheFrontendTable(t *testing.T) {
	// Read the mirror rather than restating it: a hand-copied expectation here
	// passes while the two tables disagree, which is the one thing this test
	// exists to catch. A command queued by the server but missing from the JS
	// set is rejected in the composer before it is ever sent.
	src, err := os.ReadFile(filepath.Join("..", "serve", "frontend", "src", "data", "util", "command-policy.js"))
	if err != nil {
		t.Fatal(err)
	}
	frontend := map[string]QueuePolicy{}
	for _, set := range []struct {
		name   string
		policy QueuePolicy
	}{{"QUEUE", PolicyQueue}, {"REJECT", PolicyReject}} {
		re := regexp.MustCompile(`(?s)const ` + set.name + ` = new Set\(\[(.*?)\]\)`)
		m := re.FindSubmatch(src)
		if m == nil {
			t.Fatalf("could not find the %s set in command-policy.js", set.name)
		}
		for _, q := range regexp.MustCompile(`'([^']+)'`).FindAllSubmatch(m[1], -1) {
			frontend[string(q[1])] = set.policy
		}
	}

	if len(queuePolicyByName) != len(frontend) {
		t.Errorf("the tables disagree: %d entries in Go, %d in command-policy.js", len(queuePolicyByName), len(frontend))
	}
	for name, policy := range frontend {
		if got := queuePolicyByName[name]; got != policy {
			t.Errorf("%s: %v in Go, %v in command-policy.js", name, got, policy)
		}
	}
	for name := range queuePolicyByName {
		if _, ok := frontend[name]; !ok {
			t.Errorf("%s is in the Go table but missing from command-policy.js", name)
		}
	}
}
