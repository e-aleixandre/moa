package tool

import (
	"os"
	"path/filepath"
)

// atomicWriteFile writes data to path atomically: temp file in the same
// directory (rename is only atomic within a filesystem), fsync, chmod to
// perm, then rename over path. The temp file is removed on any error.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".moa-write-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

// fileModeOr returns the permission bits of path, or def if it can't be
// stat'ed (e.g. the file doesn't exist yet).
func fileModeOr(path string, def os.FileMode) os.FileMode {
	info, err := os.Stat(path)
	if err != nil {
		return def
	}
	return info.Mode().Perm()
}
