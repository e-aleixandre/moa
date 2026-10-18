package serve

import (
	"os"
	"testing"

	"github.com/e-aleixandre/moa/internal/testenv"
)

// TestMain keeps this package's tests away from the developer's real moa config.
// The suite runs the asynchronous ask_user: it is the contract these tests
// specify. The ones that exercise the blocking path pin it with
// AskUserAsync: false in their config.
func TestMain(m *testing.M) {
	askUserAsyncDefault.Store(true)
	os.Exit(testenv.Run(m))
}
