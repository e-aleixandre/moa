package serve

import (
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/e-aleixandre/moa/internal/testenv"
)

// TestMain keeps this package's tests away from the developer's real moa config.
// The suite runs the asynchronous ask_user: it is the contract these tests
// specify. The ones that exercise the blocking path call pinSyncAskUser.
//
// MOA_TEST_ASK_ASYNC=0 runs the whole package with the build's default
// instead (off): what production runs until the pilot is enabled. The tests
// that only make sense with the pilot on pin it with pinAsyncAskUser.
func TestMain(m *testing.M) {
	// The crash children of the asynchronous tests are asynchronous whatever
	// the suite's mode: they are re-executions of those tests.
	askUserAsyncDefault.Store(os.Getenv("MOA_TEST_ASK_ASYNC") != "0" || os.Getenv(f1CrashChildEnv) != "")
	os.Exit(testenv.Run(m))
}

// pinAsyncAskUser makes the sessions a test builds use the asynchronous
// ask_user whatever the suite's default is (a no-op in the default, on, mode).
// A test that asked for the blocking path with pinSyncAskUser keeps it: the
// shared helpers of the asynchronous tests call this too, and must not undo it.
func pinAsyncAskUser(t *testing.T) {
	t.Helper()
	if syncPinned(t) {
		return
	}
	prev := askUserAsyncDefault.Swap(true)
	t.Cleanup(func() { askUserAsyncDefault.Store(prev) })
}

// pinSyncAskUser makes the sessions a test builds use the blocking ask_user.
// The tests that call it specify that path (a pending ask holds the run in
// StatePermission, the answer is the tool_result); the asynchronous contract is
// covered by the TestF1Bis* suites.
func pinSyncAskUser(t *testing.T) {
	t.Helper()
	syncPins.Store(t.Name(), struct{}{})
	prev := askUserAsyncDefault.Swap(false)
	t.Cleanup(func() {
		askUserAsyncDefault.Store(prev)
		syncPins.Delete(t.Name())
	})
}

// syncPins holds the names of the tests that pinned the blocking path.
var syncPins sync.Map

// syncPinned reports whether t, or the test it is a subtest of, pinned it.
func syncPinned(t *testing.T) bool {
	pinned := false
	syncPins.Range(func(k, _ any) bool {
		name := k.(string)
		if t.Name() == name || strings.HasPrefix(t.Name(), name+"/") {
			pinned = true
		}
		return !pinned
	})
	return pinned
}
