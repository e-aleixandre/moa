package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/creack/pty"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/secretaction"
)

// RunWithSecretToolName is the model-facing name of the PTY secret tool.
const RunWithSecretToolName = "run_with_secret"

var (
	defaultPasswordPrompt = regexp.MustCompile(`(?i)\b(password|passphrase|contraseña|pin)\b[^\n]*:\s*$`)
	defaultUsernamePrompt = regexp.MustCompile(`(?i)(user(name)?|login|e-?mail)[^\n]*:\s*$`)
	// authFailure is the only heuristic: a prompt that comes back after one of
	// these words means the value was wrong, and resending it would loop.
	authFailure = regexp.MustCompile(`(?i)(incorrect|wrong|denied|failed|invalid|try again|sorry)`)
)

const (
	// maxPromptTail bounds the current-line buffer prompts are matched on.
	maxPromptTail = 4 << 10
	// failWindow is the output kept from the previous chunk so a failure
	// message split between two reads is still recognised.
	failWindow = 32
	// ptyDrainDelay is how long the reader may outlive the command: a
	// background grandchild can keep the terminal open after bash exits.
	ptyDrainDelay  = 2 * time.Second
	maxPromptRegex = 500
)

// NewRunWithSecret creates the run_with_secret tool: the user approves the
// exact command and hands it values that are typed into its terminal when a
// prompt appears, without the values reaching the model or the disk.
func NewRunWithSecret(cfg ToolConfig, broker *secretaction.Broker) core.Tool {
	cfg.Defaults()
	return core.Tool{
		Name:  RunWithSecretToolName,
		Label: "Run with secret",
		Description: "Run a command that asks for a password (sudo, ssh, security unlock-keychain, a login prompt) and let the user type or autofill the value. " +
			"The user sees the exact command and approves it every time; the server types each value into the command's terminal when its prompt appears and answers every prompt of this execution. " +
			"You never see the values: they are replaced by [secret] in the output. " +
			"Never put a secret in the command line, an environment variable or a file. " +
			"For a remote command use `ssh -tt host …` so the remote program gets a terminal and prompts. " +
			"alias is a stable short name for the credential (e.g. \"agents-keychain\") so the user's phone can offer a saved value. " +
			"The command runs with moa's own environment and the given cwd: exports and environment changes from earlier bash calls do not apply, so set anything it needs in the command itself. " +
			"Output is truncated to 50KB. Returns the output, the exit code and how many prompts were answered.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"command": {"type": "string", "description": "The bash command to run on a terminal"},
				"cwd": {"type": "string", "description": "Working directory (default: workspace root)"},
				"timeout": {"type": "integer", "description": "Timeout in seconds, counted from the user's approval (default: 300)"},
				"fields": {
					"type": "array",
					"minItems": 1,
					"maxItems": 4,
					"description": "The values the command will prompt for.",
					"items": {
						"type": "object",
						"properties": {
							"label": {"type": "string", "description": "Short name shown to the user, e.g. \"Keychain password\""},
							"kind": {"type": "string", "enum": ["password", "username", "text"], "description": "What the value is (default password)"},
							"alias": {"type": "string", "description": "Stable short name of the credential, e.g. \"agents-keychain\""},
							"prompt": {"type": "string", "description": "Regex matched against the current output line to detect this field's prompt. Defaults: password-like prompts ending in ':' for password, user/login/email prompts for username. Required for kind text."}
						},
						"required": ["label"]
					}
				}
			},
			"required": ["command", "fields"]
		}`),
		Effect: core.EffectInteractive,
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			command := getString(params, "command", "")
			if command == "" {
				return core.ErrorResult("command is required"), nil
			}
			prompts, cardFields, err := parseSecretFields(params)
			if err != nil {
				return core.ErrorResult(err.Error()), nil
			}
			// The persisted shell environment is not inherited: the card shows
			// command and cwd, and an earlier export (PATH, an alias file)
			// would silently change what that same command runs.
			cwd, _, _, err := bashCwdAndEnv(ctx, cfg, params)
			if err != nil {
				return core.ErrorResult(err.Error()), nil
			}
			timeout := bashTimeout(cfg, params)
			if broker == nil {
				return core.ErrorResult("nobody can answer a secret request in this session"), nil
			}

			host := secretaction.ParseSSHHost(command)
			ans, err := broker.Request(ctx, secretaction.Request{
				Requester:         secretaction.Requester(ctx),
				Kind:              secretaction.KindCommand,
				Command:           command,
				Cwd:               cwd,
				SSHHost:           host,
				Fields:            cardFields,
				ActionFingerprint: secretaction.CommandFingerprint(command, cwd, host),
			})
			// Every path below returns through here: the values die with the
			// execution.
			defer ans.Wipe()
			if err != nil {
				return core.ErrorResult("cancelled"), nil
			}
			if !ans.Allow {
				return core.RejectedResult("The user denied the request; the command did not run."), nil
			}
			for i := range prompts {
				prompts[i].value = ans.Values[i]
			}
			return runOnPTY(ctx, command, cwd, timeout, prompts, ans.Values, onUpdate), nil
		},
	}
}

type secretPrompt struct {
	kind      string
	re        *regexp.Regexp
	custom    bool
	value     []byte
	delivered bool
}

func parseSecretFields(params map[string]any) ([]secretPrompt, []secretaction.Field, error) {
	raw, _ := params["fields"].([]any)
	if len(raw) == 0 {
		return nil, nil, errors.New("fields is required: list the values the command will prompt for")
	}
	if len(raw) > secretaction.MaxFields {
		return nil, nil, fmt.Errorf("too many fields, max %d", secretaction.MaxFields)
	}
	prompts := make([]secretPrompt, 0, len(raw))
	fields := make([]secretaction.Field, 0, len(raw))
	for i, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("field %d must be an object", i+1)
		}
		label := getString(m, "label", "")
		kind := getString(m, "kind", secretaction.FieldPassword)
		alias := getString(m, "alias", "")
		if err := secretaction.ValidateField(label, kind, alias); err != nil {
			return nil, nil, fmt.Errorf("field %d: %w", i+1, err)
		}
		p := secretPrompt{kind: kind}
		if expr := getString(m, "prompt", ""); expr != "" {
			if len(expr) > maxPromptRegex {
				return nil, nil, fmt.Errorf("field %d: prompt regex too long (max %d)", i+1, maxPromptRegex)
			}
			re, err := regexp.Compile(expr)
			if err != nil {
				return nil, nil, fmt.Errorf("field %d: invalid prompt regex: %v", i+1, err)
			}
			p.re, p.custom = re, true
		} else {
			switch kind {
			case secretaction.FieldPassword:
				p.re = defaultPasswordPrompt
			case secretaction.FieldUsername:
				p.re = defaultUsernamePrompt
			default:
				return nil, nil, fmt.Errorf("field %d: a text field needs a prompt regex", i+1)
			}
		}
		prompts = append(prompts, p)
		fields = append(fields, secretaction.Field{Label: label, Kind: kind, Alias: alias})
	}
	return prompts, fields, nil
}

// ptyRun is the state of one execution. Everything but the final result and
// exited is touched only by the reader goroutine until it finishes.
type ptyRun struct {
	ctx      context.Context
	ptmx     *os.File
	cmd      *exec.Cmd
	prompts  []secretPrompt
	redactor *secretaction.Redactor
	out      headTailBuffer
	onUpdate func(core.Result)

	// exited is set once the command has ended: output is still drained,
	// but no prompt is answered after that (a background child may still
	// hold the terminal).
	exited atomic.Bool

	ansi     ansiStripper
	tail     wipeBuffer // current line, without escapes: prompts are matched on it
	failTail wipeBuffer // end of the previous chunk, for a failure split across reads
	failSeen bool       // a failure message appeared since the first delivery

	answered    int
	byKind      map[string]int
	authStopped bool
	heldCR      bool
}

func newPtyRun(ctx context.Context, ptmx *os.File, cmd *exec.Cmd, prompts []secretPrompt, values [][]byte, onUpdate func(core.Result)) *ptyRun {
	r := &ptyRun{
		ctx:      ctx,
		ptmx:     ptmx,
		cmd:      cmd,
		prompts:  prompts,
		redactor: secretaction.NewRedactor(values),
		onUpdate: onUpdate,
		tail:     newWipeBuffer(maxPromptTail),
		failTail: newWipeBuffer(failWindow),
		byKind:   map[string]int{},
	}
	r.out.headMax = maxOutputBytes / 2
	r.out.tailMax = maxOutputBytes / 2
	r.out.NoSpill = true
	return r
}

func runOnPTY(ctx context.Context, command, cwd string, timeout time.Duration, prompts []secretPrompt, values [][]byte, onUpdate func(core.Result)) core.Result {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "NO_COLOR=1", "TERM=dumb")
	setSessionCancel(cmd)
	cmd.WaitDelay = 5 * time.Second

	// Wide enough that a prompt is never wrapped before it is matched.
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 50, Cols: 250})
	if err != nil {
		return core.ErrorResult(fmt.Sprintf("failed to start on a terminal: %v", err))
	}
	defer func() { _ = ptmx.Close() }()

	r := newPtyRun(ctx, ptmx, cmd, prompts, values, onUpdate)
	defer r.tail.wipe()
	defer r.failTail.wipe()

	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		buf := make([]byte, 32<<10)
		defer secretaction.Wipe(buf)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				r.feed(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()

	waitErr := cmd.Wait()
	r.exited.Store(true)
	if ctx.Err() != nil {
		killProcGroup(cmd)
	}
	select {
	case <-readerDone:
	case <-time.After(ptyDrainDelay):
		killProcGroup(cmd)
		_ = ptmx.Close()
		<-readerDone
	}
	r.emit(r.redactor.Flush())
	r.out.Close()

	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return core.ErrorResult("command timed out\n" + r.report(0))
		}
		return core.ErrorResult("command cancelled")
	}
	if r.authStopped {
		return core.ErrorResult(r.report(0) + "\nStopped: the prompt reappeared after an authentication failure; the password was not resent.")
	}
	exitCode := 0
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			killProcGroup(cmd)
			return core.ErrorResult(fmt.Sprintf("exec: %v\n%s", waitErr, r.report(0)))
		}
	}
	return core.TextResult(r.report(exitCode))
}

// feed handles one chunk of terminal output: it removes escape sequences
// (even when one is split between reads), remembers the current line for
// prompt matching, notes a failure message, emits the redacted bytes, then
// answers a prompt. Redaction runs on exactly the text that is emitted, so no
// later transformation can rebuild a value it missed.
func (r *ptyRun) feed(chunk []byte) {
	clean := r.ansi.strip(chunk)
	defer secretaction.Wipe(clean)
	if i := bytes.LastIndexAny(clean, "\r\n"); i >= 0 {
		r.tail.reset()
		r.tail.append(clean[i+1:])
	} else {
		r.tail.append(clean)
	}
	r.noteAuthFailure(clean)
	lines := r.normalizeCRLF(clean)
	r.emit(r.redactor.Write(lines))
	secretaction.Wipe(lines)
	r.answer()
}

// noteAuthFailure latches a failure message, however much output follows it
// before a prompt returns.
func (r *ptyRun) noteAuthFailure(clean []byte) {
	if !r.failSeen {
		// The previous chunk's end joined to this one's start catches a
		// message split between two reads.
		var joined [2 * failWindow]byte
		n := copy(joined[:], r.failTail.bytes())
		n += copy(joined[n:], clean[:min(len(clean), failWindow)])
		r.failSeen = authFailure.Match(joined[:n]) || authFailure.Match(clean)
		secretaction.Wipe(joined[:n])
	}
	r.failTail.append(clean)
}

// normalizeCRLF returns p with CRLF turned into LF, holding a trailing CR
// until the next chunk shows whether an LF follows. The result is fresh.
func (r *ptyRun) normalizeCRLF(p []byte) []byte {
	out := make([]byte, 0, len(p)+1)
	if r.heldCR {
		out = append(out, '\r')
		r.heldCR = false
	}
	out = append(out, p...)
	if n := len(out); n > 0 && out[n-1] == '\r' {
		out = out[:n-1]
		r.heldCR = true
	}
	j := 0
	for i := 0; i < len(out); i++ {
		if out[i] == '\r' && i+1 < len(out) && out[i+1] == '\n' {
			continue
		}
		out[j] = out[i]
		j++
	}
	secretaction.Wipe(out[j:])
	return out[:j]
}

func (r *ptyRun) answer() {
	if r.authStopped {
		return
	}
	line := r.tail.bytes()
	if len(line) == 0 {
		return
	}
	p := r.match(line)
	if p == nil {
		return
	}
	if p.delivered && r.failSeen {
		r.authStopped = true
		killProcGroup(r.cmd)
		return
	}
	// The approval covers this live execution only: after Stop, a timeout or
	// the command's exit, nothing is typed into the terminal.
	if r.ctx.Err() != nil || r.exited.Load() {
		return
	}
	msg := make([]byte, 0, len(p.value)+1)
	msg = append(msg, p.value...)
	msg = append(msg, '\n')
	_, _ = r.ptmx.Write(msg)
	secretaction.Wipe(msg)
	// A failure stays latched across later deliveries: answering a new
	// field (a username after "Sorry, try again.") does not make resending a
	// rejected one safe. Only output before the first delivery cannot be
	// about a value of ours.
	if r.answered == 0 {
		r.failSeen = false
	}
	p.delivered = true
	r.answered++
	r.byKind[p.kind]++
	r.tail.reset()
	r.failTail.reset()
}

// match picks the field whose prompt the current line shows: explicit
// prompts first, then password fields (a "Password for user x:" line also
// looks like a username prompt), then the rest, each in field order.
func (r *ptyRun) match(line []byte) *secretPrompt {
	for pass := 0; pass < 3; pass++ {
		for i := range r.prompts {
			p := &r.prompts[i]
			switch pass {
			case 0:
				if !p.custom {
					continue
				}
			case 1:
				if p.custom || p.kind != secretaction.FieldPassword {
					continue
				}
			case 2:
				if p.custom || p.kind == secretaction.FieldPassword {
					continue
				}
			}
			if p.re.Match(line) {
				return p
			}
		}
	}
	return nil
}

// emit records redacted output and streams it live.
func (r *ptyRun) emit(p []byte) {
	if len(p) == 0 {
		return
	}
	accepted := r.out.Append(p)
	if r.onUpdate != nil && accepted > 0 {
		r.onUpdate(core.TextResult(string(p[:accepted])))
	}
}

func (r *ptyRun) report(exitCode int) string {
	var sb strings.Builder
	out := r.out.String()
	if out == "" {
		out = "(no output)"
	}
	sb.WriteString(out)
	if !strings.HasSuffix(out, "\n") {
		sb.WriteString("\n")
	}
	if exitCode != 0 {
		fmt.Fprintf(&sb, "Exit code: %d\n", exitCode)
	}
	switch {
	case r.answered == 0:
		sb.WriteString("No prompt was answered: the expected prompt never appeared, so nothing was sent.")
	case r.byKind[secretaction.FieldPassword] == r.answered:
		fmt.Fprintf(&sb, "%d password %s answered.", r.answered, plural(r.answered, "prompt", "prompts"))
	default:
		var parts []string
		for _, k := range []string{secretaction.FieldUsername, secretaction.FieldPassword, secretaction.FieldText} {
			if n := r.byKind[k]; n > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", n, k))
			}
		}
		fmt.Fprintf(&sb, "%d %s answered (%s).", r.answered, plural(r.answered, "prompt", "prompts"), strings.Join(parts, ", "))
	}
	return sb.String()
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// wipeBuffer keeps the last max bytes in a fixed backing array, so raw output
// (which may hold an echoed value) is never copied into a buffer that the
// collector reclaims without being zeroed.
type wipeBuffer struct {
	buf []byte
}

func newWipeBuffer(max int) wipeBuffer {
	return wipeBuffer{buf: make([]byte, 0, max)}
}

func (w *wipeBuffer) append(p []byte) {
	max := cap(w.buf)
	if len(p) >= max {
		w.buf = w.buf[:max]
		copy(w.buf, p[len(p)-max:])
		return
	}
	if over := len(w.buf) + len(p) - max; over > 0 {
		copy(w.buf, w.buf[over:])
		secretaction.Wipe(w.buf[len(w.buf)-over:])
		w.buf = w.buf[:len(w.buf)-over]
	}
	w.buf = append(w.buf, p...)
}

func (w *wipeBuffer) bytes() []byte { return w.buf }

func (w *wipeBuffer) reset() {
	secretaction.Wipe(w.buf)
	w.buf = w.buf[:0]
}

func (w *wipeBuffer) wipe() {
	secretaction.Wipe(w.buf[:cap(w.buf)])
	w.buf = w.buf[:0]
}

// ansiStripper removes CSI, OSC and other escape sequences from a stream,
// keeping its state between chunks: a sequence split across two reads would
// otherwise survive in pieces and break up a value before redaction.
type ansiStripper struct {
	state int
}

const (
	ansiText = iota
	ansiEsc
	ansiEscIntermediate
	ansiCSI
	ansiOSC
	ansiOSCEsc
)

// strip returns the text of p outside escape sequences, in a fresh slice.
func (s *ansiStripper) strip(p []byte) []byte {
	out := make([]byte, 0, len(p))
	for _, c := range p {
		out = s.step(out, c)
	}
	return out
}

func (s *ansiStripper) step(out []byte, c byte) []byte {
	switch s.state {
	case ansiEsc:
		switch {
		case c == '[':
			s.state = ansiCSI
		case c == ']':
			s.state = ansiOSC
		case c == 0x1b:
		case c >= 0x20 && c <= 0x2f:
			s.state = ansiEscIntermediate
		case c >= 0x30 && c <= 0x7e:
			s.state = ansiText // two-byte escape: drop both
		default:
			s.state = ansiText
			return append(out, c)
		}
		return out
	case ansiEscIntermediate:
		switch {
		case c >= 0x20 && c <= 0x2f:
		case c >= 0x30 && c <= 0x7e:
			s.state = ansiText
		default:
			s.state = ansiText
			return s.step(out, c)
		}
		return out
	case ansiCSI:
		switch {
		case c >= 0x20 && c <= 0x3f:
		case c >= 0x40 && c <= 0x7e:
			s.state = ansiText
		default:
			// Malformed: the sequence ends here and c is ordinary input.
			s.state = ansiText
			return s.step(out, c)
		}
		return out
	case ansiOSC:
		switch c {
		case 0x07:
			s.state = ansiText
		case 0x1b:
			s.state = ansiOSCEsc
		case '\n':
			// An unterminated title must not swallow the rest of the output.
			s.state = ansiText
			return append(out, c)
		}
		return out
	case ansiOSCEsc:
		if c == '\\' {
			s.state = ansiText
			return out
		}
		s.state = ansiEsc
		return s.step(out, c)
	}
	if c == 0x1b {
		s.state = ansiEsc
		return out
	}
	return append(out, c)
}
