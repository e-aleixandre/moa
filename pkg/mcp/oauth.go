package mcp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"golang.org/x/oauth2"

	"github.com/e-aleixandre/moa/pkg/auth"
	"github.com/e-aleixandre/moa/pkg/core"
)

// errAuthRequired classifies a connect that failed because the server wants
// the user to sign in, as opposed to any other failure.
var errAuthRequired = errors.New("sign-in required")

// oauthHandler plugs the shared MCP token store into the SDK transport for one
// connection of one remote server. The SDK asks it for a token before every
// request and calls Authorize once on a 401/403.
type oauthHandler struct {
	store *auth.MCPOAuthStore
	key   string

	// onAuthRequired is told when only a human can restore access. It may run
	// while the SDK holds its connection mutex, so it must not block or call
	// back into the session.
	onAuthRequired func()

	// authRequired lets connect classify its failure without depending on
	// how the SDK wraps (or drops) our error.
	authRequired atomic.Bool
}

var _ sdkauth.OAuthHandler = (*oauthHandler)(nil)

func (h *oauthHandler) markAuthRequired() {
	h.authRequired.Store(true)
	if h.onAuthRequired != nil {
		h.onAuthRequired()
	}
}

// TokenSource always returns a source; a nil token from it (no stored record)
// makes the SDK send no Authorization, leaving static headers untouched.
func (h *oauthHandler) TokenSource(ctx context.Context) (oauth2.TokenSource, error) {
	return tokenSourceFunc(func() (*oauth2.Token, error) {
		tok, err := h.store.Token(ctx, h.key)
		if errors.Is(err, auth.ErrMCPAuthRequired) {
			h.markAuthRequired()
		}
		return tok, err
	}), nil
}

func (h *oauthHandler) Authorize(ctx context.Context, req *http.Request, resp *http.Response) error {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		// 403 means authenticated but not allowed; a new token would not help.
		// Like the SDK's own handler, return nil: the SDK retries once and the
		// second 403 fails the connection exactly as it did without OAuth.
		return nil
	}
	if exists, needsReauth := h.store.Has(h.key); !exists || needsReauth {
		h.markAuthRequired()
		return auth.ErrMCPAuthRequired
	}
	// req is the SDK's own request, before headerRoundTripper adds static
	// headers, so its Authorization is the OAuth token (or none).
	rejected := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
	err := h.store.RefreshIfCurrent(ctx, h.key, rejected)
	if errors.Is(err, auth.ErrMCPAuthRequired) {
		h.markAuthRequired()
	}
	return err
}

type tokenSourceFunc func() (*oauth2.Token, error)

func (f tokenSourceFunc) Token() (*oauth2.Token, error) { return f() }

// authFailed reports whether a failed connect was the server asking for
// sign-in.
func authFailed(h *oauthHandler, err error) bool {
	return (h != nil && h.authRequired.Load()) || errors.Is(err, auth.ErrMCPAuthRequired)
}

// setConnectFailed records a failed connect: StateAuthRequired when the
// server wants sign-in, StateFailed otherwise.
func (m *Manager) setConnectFailed(sess *serverSession, cfg core.MCPServer, err error) {
	if errors.Is(err, errAuthRequired) {
		sess.setAuthRequired(m.authAction(cfg))
		return
	}
	sess.setFailed(err.Error())
}

// authAction tells the UI whether this is a first authorization ("connect")
// or a lost one ("reconnect").
func (m *Manager) authAction(cfg core.MCPServer) string {
	m.mu.Lock()
	store := m.oauthStore
	m.mu.Unlock()
	if store != nil {
		if exists, _ := store.Has(auth.MCPOAuthKey(cfg.URL)); exists && !store.SignedOut(auth.MCPOAuthKey(cfg.URL)) {
			return "reconnect"
		}
	}
	return "connect"
}

// handleAuthLost moves a live server to StateAuthRequired after its tokens
// stopped working. It runs in its own goroutine because the trigger fires
// under the SDK's connection mutex, and closing the session from there would
// deadlock. h identifies the connection that lost access: if a restart or
// disable replaced it meanwhile, there is nothing to do.
func (m *Manager) handleAuthLost(sess *serverSession, h *oauthHandler) {
	sess.lifecycle.Lock()
	defer sess.lifecycle.Unlock()

	m.mu.Lock()
	closed := m.closed
	cfg, ok := m.configs[sess.name]
	m.mu.Unlock()
	if closed || !ok {
		return
	}

	sess.mu.Lock()
	if sess.oauth != h || sess.state != StateReady {
		sess.mu.Unlock()
		return
	}
	sess.gen++ // the exit watcher must not report this teardown as an exit
	oldSession := sess.session
	sess.session = nil
	sess.mu.Unlock()

	if oldSession != nil {
		_ = oldSession.Close()
	}
	sess.setAuthRequired(m.authAction(cfg))
	st := sess.status()
	m.logger.Warn("MCP server needs sign-in", "server", sess.name)
	m.notify(st)
}

// onOAuthAuthorized reconnects every server of this manager that was waiting
// for the authorization just stored for key — in whichever session it was
// done — through the same path as an enable.
func (m *Manager) onOAuthAuthorized(key string) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	ctx := m.startCtx
	type target struct {
		sess *serverSession
		cfg  core.MCPServer
	}
	var targets []target
	for _, sess := range m.servers {
		cfg, ok := m.configs[sess.name]
		if ok && cfg.IsRemote() && auth.MCPOAuthKey(cfg.URL) == key {
			targets = append(targets, target{sess, cfg})
		}
	}
	m.mu.Unlock()

	for _, t := range targets {
		go m.reconnectAfterAuth(ctx, t.sess, t.cfg)
	}
}

// onOAuthSignedOut closes every live connection for key. The token store keeps
// a tokenless marker, so reconnects cannot fall back to a static header.
func (m *Manager) onOAuthSignedOut(key string) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	type target struct {
		sess *serverSession
		cfg  core.MCPServer
	}
	var targets []target
	for _, sess := range m.servers {
		if cfg, ok := m.configs[sess.name]; ok && cfg.IsRemote() && auth.MCPOAuthKey(cfg.URL) == key {
			targets = append(targets, target{sess, cfg})
		}
	}
	m.mu.Unlock()
	for _, target := range targets {
		m.signOut(target.sess)
	}
}

func (m *Manager) signOut(sess *serverSession) {
	sess.lifecycle.Lock()
	defer sess.lifecycle.Unlock()
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return
	}
	sess.mu.Lock()
	if sess.state == StateDisabled || sess.state == StateDisabling {
		sess.mu.Unlock()
		return
	}
	sess.gen++
	oldSession := sess.session
	sess.session = nil
	sess.client = nil
	sess.oauth = nil
	sess.oauthAuthenticated = false
	sess.tools = nil
	sess.state = StateAuthRequired
	sess.authAction = "connect"
	sess.err = authRequiredMessage
	sess.changedAt = time.Now()
	st := sess.statusLocked()
	sess.mu.Unlock()
	if oldSession != nil {
		_ = oldSession.Close()
	}
	m.notify(st)
}

func (m *Manager) reconnectAfterAuth(ctx context.Context, sess *serverSession, cfg core.MCPServer) {
	sess.lifecycle.Lock()
	defer sess.lifecycle.Unlock()

	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return
	}
	sess.mu.Lock()
	waiting := sess.state == StateAuthRequired
	sess.mu.Unlock()
	if !waiting {
		return
	}
	m.enableLocked(ctx, sess, cfg)
}
