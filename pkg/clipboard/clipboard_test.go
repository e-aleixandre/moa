//go:build darwin

package clipboard

import "testing"

func TestHasImage_DoesNotPanic(t *testing.T) {
	_ = HasImage()
}

func TestReadImage_NoImageReturnsError(t *testing.T) {
	if HasImage() {
		t.Skip("clipboard contains an image — can't test no-image path")
	}
	_, _, err := ReadImage()
	if err == nil {
		t.Error("expected error when no image in clipboard")
	}
}
