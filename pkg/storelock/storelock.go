// Package storelock serializes writers of an on-disk store (the owner's book,
// a memory directory) across goroutines and processes.
//
// The lock file is a sibling of the store, never inside it: a store directory
// can be replaced wholesale (book-tidy moves the whole book), and two writers
// holding locks on two different inodes would no longer exclude each other.
package storelock

import "path/filepath"

// Path is the lock file that guards dir: "<parent>/.<base>.lock".
func Path(dir string) string {
	dir = filepath.Clean(dir)
	return filepath.Join(filepath.Dir(dir), "."+filepath.Base(dir)+".lock")
}
