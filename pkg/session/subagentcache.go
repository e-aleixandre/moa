package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/e-aleixandre/moa/pkg/core"
)

const (
	subagentCacheAuditVersion = 1
	cacheAuditFileSuffix      = ".cache.json"
	// cacheAuditJobSuffix is reserved: a job named "x.cache" would save its
	// transcript to the file that holds job "x"'s cache audit.
	cacheAuditJobSuffix = ".cache"
)

// SubagentCacheAudit is the cache-diagnostics sidecar of one subagent job:
// content-free fingerprints (hashes and cache-marker positions) of its first
// and most recent provider requests, plus how many requests were observed.
// It lives apart from the transcript because the transcript is rewritten on
// every title/end update and must not erase it.
//
// Count is the number of final provider request bodies built for THIS job.
// Every resume starts a new job with its own audit file, so counts are never
// aggregated across resumes. An HTTP retry of the same body is one request; a
// fast-mode fallback that rebuilds the body is another. To judge whether a
// resume kept the cache prefix, compare the original job's Last with the
// resumed job's First (linked by ResumedFrom), not First and Last of one job.
type SubagentCacheAudit struct {
	Version     int                     `json:"version"`
	JobID       string                  `json:"job_id"`
	ResumedFrom string                  `json:"resumed_from,omitempty"`
	Count       uint64                  `json:"count"`
	First       core.RequestFingerprint `json:"first"`
	Last        core.RequestFingerprint `json:"last"`
}

func isCacheAuditFile(name string) bool { return strings.HasSuffix(name, cacheAuditFileSuffix) }

func (s *SubagentStore) cachePath(jobID string) string {
	return filepath.Join(s.dir, jobID+cacheAuditFileSuffix)
}

// RecordRequestFingerprint folds one request into the job's audit: the first
// fingerprint is kept forever, Last is replaced and Count grows. An unreadable,
// corrupt or foreign existing file is reported and left untouched rather than
// silently reset.
func (s *SubagentStore) RecordRequestFingerprint(jobID, resumedFrom string, fp core.RequestFingerprint) error {
	if err := validJobID(jobID); err != nil {
		return err
	}
	if resumedFrom != "" {
		if err := validJobID(resumedFrom); err != nil {
			return err
		}
	}
	if err := validateFingerprint(fp); err != nil {
		return err
	}

	s.auditMu.Lock()
	defer s.auditMu.Unlock()

	audit, err := s.readCacheAudit(jobID)
	switch {
	case err == nil:
		if audit.ResumedFrom != resumedFrom {
			return errors.New("session: subagent cache audit source mismatch")
		}
		audit.Count++
		audit.Last = fp
	case errors.Is(err, ErrNotFound):
		audit = &SubagentCacheAudit{Version: subagentCacheAuditVersion, JobID: jobID, ResumedFrom: resumedFrom, Count: 1, First: fp, Last: fp}
	default:
		return err
	}
	return s.writeCacheAudit(jobID, audit)
}

// LoadCacheAudit reads one job's cache audit. Returns ErrNotFound (wrapped)
// if the job has none.
func (s *SubagentStore) LoadCacheAudit(jobID string) (*SubagentCacheAudit, error) {
	if err := validJobID(jobID); err != nil {
		return nil, err
	}
	s.auditMu.Lock()
	defer s.auditMu.Unlock()
	return s.readCacheAudit(jobID)
}

func (s *SubagentStore) readCacheAudit(jobID string) (*SubagentCacheAudit, error) {
	data, err := os.ReadFile(s.cachePath(jobID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("session: subagent cache audit %q: %w", jobID, ErrNotFound)
		}
		return nil, fmt.Errorf("session: subagent cache audit read: %w", err)
	}
	var a SubagentCacheAudit
	if err := json.Unmarshal(data, &a); err != nil {
		return nil, errors.New("session: subagent cache audit is corrupt")
	}
	if a.Version != subagentCacheAuditVersion || a.JobID != jobID || a.Count == 0 {
		return nil, errors.New("session: subagent cache audit has unexpected identity")
	}
	if a.ResumedFrom != "" && validJobID(a.ResumedFrom) != nil {
		return nil, errors.New("session: subagent cache audit has invalid source")
	}
	if validateFingerprint(a.First) != nil || validateFingerprint(a.Last) != nil {
		return nil, errors.New("session: subagent cache audit has invalid fingerprint")
	}
	return &a, nil
}

func (s *SubagentStore) writeCacheAudit(jobID string, a *SubagentCacheAudit) error {
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return fmt.Errorf("session: subagent mkdir: %w", err)
	}
	f, err := os.CreateTemp(s.dir, "."+jobID+".cache-*.tmp")
	if err != nil {
		return fmt.Errorf("session: subagent cache audit write: %w", err)
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if err := encodeCompactJSON(f, a); err != nil {
		_ = f.Close()
		return fmt.Errorf("session: subagent cache audit marshal: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("session: subagent cache audit write: %w", err)
	}
	if err := os.Rename(tmp, s.cachePath(jobID)); err != nil {
		return fmt.Errorf("session: subagent cache audit rename: %w", err)
	}
	ok = true
	return nil
}

// validateFingerprint guarantees the audit can only hold digests and
// positions, whatever a provider hands in.
func validateFingerprint(fp core.RequestFingerprint) error {
	bad := errors.New("session: invalid request fingerprint")
	if !isSHA256Hex(fp.BodySHA256) || !isSHA256Hex(fp.OptionsSHA256) {
		return bad
	}
	for _, h := range []string{fp.ToolsSHA256, fp.SystemSHA256} {
		if h != "" && !isSHA256Hex(h) {
			return bad
		}
	}
	for _, p := range fp.Prefixes {
		if !validSection(p.Section) || p.Message < -1 || p.Block < 0 || !isSHA256Hex(p.SHA256) {
			return bad
		}
	}
	for _, b := range fp.Breakpoints {
		if !validSection(b.Section) || b.Message < -1 || b.Block < 0 {
			return bad
		}
		switch b.TTL {
		case "5m", "1h", "other":
		default:
			return bad
		}
	}
	return nil
}

func validSection(s string) bool { return s == "tools" || s == "system" || s == "messages" }

func isSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
