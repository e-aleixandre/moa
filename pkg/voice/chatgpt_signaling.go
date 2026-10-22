package voice

import (
	"context"
	"crypto/tls"
	"net/http"
)

// Creation POSTs must not be replayed by HTTP/2 or a reused connection. Own
// the clone, retaining the caller's proxy, TLS validation and dial policy.
func chatGPTSignalingClient(client *http.Client) (*http.Client, func()) {
	cp := noRedirect(client)
	selected := cp.Transport
	if selected == nil {
		selected = http.DefaultTransport
	}
	base, ok := selected.(*http.Transport)
	if !ok {
		// An injected RoundTripper must itself provide one-shot semantics.
		return cp, func() {}
	}
	tr := base.Clone()
	tr.ForceAttemptHTTP2 = false
	tr.Protocols = new(http.Protocols)
	tr.Protocols.SetHTTP1(true)
	tr.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	if tr.TLSClientConfig == nil {
		tr.TLSClientConfig = &tls.Config{}
	}
	tr.TLSClientConfig.NextProtos = []string{"http/1.1"}
	tr.DisableKeepAlives = true
	cp.Transport = tr
	return cp, tr.CloseIdleConnections
}

// Both accepted attempts share one credential snapshot and at most the
// existing single OAuth refresh. This state is owned by the serial Open.
type chatGPTStartupCredentials struct {
	source     CredentialSource
	credential Credential
	resolved   bool
	refreshed  bool
}

func (c *chatGPTStartupCredentials) Resolve(ctx context.Context) (Credential, error) {
	if !c.resolved {
		cred, err := c.source.Resolve(ctx)
		if err != nil {
			return Credential{}, err
		}
		c.credential, c.resolved = cred, true
	}
	return c.credential, nil
}

func (c *chatGPTStartupCredentials) Refresh(ctx context.Context, old Credential) (Credential, error) {
	c.refreshed = true
	cred, err := c.source.Refresh(ctx, old)
	if err == nil {
		c.credential = cred
	}
	return cred, err
}
