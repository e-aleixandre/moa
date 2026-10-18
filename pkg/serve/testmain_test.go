package serve

import (
	"os"
	"testing"

	"github.com/e-aleixandre/moa/internal/testenv"
)

// TestMain keeps this package's tests away from the developer's real moa config.
// The suite runs the asynchronous ask_user: it is the contract these tests
// specify. The ones that exercise the blocking path call pinSyncAskUser.
func TestMain(m *testing.M) {
	askUserAsyncDefault.Store(true)
	os.Exit(testenv.Run(m))
}

// pinSyncAskUser makes the sessions a test builds use the blocking ask_user.
// The tests that call it specify that path (a pending ask holds the run in
// StatePermission, the answer is the tool_result); the asynchronous contract is
// covered by the TestF1Bis* suites.
func pinSyncAskUser(t *testing.T) {
	t.Helper()
	prev := askUserAsyncDefault.Swap(false)
	t.Cleanup(func() { askUserAsyncDefault.Store(prev) })
}
