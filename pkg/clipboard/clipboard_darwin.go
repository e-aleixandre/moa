//go:build darwin

package clipboard

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// HasImage returns true if the system clipboard contains image data.
func HasImage() bool {
	cmd := exec.Command("osascript", "-e", `try
	the clipboard as «class PNGf»
	return "yes"
on error
	return "no"
end try`)
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "yes"
}

// ReadImage reads image data from the clipboard as PNG.
// Returns (imageBytes, mimeType, error).
func ReadImage() ([]byte, string, error) {
	tmp, err := os.CreateTemp("", "moa-clip-*.png")
	if err != nil {
		return nil, "", fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	script := fmt.Sprintf(`set f to open for access POSIX file %q with write permission
set img to the clipboard as «class PNGf»
write img to f
close access f`, tmpPath)

	cmd := exec.Command("osascript", "-e", script)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// AppleScript "Can't make «class PNGf»" means no image — everything else is a real failure.
		errText := stderr.String()
		if strings.Contains(errText, "PNGf") || strings.Contains(errText, "clipboard") {
			return nil, "", ErrNoImage
		}
		return nil, "", fmt.Errorf("clipboard read failed: %w: %s", err, strings.TrimSpace(errText))
	}

	data, err := os.ReadFile(tmpPath)
	if err != nil {
		return nil, "", fmt.Errorf("read clipboard image: %w", err)
	}
	if len(data) == 0 {
		return nil, "", ErrNoImage
	}
	if len(data) > MaxImageBytes {
		return nil, "", fmt.Errorf("clipboard image too large (%d MB, max %d MB)",
			len(data)/(1024*1024), MaxImageBytes/(1024*1024))
	}

	return data, "image/png", nil
}
