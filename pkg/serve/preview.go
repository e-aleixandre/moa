package serve

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed frontend/src/components/LivePreview/inspector.js
var previewInspector []byte

const previewMaxBody = 8 << 20
const previewAuthCookie = "moa_preview_auth"

type previewTarget struct {
	url          *url.URL
	aliases      []string
	ips          []net.IP
	port         string
	generation   uint64
	parentOrigin string
}

type previewDialPlanKey struct{}
type previewDialPlan struct {
	ips  []net.IP
	port string
	gen  uint64
}

// PreviewProxy is the single, switchable upstream exposed by the preview port.
// It owns one bounded transport; targets are validated and pinned at dial time.
type PreviewProxy struct {
	publicURL  string
	moaPort    int
	listenPort int
	capability string
	authSecret string
	secure     bool
	transport  *http.Transport
	resolve    func(string) ([]net.IP, error)
	localIPs   func() ([]net.IP, error)
	dial       func(context.Context, string, string) (net.Conn, error)
	// closed is cancelled by Close. Dials in flight watch it, so shutting the
	// proxy down cannot leave a connection being opened to the old target.
	closeCtx    context.Context
	closeCancel context.CancelFunc

	mu           sync.RWMutex
	target       *previewTarget
	generation   uint64
	connections  map[uint64]map[net.Conn]struct{}
	clearPending bool
	closed       bool
}

func NewPreviewProxy(publicURL string, moaPort, listenPort int) *PreviewProxy {
	capability, err := newPreviewSecret()
	if err != nil {
		panic(fmt.Sprintf("preview capability: %v", err))
	}
	authSecret, err := newPreviewSecret()
	if err != nil {
		panic(fmt.Sprintf("preview authentication: %v", err))
	}
	closeCtx, closeCancel := context.WithCancel(context.Background())
	p := &PreviewProxy{
		publicURL: strings.TrimRight(publicURL, "/"), moaPort: moaPort, listenPort: listenPort,
		capability: capability, authSecret: authSecret, resolve: net.LookupIP, localIPs: localInterfaceIPs,
		connections: make(map[uint64]map[net.Conn]struct{}),
		closeCtx:    closeCtx,
		closeCancel: closeCancel,
	}
	if u, err := url.Parse(p.publicURL); err == nil {
		p.secure = u.Scheme == "https"
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil // Never let environment proxy settings bypass the validated dial.
	tr.DisableCompression = true
	tr.MaxIdleConns = 32
	tr.MaxIdleConnsPerHost = 8
	tr.MaxConnsPerHost = 16
	tr.IdleConnTimeout = 30 * time.Second
	tr.TLSHandshakeTimeout = 10 * time.Second
	tr.ResponseHeaderTimeout = 20 * time.Second
	tr.ExpectContinueTimeout = time.Second
	p.dial = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	tr.DialContext = p.dialContext
	p.transport = tr
	return p
}

func newPreviewSecret() (string, error) {
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(secretBytes), nil
}

func (p *PreviewProxy) PublicURL() string { return p.publicURL }

// PreviewURL is a one-hop capability URL. The listener exchanges it for an
// HttpOnly cookie and redirects to a clean URL, so the app never receives it.
func (p *PreviewProxy) PreviewURL() string {
	u, err := url.Parse(p.publicURL)
	if err != nil {
		return p.publicURL
	}
	p.mu.RLock()
	capability := p.capability
	p.mu.RUnlock()
	if capability == "" {
		return p.publicURL
	}
	q := u.Query()
	q.Set("preview_token", capability)
	u.RawQuery = q.Encode()
	return u.String()
}

// Close makes the proxy permanently unusable: dials in flight are cancelled,
// every tracked upstream connection is dropped, and a dial that completes after
// this point is closed instead of handed back. Deactivating the preview must
// not leave a single connection to the previewed app alive.
func (p *PreviewProxy) Close() {
	p.mu.Lock()
	p.closed = true
	p.capability, p.authSecret = "", ""
	p.mu.Unlock()
	p.closeCancel()
	p.transport.CloseIdleConnections()
	p.closeGenerations(^uint64(0))
}

func (p *PreviewProxy) targetSnapshot() *previewTarget {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.target == nil {
		return nil
	}
	out := *p.target
	out.url = cloneURL(p.target.url)
	out.aliases = append([]string(nil), p.target.aliases...)
	out.ips = append([]net.IP(nil), p.target.ips...)
	return &out
}
func cloneURL(u *url.URL) *url.URL { v := *u; return &v }

func (p *PreviewProxy) SetTarget(raw string, aliases []string) error {
	return p.setTarget(raw, aliases, p.publicURL)
}
func (p *PreviewProxy) setTarget(raw string, aliases []string, parentOrigin string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return errors.New("target must be an absolute http(s) URL")
	}
	parent, err := normalizedOrigin(parentOrigin)
	if err != nil {
		return errors.New("parent origin must be an absolute URL")
	}
	ips, port, err := p.validateTarget(u)
	if err != nil {
		return err
	}
	capability, err := newPreviewSecret()
	if err != nil {
		return fmt.Errorf("preview capability: %w", err)
	}
	authSecret, err := newPreviewSecret()
	if err != nil {
		return fmt.Errorf("preview authentication: %w", err)
	}
	clean := make([]string, 0, len(aliases))
	for _, a := range aliases {
		x, e := url.Parse(a)
		if e == nil && (x.Scheme == "http" || x.Scheme == "https") && x.Host != "" && x.User == nil {
			clean = append(clean, x.Scheme+"://"+x.Host)
		}
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return errors.New("the preview proxy is no longer running")
	}
	oldGeneration := p.generation
	p.generation++
	p.target = &previewTarget{url: u, aliases: clean, ips: ips, port: port, generation: p.generation, parentOrigin: parent}
	p.capability = capability
	p.authSecret = authSecret
	p.clearPending = true
	p.mu.Unlock()
	if oldGeneration != 0 {
		p.closeGenerations(oldGeneration)
	}
	p.transport.CloseIdleConnections()
	return nil
}

func normalizedOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("invalid origin")
	}
	return u.Scheme + "://" + u.Host, nil
}

func (p *PreviewProxy) validateTarget(u *url.URL) ([]net.IP, string, error) {
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return nil, "", errors.New("target has an invalid port")
	}
	port = strconv.Itoa(portNumber)
	if ip := net.ParseIP(u.Hostname()); ip != nil && strings.Contains(u.Hostname(), ":") && ip.To4() != nil {
		return nil, "", errors.New("IPv4-mapped IPv6 targets are not allowed")
	}
	ips, err := p.resolve(u.Hostname())
	if err != nil || len(ips) == 0 {
		if err != nil {
			return nil, "", err
		}
		return nil, "", errors.New("target did not resolve")
	}
	var ownIPs []net.IP
	if portNumber == p.moaPort || portNumber == p.listenPort {
		ownIPs, err = p.localIPs()
		if err != nil {
			return nil, "", fmt.Errorf("list local interfaces: %w", err)
		}
	}
	for _, ip := range ips {
		if !allowedPreviewIP(ip) {
			return nil, "", errors.New("target must resolve to loopback, private, or tailnet address")
		}
		if (portNumber == p.moaPort || portNumber == p.listenPort) && isLocalIP(ip, ownIPs) {
			return nil, "", errors.New("target may not point at moa or the preview proxy")
		}
	}
	return ips, port, nil
}
func allowedPreviewIP(ip net.IP) bool {
	if ip == nil || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	// The Tailscale metadata service is inside CGNAT but must never be proxied.
	if ip.Equal(net.ParseIP("100.100.100.200")) {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || (ip.To4() != nil && ip.To4()[0] == 100 && ip.To4()[1]&0xc0 == 0x40)
}
func localInterfaceIPs() ([]net.IP, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, addr := range addrs {
		if network, ok := addr.(*net.IPNet); ok && network.IP != nil {
			ips = append(ips, network.IP)
		}
	}
	return ips, nil
}

func isLocalIP(ip net.IP, localIPs []net.IP) bool {
	if ip.IsLoopback() || ip.IsUnspecified() {
		return true
	}
	for _, local := range localIPs {
		if ip.Equal(local) {
			return true
		}
	}
	return false
}

func (p *PreviewProxy) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /__moa/inspector.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(previewInspector)
	})
	mux.HandleFunc("/", p.serve)
	return mux
}

// ProtectedHandler authenticates every document, asset, request and upgrade.
// In both token and network-owner modes the capability is issued only by the
// owner-only main API, so publishing this port does not publish a network pivot.
func (p *PreviewProxy) ProtectedHandler(allowedHosts []string) http.Handler {
	return previewReferrerPolicy(hostMiddleware(allowedHosts, p.requireCapability(bodyTimeoutMiddleware(p.Handler()))))
}

func previewReferrerPolicy(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (p *PreviewProxy) requireCapability(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(previewAuthCookie); err == nil && p.validAuthCookie(c.Value) {
			if r.URL.Query().Has("preview_token") {
				redirectWithoutPreviewToken(w, r)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if authSecret, ok := p.exchangeCapability(r.URL.Query().Get("preview_token")); ok {
			http.SetCookie(w, &http.Cookie{Name: previewAuthCookie, Value: authSecret, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: p.secure})
			redirectWithoutPreviewToken(w, r)
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

func redirectWithoutPreviewToken(w http.ResponseWriter, r *http.Request) {
	u := *r.URL
	q := u.Query()
	q.Del("preview_token")
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.RequestURI(), http.StatusFound)
}

func (p *PreviewProxy) validAuthCookie(value string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	// An empty secret means the proxy was closed (or never issued one): an empty
	// cookie must never compare equal to it.
	if p.authSecret == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(value), []byte(p.authSecret)) == 1
}

func (p *PreviewProxy) exchangeCapability(token string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if token == "" || p.capability == "" || subtle.ConstantTimeCompare([]byte(token), []byte(p.capability)) != 1 {
		return "", false
	}
	p.capability = ""
	return p.authSecret, true
}

func (p *PreviewProxy) serve(w http.ResponseWriter, r *http.Request) {
	target := p.targetSnapshot()
	if target == nil {
		http.Error(w, "No preview target is configured. Change destination in moa.", http.StatusBadGateway)
		return
	}
	p.mu.Lock()
	clear := p.clearPending
	p.clearPending = false
	p.mu.Unlock()
	proxy := &httputil.ReverseProxy{Transport: p.transport}
	proxy.Rewrite = func(pr *httputil.ProxyRequest) {
		pr.SetURL(target.url)
		pr.Out = pr.Out.WithContext(context.WithValue(pr.Out.Context(), previewDialPlanKey{}, previewDialPlan{ips: target.ips, port: target.port, gen: target.generation}))
		pr.Out.Host = "localhost"
		if target.url.Port() != "" {
			pr.Out.Host += ":" + target.url.Port()
		}
		pr.Out.Header.Del("Accept-Encoding")
		pr.Out.Header.Del("Authorization")
		filterRequestCookies(pr.Out, target.generation)
		pr.Out.Header.Set("X-Forwarded-Proto", target.url.Scheme)
		pr.Out.Header.Set("X-Forwarded-Host", r.Host)
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		if clear {
			resp.Header.Set("Clear-Site-Data", `"storage", "cache"`)
		}
		return p.rewriteResponse(resp, target)
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "The preview server is unavailable. Change destination in moa.", http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}

func (p *PreviewProxy) dialContext(ctx context.Context, network, _ string) (net.Conn, error) {
	plan, ok := ctx.Value(previewDialPlanKey{}).(previewDialPlan)
	if !ok || len(plan.ips) == 0 {
		return nil, errors.New("preview target was not validated")
	}
	// A dial started just before Deactivate must not complete afterwards: tie
	// its lifetime to the proxy's, not only to the request's.
	dialCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(p.closeCtx, cancel)()
	var last error
	for _, ip := range plan.ips {
		conn, err := p.dial(dialCtx, network, net.JoinHostPort(ip.String(), plan.port))
		if err == nil {
			tracked, current := p.trackConnection(plan.gen, conn)
			if !current {
				_ = conn.Close()
				return nil, errors.New("the preview target changed or was turned off while connecting")
			}
			return tracked, nil
		}
		last = err
	}
	return nil, last
}

type trackedPreviewConn struct {
	net.Conn
	p    *PreviewProxy
	gen  uint64
	once sync.Once
}

func (c *trackedPreviewConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { c.p.untrackConnection(c.gen, c) })
	return err
}
func (p *PreviewProxy) trackConnection(gen uint64, conn net.Conn) (net.Conn, bool) {
	c := &trackedPreviewConn{Conn: conn, p: p, gen: gen}
	p.mu.Lock()
	if gen != p.generation || p.closed {
		p.mu.Unlock()
		return nil, false
	}
	if p.connections[gen] == nil {
		p.connections[gen] = make(map[net.Conn]struct{})
	}
	p.connections[gen][c] = struct{}{}
	p.mu.Unlock()
	return c, true
}
func (p *PreviewProxy) untrackConnection(gen uint64, conn net.Conn) {
	p.mu.Lock()
	delete(p.connections[gen], conn)
	p.mu.Unlock()
}
func (p *PreviewProxy) closeGenerations(through uint64) {
	p.mu.Lock()
	var closeList []net.Conn
	for gen, conns := range p.connections {
		if gen <= through {
			for c := range conns {
				closeList = append(closeList, c)
			}
			delete(p.connections, gen)
		}
	}
	p.mu.Unlock()
	for _, c := range closeList {
		_ = c.Close()
	}
}

func filterRequestCookies(r *http.Request, generation uint64) {
	prefix := previewCookiePrefix(generation)
	var keep []string
	for _, c := range r.Cookies() {
		if strings.HasPrefix(c.Name, prefix) {
			keep = append(keep, strings.TrimPrefix(c.Name, prefix)+"="+c.Value)
		}
	}
	r.Header.Del("Cookie")
	if len(keep) > 0 {
		r.Header.Set("Cookie", strings.Join(keep, "; "))
	}
}
func previewCookiePrefix(generation uint64) string {
	return "moa_preview_" + strconv.FormatUint(generation, 10) + "_"
}

var metaCSP = regexp.MustCompile(`(?is)<meta\b[^>]*\bhttp-equiv\s*=\s*(?:"content-security-policy"|'content-security-policy'|content-security-policy)[^>]*>`)
var headTag = regexp.MustCompile(`(?is)<head\b[^>]*>`)

func (p *PreviewProxy) rewriteResponse(resp *http.Response, target *previewTarget) error {
	resp.Header.Del("X-Frame-Options")
	resp.Header.Del("Content-Security-Policy")
	resp.Header.Del("Content-Security-Policy-Report-Only")
	resp.Header.Set("Content-Security-Policy", "frame-ancestors "+target.parentOrigin)
	if location := resp.Header.Get("Location"); location != "" {
		if err := p.validateRedirect(location, target); err != nil {
			return err
		}
	}
	for _, h := range []string{"Location", "Refresh", "Link"} {
		rewriteHeaderValues(resp.Header, h, func(v string) string { return p.rewrite(v, target) })
	}
	filterResponseCookies(resp.Header, target.generation, p.secure)
	if resp.Body == nil || resp.Header.Get("Content-Encoding") != "" || resp.Request.Method == http.MethodHead || resp.StatusCode == http.StatusPartialContent || resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotModified {
		return nil
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	textual := strings.HasPrefix(ct, "text/html") || strings.HasPrefix(ct, "text/css") || strings.Contains(ct, "javascript") || strings.HasPrefix(ct, "application/json")
	if !textual {
		return nil
	}
	prefix, err := io.ReadAll(io.LimitReader(resp.Body, previewMaxBody+1))
	if err != nil {
		return err
	}
	if len(prefix) > previewMaxBody {
		resp.Body = io.NopCloser(io.MultiReader(bytes.NewReader(prefix), resp.Body))
		return nil
	}
	_ = resp.Body.Close()
	body := []byte(p.rewrite(string(prefix), target))
	if strings.HasPrefix(ct, "text/html") {
		body = metaCSP.ReplaceAll(body, nil)
		body = injectInspector(body, target.parentOrigin)
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	for _, h := range []string{"Content-Length", "ETag", "Content-MD5", "Digest", "Content-Range", "Accept-Ranges"} {
		resp.Header.Del(h)
	}
	return nil
}
func rewriteHeaderValues(h http.Header, name string, rewrite func(string) string) {
	values := h.Values(name)
	if len(values) == 0 {
		return
	}
	h.Del(name)
	for _, v := range values {
		h.Add(name, rewrite(v))
	}
}
func (p *PreviewProxy) validateRedirect(location string, target *previewTarget) error {
	u, err := url.Parse(location)
	if err != nil || !u.IsAbs() {
		return nil
	}
	origin := u.Scheme + "://" + u.Host
	if origin == target.url.Scheme+"://"+target.url.Host {
		return nil
	}
	for _, alias := range target.aliases {
		if origin == alias {
			return nil
		}
	}
	return errors.New("preview redirect left the validated target")
}
func (p *PreviewProxy) rewrite(s string, target *previewTarget) string {
	origins := []string{target.url.Scheme + "://" + target.url.Host}
	if target.url.Port() != "" {
		origins = append(origins, "http://localhost:"+target.url.Port(), "http://127.0.0.1:"+target.url.Port(), "http://[::1]:"+target.url.Port())
	}
	origins = append(origins, target.aliases...)
	for _, origin := range origins {
		s = replaceOrigin(s, origin, p.publicURL)
	}
	return s
}
func replaceOrigin(s, origin, replacement string) string {
	var b strings.Builder
	for from := 0; ; {
		i := strings.Index(s[from:], origin)
		if i < 0 {
			b.WriteString(s[from:])
			return b.String()
		}
		i += from
		end := i + len(origin)
		if end < len(s) && isOriginContinuation(s[end]) {
			b.WriteString(s[from:end])
			from = end
			continue
		}
		b.WriteString(s[from:i])
		b.WriteString(replacement)
		from = end
	}
}
func isOriginContinuation(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '_'
}
func injectInspector(b []byte, parentOrigin string) []byte {
	if bytes.Contains(b, []byte("/__moa/inspector.js")) {
		return b
	}
	tag := []byte(`<script src="/__moa/inspector.js" data-moa-origin="` + parentOrigin + `"></script>`)
	if loc := headTag.FindIndex(b); loc != nil {
		return insertBytes(b, loc[1], tag)
	}
	return insertBytes(b, 0, tag)
}
func insertBytes(body []byte, at int, addition []byte) []byte {
	out := make([]byte, 0, len(body)+len(addition))
	out = append(out, body[:at]...)
	out = append(out, addition...)
	return append(out, body[at:]...)
}
func filterResponseCookies(h http.Header, generation uint64, secure bool) {
	out := []string{}
	for _, v := range h.Values("Set-Cookie") {
		parts := strings.Split(v, ";")
		if len(parts) == 0 {
			continue
		}
		name, value, ok := strings.Cut(strings.TrimSpace(parts[0]), "=")
		if !ok || strings.EqualFold(name, authCookieName) || strings.EqualFold(name, previewAuthCookie) {
			continue
		}
		kept := []string{previewCookiePrefix(generation) + name + "=" + value}
		for _, part := range parts[1:] {
			t := strings.TrimSpace(part)
			if strings.HasPrefix(strings.ToLower(t), "domain=") {
				continue
			}
			kept = append(kept, t)
		}
		if secure && !containsAttr(kept, "secure") {
			kept = append(kept, "Secure")
		}
		out = append(out, strings.Join(kept, "; "))
	}
	h.Del("Set-Cookie")
	for _, v := range out {
		h.Add("Set-Cookie", v)
	}
}
func containsAttr(parts []string, want string) bool {
	for _, p := range parts {
		if strings.EqualFold(p, want) {
			return true
		}
	}
	return false
}

// handlePreviewTarget is the whole hot-activation surface.
//
// GET reports what is configured and what is actually listening. PUT with a
// URL brings the listener up (creating it if needed) and points it at the dev
// server; PUT with "enabled": false takes it down. Nothing about running state
// is persisted: the listener exists only while a preview is in use.
func handlePreviewTarget(c *PreviewController) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c == nil {
			writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "supported": false})
			return
		}
		if r.Method == http.MethodGet {
			status := c.Status()
			out := map[string]any{
				"enabled":        status.Enabled,
				"supported":      true,
				"public_url":     status.PublicURL,
				"port":           status.Port,
				"suggested_port": status.SuggestedPort,
			}
			if status.Error != "" {
				out["error"] = status.Error
			}
			if proxy := c.Proxy(); proxy != nil {
				out["preview_url"] = proxy.PreviewURL()
				if t := proxy.targetSnapshot(); t != nil {
					out["url"] = t.url.String()
				}
			}
			writeJSON(w, http.StatusOK, out)
			return
		}
		var in struct {
			URL          string   `json:"url"`
			Aliases      []string `json:"aliases"`
			ParentOrigin string   `json:"parent_origin"`
			PublicURL    string   `json:"public_url"`
			Port         int      `json:"port"`
			Enabled      *bool    `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if in.Enabled != nil && !*in.Enabled {
			c.Deactivate()
			writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "supported": true})
			return
		}
		if in.ParentOrigin == "" {
			in.ParentOrigin = r.Header.Get("Origin")
		}
		proxy, started, err := c.Activate(in.PublicURL, in.Port)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := proxy.setTarget(in.URL, in.Aliases, in.ParentOrigin); err != nil {
			// This call opened the port and then found the target unusable:
			// close it again rather than leave a listener nobody asked for.
			if started {
				c.Deactivate()
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		status := c.Status()
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled":     true,
			"supported":   true,
			"url":         in.URL,
			"public_url":  proxy.PublicURL(),
			"port":        status.Port,
			"preview_url": proxy.PreviewURL(),
		})
	}
}
