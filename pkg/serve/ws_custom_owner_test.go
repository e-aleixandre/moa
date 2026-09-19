package serve

import "testing"

func TestProjectWSMessageCustom_OwnerAndReport(t *testing.T) {
	got := projectWSMessageCustom(map[string]any{"source": "owner", "owner_id": "own_1", "owner_name": "Winerim", "internal": true})
	if got["owner_name"] != "Winerim" || got["owner_id"] != "own_1" {
		t.Fatalf("owner provenance not projected: %v", got)
	}
	if _, leaked := got["internal"]; leaked {
		t.Fatalf("unknown key leaked: %v", got)
	}

	live := projectWSMessageCustom(map[string]any{"source": "report", "count": 2, "sessions": []map[string]string{{"id": "s1", "title": "A", "status": "done"}}})
	if live["count"] != 2 {
		t.Fatalf("count not projected live: %v", live)
	}
	if list, _ := live["sessions"].([]map[string]string); len(list) != 1 || list[0]["id"] != "s1" {
		t.Fatalf("sessions not projected live: %v", live)
	}

	fromDisk := projectWSMessageCustom(map[string]any{"source": "report", "count": float64(1), "sessions": []any{map[string]any{"id": "s2", "title": "B", "status": "failed"}}})
	if fromDisk["count"] != 1 {
		t.Fatalf("count not projected from disk: %v", fromDisk)
	}
	if list, _ := fromDisk["sessions"].([]map[string]string); len(list) != 1 || list[0]["status"] != "failed" {
		t.Fatalf("sessions not projected from disk: %v", fromDisk)
	}
}
