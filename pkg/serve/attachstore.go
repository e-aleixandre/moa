package serve

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

// sessionIDPattern validates session ids used to build attachment directory
// names, preventing path traversal.
var sessionIDPattern = regexp.MustCompile(`^[a-f0-9]{8,64}$`)

// attachmentsBaseDir returns the base directory under which per-session
// attachment directories are created. It honors MOA_ATTACHMENTS_DIR when
// set, defaulting to /tmp/moa otherwise.
func attachmentsBaseDir() string {
	if dir := os.Getenv("MOA_ATTACHMENTS_DIR"); dir != "" {
		return dir
	}
	return "/tmp/moa"
}

// sessionAttachDir validates id and returns the path of its attachment
// directory, without creating it.
func sessionAttachDir(id string) (string, error) {
	if !sessionIDPattern.MatchString(id) {
		return "", fmt.Errorf("invalid session id: %q", id)
	}
	return filepath.Join(attachmentsBaseDir(), id), nil
}

// ensureBaseDir creates the attachments base directory if needed, refusing to
// operate through a symlink, a non-directory, a dir not owned by the current
// user, or one with group/other write bits. This hardens against the default
// base (/tmp/moa) being pre-created by another local process with lax perms.
func ensureBaseDir() (string, error) {
	base := attachmentsBaseDir()
	info, err := os.Lstat(base)
	if err != nil {
		if os.IsNotExist(err) {
			if err := os.MkdirAll(base, 0o700); err != nil {
				return "", err
			}
			// Re-stat what we created and validate it below (a racing actor
			// could have won the create; validation catches an unsafe result).
			info, err = os.Lstat(base)
			if err != nil {
				return "", err
			}
		} else {
			return "", err
		}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("attachments base dir %q is a symlink", base)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("attachments base dir %q is not a directory", base)
	}
	// Reject a base dir owned by a different user (another local account could
	// tamper with attachment storage). Ownership can't be fixed by chmod.
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		if int(st.Uid) != os.Getuid() {
			return "", fmt.Errorf("attachments base dir %q is not owned by the current user", base)
		}
	}
	// Tighten to 0700, then verify the perms actually took — this closes a
	// pre-created-with-lax-perms base dir (group/other access) rather than
	// silently trusting it.
	if err := os.Chmod(base, 0o700); err != nil {
		return "", fmt.Errorf("attachments base dir %q: cannot secure permissions: %w", base, err)
	}
	if info2, serr := os.Lstat(base); serr != nil {
		return "", serr
	} else if info2.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("attachments base dir %q still has group/other access after chmod (mode %o)", base, info2.Mode().Perm())
	}
	return base, nil
}

// ensureSessionAttachDir ensures the base dir and the session's attachment
// directory exist, returning the session directory path.
func ensureSessionAttachDir(id string) (string, error) {
	if _, err := ensureBaseDir(); err != nil {
		return "", err
	}
	dir, err := sessionAttachDir(id)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	// Refuse to operate through a symlinked session dir: everything must stay
	// physically under the base dir (defense-in-depth beyond the id allowlist).
	if info, lerr := os.Lstat(dir); lerr != nil {
		return "", lerr
	} else if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("session attachment dir %q is a symlink", dir)
	} else if !info.IsDir() {
		return "", fmt.Errorf("session attachment dir %q is not a directory", dir)
	}
	return dir, nil
}

// removeSessionAttachDir removes a session's attachment directory safely: it
// validates the id, refuses to follow a symlinked base dir or a symlinked
// session dir (removing the link itself, never its target), and only then
// removes the real directory tree. Best-effort — returns nil if there is
// nothing to remove. This is the ONLY sanctioned way to delete an attachment
// dir; callers must not os.RemoveAll a client-influenced path directly.
func removeSessionAttachDir(id string) error {
	// Base dir must exist and not be a symlink (never delete through a symlink
	// that could point outside our tree).
	base := attachmentsBaseDir()
	if bi, err := os.Lstat(base); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	} else if bi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("attachments base dir %q is a symlink; refusing to remove", base)
	}
	dir, err := sessionAttachDir(id)
	if err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		// The session path is itself a symlink: remove only the link, never
		// follow it into an arbitrary target with RemoveAll.
		return os.Remove(dir)
	}
	if !info.IsDir() {
		return os.Remove(dir)
	}
	return os.RemoveAll(dir)
}

// safeBase sanitizes an untrusted client-provided filename into a safe
// basename. It returns "" if no safe name could be derived, leaving the
// fallback name choice to the caller.
func safeBase(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base(filepath.Clean(name))

	var b strings.Builder
	for _, r := range name {
		if r == 0 || (r < 0x20) || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	name = strings.TrimSpace(b.String())

	if name == "" || name == "." || name == ".." {
		return ""
	}
	// After Base/Clean a separator should not survive, but guard defensively:
	// a bare "/" (filepath.Base("/") == "/") or any residual separator is not a
	// usable basename.
	if strings.ContainsRune(name, '/') {
		return ""
	}

	const maxLen = 200
	if len(name) > maxLen {
		ext := filepath.Ext(name)
		if len(ext) >= maxLen {
			name = truncateRunes(name, maxLen)
		} else {
			stem := strings.TrimSuffix(name, ext)
			stem = truncateRunes(stem, maxLen-len(ext))
			name = stem + ext
		}
	}
	return name
}

// truncateRunes returns s truncated to at most maxBytes bytes without splitting
// a multi-byte UTF-8 rune.
func truncateRunes(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	end := 0
	for i := range s { // range over a string yields rune-start byte indices
		if i > maxBytes {
			break
		}
		end = i
	}
	return s[:end]
}

// writeUnique writes data to a new file inside dir, deriving a safe unique
// name from name. It is TOCTOU-safe: file creation uses O_EXCL (never
// overwrite) and O_NOFOLLOW (never follow a symlink planted at the leaf path
// by a racing same-UID process), retrying with a numeric suffix on collision.
func writeUnique(dir, name string, data []byte) (finalPath string, err error) {
	sanitized := safeBase(name)
	if sanitized == "" {
		sanitized = "attachment"
	}
	ext := filepath.Ext(sanitized)
	stem := strings.TrimSuffix(sanitized, ext)

	const maxAttempts = 10000
	for i := 0; i < maxAttempts; i++ {
		candidate := sanitized
		if i > 0 {
			candidate = fmt.Sprintf("%s-%d%s", stem, i+1, ext)
		}
		p := filepath.Join(dir, candidate)

		f, ferr := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
		if errors.Is(ferr, os.ErrExist) {
			continue
		}
		if ferr != nil {
			return "", ferr
		}

		_, werr := f.Write(data)
		cerr := f.Close()
		if werr != nil {
			_ = os.Remove(p)
			return "", werr
		}
		if cerr != nil {
			_ = os.Remove(p)
			return "", cerr
		}
		return p, nil
	}
	return "", fmt.Errorf("writeUnique: exceeded %d attempts for %q in %q", maxAttempts, name, dir)
}
