package voice

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
)

var errDTLSStartupStall = errors.New("voice DTLS startup stall")

// Callbacks only record observations. In particular the DTLS callback holds
// Pion's transport lock, so it must not call back into Pion or perform I/O.
type peerStartup struct {
	mu        sync.Mutex
	wake      chan struct{}
	firstICE  time.Time
	dtlsSeen  bool
	dtlsEnded bool
	ready     bool
	abandoned bool
}

func (s *peerStartup) notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *peerStartup) iceConnected(now time.Time) {
	s.mu.Lock()
	if s.firstICE.IsZero() {
		s.firstICE = now
	}
	s.mu.Unlock()
	s.notify()
}

func (s *peerStartup) firstICEAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.firstICE
}

func (s *peerStartup) dtlsChanged(state webrtc.DTLSTransportState) {
	s.mu.Lock()
	if !s.abandoned {
		s.dtlsSeen = s.dtlsSeen || state == webrtc.DTLSTransportStateConnected
		s.dtlsEnded = state == webrtc.DTLSTransportStateFailed || state == webrtc.DTLSTransportStateClosed
	}
	s.mu.Unlock()
	s.notify()
}

func (s *peerStartup) channelOpened() {
	s.mu.Lock()
	if !s.abandoned {
		s.ready = true
	}
	s.mu.Unlock()
	s.notify()
}

// decision serializes abandonment with readiness/DTLS observations. A late
// callback cannot resurrect an abandoned attempt or unlatch DTLS-connected.
func (s *peerStartup) decision(ctx context.Context, now time.Time, stall time.Duration) (bool, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, time.Time{}, err
	}
	if s.abandoned {
		return false, time.Time{}, errDTLSStartupStall
	}
	if s.ready {
		return true, time.Time{}, nil
	}
	if !s.dtlsSeen && !s.firstICE.IsZero() {
		deadline := s.firstICE.Add(stall)
		if s.dtlsEnded || !now.Before(deadline) {
			s.abandoned = true
			return false, time.Time{}, errDTLSStartupStall
		}
		return false, deadline, nil
	}
	return false, time.Time{}, nil
}

func (s *peerStartup) seen() (ice, dtls bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.firstICE.IsZero(), s.dtlsSeen
}

// WaitReady also serves established-call sends; only startup owns this watchdog.
func (p *Peer) waitStartupReady(ctx context.Context, stall time.Duration) error {
	for {
		ready, deadline, err := p.startup.decision(ctx, time.Now(), stall)
		if err != nil || ready {
			return err
		}
		var timer *time.Timer
		var expired <-chan time.Time
		if !deadline.IsZero() {
			timer = time.NewTimer(time.Until(deadline))
			expired = timer.C
		}
		select {
		case <-p.startup.wake:
		case <-expired:
		case <-ctx.Done():
		case <-p.done:
			if timer != nil {
				timer.Stop()
			}
			ready, _, err = p.startup.decision(ctx, time.Now(), stall)
			if err != nil || ready {
				return err
			}
			return p.closedErr()
		}
		if timer != nil {
			timer.Stop()
		}
	}
}

// Done is not a join barrier. The startup owner waits for concurrent Close
// and then Pion's internal operations before it may create another peer.
func (p *Peer) closeStartup() error {
	err := p.Close()
	return errors.Join(err, p.pc.GracefulClose())
}
