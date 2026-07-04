package tool

import (
	"fmt"
	"slices"
	"sync"
	"testing"
)

func TestBashState_SeedAndDrop(t *testing.T) {
	st := NewBashState()
	st.Update("parent", "/work", []string{"A=1", "B=2"})

	// Seed copies cwd+env from the parent.
	st.Seed("child", "parent")
	cwd, env := st.Snapshot("child")
	if cwd != "/work" {
		t.Fatalf("child cwd = %q, want /work", cwd)
	}
	if !slices.Equal(env, []string{"A=1", "B=2"}) {
		t.Fatalf("child env = %v, want [A=1 B=2]", env)
	}

	// Child changes must not propagate back to the parent (subshell semantics).
	st.Update("child", "/other", []string{"A=9"})
	if pcwd, _ := st.Snapshot("parent"); pcwd != "/work" {
		t.Fatalf("parent cwd mutated to %q, want /work", pcwd)
	}

	// Seeding from an unknown parent is a no-op.
	st.Seed("orphan", "missing")
	if c, e := st.Snapshot("orphan"); c != "" || e != nil {
		t.Fatalf("orphan snapshot = (%q, %v), want (\"\", nil)", c, e)
	}

	// Drop removes the entry.
	st.Drop("child")
	if c, e := st.Snapshot("child"); c != "" || e != nil {
		t.Fatalf("dropped child snapshot = (%q, %v), want (\"\", nil)", c, e)
	}
}

func TestBashState_SnapshotReturnsCopy(t *testing.T) {
	st := NewBashState()
	st.Update("a", "/x", []string{"K=V"})

	_, env := st.Snapshot("a")
	env[0] = "MUTATED=1" // caller mutation must not affect stored state

	_, env2 := st.Snapshot("a")
	if env2[0] != "K=V" {
		t.Fatalf("stored env corrupted by caller mutation: %v", env2)
	}
}

func TestBashState_ConcurrentSnapshotUpdate(t *testing.T) {
	// Run under -race: concurrent readers/writers across distinct and shared
	// agent IDs must not race.
	st := NewBashState()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := fmt.Sprintf("agent-%d", n%5)
			st.Update(id, "/tmp", []string{fmt.Sprintf("K=%d", n)})
			_, _ = st.Snapshot(id)
			child := fmt.Sprintf("child-%d", n)
			st.Seed(child, id)
			_, _ = st.Snapshot(child)
			st.Drop(child)
		}(i)
	}
	wg.Wait()
}

func TestParseNullSepEnv_FiltersDenylistAndMalformed(t *testing.T) {
	// PWD/OLDPWD/SHLVL/_ are regenerated; BASH_ENV/ENV would run code on the next
	// call before the trap installs; BASH_FUNC_* are exported functions that could
	// shadow the trap's own commands. All must be stripped; PATH/FOO survive.
	raw := []byte("PATH=/bin\x00PWD=/x\x00OLDPWD=/y\x00SHLVL=2\x00_=/usr/bin/env\x00" +
		"BASH_ENV=/tmp/evil.sh\x00ENV=/tmp/evil2.sh\x00BASH_FUNC_pwd%%=() {  echo hi\n}\x00FOO=bar\x00NOEQ\x00=noval\x00\x00")
	got := parseNullSepEnv(raw)
	want := []string{"PATH=/bin", "FOO=bar"}
	if !slices.Equal(got, want) {
		t.Fatalf("parseNullSepEnv = %v, want %v", got, want)
	}
}
