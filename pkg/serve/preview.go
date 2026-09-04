package serve

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed frontend/src/components/LivePreview/inspector.js
var previewInspector []byte

const previewMaxBody = 8 << 20

// PreviewProxy is the single, switchable upstream exposed by the preview port.
// It deliberately has its own mux: no moa API is reachable on this origin.
type PreviewProxy struct {
	publicURL  string
	moaPort    int
	listenPort int
	mu         sync.RWMutex
	target     *url.URL
	aliases    []string
}

func NewPreviewProxy(publicURL string, moaPort, listenPort int) *PreviewProxy {
	return &PreviewProxy{publicURL: strings.TrimRight(publicURL, "/"), moaPort: moaPort, listenPort: listenPort}
}

func (p *PreviewProxy) PublicURL() string { return p.publicURL }

func (p *PreviewProxy) targetSnapshot() (*url.URL, []string) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.target == nil {
		return nil, nil
	}
	u := *p.target
	return &u, append([]string(nil), p.aliases...)
}

func (p *PreviewProxy) SetTarget(raw string, aliases []string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("target must be an absolute http(s) URL")
	}
	if err := p.allowed(u); err != nil {
		return err
	}
	clean := make([]string, 0, len(aliases))
	for _, a := range aliases {
		if x, e := url.Parse(a); e == nil && x.Scheme != "" && x.Host != "" {
			clean = append(clean, x.Scheme+"://"+x.Host)
		}
	}
	p.mu.Lock()
	p.target, p.aliases = u, clean
	p.mu.Unlock()
	return nil
}

func (p *PreviewProxy) allowed(u *url.URL) error {
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	if (port == itoa(p.moaPort) || port == itoa(p.listenPort)) && (net.ParseIP(host) != nil || strings.EqualFold(host, "localhost")) {
		return errors.New("target may not point at moa or the preview proxy")
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return err
	}
	for _, ip := range ips {
		if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return errors.New("link-local and metadata targets are not allowed")
		}
		if ip.IsLoopback() || ip.IsPrivate() || (ip.To4() != nil && ip.To4()[0] == 100 && ip.To4()[1]&0xc0 == 0x40) {
			continue
		}
		return errors.New("target must resolve to loopback, private, or tailnet address")
	}
	return nil
}
func itoa(n int) string { return strconv.Itoa(n) }

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

// ProtectedHandler applies the same DNS-rebinding host policy as moa's main
// listener while keeping this origin free of the main API.
func (p *PreviewProxy) ProtectedHandler(allowedHosts []string) http.Handler {
	return hostMiddleware(allowedHosts, p.Handler())
}

func (p *PreviewProxy) serve(w http.ResponseWriter, r *http.Request) {
	target, aliases := p.targetSnapshot()
	if target == nil {
		http.Error(w, "No preview target is configured. Change destination in moa.", http.StatusBadGateway)
		return
	}
	proxy := &httputil.ReverseProxy{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, DisableCompression: true, DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext}}
	proxy.Rewrite = func(pr *httputil.ProxyRequest) {
		pr.SetURL(target)
		pr.Out.Host = "localhost"
		if target.Port() != "" {
			pr.Out.Host += ":" + target.Port()
		}
		pr.Out.Header.Del("Accept-Encoding")
		filterRequestCookies(pr.Out)
		pr.Out.Header.Set("X-Forwarded-Proto", "https")
		pr.Out.Header.Set("X-Forwarded-Host", r.Host)
	}
	proxy.ModifyResponse = func(resp *http.Response) error { return p.rewriteResponse(resp, target, aliases) }
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "The preview server is unavailable. Change destination in moa.", http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}

func filterRequestCookies(r *http.Request) {
	cookies := r.Cookies()
	r.Header.Del("Cookie")
	var keep []string
	for _, c := range cookies {
		if c.Name != authCookieName {
			keep = append(keep, c.Name+"="+c.Value)
		}
	}
	if len(keep) > 0 {
		r.Header.Set("Cookie", strings.Join(keep, "; "))
	}
}

func (p *PreviewProxy) rewriteResponse(resp *http.Response, target *url.URL, aliases []string) error {
	resp.Header.Del("X-Frame-Options")
	resp.Header.Del("Content-Security-Policy")
	resp.Header.Del("Content-Security-Policy-Report-Only")
	resp.Header.Set("Content-Security-Policy", "frame-ancestors "+p.frameAncestors())
	for _, h := range []string{"Location", "Refresh", "Link"} {
		if v := resp.Header.Get(h); v != "" {
			resp.Header.Set(h, p.rewrite(v, target, aliases))
		}
	}
	filterResponseCookies(resp.Header)
	if resp.Header.Get("Content-Encoding") != "" || resp.Body == nil {
		return nil
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	textual := strings.HasPrefix(ct, "text/html") || strings.HasPrefix(ct, "text/css") || strings.Contains(ct, "javascript") || strings.HasPrefix(ct, "application/json")
	if !textual {
		return nil
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, previewMaxBody+1))
	resp.Body.Close()
	if err != nil {
		return err
	}
	if len(b) > previewMaxBody {
		resp.Body = io.NopCloser(bytes.NewReader(b))
		return nil
	}
	b = []byte(p.rewrite(string(b), target, aliases))
	if strings.HasPrefix(ct, "text/html") {
		parentOrigin := "*"
		if ref, err := url.Parse(resp.Request.Referer()); err == nil && ref.Scheme != "" && ref.Host != "" {
			parentOrigin = ref.Scheme + "://" + ref.Host
		}
		b = injectInspector(b, parentOrigin)
	}
	resp.Body = io.NopCloser(bytes.NewReader(b))
	resp.ContentLength = int64(len(b))
	resp.Header.Del("Content-Length")
	return nil
}

func (p *PreviewProxy) frameAncestors() string {
	u, err := url.Parse(p.publicURL)
	if err != nil || u.Scheme == "" || u.Hostname() == "" {
		return "'none'"
	}
	return u.Scheme + "://" + u.Hostname() + ":*"
}
func (p *PreviewProxy) rewrite(s string, target *url.URL, aliases []string) string {
	origins := []string{target.Scheme + "://" + target.Host}
	if target.Port() != "" {
		origins = append(origins, "http://localhost:"+target.Port(), "http://127.0.0.1:"+target.Port(), "http://[::1]:"+target.Port())
	}
	origins = append(origins, aliases...)
	for _, o := range origins {
		s = strings.ReplaceAll(s, o, p.publicURL)
	}
	return s
}
func injectInspector(b []byte, parentOrigin string) []byte {
	tag := []byte(`<script src="/__moa/inspector.js" data-moa-origin="` + parentOrigin + `"></script>`)
	lower := bytes.ToLower(b)
	if i := bytes.Index(lower, []byte("<head")); i >= 0 {
		if j := bytes.IndexByte(lower[i:], '>'); j >= 0 {
			return insertBytes(b, i+j+1, tag)
		}
	}
	if i := bytes.Index(lower, []byte("<body")); i >= 0 {
		return insertBytes(b, i, tag)
	}
	return insertBytes(b, 0, tag)
}

func insertBytes(body []byte, at int, addition []byte) []byte {
	out := make([]byte, 0, len(body)+len(addition))
	out = append(out, body[:at]...)
	out = append(out, addition...)
	return append(out, body[at:]...)
}
func filterResponseCookies(h http.Header) {
	out := []string{}
	for _, v := range h.Values("Set-Cookie") {
		if strings.HasPrefix(strings.ToLower(v), strings.ToLower(authCookieName)+"=") {
			continue
		}
		parts := strings.Split(v, ";")
		kept := []string{}
		for _, part := range parts {
			t := strings.TrimSpace(part)
			if strings.HasPrefix(strings.ToLower(t), "domain=") {
				continue
			}
			kept = append(kept, t)
		}
		if !containsAttr(kept, "secure") {
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

func handlePreviewTarget(p *PreviewProxy) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if p == nil {
			writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
			return
		}
		if r.Method == http.MethodGet {
			t, _ := p.targetSnapshot()
			out := map[string]any{"enabled": true, "public_url": p.PublicURL()}
			if t != nil {
				out["url"] = t.String()
			}
			writeJSON(w, http.StatusOK, out)
			return
		}
		var in struct {
			URL     string   `json:"url"`
			Aliases []string `json:"aliases"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "invalid JSON", 400)
			return
		}
		if err := p.SetTarget(in.URL, in.Aliases); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"url": in.URL, "public_url": p.PublicURL()})
	}
}
