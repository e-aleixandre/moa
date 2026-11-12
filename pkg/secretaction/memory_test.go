//go:build unix

package secretaction

import (
	"bytes"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

const memTestValue = "Remembered-Test-9d41e2"

var (
	keyPass = MemoryKey{Alias: "vault-agents", Kind: FieldPassword}
	keyUser = MemoryKey{Alias: "vault-agents", Kind: FieldUsername}
	keySudo = MemoryKey{Alias: "sudo-prod", Kind: FieldPassword}
)

func remember(t *testing.T, m *Memory, d time.Duration, keys ...MemoryKey) []uint64 {
	t.Helper()
	values := make([][]byte, len(keys))
	for i, k := range keys {
		values[i] = []byte(memTestValue + "-" + k.Alias + "-" + k.Kind)
	}
	gens, _, err := m.RememberMany(keys, values, d, m.Epoch(), MemoryUse{SessionID: "s1", Command: "login"})
	if err != nil {
		t.Fatalf("RememberMany: %v", err)
	}
	return gens
}

func TestRememberableNeedsAnAliasOnEveryField(t *testing.T) {
	a := Field{Kind: FieldPassword, Alias: "vault-agents"}
	cases := []struct {
		name string
		req  Request
		want bool
	}{
		{"one aliased field", Request{Kind: KindCommand, Fields: []Field{a}}, true},
		{"two aliased fields", Request{Kind: KindCommand, Fields: []Field{a, {Kind: FieldUsername, Alias: "x"}}}, true},
		{"a field without alias", Request{Kind: KindCommand, Fields: []Field{a, {Kind: FieldPassword}}}, false},
		{"browser", Request{Kind: KindBrowser, Fields: []Field{a}}, false},
		{"no fields", Request{Kind: KindCommand}, false},
		{"bad kind", Request{Kind: KindCommand, Fields: []Field{{Kind: "pin", Alias: "x"}}}, false},
	}
	for _, c := range cases {
		if got := Rememberable(c.req); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestTakeAllNeedsEveryKey(t *testing.T) {
	m := NewMemory()
	if _, _, _, ok := m.TakeAll([]MemoryKey{keyPass}, MemoryUse{}); ok {
		t.Fatal("TakeAll on empty memory")
	}
	remember(t, m, time.Hour, keyPass)
	if _, _, _, ok := m.TakeAll([]MemoryKey{keyPass, keySudo}, MemoryUse{}); ok {
		t.Fatal("partial coverage answered")
	}
	if _, _, _, ok := m.TakeAll([]MemoryKey{keyUser}, MemoryUse{}); ok {
		t.Fatal("same alias, other kind answered")
	}
	vals, gens, until, ok := m.TakeAll([]MemoryKey{keyPass}, MemoryUse{SessionID: "s2", Command: "c", Cwd: "/w"})
	if !ok || string(vals[0]) != memTestValue+"-vault-agents-password" || gens[0] == 0 || time.Until(until) < 59*time.Minute {
		t.Fatalf("TakeAll = %q %v %v %v", vals, gens, until, ok)
	}
	Wipe(vals[0])
	again, _, _, _ := m.TakeAll([]MemoryKey{keyPass}, MemoryUse{})
	if string(again[0]) != memTestValue+"-vault-agents-password" {
		t.Fatal("wiping a copy changed the remembered value")
	}
	st := m.Status()
	if len(st.Entries) != 1 || st.Entries[0].Uses != 2 || len(st.Uses) != 3 || st.Uses[1].Kind != UseRun || st.Uses[1].Cwd != "/w" {
		t.Fatalf("status = %+v", st)
	}
}

func TestStatusNeverCarriesAValue(t *testing.T) {
	m := NewMemory()
	remember(t, m, time.Hour, keyPass, keyUser)
	m.TakeAll([]MemoryKey{keyPass, keyUser}, MemoryUse{})
	b, _ := json.Marshal(m.Status())
	if bytes.Contains(b, []byte(memTestValue)) {
		t.Fatalf("status JSON has a value: %s", b)
	}
}

func TestRememberValidation(t *testing.T) {
	m := NewMemory()
	for _, d := range []time.Duration{0, -time.Hour, MaxHoldHours*time.Hour + time.Second} {
		if _, _, err := m.RememberMany([]MemoryKey{keyPass}, [][]byte{[]byte("x")}, d, m.Epoch(), MemoryUse{}); !errors.Is(err, ErrHoldDuration) {
			t.Errorf("d=%v: %v", d, err)
		}
	}
	if _, _, err := m.RememberMany([]MemoryKey{keyPass}, [][]byte{bytes.Repeat([]byte("a"), MaxHoldValueBytes+1)}, time.Hour, m.Epoch(), MemoryUse{}); !errors.Is(err, ErrHoldValue) {
		t.Errorf("too long: %v", err)
	}
	if _, _, err := m.RememberMany([]MemoryKey{keyPass, keyPass}, [][]byte{[]byte("a"), []byte("b")}, time.Hour, m.Epoch(), MemoryUse{}); !errors.Is(err, ErrHoldConflict) {
		t.Errorf("conflict: %v", err)
	}
	if len(m.Status().Entries) != 0 {
		t.Fatal("a rejected batch was remembered")
	}
	gens, _, err := m.RememberMany([]MemoryKey{keyPass, keyPass}, [][]byte{[]byte("a"), []byte("a")}, MaxHoldHours*time.Hour, m.Epoch(), MemoryUse{})
	if err != nil || gens[0] != gens[1] {
		t.Fatalf("same key twice, same value: %v %v", gens, err)
	}
}

func TestRememberIsAllOrNothing(t *testing.T) {
	m := NewMemory()
	remember(t, m, time.Hour, keySudo)
	calls := 0
	allocLocked = func(v []byte) (*lockedBuffer, error) {
		calls++
		if calls == 2 {
			return nil, errors.New("mlock failed")
		}
		return newLockedBuffer(v)
	}
	defer func() { allocLocked = newLockedBuffer }()
	if _, _, err := m.RememberMany([]MemoryKey{keyPass, keySudo}, [][]byte{[]byte("p"), []byte("new")}, time.Hour, m.Epoch(), MemoryUse{}); err == nil {
		t.Fatal("expected failure")
	}
	st := m.Status()
	if len(st.Entries) != 1 || st.Entries[0].MemoryKey != keySudo {
		t.Fatalf("failed batch changed the memory: %+v", st.Entries)
	}
	v, _, _, _ := m.TakeAll([]MemoryKey{keySudo}, MemoryUse{})
	if string(v[0]) != memTestValue+"-sudo-prod-password" {
		t.Fatalf("previous value replaced: %q", v[0])
	}
}

func TestExpiry(t *testing.T) {
	m := NewMemory()
	remember(t, m, 40*time.Millisecond, keyPass)
	time.Sleep(120 * time.Millisecond)
	if _, _, _, ok := m.TakeAll([]MemoryKey{keyPass}, MemoryUse{}); ok {
		t.Fatal("answered after the deadline")
	}
	if len(m.Status().Entries) != 0 {
		t.Fatal("still listed after expiry")
	}
}

func TestDeadlineHoldsBeforeTimerFires(t *testing.T) {
	m := NewMemory()
	gens := remember(t, m, time.Hour, keyPass)
	m.mu.Lock()
	m.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	m.mu.Unlock()
	if _, _, _, ok := m.TakeAll([]MemoryKey{keyPass}, MemoryUse{}); ok {
		t.Fatal("answered past the deadline")
	}
	if ran, _ := m.Dispatch(keyPass, gens[0], func() error { return nil }); ran {
		t.Fatal("dispatched past the deadline")
	}
	m.ForgetAll()
}

func TestForgetAliasDropsEveryKindOnly(t *testing.T) {
	m := NewMemory()
	gens := remember(t, m, time.Hour, keyPass, keyUser, keySudo)
	m.Forget("vault-agents")
	if _, _, _, ok := m.TakeAll([]MemoryKey{keyPass}, MemoryUse{}); ok {
		t.Fatal("password still remembered")
	}
	if _, _, _, ok := m.TakeAll([]MemoryKey{keyUser}, MemoryUse{}); ok {
		t.Fatal("username still remembered")
	}
	if ran, _ := m.Dispatch(keyPass, gens[0], func() error { t.Fatal("typed after Forget"); return nil }); ran {
		t.Fatal("Dispatch after Forget")
	}
	if _, _, _, ok := m.TakeAll([]MemoryKey{keySudo}, MemoryUse{}); !ok {
		t.Fatal("another alias was forgotten")
	}
	m.ForgetAll()
	if len(m.Status().Entries) != 0 {
		t.Fatal("ForgetAll left entries")
	}
}

func TestForgetFencesARememberInFlight(t *testing.T) {
	m := NewMemory()
	epoch := m.Epoch() // owner approved with "remember"
	m.Forget("nothing-matching")
	if _, _, err := m.RememberMany([]MemoryKey{keyPass}, [][]byte{[]byte("x")}, time.Hour, epoch, MemoryUse{}); !errors.Is(err, ErrForgotten) {
		t.Fatalf("remember after Forget = %v", err)
	}
	if len(m.Status().Entries) != 0 {
		t.Fatal("remembered after Forget")
	}
}

func TestDispatchIsSerializedWithForget(t *testing.T) {
	m := NewMemory()
	gens := remember(t, m, time.Hour, keyPass)
	inWrite, release := make(chan struct{}), make(chan struct{})
	go func() { _, _ = m.Dispatch(keyPass, gens[0], func() error { close(inWrite); <-release; return nil }) }()
	<-inWrite
	done := make(chan struct{})
	go func() { m.ForgetAll(); close(done) }()
	select {
	case <-done:
		t.Fatal("Forget returned while a value was being typed")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	<-done
}

func TestDispatchReportsAWriteError(t *testing.T) {
	m := NewMemory()
	gens := remember(t, m, time.Hour, keyPass)
	ran, err := m.Dispatch(keyPass, gens[0], func() error { return errors.New("deadline") })
	if !ran || err == nil {
		t.Fatalf("Dispatch = %v, %v", ran, err)
	}
	m.ForgetAll()
}

func TestNoGenerationReuseAfterRecreate(t *testing.T) {
	m := NewMemory()
	old := remember(t, m, 30*time.Millisecond, keyPass)
	m.Forget("vault-agents")
	cur := remember(t, m, time.Hour, keyPass)
	if old[0] == cur[0] {
		t.Fatal("a recreated entry reused a generation")
	}
	m.AuthFailed(keyPass, old[0])
	if ran, _ := m.Dispatch(keyPass, old[0], func() error { return nil }); ran {
		t.Fatal("an old run typed into the new entry's generation")
	}
	time.Sleep(80 * time.Millisecond) // the old timer would have fired
	if _, _, _, ok := m.TakeAll([]MemoryKey{keyPass}, MemoryUse{}); !ok {
		t.Fatal("a stale timer or failure dropped the new entry")
	}
	m.AuthFailed(keyPass, cur[0])
	if _, _, _, ok := m.TakeAll([]MemoryKey{keyPass}, MemoryUse{}); ok {
		t.Fatal("rejection did not forget")
	}
}

func TestShutdownForgets(t *testing.T) {
	m := NewMemory()
	remember(t, m, time.Hour, keyPass)
	m.Shutdown()
	if _, _, _, ok := m.TakeAll([]MemoryKey{keyPass}, MemoryUse{}); ok {
		t.Fatal("remembered after shutdown")
	}
}

func TestLockedBufferRoundTripsAcrossPages(t *testing.T) {
	for _, n := range []int{1, MaxHoldValueBytes, 4096, 4097, MaxValueBytes} {
		v := bytes.Repeat([]byte("k"), n)
		b, err := newLockedBuffer(v)
		if err != nil {
			t.Fatalf("newLockedBuffer(%d): %v", n, err)
		}
		if !bytes.Equal(b.bytes(), v) {
			t.Fatalf("round trip of %d bytes differs", n)
		}
		b.destroy()
	}
}

func TestConcurrentUse(t *testing.T) {
	m := NewMemory()
	remember(t, m, time.Hour, keyPass)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if v, g, _, ok := m.TakeAll([]MemoryKey{keyPass}, MemoryUse{}); ok {
				_, _ = m.Dispatch(keyPass, g[0], func() error { return nil })
				Wipe(v[0])
			}
			if i == 10 {
				m.ForgetAll()
			}
		}(i)
	}
	wg.Wait()
	if len(m.Status().Entries) != 0 {
		t.Fatal("still remembered after ForgetAll")
	}
}

func TestResolveHoldRefusesWhatCannotBeRemembered(t *testing.T) {
	for name, req := range map[string]Request{
		"browser":  {Kind: KindBrowser, Fields: []Field{{Label: "p", Kind: FieldPassword, Alias: "site"}}},
		"no alias": {Kind: KindCommand, Fields: []Field{{Label: "p", Kind: FieldPassword}}},
	} {
		b := NewBroker("s")
		got := make(chan error, 1)
		b.SetHooks(func(r Request) { got <- b.ResolveHold(r.ID, [][]byte{[]byte("v")}, time.Hour, 0) }, nil)
		go func() { _, _ = b.Request(t.Context(), req) }()
		if err := <-got; !errors.Is(err, ErrInvalidValue) {
			t.Errorf("%s: ResolveHold = %v", name, err)
		}
	}
}
