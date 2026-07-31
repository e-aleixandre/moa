package openai

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

// History is portable across providers, so an image recorded with the wrong
// media type (a GIF read from a .png) must go out declaring what it really is
// rather than repeating the block's claim.
func TestConvertUserContent_MislabeledImageMediaTypeCorrected(t *testing.T) {
	gif := base64.StdEncoding.EncodeToString(append([]byte("GIF89a"), make([]byte, 512)...))
	parts := convertUserContent([]core.Content{core.ImageContent(gif, "image/png")}, true)
	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(parts))
	}
	url, _ := parts[0]["image_url"].(string)
	if !strings.HasPrefix(url, "data:image/gif;base64,") {
		t.Fatalf("image_url should declare image/gif, got %.40q", url)
	}
}
