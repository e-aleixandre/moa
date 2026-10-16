package voice

import (
	"math"
	"testing"
)

func tone(n int, hz, rate, amp float64) []int16 {
	s := make([]int16, n)
	for i := range s {
		s[i] = int16(amp * math.Sin(2*math.Pi*hz*float64(i)/rate))
	}
	return s
}

// goertzel returns the amplitude of the hz component of s sampled at rate.
func goertzel(s []int16, hz, rate float64) float64 {
	w := 2 * math.Pi * hz / rate
	c := 2 * math.Cos(w)
	var s1, s2 float64
	for _, v := range s {
		s0 := float64(v) + c*s1 - s2
		s2, s1 = s1, s0
	}
	re, im := s1-s2*math.Cos(w), s2*math.Sin(w)
	return 2 * math.Hypot(re, im) / float64(len(s))
}

func TestUpsampler2xImageRejection(t *testing.T) {
	out := NewUpsampler2x().Process(tone(24000, 5000, 24000, 10000), nil)
	out = out[2000:] // skip the filter warm-up
	sig, img := goertzel(out, 5000, 48000), goertzel(out, 19000, 48000)
	db := 20 * math.Log10(img/sig)
	t.Logf("tone=%.1f image19k=%.4f rejection=%.1f dB", sig, img, -db)
	if db > -40 {
		t.Fatalf("image only %.1f dB below the tone", -db)
	}
}

func TestUpsampler2xChunkedEqualsBlock(t *testing.T) {
	in := append(tone(4800, 1234, 24000, 9000), tone(4800, 7777, 24000, 12000)...)
	whole := NewUpsampler2x().Process(in, nil)
	u := NewUpsampler2x()
	var chunked []int16
	for i := 0; i < len(in); i += 480 {
		chunked = u.Process(in[i:i+480], chunked)
	}
	// Odd chunk sizes (including shorter than the history) must also match.
	u = NewUpsampler2x()
	var ragged []int16
	for i := 0; i < len(in); {
		n := min(1+i%97, len(in)-i)
		ragged = u.Process(in[i:i+n], ragged)
		i += n
	}
	for name, got := range map[string][]int16{"480": chunked, "ragged": ragged} {
		if len(got) != len(whole) {
			t.Fatalf("%s: length %d != %d", name, len(got), len(whole))
		}
		for i := range got {
			if got[i] != whole[i] {
				t.Fatalf("%s: sample %d differs: %d != %d", name, i, got[i], whole[i])
			}
		}
	}
}

func TestUpsampler2xPassbandFlatness(t *testing.T) {
	for _, hz := range []float64{300, 1000, 4000, 8000} {
		out := NewUpsampler2x().Process(tone(24000, hz, 24000, 10000), nil)[2000:]
		db := 20 * math.Log10(goertzel(out, hz, 48000)/10000)
		t.Logf("%.0f Hz gain %.3f dB", hz, db)
		if math.Abs(db) > 0.5 {
			t.Fatalf("%.0f Hz gain %.2f dB", hz, db)
		}
	}
}

func TestUpsampler2xSaturates(t *testing.T) {
	in := make([]int16, 400)
	for i := range in {
		in[i] = math.MaxInt16
		if i/20%2 == 1 {
			in[i] = math.MinInt16
		}
	}
	out := NewUpsampler2x().Process(in, nil)
	var hi, lo bool
	for i := 40; i < len(out); i++ {
		// A wrapped overshoot would flip sign right next to a full-scale run.
		prev := out[i-1]
		if prev > 30000 && out[i] < -30000 && in[i/2] > 0 {
			t.Fatalf("sample %d wrapped: %d after %d", i, out[i], prev)
		}
		hi = hi || out[i] == math.MaxInt16
		lo = lo || out[i] == math.MinInt16
	}
	if !hi || !lo {
		t.Fatalf("full-scale square wave never reached the rails (hi=%v lo=%v)", hi, lo)
	}
	if got := saturate16(1e9); got != math.MaxInt16 {
		t.Fatalf("saturate16(1e9)=%d", got)
	}
	if got := saturate16(-1e9); got != math.MinInt16 {
		t.Fatalf("saturate16(-1e9)=%d", got)
	}
}
