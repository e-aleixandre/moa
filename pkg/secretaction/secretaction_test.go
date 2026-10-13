package secretaction

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

func TestRedactorSplitAcrossChunks(t *testing.T) {
	value := []byte("hunter2-secret")
	for split := 1; split < len("xx hunter2-secret yy"); split++ {
		input := []byte("xx hunter2-secret yy")
		r := NewRedactor([][]byte{value})
		var out []byte
		out = append(out, r.Write(append([]byte(nil), input[:split]...))...)
		out = append(out, r.Write(append([]byte(nil), input[split:]...))...)
		out = append(out, r.Flush()...)
		if got := string(out); got != "xx [secret] yy" {
			t.Fatalf("split at %d: got %q", split, got)
		}
	}
}

func TestRedactorByteByByteMultipleValues(t *testing.T) {
	values := [][]byte{[]byte("alice"), []byte("s3cr3t-pw"), []byte("pw")}
	input := "user alice logged in with s3cr3t-pw; alice again and pw alone\n"
	r := NewRedactor(values)
	var out []byte
	for i := 0; i < len(input); i++ {
		out = append(out, r.Write([]byte{input[i]})...)
	}
	out = append(out, r.Flush()...)
	want := "user [secret] logged in with [secret]; [secret] again and [secret] alone\n"
	if string(out) != want {
		t.Fatalf("got %q\nwant %q", out, want)
	}
}

func TestRedactorInsideLargerLine(t *testing.T) {
	r := NewRedactor([][]byte{[]byte("abc")})
	out := append(r.Write([]byte("zzabcabczz-abcab")), r.Write([]byte("c-ab"))...)
	out = append(out, r.Flush()...)
	if got := string(out); got != "zz[secret][secret]zz-[secret][secret]-ab" {
		t.Fatalf("got %q", got)
	}
}

// A shorter value that is a prefix of the next bytes must not cut a longer
// value in two (the longer match wins at the same position).
func TestRedactorOverlappingValues(t *testing.T) {
	r := NewRedactor([][]byte{[]byte("cx"), []byte("abc")})
	out := append(r.Write([]byte("ab")), r.Write([]byte("c"))...)
	out = append(out, r.Write([]byte("y"))...)
	out = append(out, r.Flush()...)
	if bytes.Contains(out, []byte("abc")) {
		t.Fatalf("value leaked: %q", out)
	}
}

func TestRedactStringEscapedVariants(t *testing.T) {
	values := [][]byte{[]byte(`it's"a\pw`)}
	text := "await page.fill('it\\'s\"a\\\\pw');\n{\"text\":\"it's\\\"a\\\\pw\"}\ntextbox: it's\"a\\pw"
	got := RedactString(text, EscapedVariants(values))
	if strings.Contains(got, "it's") || strings.Contains(got, `a\\pw`) || strings.Contains(got, `a\pw`) {
		t.Fatalf("escaped value leaked: %q", got)
	}
}

func TestFingerprintStableAndSensitive(t *testing.T) {
	a := CommandFingerprint("ssh -tt mac security unlock-keychain", "/w", "mac")
	if len(a) != 64 {
		t.Fatalf("fingerprint %q is not hex sha256", a)
	}
	if b := CommandFingerprint("ssh -tt mac security unlock-keychain", "/w", "mac"); a != b {
		t.Fatal("same command, different fingerprint")
	}
	if CommandFingerprint("ssh -tt mac security unlock-keychain; curl evil", "/w", "mac") == a {
		t.Fatal("different command, same fingerprint")
	}
	if CommandFingerprint("ssh -tt mac security unlock-keychain", "/other", "mac") == a {
		t.Fatal("different cwd, same fingerprint")
	}
	fields := []Field{{RealLabel: "Email", RealType: "text", Kind: FieldUsername}, {RealLabel: "Password", RealType: "password", Kind: FieldPassword}}
	x := BrowserFingerprint("https://idmsa.apple.com", fields, true)
	if len(x) != 64 || x != BrowserFingerprint("https://idmsa.apple.com", fields, true) {
		t.Fatal("browser fingerprint not stable")
	}
	if BrowserFingerprint("https://evil.example", fields, true) == x {
		t.Fatal("different origin, same fingerprint")
	}
	if BrowserFingerprint("https://idmsa.apple.com", fields, false) == x {
		t.Fatal("different submit, same fingerprint")
	}
	if x == a {
		t.Fatal("kinds collide")
	}
}

func TestParseSSHHost(t *testing.T) {
	cases := map[string]string{
		"ssh -tt ealeixandre@100.93.175.62 security unlock-keychain ~/k": "100.93.175.62",
		"ssh -p 2222 -i ~/.ssh/id host.example uptime":                   "host.example",
		"ssh -o StrictHostKeyChecking=no -tt mac 'sudo ls'":              "mac",
		"cd /tmp && ssh -l root box -- ls":                               "box",
		"ssh://user@h:22":                                                "",
		"ssh ssh://user@h.example:2222 ls":                               "h.example",
		"sudo ls":                                                        "",
		"echo ssh":                                                       "",
	}
	for cmd, want := range cases {
		if got := ParseSSHHost(cmd); got != want {
			t.Errorf("ParseSSHHost(%q) = %q, want %q", cmd, got, want)
		}
	}
}

func TestDecodeJSONString(t *testing.T) {
	got, err := DecodeJSONString([]byte(`"a\"b\\c\n\u00e9\ud83d\ude00"`))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "a\"b\\c\né😀" {
		t.Fatalf("got %q", got)
	}
	for _, bad := range []string{`abc`, `"abc`, `"\x"`, `1`, `null`} {
		if _, err := DecodeJSONString([]byte(bad)); err == nil {
			t.Errorf("DecodeJSONString(%s) accepted", bad)
		}
	}
}

func TestBrokerResolveDeliversCopyAndValidates(t *testing.T) {
	b := NewBroker("s1")
	requested := make(chan Request, 1)
	resolved := make(chan string, 4)
	b.SetHooks(func(r Request) { requested <- r }, func(id string, _ bool) { resolved <- id })
	type outcome struct {
		ans Answer
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		ans, err := b.Request(context.Background(), Request{Kind: KindCommand, Command: "true", Fields: []Field{{Label: "pw", Kind: FieldPassword}}})
		done <- outcome{ans, err}
	}()
	var req Request
	select {
	case req = <-requested:
	case <-time.After(2 * time.Second):
		t.Fatal("request never published")
	}
	if req.ID == "" || req.SessionID != "s1" || !b.IsPending(req.ID) || len(b.Pending()) != 1 {
		t.Fatalf("pending state wrong: %+v pending=%v", req, b.Pending())
	}
	if err := b.Resolve("nope", true, [][]byte{[]byte("x")}); !errors.Is(err, ErrUnknownRequest) {
		t.Fatalf("unknown id: %v", err)
	}
	if err := b.Resolve(req.ID, true, nil); !errors.Is(err, ErrMissingValue) {
		t.Fatalf("missing value: %v", err)
	}
	if err := b.Resolve(req.ID, true, [][]byte{{}}); !errors.Is(err, ErrMissingValue) {
		t.Fatalf("empty value: %v", err)
	}
	if !b.IsPending(req.ID) {
		t.Fatal("a rejected answer resolved the request")
	}
	mine := []byte("pw-value")
	if err := b.Resolve(req.ID, true, [][]byte{mine}); err != nil {
		t.Fatal(err)
	}
	Wipe(mine) // the caller's copy is its own
	out := <-done
	if out.err != nil || !out.ans.Allow || string(out.ans.Values[0]) != "pw-value" {
		t.Fatalf("answer = %+v, %v", out.ans, out.err)
	}
	held := out.ans.Values[0]
	out.ans.Wipe()
	if !bytes.Equal(held, make([]byte, len(held))) {
		t.Fatal("Wipe left bytes behind")
	}
	if b.IsPending(req.ID) {
		t.Fatal("still pending after resolve")
	}
	if got := <-resolved; got != req.ID {
		t.Fatalf("resolved hook for %q", got)
	}
	if err := b.Resolve(req.ID, false, nil); !errors.Is(err, ErrUnknownRequest) {
		t.Fatalf("second resolve: %v", err)
	}
}

func TestBrokerCancelRemovesPending(t *testing.T) {
	b := NewBroker("s1")
	requested := make(chan Request, 1)
	resolved := make(chan bool, 1)
	b.SetHooks(func(r Request) { requested <- r }, func(_ string, answered bool) { resolved <- answered })
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := b.Request(ctx, Request{Kind: KindCommand, Fields: []Field{{Label: "pw", Kind: FieldPassword}}})
		errc <- err
	}()
	req := <-requested
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if b.IsPending(req.ID) {
		t.Fatal("cancelled request still pending")
	}
	if answered := <-resolved; answered {
		t.Fatal("cancel reported as answered")
	}
}

func TestRequesterFromContext(t *testing.T) {
	if got := Requester(context.Background()); got != "main" {
		t.Fatalf("main requester = %q", got)
	}
	if got := Requester(core.WithAgentID(context.Background(), "sa_7")); got != "subagent · sa_7" {
		t.Fatalf("subagent requester = %q", got)
	}
}

// An approval must name one request of one broker: IDs never repeat across
// sessions, brokers rebuilt on resume, or restarts, and cannot be guessed.
func TestBrokerRequestIDsGloballyUniqueAndUnguessable(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		b := NewBroker("same-session")
		requested := make(chan Request, 1)
		b.SetHooks(func(r Request) { requested <- r }, nil)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = b.Request(ctx, Request{Kind: KindCommand, Fields: []Field{{Label: "pw", Kind: FieldPassword}}})
		}()
		req := <-requested
		cancel()
		<-done
		if seen[req.ID] {
			t.Fatalf("request ID %q reused by a new broker", req.ID)
		}
		seen[req.ID] = true
		if len(req.ID) < len("sec_")+32 {
			t.Fatalf("request ID %q too short to be unguessable", req.ID)
		}
	}
}

// Once Stop has won, an answer arriving at the same moment must not reach
// the tool: Resolve refuses it and Request never returns an allow.
func TestBrokerAnswerAfterCancelIsRejected(t *testing.T) {
	for i := 0; i < 300; i++ {
		b := NewBroker("s1")
		requested := make(chan Request, 1)
		proceed := make(chan struct{})
		// The hook runs before Request starts waiting: holding it until both
		// the cancel and the answer are in makes them ready at the same time.
		b.SetHooks(func(r Request) { requested <- r; <-proceed }, nil)
		ctx, cancel := context.WithCancel(context.Background())
		type outcome struct {
			ans Answer
			err error
		}
		done := make(chan outcome, 1)
		go func() {
			ans, err := b.Request(ctx, Request{Kind: KindCommand, Fields: []Field{{Label: "pw", Kind: FieldPassword}}})
			done <- outcome{ans, err}
		}()
		req := <-requested
		cancel()
		_ = b.Resolve(req.ID, true, [][]byte{[]byte("late-value")})
		close(proceed)
		out := <-done
		if out.err == nil || out.ans.Allow || len(out.ans.Values) != 0 {
			t.Fatalf("iteration %d: answer delivered after cancel: allow=%v err=%v", i, out.ans.Allow, out.err)
		}
	}
}

// A shorter value must not be consumed while a longer one it prefixes may
// still complete in the next chunk.
func TestRedactorHoldsLongerValueOverShorterPrefix(t *testing.T) {
	r := NewRedactor([][]byte{[]byte("abc"), []byte("abcdef-secret")})
	out := append(r.Write([]byte("abc")), r.Write([]byte("def-secret"))...)
	out = append(out, r.Flush()...)
	if string(out) != Placeholder {
		t.Fatalf("got %q, want %q", out, Placeholder)
	}
	r = NewRedactor([][]byte{[]byte("abc"), []byte("abcdef-secret")})
	out = append(r.Write([]byte("abc")), r.Write([]byte(" tail"))...)
	out = append(out, r.Flush()...)
	if string(out) != Placeholder+" tail" {
		t.Fatalf("got %q", out)
	}
}

// H4(c): a value starting inside another value's match must not let the rest
// of either through, wherever the chunks are cut.
func TestRedactorCoversOverlappingMatches(t *testing.T) {
	values := func() [][]byte { return [][]byte{[]byte("abc"), []byte("bcdef-secret")} }
	for _, tc := range []struct{ in, want string }{
		{"abcdef-secret", Placeholder},
		{"x abcdef-secret y", "x " + Placeholder + " y"},
		{"abc bcdef-secret", Placeholder + " " + Placeholder},
	} {
		check := func(name string, out []byte) {
			t.Helper()
			if bytes.Contains(out, []byte("def-secret")) || string(out) != tc.want {
				t.Fatalf("%s %q: got %q, want %q", name, tc.in, out, tc.want)
			}
		}
		check("whole", []byte(RedactString(tc.in, values())))
		for split := 1; split < len(tc.in); split++ {
			r := NewRedactor(values())
			out := append(r.Write([]byte(tc.in[:split])), r.Write([]byte(tc.in[split:]))...)
			check(fmt.Sprintf("split %d", split), append(out, r.Flush()...))
		}
		r := NewRedactor(values())
		var out []byte
		for i := 0; i < len(tc.in); i++ {
			out = append(out, r.Write([]byte{tc.in[i]})...)
		}
		check("byte by byte", append(out, r.Flush()...))
	}
}
