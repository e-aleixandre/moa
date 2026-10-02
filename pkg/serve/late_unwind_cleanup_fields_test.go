package serve

import (
	"reflect"
	"testing"

	"github.com/e-aleixandre/moa/pkg/bus"
)

// The late G1 discard is published on the bus with its concrete IDs, marked
// as cleanup only, for the server-side consumers.
func TestLateUnwindDiscardIsPublishedAsCleanupOnly(t *testing.T) {
	f := runLateUnwind(t)
	var late []bus.SteersCanceled
	for _, e := range f.canceled {
		if e.CleanupOnly {
			late = append(late, e)
		}
	}
	if len(late) != 1 || !reflect.DeepEqual(late[0].SteerIDs, []string{"q-reused"}) || !reflect.DeepEqual(late[0].AttachmentIDs, []string{f.attID}) {
		t.Errorf("late discard events = %+v, want one cleanup-only event with q-reused and %s", f.canceled, f.attID)
	}
}

func TestCleanupOnlySteersCanceledIsNotSentToClients(t *testing.T) {
	if ev, ok := wsEventFromBus(bus.SteersCanceled{SteerIDs: []string{"s1"}, AttachmentIDs: []string{"a1"}, CleanupOnly: true}); ok {
		t.Errorf("cleanup-only discard forwarded: %+v", ev)
	}
	if _, ok := wsEventFromBus(bus.SteersCanceled{SteerIDs: []string{"s1"}}); !ok {
		t.Error("ordinary discard no longer forwarded")
	}
}
