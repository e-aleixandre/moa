package serve

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/tool"
)

// Callback statuses. They describe the run, not the session: a session can
// report needs_input and later done, and can report done once per run.
const (
	callbackStatusDone       = "done"
	callbackStatusFailed     = "failed"
	callbackStatusNeedsInput = "needs_input"
)

// callbackTimeout bounds a single delivery attempt (connect + response).
const callbackTimeout = 10 * time.Second

// maxCallbackResponseBytes caps what we read back from the receiver. The body
// is discarded either way — the cap only stops a hostile endpoint from
// streaming forever inside the attempt timeout.
const maxCallbackResponseBytes = 1 << 20

// maxCallbackSummaryBytes truncates the summary carried in the payload. The
// detail lives in the session; the callback carries a hint plus a link.
const maxCallbackSummaryBytes = 500

// summaryEllipsis marks a truncated field. Its bytes count against the cap.
const summaryEllipsis = "…"

// callbackBackoff is the wait before each retry: three attempts total, so only
// the first two waits are ever used. Overridden in tests.
var callbackBackoff = []time.Duration{time.Second, 5 * time.Second, 25 * time.Second}

// errCallbackRedirect marks a refused redirect: a permanent configuration
// problem, never retried (see newCallbackClient).
var errCallbackRedirect = errors.New("callback redirect refused")

// AutomationCallback is the JSON body POSTed to an automation session's
// callback_url. It is deliberately small: a status, a hint of what happened and
// a link back to the session, which holds the full transcript.
type AutomationCallback struct {
	SessionID string `json:"session_id"`
	Status    string `json:"status"` // done | failed | needs_input
	Title     string `json:"title"`
	Summary   string `json:"summary"`
	URL       string `json:"url"`
	Error     string `json:"error,omitempty"`
	// Pending describes what the run is blocked on. Only set for needs_input,
	// so a machine caller can answer it through the scoped interaction
	// endpoints instead of only learning that a human is needed.
	Pending   *CallbackPending `json:"pending,omitempty"`
	Timestamp string           `json:"timestamp"` // RFC3339
}

// CallbackPending is the interaction a needs_input callback is blocked on. It
// mirrors the bus event that raised it: a question carries its questions, a
// permission carries the tool and a human-readable summary of its arguments.
type CallbackPending struct {
	Kind      string            `json:"kind"` // question | permission
	ID        string            `json:"id"`
	Questions []bus.AskQuestion `json:"questions,omitempty"`
	Tool      string            `json:"tool,omitempty"`
	Summary   string            `json:"summary,omitempty"`
}

// pendingKind values carried by CallbackPending.
const (
	pendingKindQuestion   = "question"
	pendingKindPermission = "permission"
)

// callbackTarget is the delivery configuration read from session metadata.
type callbackTarget struct {
	url    string
	secret string
}

// subscribeAutomationCallback wires the outbound completion callback for a
// session created through the Automation API. Sessions without a callback_url
// in their metadata — every ordinary user session — get no subscription at all,
// so nothing is ever delivered for them.
//
// The trigger policy mirrors the shape of subscribePush (same bus, same
// subscription list, unsubscribed by Delete before the runtime closes), but not
// its human-attention gating: subscribePush suppresses a notification when a
// browser is watching or the run was short, which are properties of a human
// looking at a screen. A machine caller is never watching, so a callback fires
// regardless of duration and connected clients.
//
//   - RunEnded with Err == nil → "done", but only once the session is fully
//     quiescent (no background subagent/bash work that could still push another
//     run), so we don't report completion in the middle of an autonomous chain.
//   - RunEnded with Err != nil → "failed", immediately: the run is over.
//   - PermissionRequested / AskUserRequested → "needs_input", carrying the
//     pending interaction, at most once per run (a run can ask many times; the
//     caller only needs to learn that somebody has to answer). A later RunEnded
//     still delivers done/failed.
//
// All four triggers are handled by ONE SubscribeAll subscriber: it sees events
// in publication order on a single goroutine, which is what makes the
// once-per-run guard exact. Separate typed subscriptions would each run on
// their own goroutine, so a late RunStarted could clear the guard after a
// needs_input was already delivered (double fire), or a stale blocking event
// from the previous run could fire against the new run's reset guard.
//
// Delivery is best-effort and always happens on its own goroutine: it never
// blocks the bus, the run, or shutdown.
func (m *Manager) subscribeAutomationCallback(sess *ManagedSession, meta map[string]any) {
	url, _ := meta[session.MetaCallbackURL].(string)
	if url == "" {
		return
	}
	// Re-validate what was stored: metadata is on disk and may predate (or
	// outlive) the validation the submitting endpoint applied.
	if err := validateCallbackURL(url); err != nil {
		slog.Warn("automation callback disabled: invalid callback_url", "session", sess.ID, "error", err)
		return
	}
	secret, _ := meta[session.MetaCallbackSecret].(string)
	cb := callbackTarget{url: url, secret: secret}

	// lastRunGen records the most recent run we saw end, so a "done" waiter that
	// was still waiting for quiescence when a newer run started drops out: the
	// newer run delivers its own callback.
	var lastRunGen atomic.Uint64
	// needsInputSent is cleared at the start of every run, so a run that asks
	// five times still produces one needs_input callback. Only ever touched from
	// the single subscriber goroutine below, in publication order.
	var needsInputSent bool

	b := sess.runtime.Bus
	needsInput := func(pending *CallbackPending) {
		if needsInputSent {
			return
		}
		needsInputSent = true
		go deliverAutomationCallback(sess, cb, callbackStatusNeedsInput, "", "", pending)
	}

	sess.pushUnsubs = append(sess.pushUnsubs, b.SubscribeAll(func(event any) {
		switch e := event.(type) {
		case bus.RunStarted:
			needsInputSent = false
		case bus.PermissionRequested:
			needsInput(permissionPending(e))
		case bus.AskUserRequested:
			needsInput(askPending(e))
		case bus.RunEnded:
			lastRunGen.Store(e.RunGen)
			if e.Err != nil {
				go deliverAutomationCallback(sess, cb, callbackStatusFailed, e.Err.Error(), e.FinalText, nil)
				return
			}
			go func() {
				// WaitQuiescent drains the bus, so it must not run on a
				// subscriber goroutine (it would wait on itself).
				if !sess.runtime.WaitQuiescent(sess.infra.sessionCtx) {
					return // session is going away; nothing useful to report
				}
				if lastRunGen.Load() != e.RunGen {
					return // superseded by a newer run, which reports for itself
				}
				deliverAutomationCallback(sess, cb, callbackStatusDone, "", e.FinalText, nil)
			}()
		}
	}))
}

// deliverAutomationCallback builds the payload and POSTs it with retries. It is
// always called on its own goroutine.
func deliverAutomationCallback(sess *ManagedSession, cb callbackTarget, status, errText, finalText string, pending *CallbackPending) {
	if sess.deleted.Load() || sess.infra.sessionCtx.Err() != nil {
		// Deleted session or a server already shutting down: nothing left to
		// speak for, and the delivery could not complete anyway.
		return
	}
	payload := AutomationCallback{
		SessionID: sess.ID,
		Status:    status,
		Title:     sess.title(),
		Summary:   callbackSummary(sess, finalText),
		URL:       sessionWebURL(sess.ID),
		Error:     truncateSummary(errText),
		Pending:   pending,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	// The session context bounds the retry loop: a deleted session or a server
	// shutdown stops the waiting instead of holding a goroutine. In-flight
	// attempts are cut with it — the callback is best-effort by contract.
	if err := postAutomationCallback(sess.infra.sessionCtx, cb, payload); err != nil {
		// Never log the callback URL itself, nor an error string that embeds it:
		// it may carry credentials in its userinfo or query (see
		// sanitizeCallbackURL / sanitizeCallbackError).
		slog.Warn("automation callback delivery failed",
			"session", sess.ID, "status", status,
			"destination", sanitizeCallbackURL(cb.url), "error", err)
	}
}

// askPending describes a blocking ask_user prompt for the callback payload. It
// mirrors the bus event: the request ID plus its questions and their offered
// options, each truncated like every other free-text field in the payload.
func askPending(e bus.AskUserRequested) *CallbackPending {
	questions := make([]bus.AskQuestion, 0, len(e.Questions))
	for _, q := range e.Questions {
		trimmed := bus.AskQuestion{Text: truncateSummary(q.Text)}
		for _, opt := range q.Options {
			trimmed.Options = append(trimmed.Options, truncateSummary(opt))
		}
		questions = append(questions, trimmed)
	}
	return &CallbackPending{Kind: pendingKindQuestion, ID: e.ID, Questions: questions}
}

// permissionPending describes a blocking permission request: the tool and a
// human-readable summary of what it wants to do.
func permissionPending(e bus.PermissionRequested) *CallbackPending {
	return &CallbackPending{
		Kind:    pendingKindPermission,
		ID:      e.ID,
		Tool:    e.ToolName,
		Summary: truncateSummary(permissionArgsSummary(e.ToolName, e.Args)),
	}
}

// permissionArgsSummary renders the most relevant argument of a permission
// request, the same way the permission prompt picks it (command for bash, path for the
// file tools), falling back to a deterministic key=value rendering.
func permissionArgsSummary(toolName string, args map[string]any) string {
	switch toolName {
	case "bash":
		if cmd, ok := args["command"].(string); ok {
			return cmd
		}
	case "write", "edit", "read":
		if path, ok := args["path"].(string); ok {
			return path
		}
	}
	return tool.SummarizeArgs(args)
}

// callbackSummary returns a short, honest hint of what happened. It uses the
// run's final assistant text (free, already in the event) falling back to the
// last assistant text in the transcript — never the cheap-model brief from
// brief.go: that one needs an LLM call, can be minutes stale, and is empty for
// most automation sessions.
func callbackSummary(sess *ManagedSession, finalText string) string {
	if text := truncateSummary(finalText); text != "" {
		return text
	}
	msgs := sess.History()
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "assistant" {
			continue
		}
		if text := truncateSummary(assistantText(msgs[i])); text != "" {
			return text
		}
	}
	return ""
}

func assistantText(msg core.AgentMessage) string {
	var buf bytes.Buffer
	for _, c := range msg.Content {
		if c.Type == "text" && c.Text != "" {
			if buf.Len() > 0 {
				buf.WriteString("\n")
			}
			buf.WriteString(c.Text)
		}
	}
	return buf.String()
}

// truncateSummary caps a field at maxCallbackSummaryBytes without splitting a
// UTF-8 rune.
func truncateSummary(s string) string {
	if len(s) <= maxCallbackSummaryBytes {
		return s
	}
	// Reserve room for the marker so the result never exceeds the documented cap.
	cut := maxCallbackSummaryBytes - len(summaryEllipsis)
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	return s[:cut] + summaryEllipsis
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// sanitizeCallbackURL reduces a callback target to what is safe to log: scheme
// and host (with port). Userinfo, path and query — any of which can carry a
// credential — are dropped.
func sanitizeCallbackURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "invalid-url"
	}
	return u.Scheme + "://" + u.Host
}

// sanitizeCallbackError strips the callback URL out of a transport error: the
// net/http client wraps failures in *url.Error, whose message embeds the full
// URL (userinfo and query included). Only the inner error — a dial/TLS failure
// or our redirect sentinel — is kept, so errors.Is still works on it.
func sanitizeCallbackError(err error) error {
	var uerr *url.Error
	if errors.As(err, &uerr) && uerr.Err != nil {
		return fmt.Errorf("%s %s: %w", uerr.Op, sanitizeCallbackURL(uerr.URL), uerr.Err)
	}
	return err
}

// newCallbackClient builds the delivery client: no redirects (a 30x could point
// the signed payload somewhere the operator never named) and a per-attempt
// timeout. IP ranges are deliberately NOT filtered — automation callbacks
// legitimately target internal endpoints on a tailnet; callback_url is
// operator-trusted input (see docs/automation.md).
func newCallbackClient() *http.Client {
	return &http.Client{
		Timeout: callbackTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errCallbackRedirect
		},
	}
}

// postAutomationCallback delivers one callback with up to len(callbackBackoff)
// attempts. Network errors, 408, 429 and 5xx are retried; any other non-2xx is
// permanent (the receiver understood us and said no).
func postAutomationCallback(ctx context.Context, cb callbackTarget, payload AutomationCallback) error {
	if cb.url == "" {
		return errAutomationInvalidCallback
	}
	if err := validateCallbackURL(cb.url); err != nil {
		return err // never dial a non-http(s) target
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal callback: %w", err)
	}
	client := newCallbackClient()
	var lastErr error
	for attempt := 0; attempt < len(callbackBackoff); attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(callbackBackoff[attempt-1]):
			case <-ctx.Done():
				return fmt.Errorf("callback abandoned: %w", ctx.Err())
			}
		}
		retryable, err := deliverCallbackOnce(ctx, client, cb, body)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retryable {
			return err
		}
	}
	return fmt.Errorf("callback gave up after %d attempts: %w", len(callbackBackoff), lastErr)
}

// deliverCallbackOnce performs a single POST, reporting whether a failure is
// worth retrying.
func deliverCallbackOnce(ctx context.Context, client *http.Client, cb callbackTarget, body []byte) (retryable bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cb.url, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "moa-automation")
	if cb.secret != "" {
		req.Header.Set("X-Moa-Signature", callbackSignature(cb.secret, body))
	}
	resp, err := client.Do(req)
	if err != nil {
		err = sanitizeCallbackError(err)
		if errors.Is(err, errCallbackRedirect) {
			// A refused redirect is a permanent configuration problem: retrying
			// would POST the signed payload to the redirector two more times.
			return false, err
		}
		return true, err
	}
	defer resp.Body.Close() //nolint:errcheck
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxCallbackResponseBytes))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return false, nil
	}
	retryable = resp.StatusCode >= 500 ||
		resp.StatusCode == http.StatusRequestTimeout ||
		resp.StatusCode == http.StatusTooManyRequests
	return retryable, fmt.Errorf("callback returned %d", resp.StatusCode)
}

// callbackSignature is the value of X-Moa-Signature: HMAC-SHA256 of the exact
// bytes on the wire, keyed with callback_secret.
func callbackSignature(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
