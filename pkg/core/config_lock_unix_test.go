//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package core

import (
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
)

func TestSaveConfigFile_ConcurrentMutations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	const mutations = 16

	start := make(chan struct{})
	errs := make(chan error, mutations)
	var wg sync.WaitGroup
	for i := range mutations {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- saveConfigFile(path, func(cfg *MoaConfig) {
				// Make an unlocked read-modify-write reliably overlap.
				time.Sleep(time.Millisecond)
				cfg.PinnedModels = UpdatePinnedModels(cfg.PinnedModels, fmt.Sprintf("model-%02d", i), true)
			})
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("saveConfigFile: %v", err)
		}
	}

	want := make([]string, mutations)
	for i := range want {
		want[i] = fmt.Sprintf("model-%02d", i)
	}
	got := loadConfigFile(path).PinnedModels
	if len(got) != mutations {
		t.Fatalf("PinnedModels = %v, want %d saved mutations", got, mutations)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("PinnedModels = %v, want %v", got, want)
	}
}
