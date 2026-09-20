package owner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func reportForTest(id string) Report { return Report{ID: id, SessionID: id, Status: "done"} }

func TestReportsOutboxPreservesMalformedCanonical(t *testing.T) {
	store := NewStore(t.TempDir())
	key := "project"
	dir := store.CodebaseDir(key)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	broken := []byte("{not reports\n")
	canonical := filepath.Join(dir, reportsFile)
	if err := os.WriteFile(canonical, broken, 0o600); err != nil {
		t.Fatal(err)
	}
	outbox, err := store.LoadReportsOutbox(key)
	if err != nil || len(outbox.Unreadable) == 0 {
		t.Fatalf("LoadReportsOutbox = %+v, %v", outbox, err)
	}
	outbox, err = store.SaveReportsOutbox(key, outbox, []Report{reportForTest("new")})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(canonical); err != nil || string(got) != string(broken) {
		t.Fatalf("canonical changed to %q (%v)", got, err)
	}
	if want := filepath.Join(dir, reportsRecoveryFile); outbox.ActivePath != want {
		t.Fatalf("active path = %q, want %q", outbox.ActivePath, want)
	}
	if reports, err := loadReportsFromPath(outbox.ActivePath); err != nil || len(reports) != 1 || reports[0].ID != "new" {
		t.Fatalf("recovery reports = %+v, %v", reports, err)
	}
}

func TestReportsOutboxPartialLoadNeverWritesCanonical(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory mode 0300 does not express read-directory permission on Windows")
	}
	store := NewStore(t.TempDir())
	key := "project"
	if err := store.SaveReports(key, []Report{reportForTest("old")}); err != nil {
		t.Fatal(err)
	}
	canonical := filepath.Join(store.CodebaseDir(key), reportsFile)
	before, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatal(err)
	}
	dir := store.CodebaseDir(key)
	if err := os.Chmod(dir, 0o300); err != nil {
		t.Fatal(err)
	}
	restore := func() { _ = os.Chmod(dir, 0o700) }
	t.Cleanup(restore)
	if _, err := os.ReadDir(dir); err == nil {
		t.Skip("filesystem does not reject ReadDir for mode 0300")
	}
	outbox, err := store.LoadReportsOutbox(key)
	if err == nil || !outbox.Incomplete || len(outbox.Reports) != 1 {
		t.Fatalf("partial LoadReportsOutbox = %+v, %v", outbox, err)
	}
	outbox, err = store.SaveReportsOutbox(key, outbox, append(outbox.Reports, reportForTest("new")))
	if err != nil {
		t.Skipf("filesystem rejects fresh file creation under mode 0300: %v", err)
	}
	outbox, err = store.SaveReportsOutbox(key, outbox, append(outbox.Reports, reportForTest("newer")))
	if err != nil {
		t.Fatal(err)
	}
	restore()
	if got, err := os.ReadFile(canonical); err != nil || string(got) != string(before) {
		t.Fatalf("canonical changed to %q (%v)", got, err)
	}
	recovery := filepath.Join(store.CodebaseDir(key), reportsRecoveryFile)
	if reports, err := loadReportsFromPath(recovery); err != nil || len(reports) != 3 {
		t.Fatalf("active recovery lane = %+v, %v", reports, err)
	}
	if matches, err := filepath.Glob(filepath.Join(store.CodebaseDir(key), "reports.recovery*.json")); err != nil || len(matches) != 1 {
		t.Fatalf("recovery lane count = %v, %v; want one", matches, err)
	}
}

func TestReportsOutboxPreservesMalformedRecoveryAndUsesNumberedLane(t *testing.T) {
	store := NewStore(t.TempDir())
	key := "project"
	dir := store.CodebaseDir(key)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	canonical := filepath.Join(dir, reportsFile)
	recovery := filepath.Join(dir, reportsRecoveryFile)
	if err := os.WriteFile(canonical, []byte("bad canonical"), 0o600); err != nil {
		t.Fatal(err)
	}
	brokenRecovery := []byte("bad recovery")
	if err := os.WriteFile(recovery, brokenRecovery, 0o600); err != nil {
		t.Fatal(err)
	}
	outbox, err := store.LoadReportsOutbox(key)
	if err != nil {
		t.Fatal(err)
	}
	outbox, err = store.SaveReportsOutbox(key, outbox, []Report{reportForTest("new")})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "reports.recovery.1.json"); outbox.ActivePath != want {
		t.Fatalf("active path = %q, want %q", outbox.ActivePath, want)
	}
	if got, err := os.ReadFile(recovery); err != nil || string(got) != string(brokenRecovery) {
		t.Fatalf("recovery changed to %q (%v)", got, err)
	}
}

func TestReportsOutboxConsolidatesValidRecoveryWithCanonical(t *testing.T) {
	store := NewStore(t.TempDir())
	key := "project"
	if err := store.SaveReports(key, []Report{reportForTest("canonical"), reportForTest("same")}); err != nil {
		t.Fatal(err)
	}
	recovery := filepath.Join(store.CodebaseDir(key), reportsRecoveryFile)
	if err := writeFileAtomic(recovery, mustReportJSON(t, []Report{reportForTest("same"), reportForTest("recovery")}), 0o600); err != nil {
		t.Fatal(err)
	}
	outbox, err := store.LoadReportsOutbox(key)
	if err != nil {
		t.Fatal(err)
	}
	if len(outbox.Reports) != 3 || len(outbox.Lanes) != 2 {
		t.Fatalf("loaded outbox = %+v", outbox)
	}
	if _, err := store.SaveReportsOutbox(key, outbox, outbox.Reports); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(recovery); !os.IsNotExist(err) {
		t.Fatalf("recovery lane remains: %v", err)
	}
	pending, err := store.LoadReports(key)
	if err != nil || len(pending) != 3 {
		t.Fatalf("canonical reports = %+v, %v", pending, err)
	}
}

func TestReportsOutboxOrdersRecoveryLanesAndKeepsEmptyIDs(t *testing.T) {
	store := NewStore(t.TempDir())
	key := "project"
	dir := store.CodebaseDir(key)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, lane := range []struct {
		name string
		id   string
	}{
		{reportsRecoveryFile, ""},
		{"reports.recovery.2.json", ""},
		{"reports.recovery.10.json", "later"},
	} {
		reports := []Report{reportForTest(lane.id)}
		if lane.name == reportsRecoveryFile {
			reports = append(reports, reportForTest(""))
		}
		if err := os.WriteFile(filepath.Join(dir, lane.name), mustReportJSON(t, reports), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	outbox, err := store.LoadReportsOutbox(key)
	if err != nil || len(outbox.Reports) != 4 {
		t.Fatalf("LoadReportsOutbox = %+v, %v", outbox, err)
	}
	if got, want := outbox.Reports[0].SessionID, ""; got != want || outbox.Reports[0].ID == "" || outbox.Reports[1].ID == "" || outbox.Reports[2].ID == "" || outbox.Reports[0].ID == outbox.Reports[1].ID || outbox.Reports[0].ID == outbox.Reports[2].ID {
		t.Fatalf("empty recovered IDs collapsed: %+v", outbox.Reports)
	}
	if got := []string{filepath.Base(outbox.Lanes[0]), filepath.Base(outbox.Lanes[1]), filepath.Base(outbox.Lanes[2])}; got[0] != reportsRecoveryFile || got[1] != "reports.recovery.2.json" || got[2] != "reports.recovery.10.json" {
		t.Fatalf("lane order = %v", got)
	}
}

func mustReportJSON(t *testing.T, reports []Report) []byte {
	t.Helper()
	data, err := json.MarshalIndent(reports, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}

func loadReportsFromPath(path string) ([]Report, error) {
	reports, _, err := readReportLane(path)
	return reports, err
}
