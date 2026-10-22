package voice

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

type startupCredentialsProbe struct {
	resolves, refreshes atomic.Int32
	deadlines           chan time.Time
}

type startupDeadlineTransport struct {
	base      http.RoundTripper
	deadlines chan<- time.Time
}

func (tr startupDeadlineTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	deadline, _ := req.Context().Deadline()
	tr.deadlines <- deadline
	return tr.base.RoundTrip(req)
}

func (c *startupCredentialsProbe) Resolve(ctx context.Context) (Credential, error) {
	c.resolves.Add(1)
	if c.deadlines != nil {
		d, _ := ctx.Deadline()
		c.deadlines <- d
	}
	return Credential{Token: "PRIVATE-ORIGINAL", AccountID: "PRIVATE-ACCOUNT"}, nil
}
func (c *startupCredentialsProbe) Refresh(context.Context, Credential) (Credential, error) {
	c.refreshes.Add(1)
	return Credential{Token: "PRIVATE-REFRESHED", AccountID: "PRIVATE-ACCOUNT"}, nil
}
func TestChatGPTStartup_OneAuthRefreshAcrossAttempts(t *testing.T) {
	for _, denied := range [][]int{{1}, {2}, {1, 3}} {
		t.Run(fmt.Sprint(denied), func(t *testing.T) {
			f := newStartupFixture(t, "stall", "healthy")
			var requests atomic.Int32
			original := f.srv.Config.Handler
			f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := int(requests.Add(1))
				for _, refusal := range denied {
					if refusal == n {
						_, _ = io.Copy(io.Discard, r.Body)
						w.WriteHeader(401)
						return
					}
				}
				original.ServeHTTP(w, r)
			})
			probe := &startupCredentialsProbe{}
			a := f.adapter().(*chatGPTPlan)
			a.cfg.Credentials = probe
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			up, err := a.Open(ctx, CallSpec{})
			exhausted := len(denied) == 2
			if exhausted {
				if up != nil || Classify(err).Class != ClassUnauthorized {
					t.Fatalf("exhausted refresh returned %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				startupClose(t, up)
			}
			if probe.resolves.Load() != 1 || probe.refreshes.Load() != 1 || requests.Load() != 3 {
				t.Fatalf("resolve=%d refresh=%d wire requests=%d", probe.resolves.Load(), probe.refreshes.Load(), requests.Load())
			}
			want := 2
			if exhausted {
				want = 1
			}
			if n := len(f.snapshot()); n != want {
				t.Fatalf("accepted=%d, want %d", n, want)
			}
			if strings.Contains(f.log.String(), "PRIVATE-") {
				t.Fatal("credential/account leaked in logs")
			}
		})
	}
}

func TestChatGPTSignaling_RefusalsAndAmbiguityNeverRetry(t *testing.T) {
	for _, mode := range []string{"403", "429", "500", "redirect", "body_loss", "bad_sdp", "transport_loss"} {
		t.Run(mode, func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				switch mode {
				case "redirect":
					w.Header().Set("Location", "/unexpected-replay")
					w.WriteHeader(307)
				case "bad_sdp":
					w.WriteHeader(201)
					_, _ = io.WriteString(w, "PRIVATE-SDP-INVALID")
				case "body_loss":
					w.Header().Set("Content-Length", "12345")
					w.WriteHeader(201)
					_, _ = io.WriteString(w, "PRIVATE-PARTIAL-ANSWER")
				case "transport_loss":
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
				default:
					status := 403
					if mode == "429" {
						status = 429
					} else if mode == "500" {
						status = 500
					}
					w.WriteHeader(status)
					_, _ = io.WriteString(w, "PRIVATE-REFUSAL-CONTENT")
				}
			}))
			defer srv.Close()
			var logs startupLog
			// No provider bodies/errors are admitted to the safe adapter log.
			old := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			defer slog.SetDefault(old)
			a := NewChatGPTPlan(ChatGPTPlanConfig{URL: srv.URL, HTTPClient: srv.Client(), Credentials: fixedCred{}, Peer: PeerConfig{ICEServers: []webrtc.ICEServer{}, SettingEngine: loopbackEngine()}})
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			up, err := a.Open(ctx, CallSpec{})
			if up != nil || err == nil || requests.Load() != 1 {
				t.Fatalf("ambiguous/refused startup: up=%v err=%v requests=%d", up, err, requests.Load())
			}
			if strings.Contains(logs.String(), "PRIVATE-") || strings.Contains(logs.String(), "127.0.0.1") {
				t.Fatal("unsafe error log")
			}
		})
	}
}

func TestChatGPTSignaling_PreservesProxyTLSAndDialPolicy(t *testing.T) {
	var requests, dials, proxies atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(403) }))
	defer proxy.Close()
	proxyURL, _ := url.Parse(proxy.URL)
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = func(*http.Request) (*url.URL, error) { proxies.Add(1); return proxyURL, nil }
	dialer := &net.Dialer{Timeout: time.Second}
	base.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		dials.Add(1)
		return dialer.DialContext(ctx, network, address)
	}
	base.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: "policy.invalid", NextProtos: []string{"h2", "http/1.1"}}
	client, closeClient := chatGPTSignalingClient(&http.Client{Transport: base})
	defer closeClient()
	tr := client.Transport.(*http.Transport)
	if tr.TLSClientConfig == base.TLSClientConfig || tr.TLSClientConfig.ServerName != "policy.invalid" || tr.TLSClientConfig.MinVersion != tls.VersionTLS12 {
		t.Fatal("TLS policy lost or shared")
	}
	if !base.ForceAttemptHTTP2 || base.DisableKeepAlives || strings.Join(base.TLSClientConfig.NextProtos, ",") != "h2,http/1.1" {
		t.Fatal("mutated shared transport")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", "http://must-use-proxy.invalid/calls", strings.NewReader("synthetic"))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if requests.Load() != 1 || proxies.Load() != 1 || dials.Load() != 1 {
		t.Fatalf("proxy=%d dial=%d wire requests=%d", proxies.Load(), dials.Load(), requests.Load())
	}
}

func TestChatGPTStartup_DeadlineCapAndParentBudget(t *testing.T) {
	for _, bounded := range []bool{false, true} {
		t.Run(fmt.Sprint(bounded), func(t *testing.T) {
			ctx := context.Background()
			var expected time.Time
			if bounded {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 450*time.Millisecond)
				defer cancel()
				expected, _ = ctx.Deadline()
			}
			f := newStartupFixture(t, "noice")
			probe := &startupCredentialsProbe{deadlines: make(chan time.Time, 1)}
			a := f.adapter().(*chatGPTPlan)
			a.cfg.Credentials = probe
			if !bounded {
				// Fail signaling immediately; observing the Open-owned context is enough
				// to prove the cap without spending twenty seconds waiting for it.
				f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) })
			}
			before := time.Now()
			up, err := a.Open(ctx, CallSpec{})
			after := time.Now()
			if up != nil || err == nil {
				t.Fatal("unexpected ready upstream")
			}
			d := <-probe.deadlines
			if bounded {
				if !d.Equal(expected) {
					t.Fatalf("parent deadline reset: got %s want %s", d, expected)
				}
			} else if d.Before(before.Add(20*time.Second)) || d.After(after.Add(20*time.Second)) {
				t.Fatalf("missing 20s startup cap: %s", d)
			}
		})
	}
}

func TestChatGPTSignaling_CancelledBeforeOpenDoesNoWork(t *testing.T) {
	f := newStartupFixture(t, "healthy")
	probe := &startupCredentialsProbe{}
	a := f.adapter().(*chatGPTPlan)
	a.cfg.Credentials = probe
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	up, err := a.Open(ctx, CallSpec{})
	if up != nil || err == nil || probe.resolves.Load() != 0 || len(f.snapshot()) != 0 {
		t.Fatal("cancelled open started work")
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("control: cancellation missing")
	}
}
