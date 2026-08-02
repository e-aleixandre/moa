package core

import "io"

// AcquireFileLock takes an exclusive advisory lock on path, creating it if
// needed, and blocks until it is free. Close releases it.
//
// It is the same lock the project state uses for its read-modify-write, hoisted
// so other packages can serialize their own multi-process critical sections
// instead of inventing a second locking scheme. The memory migration is one:
// two sessions starting at once in two worktrees of a repository would
// otherwise both try to fold the old per-path stores into the shared one.
func AcquireFileLock(path string) (io.Closer, error) { return acquireStateLock(path) }
