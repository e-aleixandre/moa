package serve

import (
	"reflect"
	"testing"

	"github.com/e-aleixandre/moa/pkg/bus"
)

// The same real G1/G2 barrier fixture already owns an attachment and a reused
// live chip. Only its concrete chip IDs are filtered; cleanup is not dropped.
func TestPR26BReusedLiveIDIsFilteredButAttachmentCleanupSurvives(t *testing.T) {
	f := runLateUnwind(t)
	if len(f.canceled) != 1 || !reflect.DeepEqual(f.canceled[0].AttachmentIDs, []string{f.attID}) {
		t.Fatalf("late attachment cleanup events=%+v, want exactly one with %s", f.canceled, f.attID)
	}
	late := f.canceled[0]
	if late.SteerIDs == nil || len(late.SteerIDs) != 0 {
		t.Errorf("live q-reused must be removed from concrete discarded IDs, not kept or replaced with null: %+v", late)
	}
	if ev, ok := wsEventFromBus(late); !ok {
		t.Error("late concrete discard was hidden from clients")
	} else {
		data, _ := ev.Data.(map[string]any)
		ids, ok := data["discarded_steer_ids"].([]string)
		if !ok || ids == nil || len(ids) != 0 {
			t.Errorf("WS discarded IDs=%v; want an explicit empty array, not wildcard clearing", data)
		}
	}
	pollUntil(t, 3e9, "old attachment released despite live reused chip", func() bool {
		_, found := f.store.Lookup(f.sess.ID, f.attID)
		return !found
	})
	// Correlation is independent of the identity filter. An explicit Stop
	// acknowledgement must keep stop_id even when it discarded no chip.
	if ev, ok := wsEventFromBus(bus.SteersCanceled{SteerIDs: []string{}, StopID: "pr26-empty-stop"}); !ok {
		t.Error("empty correlated Stop was hidden")
	} else if data, _ := ev.Data.(map[string]any); data["stop_id"] != "pr26-empty-stop" {
		t.Errorf("correlated Stop lost stop_id: %+v", data)
	}
}
