package auth

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// errMCPBlockedAddress is returned when an OAuth request for a public MCP
// server would reach a loopback, private or otherwise internal address.
var errMCPBlockedAddress = errors.New("refusing to contact a non-public address")

// nonPublicPrefixes are the RFC 6890 special-purpose ranges netip has no
// predicate for. Translation prefixes (NAT64, 6to4, Teredo) are included
// because they can embed an internal IPv4 address.
var nonPublicPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, p := range []string{
		"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24",
		"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
		"64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/23", "2001:db8::/32", "2002::/16",
	} {
		out = append(out, netip.MustParsePrefix(p))
	}
	return out
}()

// isPublicAddr reports whether ap is a globally routable unicast address.
func isPublicAddr(ap netip.AddrPort) bool {
	ip := ap.Addr().Unmap()
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsMulticast() ||
		ip.IsUnspecified() {
		return false
	}
	for _, p := range nonPublicPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// newMCPHTTPClient builds an OAuth client that never follows redirects: a 30x
// would carry a code, verifier or refresh token to wherever Location points.
// With allow set, every dialed address must pass it; checking at dial time
// (after DNS) also covers rebinding. base supplies TLS settings (tests).
func newMCPHTTPClient(base *http.Transport, allow func(netip.AddrPort) bool) *http.Client {
	c := &http.Client{
		Timeout: mcpHTTPTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	if allow == nil {
		return c
	}
	if base == nil {
		base = http.DefaultTransport.(*http.Transport)
	}
	tr := base.Clone()
	// A proxy would dial the target on our behalf, out of reach of Control.
	tr.Proxy = nil
	d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil || !allow(ap) {
				return errMCPBlockedAddress
			}
			return nil
		}}
	tr.DialContext = d.DialContext
	c.Transport = tr
	return c
}

// clientFor picks the OAuth client for flows of serverURL. A server that is
// itself local (loopback/private: local development) may point anywhere; a
// public one only gets the strict client, and strict tells the caller to
// require https on every discovered endpoint.
func (s *MCPOAuthStore) clientFor(serverURL string) (c *http.Client, strict bool) {
	if s.serverIsLocal(serverURL) {
		return s.client, false
	}
	return s.strictClient, true
}

// serverIsLocal decides from the URL alone: localhost names and literal
// non-public IPs. A hostname is never resolved here, because a later lookup
// could answer differently (DNS rebinding) and lift the restrictions for a
// server that is in fact public.
func (s *MCPOAuthStore) serverIsLocal(serverURL string) bool {
	u, err := url.Parse(serverURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	port, _ := strconv.Atoi(u.Port())
	if port == 0 {
		port = 443
		if u.Scheme == "http" {
			port = 80
		}
	}
	return !s.addrPublic(netip.AddrPortFrom(ip, uint16(port)))
}

// requireHTTPS rejects a discovered endpoint that is not https when strict.
// Even a local server only gets http(s): the authorization endpoint ends up as
// a link in the browser, where a javascript: URL would run in moa's origin.
func requireHTTPS(strict bool, what, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && (strict || u.Scheme != "http")) {
		if strict {
			return errors.New("the " + what + " is not an https URL")
		}
		return errors.New("the " + what + " is not an http(s) URL")
	}
	return nil
}
