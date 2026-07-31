package mcp

import (
	"maps"
	"net"
	"net/http"
	"time"
)

// Bounds for the remote transport. They are variables (not constants) so tests
// can shorten them; production code never reassigns them.
var (
	// remoteDialTimeout caps establishing the TCP/TLS connection.
	remoteDialTimeout = 10 * time.Second
	// remoteResponseHeaderTimeout caps the wait for response HEADERS only. It
	// bounds a blackholed POST/DELETE/GET (a peer that accepts the connection
	// and never answers) without touching the SSE body, whose headers arrive at
	// stream start and whose events may then trickle in for hours.
	remoteResponseHeaderTimeout = 15 * time.Second
)

// headerRoundTripper adds fixed headers (typically Authorization) to every
// request the streamable transport makes. The SDK transport takes an
// *http.Client, not a header map, so this is where per-server credentials go.
type headerRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
}

func (h headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// A RoundTripper must not modify the request it is given.
	clone := req.Clone(req.Context())
	for k, v := range h.headers {
		clone.Header.Set(k, v)
	}
	base := h.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(clone)
}

// remoteHTTPClient builds the client a remote MCP server is reached with.
//
// No Client.Timeout is set on purpose: the streamable transport keeps a
// long-lived SSE stream open for server-initiated messages, and an overall
// deadline would tear it down mid-session. Connect is bounded by
// serverStartTimeout, tool calls by the caller's context, and every individual
// request by the transport's dial/response-header timeouts.
//
// Redirects are never followed: the round tripper re-applies the configured
// headers on every hop, so a 30x to another origin would ship the server's
// credentials there. Returning the 30x as-is makes the SDK fail the request
// instead.
func remoteHTTPClient(headers map[string]string) *http.Client {
	return &http.Client{
		Transport: headerRoundTripper{base: remoteTransport(), headers: maps.Clone(headers)},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// remoteTransport is the bounded transport every remote MCP client uses.
func remoteTransport() *http.Transport {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: remoteDialTimeout}).DialContext,
		ResponseHeaderTimeout: remoteResponseHeaderTimeout,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   remoteDialTimeout,
		ExpectContinueTimeout: time.Second,
	}
}
