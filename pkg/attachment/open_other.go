//go:build !unix

package attachment

import "os"

func openBlobReadOnly(path string) (*os.File, error) {
	return os.Open(path)
}
