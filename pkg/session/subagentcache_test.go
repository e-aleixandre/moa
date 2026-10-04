package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

func hex64(c string) string { return strings.Repeat(c, 64) }

func testFP(c string) core.RequestFingerprint {
	return core.RequestFingerprint{
		BuiltAt:       time.Unix(100, 0).UTC(),
		BodySHA256:    hex64(c),
		OptionsSHA256: hex64("b"),
		ToolsSHA256:   hex64("c"),
		Prefixes:      []core.RequestPrefixHash{{Section: "messages", Message: 0, Block: 0, SHA256: hex64(c)}},
		Breakpoints:   []core.RequestBreakpoint{{Section: "messages", Message: 0, Block: 0, TTL: "5m"}},
	}
}

func TestSubagentCacheAudit_CountFirstLastAndPermissions(t *testing.T) {
	old := syscall.Umask(022)
	defer syscall.Umask(old)
	dir := t.TempDir()
	store := NewSubagentStore(dir, "sess")
	for i, c := range []string{"1", "2", "3"} {
		if err := store.RecordRequestFingerprint("sa-1", "sa-0", testFP(c)); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	a, err := store.LoadCacheAudit("sa-1")
	if err != nil {
		t.Fatal(err)
	}
	if a.Version != 1 || a.JobID != "sa-1" || a.ResumedFrom != "sa-0" || a.Count != 3 ||
		a.First.BodySHA256 != hex64("1") || a.Last.BodySHA256 != hex64("3") {
		t.Fatalf("audit = %+v", a)
	}
	path := filepath.Join(store.Dir(), "sa-1.cache.json")
	if info, _ := os.Stat(path); info.Mode().Perm() != 0600 {
		t.Fatalf("file mode %v", info.Mode().Perm())
	}
	if info, _ := os.Stat(store.Dir()); info.Mode().Perm() != 0700 {
		t.Fatalf("dir mode %v", info.Mode().Perm())
	}
	entries, _ := os.ReadDir(store.Dir())
	if len(entries) != 1 {
		t.Fatalf("leftover files: %v", entries)
	}
	if _, err := store.LoadCacheAudit("sa-missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing err = %v", err)
	}
}

func TestSubagentCacheAudit_ConcurrentRecordsAreNotLost(t *testing.T) {
	store := NewSubagentStore(t.TempDir(), "sess")
	const n = 40
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := store.RecordRequestFingerprint("sa-1", "", testFP("4")); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	a, err := store.LoadCacheAudit("sa-1")
	if err != nil || a.Count != n {
		t.Fatalf("count=%v err=%v, want %d", a, err, n)
	}
	if entries, _ := os.ReadDir(store.Dir()); len(entries) != 1 {
		t.Fatalf("temporary files left: %v", entries)
	}
}

func TestSubagentCacheAudit_CorruptOrForeignIsNeitherResetNorOverwritten(t *testing.T) {
	store := NewSubagentStore(t.TempDir(), "sess")
	if err := os.MkdirAll(store.Dir(), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.Dir(), "sa-1.cache.json")
	for name, content := range map[string]string{
		"corrupt":   `{"version":1,"job_id":"sa-1","count":`,
		"version":   `{"version":2,"job_id":"sa-1","count":1}`,
		"identity":  `{"version":1,"job_id":"sa-other","count":1}`,
		"zerocount": `{"version":1,"job_id":"sa-1","count":0}`,
	} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if err := store.RecordRequestFingerprint("sa-1", "", testFP("1")); err == nil {
			t.Fatalf("%s: record succeeded over an unreadable audit", name)
		}
		if got, _ := os.ReadFile(path); string(got) != content {
			t.Fatalf("%s: file was rewritten", name)
		}
	}
}

func TestSubagentCacheAudit_SourceCannotChange(t *testing.T) {
	store := NewSubagentStore(t.TempDir(), "sess")
	if err := store.RecordRequestFingerprint("sa-1", "sa-0", testFP("1")); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordRequestFingerprint("sa-1", "", testFP("2")); err == nil {
		t.Fatal("source change accepted")
	}
	if a, _ := store.LoadCacheAudit("sa-1"); a.Count != 1 {
		t.Fatalf("count = %d", a.Count)
	}
}

func TestSubagentCacheAudit_RejectsNonDigestValues(t *testing.T) {
	store := NewSubagentStore(t.TempDir(), "sess")
	bad := []core.RequestFingerprint{}
	for _, mut := range []func(*core.RequestFingerprint){
		func(f *core.RequestFingerprint) { f.BodySHA256 = "some raw body text" },
		func(f *core.RequestFingerprint) { f.OptionsSHA256 = "" },
		func(f *core.RequestFingerprint) { f.ToolsSHA256 = "xyz" },
		func(f *core.RequestFingerprint) { f.Prefixes[0].Section = "raw" },
		func(f *core.RequestFingerprint) { f.Prefixes[0].SHA256 = strings.Repeat("A", 64) },
		func(f *core.RequestFingerprint) { f.Breakpoints[0].TTL = "secret ttl" },
		func(f *core.RequestFingerprint) { f.Breakpoints[0].Block = -1 },
	} {
		f := testFP("1")
		mut(&f)
		bad = append(bad, f)
	}
	for i, f := range bad {
		if err := store.RecordRequestFingerprint("sa-1", "", f); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
	if _, err := os.Stat(store.Dir()); !os.IsNotExist(err) {
		t.Fatal("rejected fingerprints must not create the directory")
	}
	if err := store.RecordRequestFingerprint("../x", "", testFP("1")); err == nil {
		t.Fatal("unsafe job id accepted")
	}
	if err := store.RecordRequestFingerprint("sa-1", "../x", testFP("1")); err == nil {
		t.Fatal("unsafe source accepted")
	}
}

func TestSubagentCacheAudit_ListsIgnoreItAndTranscriptSavesDoNotTouchIt(t *testing.T) {
	store := NewSubagentStore(t.TempDir(), "sess")
	if err := store.Save(sampleTranscript("sa-1")); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordRequestFingerprint("sa-1", "", testFP("1")); err != nil {
		t.Fatal(err)
	}
	// A cache file whose content looks like a transcript must still be skipped.
	data, _ := os.ReadFile(filepath.Join(store.Dir(), "sa-1.json"))
	if err := os.WriteFile(filepath.Join(store.Dir(), "sa-decoy.cache.json"), data, 0600); err != nil {
		t.Fatal(err)
	}

	list, err := store.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("List = %d, %v", len(list), err)
	}
	sums, err := store.ListSummaries()
	if err != nil || len(sums) != 1 {
		t.Fatalf("ListSummaries = %d, %v", len(sums), err)
	}
	// Adding only an audit leaves the cached summaries valid and still equal.
	if err := store.RecordRequestFingerprint("sa-2", "", testFP("2")); err != nil {
		t.Fatal(err)
	}
	if sums, _ = store.ListSummaries(); len(sums) != 1 {
		t.Fatalf("ListSummaries after audit-only change = %d", len(sums))
	}

	tr := sampleTranscript("sa-1")
	tr.Title = "renamed"
	if err := store.Save(tr); err != nil {
		t.Fatal(err)
	}
	if a, err := store.LoadCacheAudit("sa-1"); err != nil || a.Count != 1 {
		t.Fatalf("transcript Save disturbed the audit: %v %v", a, err)
	}

	if err := store.Remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadCacheAudit("sa-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("audit survived Remove: %v", err)
	}
}

func TestSubagentStore_ReservesCacheJobSuffix(t *testing.T) {
	store := NewSubagentStore(t.TempDir(), "sess")
	if err := store.Save(sampleTranscript("sa-1.cache")); err == nil {
		t.Fatal("job id ending in .cache must be rejected: it would collide with the audit of sa-1")
	}
	if _, err := store.Load("sa-1.cache"); err == nil {
		t.Fatal("Load accepted reserved suffix")
	}
	for _, id := range []string{"sa-1", "job_cache", "cache", "x.cached"} {
		if err := validJobID(id); err != nil {
			t.Errorf("%q rejected: %v", id, err)
		}
	}
}

// A well-formed JSON audit whose digests or source were altered is as
// untrustworthy as a corrupt one: it must be rejected on load and never be
// overwritten by a later record.
func TestSubagentCacheAudit_AlteredValidJSONIsRejectedAndNotOverwritten(t *testing.T) {
	for name, mutate := range map[string]func(*SubagentCacheAudit){
		"first_body": func(a *SubagentCacheAudit) { a.First.BodySHA256 = "INVALID_RAW_VALUE" },
		"last_body":  func(a *SubagentCacheAudit) { a.Last.BodySHA256 = "INVALID_RAW_VALUE" },
		"last_prefix": func(a *SubagentCacheAudit) {
			a.Last.Prefixes[0].SHA256 = "INVALID_RAW_VALUE"
		},
		"first_ttl":   func(a *SubagentCacheAudit) { a.First.Breakpoints[0].TTL = "INVALID_RAW_VALUE" },
		"source_path": func(a *SubagentCacheAudit) { a.ResumedFrom = "../invalid-source" },
	} {
		t.Run(name, func(t *testing.T) {
			store := NewSubagentStore(t.TempDir(), "sess")
			if err := store.RecordRequestFingerprint("sa-1", "sa-0", testFP("1")); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(store.Dir(), "sa-1.cache.json")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var audit SubagentCacheAudit
			if err := json.Unmarshal(raw, &audit); err != nil {
				t.Fatal(err)
			}
			mutate(&audit)
			altered, err := json.Marshal(audit)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, altered, 0600); err != nil {
				t.Fatal(err)
			}

			if _, err := store.LoadCacheAudit("sa-1"); err == nil {
				t.Fatal("Load accepted an altered audit")
			}
			if err := store.RecordRequestFingerprint("sa-1", audit.ResumedFrom, testFP("2")); err == nil {
				t.Fatal("Record accepted an altered audit")
			}
			if got, _ := os.ReadFile(path); string(got) != string(altered) {
				t.Fatal("altered audit was overwritten")
			}
		})
	}
}
