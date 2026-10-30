package serve

import (
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/voice"
)

func TestPhonicRecheckTranscriptLimitEndsRealController(t *testing.T) {
	for _, kind := range []string{"single", "cumulative"} {
		t.Run(kind, func(t *testing.T) {
			limits := vpDefaultLimits()
			limits.grace = 3 * time.Second
			f := vpNewFixtureWithLimits(t, "grok-plan", limits)
			phonicKey(t, f)
			phonicSwitch(t)
			c := phonicCreate(t, f, "", 201)
			call := f.dial(c, f.owner(), true)
			call.conn.SetReadLimit(4 << 20)
			call.await("listening", func(v map[string]any) bool { return v["type"] == "state" && v["state"] == "listening" })
			p := f.up.peer(t, "phonic")
			chunks := []string{strings.Repeat("x", voice.MaxTranscriptBytes+1)}
			if kind == "cumulative" {
				chunks = []string{strings.Repeat("x", voice.MaxTranscriptBytes), "y"}
			}
			for _, chunk := range chunks {
				p.emit(t, map[string]any{"type": "audio_chunk", "audio": "", "text": chunk})
			}
			ended := call.await("transcript limit ending", func(v map[string]any) bool { return v["type"] == "ended" })
			if ended["reason"] != "transcript_limit" {
				t.Fatalf("ended with %v instead of transcript_limit", ended["reason"])
			}
			vpClosed(t, p)
		})
	}
}
