package sseutil

import (
	"bytes"
	"io"
	"testing"
	"time"
)

func TestIdleTimeoutReader_PassesData(t *testing.T) {
	itr := NewIdleTimeoutReader(bytes.NewReader([]byte("hello")), 50*time.Millisecond)
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
	// After a timeout the underlying Read is still in flight. When it finally
	// returns it must write into the reader's private buffer, never the caller's
	// p — otherwise it races with the caller reusing p. Run with -race to catch
	// a regression.
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
