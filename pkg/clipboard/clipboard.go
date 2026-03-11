// Package clipboard provides system clipboard image reading.
package clipboard

import "errors"

// ErrNoImage is returned when the clipboard doesn't contain image data.
var ErrNoImage = errors.New("clipboard does not contain image data")

// ErrUnsupported is returned on platforms without clipboard image support.
var ErrUnsupported = errors.New("clipboard image reading not supported on this platform")

// MaxImageBytes is the maximum clipboard image size accepted (10 MB).
const MaxImageBytes = 10 * 1024 * 1024
