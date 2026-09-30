package session

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FindByMetadata returns the saved sessions whose metadata holds key with the
// string value, reading every project store under baseDir straight from disk.
//
// It is meant for callers that must not create a second copy of something a
// session already stands for, so an incomplete answer is an error rather than
// a shorter list: a directory that cannot be read, or a session file that
// cannot be decoded yet mentions key, makes it fail. A damaged file that does
// not mention key cannot be the one looked for and is skipped, as List does.
func FindByMetadata(baseDir, key, value string) ([]Summary, error) {
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
				raw, rerr := os.ReadFile(path)
				switch {
				case rerr != nil && !os.IsNotExist(rerr):
					errs = append(errs, fmt.Errorf("%s: %w", path, rerr))
				case rerr == nil && bytes.Contains(raw, []byte(`"`+key+`"`)):
					errs = append(errs, fmt.Errorf("%s: unreadable session mentions %s", path, key))
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
