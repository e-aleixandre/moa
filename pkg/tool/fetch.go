package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	readability "github.com/go-shiori/go-readability"

	htmltomd "github.com/JohannesKaufmann/html-to-markdown/v2"

	"github.com/ealeixandre/moa/pkg/core"
)

const maxFetchBytes = 5 * 1024 * 1024 // 5 MB

// fetchDialControl blocks connections to link-local / cloud-metadata addresses
// (169.254.0.0/16 and fe80::/10 — the latter covers 169.254.169.254, the AWS/
// GCP/Azure metadata endpoint). It runs AFTER DNS resolution on every dial,
// including each redirect hop, so a hostname that resolves to a blocked IP is
// rejected too. Minimal by design (M3): loopback and private ranges stay
// reachable so fetch_content can still hit local dev servers.
func fetchDialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); ip != nil &&
		(ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()) {
		return fmt.Errorf("blocked link-local/metadata address: %s", ip)
	}
	return nil
}

// fetchClient is used by fetch_content instead of http.DefaultClient so every
// connection (and redirect hop) passes through fetchDialControl, and redirects
// are re-validated (scheme + hop cap).
//
// Proxy is intentionally left unset (nil): honoring HTTP_PROXY/HTTPS_PROXY
// would route the request through the proxy, so fetchDialControl would only
// ever see the proxy's (public) IP and never the real destination — silently
// defeating the link-local/metadata block. Dialing direct keeps the SSRF
// guard effective.
var fetchClient = &http.Client{
	Transport: &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
			Control:   fetchDialControl,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
			return fmt.Errorf("blocked redirect to non-http(s) URL: %s", req.URL.Scheme)
		}
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		return nil
	},
}

// NewFetch creates the fetch_content tool.
func NewFetch(cfg ToolConfig) core.Tool {
	return core.Tool{
		Name:        "fetch_content",
		Label:       "Fetch",
		Description: "Fetch a URL and extract readable content as markdown. Useful for reading documentation, blog posts, changelogs, etc.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"url": {
					"type": "string",
					"description": "URL to fetch (must start with http:// or https://)"
				},
				"raw": {
					"type": "boolean",
					"description": "Return raw HTML instead of extracted markdown (default: false)"
				}
			},
			"required": ["url"]
		}`),
		Effect: core.EffectReadOnly,
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			rawURL := getString(params, "url", "")
			if rawURL == "" {
				return core.ErrorResult("url is required"), nil
			}

			parsed, err := url.Parse(rawURL)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
				return core.ErrorResult("invalid URL: must start with http:// or https://"), nil
			}

			raw := getBool(params, "raw", false)

			reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()

			req, err := http.NewRequestWithContext(reqCtx, "GET", rawURL, nil)
			if err != nil {
				return core.ErrorResult(fmt.Sprintf("building request: %v", err)), nil
			}
			req.Header.Set("User-Agent", "Moa/1.0 (coding agent)")
			req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

			resp, err := fetchClient.Do(req)
			if err != nil {
				if ctx.Err() != nil {
					return core.Result{}, ctx.Err()
				}
				return core.ErrorResult(fmt.Sprintf("fetch failed: %v", err)), nil
			}
			defer resp.Body.Close() //nolint:errcheck

			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return core.ErrorResult(fmt.Sprintf("fetch failed: HTTP %d", resp.StatusCode)), nil
			}

			body, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBytes))
			if err != nil {
				if ctx.Err() != nil {
					return core.Result{}, ctx.Err()
				}
				return core.ErrorResult(fmt.Sprintf("reading response: %v", err)), nil
			}
			htmlStr := string(body)

			if raw {
				result := truncateOutput(htmlStr, maxOutputBytes)
				result = truncateLines(result, maxOutputLines)
				return core.TextResult(result), nil
			}

			// Readability extraction → HTML → Markdown
			title, markdown := extractContent(rawURL, htmlStr)

			// Prepend metadata header
			var out strings.Builder
			if title != "" {
				fmt.Fprintf(&out, "Title: %s\n", title)
			}
			fmt.Fprintf(&out, "URL: %s\n---\n\n", rawURL)
			out.WriteString(markdown)

			result := out.String()
			result = truncateOutput(result, maxOutputBytes)
			result = truncateLines(result, maxOutputLines)

			return core.TextResult(result), nil
		},
	}
}

// extractContent tries readability first, falls back to raw html-to-markdown.
func extractContent(pageURL, htmlStr string) (title, markdown string) {
	parsed, err := url.Parse(pageURL)
	if err != nil {
		parsed = &url.URL{}
	}

	article, err := readability.FromReader(strings.NewReader(htmlStr), parsed)
	if err == nil && strings.TrimSpace(article.TextContent) != "" {
		title = article.Title
		md, convErr := htmltomd.ConvertString(article.Content)
		if convErr == nil && strings.TrimSpace(md) != "" {
			return title, md
		}
		// Conversion failed but we have text — use plain text
		return title, article.TextContent
	}

	// Readability failed — convert entire HTML to markdown
	md, err := htmltomd.ConvertString(htmlStr)
	if err != nil || strings.TrimSpace(md) == "" {
		// Last resort: return raw HTML truncated
		return "", htmlStr
	}
	return "", md
}
