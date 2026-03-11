//go:build !darwin

package clipboard

// HasImage returns false on unsupported platforms.
func HasImage() bool { return false }

// ReadImage returns ErrUnsupported on non-darwin platforms.
func ReadImage() ([]byte, string, error) { return nil, "", ErrUnsupported }
