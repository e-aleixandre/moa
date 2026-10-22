package voice

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

func TestPeerStartup_DecisionBoundaries(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name                                    string
		ice, connected, ended, ready, cancelled bool
		wantStall, wantReady                    bool
	}{
		{name: "no_ice"},
		{name: "no_ice_dtls_failed", ended: true},
		{name: "watchdog", ice: true, wantStall: true},
		{name: "explicit_dtls_failed", ice: true, ended: true, wantStall: true},
		{name: "dtls_connected_no_channel", ice: true, connected: true},
		{name: "connected_then_closed_is_sticky", ice: true, connected: true, ended: true},
		{name: "dtls_before_ice", ice: true, connected: true},
		{name: "channel_beats_stale_timer", ice: true, ready: true, wantReady: true},
		{name: "cancel_beats_timer", ice: true, cancelled: true},
		{name: "cancel_beats_ready", ice: true, ready: true, cancelled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := peerStartup{wake: make(chan struct{}, 1)}
			if tc.connected {
				s.dtlsChanged(webrtc.DTLSTransportStateConnected)
			}
			if tc.ice {
				s.iceConnected(now)
			}
			if tc.ended {
				s.dtlsChanged(webrtc.DTLSTransportStateFailed)
			}
			if tc.ready {
				s.channelOpened()
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancelled {
				cancel()
			}
			at := now.Add(chatGPTDTLSStall)
			if tc.ended {
				at = now
			}
			ready, _, err := s.decision(ctx, at, chatGPTDTLSStall)
			if ready != tc.wantReady || errors.Is(err, errDTLSStartupStall) != tc.wantStall {
				t.Fatalf("ready=%v err=%v", ready, err)
			}
			if tc.cancelled && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
		})
	}
	s := peerStartup{wake: make(chan struct{}, 1)}
	s.iceConnected(now)
	s.iceConnected(now.Add(2 * time.Second))
	_, deadline, err := s.decision(context.Background(), now.Add(time.Second), chatGPTDTLSStall)
	if err != nil || !deadline.Equal(now.Add(chatGPTDTLSStall)) {
		t.Fatal("ICE reconnect reset watchdog")
	}
	if _, _, err = s.decision(context.Background(), now.Add(chatGPTDTLSStall), chatGPTDTLSStall); !errors.Is(err, errDTLSStartupStall) {
		t.Fatal("exact boundary did not abandon")
	}
	s.dtlsChanged(webrtc.DTLSTransportStateConnected)
	s.channelOpened()
	ready, _, err := s.decision(context.Background(), now.Add(4*time.Second), chatGPTDTLSStall)
	if ready || !errors.Is(err, errDTLSStartupStall) {
		t.Fatal("late readiness resurrected abandoned attempt")
	}
}

func TestPeerStartup_ConnectedAndWatchdogSerialize(t *testing.T) {
	for i := 0; i < 200; i++ {
		s := peerStartup{wake: make(chan struct{}, 1)}
		now := time.Now()
		s.iceConnected(now)
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); <-start; s.dtlsChanged(webrtc.DTLSTransportStateConnected) }()
		go func() {
			defer wg.Done()
			<-start
			_, _, _ = s.decision(context.Background(), now.Add(chatGPTDTLSStall), chatGPTDTLSStall)
		}()
		close(start)
		wg.Wait()
		if s.dtlsSeen == s.abandoned {
			t.Fatalf("ambiguous attempt outcome: dtls_seen=%v abandoned=%v", s.dtlsSeen, s.abandoned)
		}
	}
}

func TestPeerStartup_RealStallGracefulJoinBudget(t *testing.T) {
	for i := 0; i < 3; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			f := newStartupFixture(t, "stall")
			p := newTestPeer(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			offer, err := p.Offer(ctx)
			if err != nil {
				t.Fatal(err)
			}
			// The local server handles the exact production signaling exchange.
			a := f.adapter().(*chatGPTPlan)
			client, closeClient := chatGPTSignalingClient(f.srv.Client())
			defer closeClient()
			body := []byte(fmt.Sprintf(`{"sdp":%q}`, offer))
			answer, err := a.post(ctx, client, body, "SYNTHETIC", Credential{Token: "SYNTHETIC", AccountID: "SYNTHETIC"})
			if err != nil {
				t.Fatal(err)
			}
			if err = p.SetAnswer(answer); err != nil {
				t.Fatal(err)
			}
			if err = p.waitStartupReady(ctx, 30*time.Millisecond); !errors.Is(err, errDTLSStartupStall) {
				t.Fatalf("fixture was not a DTLS stall: %v", err)
			}
			assertStalledRemote(t, f.snapshot()[0])
			// Done may precede the real worker join. Keep one worker outstanding
			// while fail races Close and the startup owner asks for a graceful join.
			release := make(chan struct{})
			p.workers.Add(1)
			go func() { defer p.workers.Done(); <-release }()
			p.fail(errPeerFailed)
			joined := make(chan error, 1)
			started := time.Now()
			go func() { joined <- p.closeStartup() }()
			select {
			case err := <-joined:
				close(release)
				t.Fatalf("returned before worker join: %v", err)
			case <-time.After(20 * time.Millisecond):
			}
			close(release)
			// No timeout escape: wait synchronously, then assert the measured budget.
			if err = <-joined; err != nil {
				t.Fatal(err)
			}
			if elapsed := time.Since(started); elapsed > 2*time.Second {
				t.Fatalf("GracefulClose/worker join exceeded budget: %s", elapsed)
			}
			if _, ok := <-p.Control(); ok {
				t.Fatal("control worker not closed")
			}
			if _, ok := <-p.PCM(); ok {
				t.Fatal("PCM worker not closed")
			}
			t.Logf("real stalled startup joined in %s", time.Since(started))
		})
	}
}

type startupBarrierHandler struct {
	next    slog.Handler
	reached chan struct{}
	release <-chan struct{}
	once    sync.Once
	cancel  context.CancelFunc
}

func (h *startupBarrierHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}
func (h *startupBarrierHandler) WithAttrs(a []slog.Attr) slog.Handler { return h.next.WithAttrs(a) }
func (h *startupBarrierHandler) WithGroup(g string) slog.Handler      { return h.next.WithGroup(g) }
func (h *startupBarrierHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Message == "voice startup peer closed" {
		h.once.Do(func() {
			if h.cancel != nil {
				h.cancel()
			}
			close(h.reached)
			<-h.release
		})
	}
	return h.next.Handle(ctx, r)
}
func TestChatGPTStartup_ClosesAndJoinsBeforeSecondPOST(t *testing.T) {
	for _, cancelAtClose := range []bool{false, true} {
		t.Run(fmt.Sprint("cancel_at_close=", cancelAtClose), func(t *testing.T) {
			f := newStartupFixture(t, "stall", "healthy")
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			release := make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			h := &startupBarrierHandler{next: slog.Default().Handler(), reached: make(chan struct{}), release: release}
			if cancelAtClose {
				h.cancel = cancel
			}
			slog.SetDefault(slog.New(h))
			type result struct {
				up  Upstream
				err error
			}
			done := make(chan result, 1)
			go func() { up, err := f.adapter().Open(ctx, CallSpec{}); done <- result{up, err} }()
			select {
			case <-h.reached:
			case <-time.After(7 * time.Second):
				t.Fatal("failed startup never reached synchronous join barrier")
			}
			if n := len(f.snapshot()); n != 1 {
				t.Fatalf("second POST before join barrier: accepted=%d", n)
			}
			once.Do(func() { close(release) })
			got := <-done
			if cancelAtClose {
				if got.up != nil || got.err == nil {
					t.Fatal("cancelled close published upstream")
				}
				if n := len(f.snapshot()); n != 1 {
					t.Fatalf("POST after cancellation: %d", n)
				}
			} else {
				if got.err != nil {
					t.Fatal(got.err)
				}
				startupClose(t, got.up)
				if len(f.snapshot()) != 2 {
					t.Fatal("missing serial retry")
				}
			}
		})
	}
}
