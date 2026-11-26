package sseutil

import (
	"bytes"
	"io"
	"testing"
	"time"
)

func TestIdleTimeoutReader_PassesData(t *testing.T) {
	itr := NewIdleTimeoutReader(io.NopCloser(bytes.NewReader([]byte("hello"))), 50*time.Millisecond)
	got, err := io.ReadAll(itr)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

func TestIdleTimeoutReader_Timeout(t *testing.T) {
	// A pipe with no writer never produces data → the reader must time out.
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	itr := NewIdleTimeoutReader(pr, 20*time.Millisecond)
	if _, err := itr.Read(make([]byte, 8)); err != ErrIdleTimeout {
		t.Fatalf("err = %v, want ErrIdleTimeout", err)
	}
}

func TestIdleTimeoutReader_LateReadDoesNotTouchCallerBuffer(t *testing.T) {
	// A timeout closes the body, which ends the underlying Read before Read
	// returns, so nothing in flight can write into the caller's p once the
	// caller reuses it. Data written to the body afterwards must not land in p.
	// Run with -race to catch a regression.
	pr, pw := io.Pipe()
	itr := NewIdleTimeoutReader(pr, 20*time.Millisecond)

	p := make([]byte, 8)
	if _, err := itr.Read(p); err != ErrIdleTimeout {
		t.Fatalf("err = %v, want ErrIdleTimeout", err)
	}

	// Caller moves on and reuses p.
	for i := range p {
		p[i] = 'x'
	}

	// Unblock the underlying reader; its late data must not land in p.
	go func() {
		_, _ = pw.Write([]byte("late"))
		_ = pw.Close()
	}()
	time.Sleep(30 * time.Millisecond)

	for _, b := range p {
		if b != 'x' {
			t.Fatalf("caller buffer was written by the late read: %q", p)
		}
	}
}

// endlessReader fills p without allocating, standing in for a body that keeps
// streaming.
type endlessReader struct{}

func (endlessReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	return len(p), nil
}

func (endlessReader) Close() error { return nil }

func TestIdleTimeoutReader_ReadDoesNotAllocate(t *testing.T) {
	itr := NewIdleTimeoutReader(endlessReader{}, time.Minute)
	p := make([]byte, 64*1024)
	allocs := testing.AllocsPerRun(100, func() {
		if _, err := itr.Read(p); err != nil {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Fatalf("Read allocates %.0f times per call, want 0", allocs)
	}
}

func BenchmarkIdleTimeoutReader_Read(b *testing.B) {
	itr := NewIdleTimeoutReader(endlessReader{}, time.Minute)
	p := make([]byte, 1024*1024)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := itr.Read(p); err != nil {
			b.Fatal(err)
		}
	}
}

// Data keeps the stream alive: an SSE ping every few milliseconds must reset
// the idle timer, so a stream longer than the timeout survives (#31).
func TestIdleTimeoutReader_DataResetsTimer(t *testing.T) {
	pr, pw := io.Pipe()
	go func() {
		for i := 0; i < 10; i++ {
			time.Sleep(10 * time.Millisecond)
			if _, err := pw.Write([]byte(": ping\n\n")); err != nil {
				return
			}
		}
		_ = pw.Close()
	}()
	itr := NewIdleTimeoutReader(pr, 40*time.Millisecond)
	if _, err := io.ReadAll(itr); err != nil {
		t.Fatalf("ReadAll over 100ms of pings with a 40ms timeout: %v", err)
	}
}

// On timeout the body is closed, which unblocks the in-flight Read, and every
// later Read reports the timeout without touching the closed body.
func TestIdleTimeoutReader_TimeoutClosesBodyAndSticks(t *testing.T) {
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	itr := NewIdleTimeoutReader(pr, 20*time.Millisecond)
	if _, err := itr.Read(make([]byte, 8)); err != ErrIdleTimeout {
		t.Fatalf("err = %v, want ErrIdleTimeout", err)
	}
	if _, err := pw.Write([]byte("x")); err != io.ErrClosedPipe {
		t.Fatalf("body still open after the timeout: write err = %v", err)
	}
	if _, err := itr.Read(make([]byte, 8)); err != ErrIdleTimeout {
		t.Fatalf("second Read err = %v, want ErrIdleTimeout", err)
	}
}

// The timer only runs inside Read: time the consumer spends between reads is
// not idleness of the connection.
func TestIdleTimeoutReader_TimerStopsBetweenReads(t *testing.T) {
	itr := NewIdleTimeoutReader(endlessReader{}, 20*time.Millisecond)
	p := make([]byte, 8)
	if _, err := itr.Read(p); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, err := itr.Read(p); err != nil {
		t.Fatalf("Read after a slow consumer: %v", err)
	}
}
