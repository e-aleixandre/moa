// Package sseutil provides shared utilities for SSE stream handling
// across LLM provider implementations.
package sseutil

import (
	"errors"
	"io"
	"math"
	"sync/atomic"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// IdleTimeoutReader wraps a stream body and fails with ErrIdleTimeout when a
// single Read waits longer than the timeout for data. This protects against
// stalled SSE streams where the connection stays open but stops sending
// events. Any bytes reset the clock, SSE keep-alive comments included: with
// thinking omitted they are all that arrives while the model reasons.
//
// On timeout the body is closed, which unblocks the pending Read; Read is
// delegated directly, so it costs no copy, goroutine or allocation. The clock
// only runs inside Read: time the consumer spends between reads is not
// idleness of the connection. Read is not safe for concurrent use, like the
// body it wraps.
type IdleTimeoutReader struct {
	r       io.ReadCloser
	timeout time.Duration
	timer   *time.Timer
	expired atomic.Bool
}

// NewIdleTimeoutReader creates a reader that errors after timeout of inactivity.
// A timeout of 0 disables the idle check (passes reads through directly).
func NewIdleTimeoutReader(r io.ReadCloser, timeout time.Duration) *IdleTimeoutReader {
	itr := &IdleTimeoutReader{r: r, timeout: timeout}
	if timeout > 0 {
		// Armed only by Read; created far in the future so it cannot fire
		// before the first Reset.
		itr.timer = time.AfterFunc(math.MaxInt64, itr.expire)
		itr.timer.Stop()
	}
	return itr
}

// expire runs on the timer goroutine. The flag is set before closing so a
// Read unblocked by the close always sees it.
func (itr *IdleTimeoutReader) expire() {
	if itr.expired.CompareAndSwap(false, true) {
		_ = itr.r.Close()
	}
}

// Read implements io.Reader. Returns ErrIdleTimeout if the underlying
// reader doesn't produce data within the timeout, and on every Read after.
func (itr *IdleTimeoutReader) Read(p []byte) (int, error) {
	if itr.timer == nil {
		return itr.r.Read(p)
	}
	if itr.expired.Load() {
		return 0, ErrIdleTimeout
	}
	itr.timer.Reset(itr.timeout)
	n, err := itr.r.Read(p)
	itr.timer.Stop()
	// The timer may fire between Read returning and Stop: the body is then
	// closed, so report the timeout now rather than a close error next time.
	if itr.expired.Load() {
		return n, ErrIdleTimeout
	}
	return n, err
}

// ErrIdleTimeout is returned when no data is received within the timeout. It
// is a transient stream failure: the agent loop repairs the stream.
var ErrIdleTimeout error = &core.StreamFailureError{
	Err:       errors.New("stream idle timeout: no data received"),
	Transient: true,
}
