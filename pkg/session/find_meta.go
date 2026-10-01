package session

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FindByMetadata returns the saved sessions whose metadata holds key with the
// string value, reading every project store under baseDir straight from disk.
//
// It is meant for callers that must not create a second copy of something a
// session already stands for, so an incomplete answer is an error rather than
// a shorter list: a directory that cannot be read, or a session file that
// cannot be decoded yet mentions key, makes it fail. A damaged file whose
// header reached the disk whole (its bytes go on to the transcript) and does
// not mention key cannot be the one looked for and is skipped, as List does;
// one cut short before that may have lost the key, so it fails too.
//
// A damaged file last modified before since is skipped: whatever wrote it
// did so before the thing looked for could exist. The zero since skips none.
func FindByMetadata(baseDir, key, value string, since time.Time) ([]Summary, error) {
	if baseDir == "" {
		var err error
		baseDir, err = defaultBaseDir()
		if err != nil {
			return nil, err
		}
	}
	dirs, err := os.ReadDir(baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Summary
	var errs []error
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		dir := filepath.Join(baseDir, d.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", d.Name(), err))
			continue
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
				continue
			}
			path := filepath.Join(dir, f.Name())
			sum, err := readSummary(path)
			if err != nil || sum.ID == "" {
				if info, ierr := f.Info(); ierr == nil && info.ModTime().Before(since) {
					continue
				}
				raw, rerr := os.ReadFile(path)
				switch {
				case rerr != nil && !os.IsNotExist(rerr):
					errs = append(errs, fmt.Errorf("%s: %w", path, rerr))
				case rerr == nil && bytes.Contains(raw, []byte(`"`+key+`"`)):
					errs = append(errs, fmt.Errorf("%s: unreadable session mentions %s", path, key))
				case rerr == nil && !reachesHistory(raw):
					errs = append(errs, fmt.Errorf("%s: incomplete session header", path))
				}
				continue
			}
			if v, _ := sum.Metadata[key].(string); v == value {
				out = append(out, sum)
			}
		}
	}
	return out, errors.Join(errs...)
}

// reachesHistory reports whether raw goes on past the header to a
// conversation history key, so a key missing from it is truly absent.
func reachesHistory(raw []byte) bool {
	for k := range heavySessionFields {
		if bytes.Contains(raw, []byte(`"`+k+`"`)) {
			return true
		}
	}
	return false
}
