// Package secrets stages short-lived credentials for an agent to install.
//
// Values never pass through chat input, are not persisted in the user's
// message, and Moa does not send them to the model. It is not a vault or a
// boundary against the agent: its shell runs as the same Unix user and can
// read these files. A value the agent reads or prints enters model context and
// the transcript like any other tool output.
package secrets

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	maxEntries   = 16
	maxValueSize = 16 << 10
	batchTTL     = 6 * time.Hour
	reapInterval = time.Minute
)

var (
	aliasPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	batchPattern = regexp.MustCompile(`^batch-[a-f0-9]{32}$`)
)

// Entry is one named secret to stage. Name is an alias, not a destination
// filename: the agent decides where the relevant client expects the value.
type Entry struct {
	Name  string
	Value string
}

// Stash writes one 0600 file per entry into a new random 0700 directory.
// Values are intentionally never included in errors.
func Stash(entries []Entry) (dir string, err error) {
	if err := validate(entries); err != nil {
		return "", err
	}
	base, err := ensureBaseDir()
	if err != nil {
		return "", errors.New("secret storage unavailable")
	}
	dir, err = makeBatchDir(base)
	if err != nil {
		return "", errors.New("secret storage unavailable")
	}
	defer func() {
		if err != nil {
			_ = forgetIn(base, dir)
			dir = ""
		}
	}()
	for _, entry := range entries {
		if err = writeFile(dir, entry.Name, []byte(entry.Value)); err != nil {
			return "", fmt.Errorf("could not stash secret %q", entry.Name)
		}
	}
	return dir, nil
}

// Note returns the one message the agent receives for a complete secret batch.
func Note(dir string, names []string) string {
	return fmt.Sprintf("A secret batch is available in %s (aliases: %s). Install each secret where its relevant client expects it, then delete these files. Never print a value or copy one into the repository or into a command that would echo it.", dir, strings.Join(names, ", "))
}

// Forget best-effort removes one batch. It refuses paths outside this package's
// base directory and never removes the base itself.
func Forget(dir string) error {
	return forgetIn(baseDir(), dir)
}

// Reap best-effort removes batches older than six hours. It only touches this
// package's random batch directory names, does not follow symlinks, and never
// removes the base directory itself.
func Reap() {
	base := baseDir()
	info, err := os.Lstat(base)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || !batchPattern.MatchString(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil || time.Since(info.ModTime()) <= batchTTL {
			continue
		}
		_ = forgetIn(base, filepath.Join(base, entry.Name()))
	}
}

// StartReaper removes expired batches periodically until ctx is cancelled. The
// returned channel closes after the ticker and its goroutine have stopped.
func StartReaper(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		reapPeriodically(ctx, reapInterval)
	}()
	return done
}

func reapPeriodically(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			Reap()
		}
	}
}

func validate(entries []Entry) error {
	if len(entries) == 0 {
		return errors.New("at least one secret is required")
	}
	if len(entries) > maxEntries {
		return fmt.Errorf("at most %d secrets are allowed", maxEntries)
	}
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if !aliasPattern.MatchString(entry.Name) {
			return fmt.Errorf("invalid secret alias %q", entry.Name)
		}
		key := strings.ToLower(entry.Name)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate secret alias %q", entry.Name)
		}
		seen[key] = struct{}{}
		if entry.Value == "" {
			return fmt.Errorf("secret %q has an empty value", entry.Name)
		}
		if len(entry.Value) > maxValueSize {
			return fmt.Errorf("secret %q exceeds %d bytes", entry.Name, maxValueSize)
		}
	}
	return nil
}

func baseDir() string {
	return filepath.Join(os.TempDir(), "moa-secrets-"+strconv.Itoa(os.Getuid()))
}

func ensureBaseDir() (string, error) {
	base := baseDir()
	info, err := os.Lstat(base)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", err
		}
		if err := os.MkdirAll(base, 0o700); err != nil {
			return "", err
		}
		info, err = os.Lstat(base)
		if err != nil {
			return "", err
		}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("secret base dir %q is a symlink", base)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("secret base dir %q is not a directory", base)
	}
	if err := checkBaseOwner(base, info); err != nil {
		return "", err
	}
	if err := os.Chmod(base, 0o700); err != nil {
		return "", fmt.Errorf("secret base dir %q: cannot secure permissions: %w", base, err)
	}
	info, err = os.Lstat(base)
	if err != nil {
		return "", err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("secret base dir %q still has group/other access after chmod", base)
	}
	return base, nil
}

func makeBatchDir(base string) (string, error) {
	for range 10 {
		id := make([]byte, 16)
		if _, err := rand.Read(id); err != nil {
			return "", err
		}
		dir := filepath.Join(base, "batch-"+hex.EncodeToString(id))
		if err := os.Mkdir(dir, 0o700); err == nil {
			if err := os.Chmod(dir, 0o700); err != nil {
				_ = os.Remove(dir)
				return "", err
			}
			info, err := os.Lstat(dir)
			if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm() != 0o700 {
				_ = os.Remove(dir)
				if err != nil {
					return "", err
				}
				return "", errors.New("could not secure secret batch directory")
			}
			return dir, nil
		} else if !os.IsExist(err) {
			return "", err
		}
	}
	return "", errors.New("could not create secret batch directory")
}

func writeFile(dir, name string, value []byte) error {
	tmp, err := os.CreateTemp(dir, ".tmp-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after rename
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(value); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	path := filepath.Join(dir, name)
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return errors.New("could not secure secret file")
	}
	return nil
}

func forgetIn(base, dir string) error {
	if filepath.Clean(base) != base || filepath.Dir(dir) != base || !batchPattern.MatchString(filepath.Base(dir)) {
		return errors.New("refusing to remove invalid secret batch")
	}
	baseInfo, err := os.Lstat(base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if baseInfo.Mode()&os.ModeSymlink != 0 || !baseInfo.IsDir() {
		return errors.New("refusing to remove through unsafe secret base dir")
	}
	info, err := os.Lstat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return os.Remove(dir)
	}
	return os.RemoveAll(dir)
}
