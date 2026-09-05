//go:build unix

package serve

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A FIFO planted at the artifact path must be refused without blocking: the
// open uses O_NONBLOCK, so no writer is needed for this to return.
func TestOpenArtifactPath_FIFODoesNotBlock(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		f, _, err := openArtifactPath(fifo)
		if err == nil {
			_ = f.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a FIFO was served as an artifact")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("openArtifactPath blocked on a FIFO")
	}
}
