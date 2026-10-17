package core

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

type fakeT struct {
	text  string
	err   error
	calls int
	got   string
}

func (f *fakeT) Transcribe(_ context.Context, a io.Reader, _ string, _ TranscribeOptions) (string, error) {
	f.calls++
	b, _ := io.ReadAll(a)
	f.got = string(b)
	return f.text, f.err
}

func TestFallbackTranscriber(t *testing.T) {
	t.Run("plan ok skips the key", func(t *testing.T) {
		p, k := &fakeT{text: "plan"}, &fakeT{text: "key"}
		got, err := (&FallbackTranscriber{p, k}).Transcribe(context.Background(), strings.NewReader("x"), "a.wav", TranscribeOptions{})
		if err != nil || got != "plan" || k.calls != 0 {
			t.Fatalf("%q %v %d", got, err, k.calls)
		}
	})
	for _, name := range []string{"network", "http 500", "http 403", "http 429", "format"} {
		t.Run("fallback on "+name, func(t *testing.T) {
			p, k := &fakeT{err: errors.New("plan transcribe: " + name)}, &fakeT{text: "key"}
			got, err := (&FallbackTranscriber{p, k}).Transcribe(context.Background(), bytes.NewReader([]byte("audio")), "a.wav", TranscribeOptions{})
			if err != nil || got != "key" || k.got != "audio" {
				t.Fatalf("%q %v %q", got, err, k.got)
			}
		})
	}
	t.Run("both fail returns the key error", func(t *testing.T) {
		p, k := &fakeT{err: errors.New("p")}, &fakeT{err: errors.New("k")}
		_, err := (&FallbackTranscriber{p, k}).Transcribe(context.Background(), strings.NewReader("x"), "a.wav", TranscribeOptions{})
		if err == nil || err.Error() != "k" {
			t.Fatal(err)
		}
	})
	t.Run("no OAuth uses only the key", func(t *testing.T) {
		k := &fakeT{text: "key"}
		got, _ := (&FallbackTranscriber{Fallback: k}).Transcribe(context.Background(), strings.NewReader("x"), "a.wav", TranscribeOptions{})
		if got != "key" {
			t.Fatal(got)
		}
	})
	t.Run("plan only without key reports the plan error", func(t *testing.T) {
		p := &fakeT{err: errors.New("p")}
		if _, err := (&FallbackTranscriber{Primary: p}).Transcribe(context.Background(), strings.NewReader("x"), "a.wav", TranscribeOptions{}); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("cancelled request does not spend the key", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		p, k := &fakeT{err: errors.New("p")}, &fakeT{text: "key"}
		_, err := (&FallbackTranscriber{p, k}).Transcribe(ctx, strings.NewReader("x"), "a.wav", TranscribeOptions{})
		if err == nil || k.calls != 0 {
			t.Fatal("key used")
		}
	})
}
