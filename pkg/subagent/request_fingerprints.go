package subagent

import (
	"log/slog"

	"github.com/e-aleixandre/moa/pkg/core"
)

// childRequestFingerprints turns the per-request thunks of one child job into
// at most two audit snapshots, paying for hashing only when a snapshot is
// emitted: the first request is evaluated immediately (so a crash still leaves
// an audit), later ones only replace the retained thunk, and the last is
// evaluated once when the job ends.
//
// It is driven by one job's agent run and its deferred finish, which never
// overlap, so it needs no locking. Diagnostics must never alter the job.
type childRequestFingerprints struct {
	count       uint64
	first       *core.RequestFingerprint
	firstFailed bool
	last        core.RequestFingerprintFunc
	emit        func(count uint64, first core.RequestFingerprint, last *core.RequestFingerprint)
}

func (t *childRequestFingerprints) observe(f core.RequestFingerprintFunc) {
	t.count++
	if t.count > 1 {
		if !t.firstFailed {
			t.last = f
		}
		return
	}
	fp, err := f()
	if err != nil {
		t.firstFailed = true
		slog.Warn("subagent: request fingerprint unavailable")
		return
	}
	t.first = &fp
	t.emit(1, fp, nil)
}

// finish emits the final snapshot and always drops the retained thunk (and
// with it the request body) even when nothing is emitted.
func (t *childRequestFingerprints) finish() {
	last := t.last
	t.last = nil
	if t.count == 0 || t.first == nil {
		return
	}
	if t.count == 1 {
		t.emit(1, *t.first, t.first)
		return
	}
	var final *core.RequestFingerprint
	if fp, err := last(); err != nil {
		slog.Warn("subagent: request fingerprint unavailable")
	} else {
		final = &fp
	}
	t.emit(t.count, *t.first, final)
}
