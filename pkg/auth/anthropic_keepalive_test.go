package auth

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

func TestAnthropicBackupLowCreditObservation(t *testing.T) {
	s, _, _ := backupFixture(t)
	revision := saveBackup(t, s).Revision
	credit := fmt.Errorf("wrapped upstream: %w", core.ErrAnthropicLowCredit)
	s.RecordAnthropicBackupUse(revision, credit)
	if !s.AnthropicBackupCreditExhausted(revision) || backupStatus(t, s).State != "api_error" {
		t.Fatal("precise credit observation or public generic state lost")
	}
	// The observation is not a new foreground financial gate.
	if err := s.AdmitAnthropicBackup(context.Background(), revision, func(AnthropicBackupSelection) error { return nil }); err != nil {
		t.Fatal(err)
	}
	for _, outcome := range []error{nil, errors.New("generic API error")} {
		s.RecordAnthropicBackupUse(revision, outcome)
		if s.AnthropicBackupCreditExhausted(revision) {
			t.Fatal("last non-credit result did not clear credit observation")
		}
		s.RecordAnthropicBackupUse(revision, credit)
	}
	old := revision
	if _, err := s.CommitLogin("anthropic", old.Primary, Credential{Type: "oauth", Access: "new-fake-plan", Refresh: "new-fake-refresh", Expires: time.Now().Add(time.Hour).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	revision = backupStatus(t, s).Revision
	if revision.Key != old.Key || revision.Primary == old.Primary || s.AnthropicBackupCreditExhausted(revision) {
		t.Fatal("credit observation leaked across primary generations")
	}
	s.RecordAnthropicBackupUse(revision, credit)
	s.RecordAnthropicBackupUse(old, nil)
	if !s.AnthropicBackupCreditExhausted(revision) {
		t.Fatal("stale success cleared the current pair")
	}
	if _, err := s.SaveAnthropicAPIKey(revision.Primary, "new-fake-key"); err != nil {
		t.Fatal(err)
	}
	next := backupStatus(t, s).Revision
	s.RecordAnthropicBackupUse(revision, credit)
	if next.Key == revision.Key || s.AnthropicBackupCreditExhausted(next) {
		t.Fatal("stale failure tainted a replacement key")
	}
	s.RecordAnthropicBackupUse(next, nil)
	if backupStatus(t, s).State != "active" {
		t.Fatal("successful current use did not restore public status")
	}
}

func TestAnthropicPlanResetRetainsReliableBoundary(t *testing.T) {
	s, _, _ := backupFixture(t)
	revision := saveBackup(t, s).Revision
	now := time.Now()
	if _, known := s.AnthropicPlanReset(revision); known {
		t.Fatal("invented reliable reset")
	}
	s.RememberAnthropicPlanLimit(revision, now.Add(-time.Minute))
	if until, known := s.AnthropicPlanReset(revision); !known || !until.Equal(now.Add(-time.Minute)) {
		t.Fatal("expired boundary lost")
	}
	if _, live := s.AnthropicPlanLimit(revision, now); live {
		t.Fatal("expired boundary became a live limit")
	}
	s.RememberAnthropicPlanLimit(revision, now.Add(time.Hour))
	s.RememberAnthropicPlanLimit(revision, now.Add(time.Minute))
	if until, known := s.AnthropicPlanReset(revision); !known || !until.Equal(now.Add(time.Hour)) {
		t.Fatal("later account reset was shortened")
	}
	for _, other := range []BackupRevision{{Primary: "other", Key: revision.Key}, {Primary: revision.Primary, Key: "other"}} {
		if _, known := s.AnthropicPlanReset(other); known {
			t.Fatal("reset leaked across generations")
		}
	}
}
