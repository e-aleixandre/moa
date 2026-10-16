package voice

import "math"

const (
	// upsamplePhaseTaps is the FIR length per polyphase branch. 32 taps per
	// branch (64 at 48 kHz) keeps the 11–19 kHz transition band well below the
	// -40 dB image target while costing ~64 MACs per 24 kHz input sample.
	upsamplePhaseTaps = 32
	// upsampleCutoffHz sits just under the 12 kHz Nyquist of the input so the
	// spectral images that zero-stuffing creates above it are rejected.
	upsampleCutoffHz = 11000.0
	upsampleKaiser   = 8.0
)

// upsampleCoeffs holds the two polyphase branches of the interpolation FIR:
// branch 0 produces the even 48 kHz outputs, branch 1 the odd ones.
var upsampleCoeffs = designUpsampler()

func designUpsampler() [2][upsamplePhaseTaps]float64 {
	const n = 2 * upsamplePhaseTaps
	fc := upsampleCutoffHz / 48000
	centre := float64(n-1) / 2
	var h [n]float64
	var sum float64
	for i := range h {
		x := float64(i) - centre
		sinc := 2 * fc
		if x != 0 {
			sinc = math.Sin(2*math.Pi*fc*x) / (math.Pi * x)
		}
		r := x / (float64(n) / 2)
		h[i] = sinc * bessel0(upsampleKaiser*math.Sqrt(1-r*r)) / bessel0(upsampleKaiser)
		sum += h[i]
	}
	// Zero-stuffing halves the energy; a DC gain of 2 restores unity gain.
	var out [2][upsamplePhaseTaps]float64
	for i := range h {
		out[i%2][i/2] = h[i] * 2 / sum
	}
	return out
}

func bessel0(x float64) float64 {
	sum, term := 1.0, 1.0
	for k := 1; k < 50; k++ {
		term *= (x / (2 * float64(k))) * (x / (2 * float64(k)))
		sum += term
	}
	return sum
}

// Upsampler2x converts a 24 kHz mono int16 stream to 48 kHz. It keeps the
// FIR history between calls, so chunk boundaries are inaudible and output
// does not depend on how the input is split. Not safe for concurrent use.
type Upsampler2x struct {
	hist [upsamplePhaseTaps]int16 // oldest..newest previous inputs
}

func NewUpsampler2x() *Upsampler2x { return &Upsampler2x{} }

// Process appends 2*len(in) samples to out and returns it.
func (u *Upsampler2x) Process(in []int16, out []int16) []int16 {
	const k = upsamplePhaseTaps
	for m := range in {
		var acc [2]float64
		for j := 0; j < k; j++ {
			idx := m - j
			var x int16
			if idx >= 0 {
				x = in[idx]
			} else {
				x = u.hist[k+idx]
			}
			v := float64(x)
			acc[0] += upsampleCoeffs[0][j] * v
			acc[1] += upsampleCoeffs[1][j] * v
		}
		out = append(out, saturate16(acc[0]), saturate16(acc[1]))
	}
	if len(in) >= k {
		copy(u.hist[:], in[len(in)-k:])
	} else {
		copy(u.hist[:], u.hist[len(in):])
		copy(u.hist[k-len(in):], in)
	}
	return out
}

// saturate16 clamps instead of wrapping: the FIR overshoots on full-scale
// edges and a wrapped sample is a loud click.
func saturate16(v float64) int16 {
	v = math.Round(v)
	if v > math.MaxInt16 {
		return math.MaxInt16
	}
	if v < math.MinInt16 {
		return math.MinInt16
	}
	return int16(v)
}
