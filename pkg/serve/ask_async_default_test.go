package serve

import (
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

// The owner's decision (19/10): the asynchronous ask_user is on unless the
// config turns it off.
func TestAskUserAsyncIsOnByDefaultAndFalseTurnsItOff(t *testing.T) {
	if !core.IsAskUserAsync(core.MoaConfig{}, askUserAsyncBuildDefault) {
		t.Fatal("an unset ask_user_async is off; the build's default is on")
	}
	off := false
	if core.IsAskUserAsync(core.MoaConfig{AskUserAsync: &off}, askUserAsyncBuildDefault) {
		t.Fatal(`"ask_user_async": false did not turn it off`)
	}
}
