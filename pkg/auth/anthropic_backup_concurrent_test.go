package auth

import (
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

func TestAnthropicBackupLateOldPrimarySuccessCannotClearCurrentCredit(t *testing.T) {
	oldP := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(oldP)
	s, path, _ := backupFixture(t)
	old := saveBackup(t, s).Revision
	other := NewStore(path)
	done := make(chan struct{})
	// Park reload after its disk read: the old result must validate its
	// captured map, not a new selection read after the replacement.
	s.mu.Lock()
	go func() { s.RecordAnthropicBackupUse(old, nil); close(done) }()
	runtime.Gosched()
	stack := make([]byte, 1<<20)
	stack = stack[:runtime.Stack(stack, true)]
	parked := false
	for _, block := range strings.Split(string(stack), "\n\n") {
		if strings.Contains(block, ".RecordAnthropicBackupUse(") && strings.Contains(block, ".reload(") && strings.Contains(block, "[sync.Mutex.Lock]") {
			parked = true
		}
	}
	if !parked {
		s.mu.Unlock()
		<-done
		t.Fatal("old public result did not park in reload: scheduling witness invalid")
	}
	nextPrimary, err := other.CommitLogin("anthropic", old.Primary, Credential{Type: "oauth", Access: "fake-next-oauth", Refresh: "fake-next-refresh", Expires: time.Now().Add(time.Hour).UnixMilli()})
	if err != nil {
		s.mu.Unlock()
		<-done
		t.Fatal(err)
	}
	next := BackupRevision{Primary: nextPrimary, Key: old.Key}
	s.mu.Unlock()
	s.RecordAnthropicBackupUse(next, core.ErrAnthropicLowCredit)
	wasExhausted := s.AnthropicBackupCreditExhausted(next)
	<-done
	if !wasExhausted {
		t.Fatal("invalid witness: current credit refusal did not publish before old success resumed")
	}
	if !s.AnthropicBackupCreditExhausted(next) || backupStatus(t, s).State != "api_error" {
		t.Fatalf("late old-primary success erased current credit refusal: old=%+v current=%+v", old, next)
	}
}
