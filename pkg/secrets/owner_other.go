//go:build !unix

package secrets

import "os"

func checkBaseOwner(_ string, _ os.FileInfo) error { return nil }
