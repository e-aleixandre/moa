package attachment

import (
	"os"
	"testing"

	"github.com/e-aleixandre/moa/internal/testenv"
)

// TestMain keeps this package's tests away from the developer's real moa config.
func TestMain(m *testing.M) { os.Exit(testenv.Run(m)) }
