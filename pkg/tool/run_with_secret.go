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
	// maxSinceDelivery bounds the output kept to recognise an auth failure.
	maxSinceDelivery = 8 << 10
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
			cwd, env, _, err := bashCwdAndEnv(ctx, cfg, params)
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
				return core.ErrorResult("The user denied the request; the command did not run."), nil
			}
			for i := range prompts {
				prompts[i].value = ans.Values[i]
			}
			return runOnPTY(ctx, command, cwd, env, timeout, prompts, ans.Values, onUpdate), nil
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

// ptyRun is the state of one execution. Everything but the final result is
// touched only by the reader goroutine until it finishes.
type ptyRun struct {
	ptmx     *os.File
	cmd      *exec.Cmd
	prompts  []secretPrompt
	redactor *secretaction.Redactor
	out      headTailBuffer
	onUpdate func(core.Result)

	tail  wipeBuffer // current line, raw: prompts are matched on it
	since wipeBuffer // raw output since the last delivery

	answered    int
	byKind      map[string]int
	authStopped bool
	heldCR      bool
}

func runOnPTY(ctx context.Context, command, cwd string, env []string, timeout time.Duration, prompts []secretPrompt, values [][]byte, onUpdate func(core.Result)) core.Result {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = cwd
	if env == nil {
		env = os.Environ()
	}
	cmd.Env = append(env, "NO_COLOR=1", "TERM=dumb")
	setSessionCancel(cmd)
	cmd.WaitDelay = 5 * time.Second

	// Wide enough that a prompt is never wrapped before it is matched.
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 50, Cols: 250})
	if err != nil {
		return core.ErrorResult(fmt.Sprintf("failed to start on a terminal: %v", err))
	}
	defer func() { _ = ptmx.Close() }()

	r := &ptyRun{
		ptmx:     ptmx,
		cmd:      cmd,
		prompts:  prompts,
		redactor: secretaction.NewRedactor(values),
		onUpdate: onUpdate,
		tail:     newWipeBuffer(maxPromptTail),
		since:    newWipeBuffer(maxSinceDelivery),
		byKind:   map[string]int{},
	}
	r.out.headMax = maxOutputBytes / 2
	r.out.tailMax = maxOutputBytes / 2
	r.out.NoSpill = true
	defer r.tail.wipe()
	defer r.since.wipe()

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

// feed handles one chunk of terminal output: it remembers the raw current
// line for prompt matching, emits the redacted bytes, then answers a prompt.
func (r *ptyRun) feed(chunk []byte) {
	clean := stripANSI(chunk)
	r.since.append(clean)
	if i := bytes.LastIndexAny(clean, "\r\n"); i >= 0 {
		r.tail.reset()
		r.tail.append(clean[i+1:])
	} else {
		r.tail.append(clean)
	}
	r.emit(r.redactor.Write(clean))
	r.answer()
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
	if p.delivered && authFailure.Match(r.since.bytes()) {
		r.authStopped = true
		killProcGroup(r.cmd)
		return
	}
	msg := make([]byte, 0, len(p.value)+1)
	msg = append(msg, p.value...)
	msg = append(msg, '\n')
	_, _ = r.ptmx.Write(msg)
	secretaction.Wipe(msg)
	p.delivered = true
	r.answered++
	r.byKind[p.kind]++
	r.tail.reset()
	r.since.reset()
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

// emit records redacted output and streams it live. A terminal ends lines
// with CRLF; they become LF, holding a trailing CR until the next chunk shows
// whether an LF follows.
func (r *ptyRun) emit(p []byte) {
	if r.heldCR {
		p = append([]byte{'\r'}, p...)
		r.heldCR = false
	}
	if n := len(p); n > 0 && p[n-1] == '\r' {
		p = p[:n-1]
		r.heldCR = true
	}
	p = bytes.ReplaceAll(p, []byte("\r\n"), []byte("\n"))
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
