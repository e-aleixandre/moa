package voice

import (
	"encoding/json"
	"math"
	"sync/atomic"
	"time"
)

// AudioDiagnostics is temporary, numeric-only state shared by the audio edges.
// Snapshots are weak concurrent cuts, not accounting transactions.
type AudioDiagnostics struct {
	// Set once before Open. The diagnostic sink must be nonblocking.
	ControlTrace func(int64)          `json:"-"`
	Gate         *AudioDiagnosticGate `json:"-"`
	counts       [audioCounterCount]atomic.Int64
	stats        [audioStatCount]audioStat
}

// AudioDiagnosticGate is shared by a registry and every peer opened by it.
// Odd generations are ON; every transition advances the generation so even a
// paused observer detects a complete OFF/ON interval. SetEnabled is control-only.
type AudioDiagnosticGate struct{ state atomic.Uint64 }

func (g *AudioDiagnosticGate) SetEnabled(on bool) {
	for {
		old := g.state.Load()
		if (old&1 != 0) == on {
			return
		}
		if g.state.CompareAndSwap(old, old+1) {
			return
		}
	}
}
func (g *AudioDiagnosticGate) Epoch() uint64 {
	state := g.state.Load()
	if state&1 == 0 {
		return 0
	}
	return state
}
func (d *AudioDiagnostics) Epoch() uint64 {
	if d == nil {
		return 0
	}
	if d.Gate == nil {
		return 1
	}
	return d.Gate.Epoch()
}

// Only recognized control kinds cross this boundary. No raw type, status,
// content, IDs or provider errors are retained, including for ignored events.
func (d *AudioDiagnostics) observeControl(data []byte) {
	if d.Epoch() == 0 || d.ControlTrace == nil {
		return
	}
	var e struct {
		Type     string `json:"type"`
		Response struct {
			Status string `json:"status"`
		} `json:"response"`
	}
	if json.Unmarshal(data, &e) != nil {
		return
	}
	var kind int64
	switch e.Type {
	case "input_audio_buffer.speech_started":
		kind = 1
	case "input_audio_buffer.speech_stopped":
		kind = 2
	case "response.done":
		kind = 3
		switch e.Response.Status {
		case "completed":
			kind = 9
		case "cancelled", "canceled":
			kind = 10
		case "failed":
			kind = 11
		case "incomplete":
			kind = 12
		}
	case "response.cancelled":
		kind = 4
	case "response.canceled":
		kind = 5
	case "response.cancel":
		kind = 6
	case "turn.started":
		kind = 7
	case "turn.done":
		kind = 8
	default:
		return
	}
	d.ControlTrace(kind)
}

type AudioCounter int

const (
	RTPTracks AudioCounter = iota
	RTPPackets
	RTPBytes
	RTPForward
	RTPGapSlots
	RTPLate
	RTPDuplicate
	RTPOldUnknown
	RTPHalfRange
	RTPEmpty
	RTPDecodeError
	RTPDecodeZero
	RTPDecoded
	RTPSamples
	RTPPCMBytes
	RTPSwitches
	RTPOverflow
	RTPTSZero
	RTPTSInvalid
	EventFrames
	EventBytes
	EventSamples
	EventNoClient
	EarlyRetained
	EarlyFlushed
	EarlyDiscarded
	EnqueueAttempts
	EnqueueAccepted
	EnqueueRejected
	WriteAttempts
	WriteOK
	WriteError
	WrittenBytes
	NumericSaturated
	audioCounterCount
)

var AudioCounterNames = [audioCounterCount]string{
	"rtp_tracks", "rtp_packets", "rtp_payload_bytes", "rtp_forward", "rtp_gap_slots", "rtp_late_first_seen", "rtp_duplicate", "rtp_old_unknown", "rtp_half_range_ambiguous",
	"rtp_empty_payload", "rtp_decode_error", "rtp_decode_zero", "rtp_decoded_packets", "rtp_samples24k", "rtp_pcm_bytes", "rtp_channel_switches", "rtp_pcm_overflow", "rtp_ts_zero", "rtp_ts_invalid",
	"event_frames", "event_bytes", "event_samples", "event_no_client", "early_retained", "early_flushed", "early_discarded",
	"enqueue_attempts", "enqueue_accepted", "enqueue_rejected", "write_attempts", "write_ok", "write_error", "written_bytes",
	"numeric_saturated",
}

type AudioStat int

const (
	RTPArrivalUS AudioStat = iota
	RTPTicks
	RTPPayloadBytes
	DecodeUS
	DecodedSamples
	EventGapUS
	EnqueueUS
	AdmissionGapUS
	OccupancyBytes
	QueueAgeUS
	WriteUS
	WriteGapUS
	WriteBytes
	audioStatCount
)

var AudioStatNames = [audioStatCount]string{"rtp_arrival_dt_us", "rtp_timestamp_delta_ticks", "rtp_payload_size", "decode_us", "decoded_samples", "event_dt_us", "enqueue_us", "admission_dt_us", "occupancy_bytes", "queue_age_us", "write_us", "write_dt_us", "write_bytes"}

type audioStat struct {
	n, max, sum atomic.Int64
	min         atomic.Uint64
}
type AudioAggregate struct{ N, Min, Max, Sum int64 }
type AudioSnapshot struct {
	Counts [audioCounterCount]int64
	Stats  [audioStatCount]AudioAggregate
}

func satAdd(a *atomic.Int64, n int64) bool {
	for {
		old := a.Load()
		next := old + n
		overflow := n > math.MaxInt64-old
		if overflow {
			next = math.MaxInt64
		}
		if a.CompareAndSwap(old, next) {
			return overflow
		}
	}
}
func (d *AudioDiagnostics) Add(k AudioCounter, n int64) {
	if d.Epoch() == 0 {
		return
	}
	if satAdd(&d.counts[k], n) && k != NumericSaturated {
		satAdd(&d.counts[NumericSaturated], 1)
	}
}
func (d *AudioDiagnostics) Observe(k AudioStat, v int64) {
	if d.Epoch() == 0 || v < 0 {
		return
	}
	s := &d.stats[k]
	// Store min encoded as value+1, leaving zero as the unobserved sentinel.
	encoded := uint64(v) + 1
	for old := s.min.Load(); old == 0 || encoded < old; old = s.min.Load() {
		if s.min.CompareAndSwap(old, encoded) {
			break
		}
	}
	for old := s.max.Load(); v > old; old = s.max.Load() {
		if s.max.CompareAndSwap(old, v) {
			break
		}
	}
	if satAdd(&s.sum, v) {
		d.Add(NumericSaturated, 1)
	}
	if satAdd(&s.n, 1) {
		d.Add(NumericSaturated, 1)
	}
}
func (d *AudioDiagnostics) Snapshot() (out AudioSnapshot) {
	for i := range out.Counts {
		out.Counts[i] = d.counts[i].Load()
	}
	for i := range out.Stats {
		s := &d.stats[i]
		minimum := s.min.Load()
		if minimum != 0 {
			minimum--
		}
		out.Stats[i] = AudioAggregate{N: s.n.Load(), Min: int64(minimum), Max: s.max.Load(), Sum: s.sum.Load()}
	}
	return
}

// rtpObservation belongs to one readTrack, never to a shared SSRC history.
type rtpObservation struct {
	d        *AudioDiagnostics
	epoch    uint64
	started  bool
	last     uint16
	seen     uint64
	ts       uint32
	arrival  time.Time
	channels int
}

func (o *rtpObservation) packet(epoch uint64, seq uint16, ts uint32, size int, clock uint32, now time.Time) {
	d := o.d
	if epoch == 0 || d.Epoch() != epoch {
		return
	}
	if o.epoch != epoch {
		if o.epoch == 0 {
			d.Add(RTPTracks, 1)
		}
		*o = rtpObservation{d: d, epoch: epoch}
	}
	d.Add(RTPPackets, 1)
	d.Add(RTPBytes, int64(size))
	d.Observe(RTPPayloadBytes, int64(size))
	if size == 0 {
		d.Add(RTPEmpty, 1)
	}
	if !o.arrival.IsZero() {
		d.Observe(RTPArrivalUS, now.Sub(o.arrival).Microseconds())
	}
	o.arrival = now
	delta := seq - o.last
	forward := !o.started || delta > 0 && delta < 32768
	if forward {
		d.Add(RTPForward, 1)
		if o.started {
			d.Add(RTPGapSlots, int64(delta-1))
			ticks := ts - o.ts
			switch {
			case clock != 48000 || ticks >= 1<<31:
				d.Add(RTPTSInvalid, 1)
			case ticks == 0:
				d.Add(RTPTSZero, 1)
			default:
				d.Observe(RTPTicks, int64(ticks))
			}
		}
		if delta >= 64 {
			o.seen = 0
		} else {
			o.seen <<= delta
		}
		o.seen |= 1
		o.started = true
		o.last = seq
		o.ts = ts
	} else {
		switch delta {
		case 32768:
			d.Add(RTPHalfRange, 1)
		case 0:
			d.Add(RTPDuplicate, 1)
		default:
			back := o.last - seq
			if back >= 64 {
				d.Add(RTPOldUnknown, 1)
			} else if o.seen&(uint64(1)<<back) != 0 {
				d.Add(RTPDuplicate, 1)
			} else {
				o.seen |= uint64(1) << back
				d.Add(RTPLate, 1)
			}
		}
	}
}
func (o *rtpObservation) decoded(channels, n int, err error, elapsed time.Duration) {
	d := o.d
	if o.epoch != d.Epoch() || o.epoch == 0 {
		return
	}
	d.Observe(DecodeUS, elapsed.Microseconds())
	if o.channels != 0 && o.channels != channels {
		d.Add(RTPSwitches, 1)
	}
	o.channels = channels
	if err != nil {
		d.Add(RTPDecodeError, 1)
	} else if n == 0 {
		d.Add(RTPDecodeZero, 1)
	} else {
		d.Add(RTPDecoded, 1)
		d.Add(RTPSamples, int64(n))
		d.Add(RTPPCMBytes, int64(2*n))
		d.Observe(DecodedSamples, int64(n))
	}
}
