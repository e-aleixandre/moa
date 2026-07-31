package release

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tarGzFixture(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "README.md", Typeflag: tar.TypeReg, Mode: 0644, Size: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("hi\n")); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0755, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zipFixture(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func checksumsFixture(entries map[string][]byte) []byte {
	var b strings.Builder
	for name, data := range entries {
		sum := sha256.Sum256(data)
		fmt.Fprintf(&b, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	}
	return []byte(b.String())
}

func TestChecksumForParsesGoreleaserFormat(t *testing.T) {
	archive := []byte("archive-bytes")
	sums := append([]byte("# comment line\n"), checksumsFixture(map[string][]byte{"moa_1.0.0_linux_amd64.tar.gz": archive})...)
	if err := verifyChecksum(archive, sums, "moa_1.0.0_linux_amd64.tar.gz"); err != nil {
		t.Fatalf("verifyChecksum() = %v, want nil", err)
	}
	if err := verifyChecksum([]byte("tampered"), sums, "moa_1.0.0_linux_amd64.tar.gz"); err == nil {
		t.Fatal("tampered archive must fail verification")
	}
	if err := verifyChecksum(archive, sums, "moa_1.0.0_darwin_arm64.tar.gz"); err == nil {
		t.Fatal("missing checksum entry must fail verification")
	}
	if err := verifyChecksum(archive, []byte("zzz  moa_1.0.0_linux_amd64.tar.gz\n"), "moa_1.0.0_linux_amd64.tar.gz"); err == nil {
		t.Fatal("malformed checksum must fail verification")
	}
	if _, err := checksumFor(checksumsFixture(map[string][]byte{"moa_1.0.0_linux_amd64.tar.gz": archive}), "moa_1.0.0_linux_amd64.tar.gz"); err != nil {
		t.Fatalf("checksumFor() = %v, want nil", err)
	}
}

func TestExtractBinary(t *testing.T) {
	want := []byte("\x7fELF fake binary")
	got, err := extractBinary(tarGzFixture(t, "moa", want), "linux")
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("extractBinary(tar.gz) = %q, %v", got, err)
	}
	got, err = extractBinary(zipFixture(t, "moa.exe", want), "windows")
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("extractBinary(zip) = %q, %v", got, err)
	}
	if _, err := extractBinary(tarGzFixture(t, "other", want), "linux"); err == nil {
		t.Fatal("archive without the moa binary must fail")
	}
	if _, err := extractBinary([]byte("not an archive"), "linux"); err == nil {
		t.Fatal("corrupt archive must fail")
	}
}

func TestArchiveName(t *testing.T) {
	if got, want := archiveName("1.2.3", "linux", "amd64"), "moa_1.2.3_linux_amd64.tar.gz"; got != want {
		t.Fatalf("archiveName() = %q, want %q", got, want)
	}
	if got, want := archiveName("1.2.3", "windows", "arm64"), "moa_1.2.3_windows_arm64.zip"; got != want {
		t.Fatalf("archiveName() = %q, want %q", got, want)
	}
}

func TestPackageManagerGuard(t *testing.T) {
	cases := map[string]string{
		"/opt/homebrew/bin/moa":                       "homebrew",
		"/usr/local/Cellar/moa/1.0.0/bin/moa":         "homebrew",
		"/nix/store/abc123-moa-1.0.0/bin/moa":         "nix",
		"/usr/local/bin/moa":                          "",
		"/home/user/go/bin/moa":                       "",
		"/home/user/.local/share/nixnotstore/bin/moa": "",
	}
	for path, want := range cases {
		if got := packageManager(path); got != want {
			t.Fatalf("packageManager(%q) = %q, want %q", path, got, want)
		}
	}

	var out bytes.Buffer
	err := Update(context.Background(), UpdateOptions{
		Info:     Info{Version: "1.0.0"},
		Out:      &out,
		ExecPath: "/opt/homebrew/bin/moa",
	})
	if err == nil || !strings.Contains(err.Error(), "brew upgrade moa") {
		t.Fatalf("Update() = %v, want a Homebrew guard error", err)
	}
	if out.Len() != 0 {
		t.Fatalf("guard must not report progress, got %q", out.String())
	}
}

func TestReplaceBinaryStagesInTargetDirectory(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "moa")
	if err := os.WriteFile(target, []byte("old"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := replaceBinary(target, []byte("new"), "linux"); err != nil {
		t.Fatalf("replaceBinary() = %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "new" {
		t.Fatalf("target content = %q, %v", data, err)
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatalf("target mode = %v, %v", info.Mode(), err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("temp file left behind: %v", entries)
	}
}

func TestReplaceBinaryWindowsMovesOldAside(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "moa.exe")
	if err := os.WriteFile(target, []byte("old"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := replaceBinary(target, []byte("new"), "windows"); err != nil {
		t.Fatalf("replaceBinary() = %v", err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "new" {
		t.Fatalf("target content = %q, %v", data, err)
	}
	if data, err := os.ReadFile(target + ".old"); err != nil || string(data) != "old" {
		t.Fatalf("old binary content = %q, %v", data, err)
	}
}

func TestReplaceBinaryReportsUnwritableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "moa")
	if err := os.WriteFile(target, []byte("old"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
	err := replaceBinary(target, []byte("new"), "linux")
	if err == nil || !strings.Contains(err.Error(), "permission") {
		t.Fatalf("replaceBinary() = %v, want a permission hint", err)
	}
}

// updateServer serves a fake release: the API endpoint plus the archive and
// checksums the update flow downloads.
func updateServer(t *testing.T, tag string, assets map[string][]byte) *httptest.Server {
	t.Helper()
	sums := checksumsFixture(assets)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := filepath.Base(r.URL.Path)
		switch {
		case r.URL.Path == "/api/latest":
			fmt.Fprintf(w, `{"tag_name":%q}`, tag)
		case name == "checksums.txt":
			_, _ = w.Write(sums)
		default:
			data, ok := assets[name]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(data)
		}
	}))
}

func TestUpdateDownloadsVerifiesAndInstalls(t *testing.T) {
	binary := []byte("\x7fELF new moa")
	archive := tarGzFixture(t, "moa", binary)
	s := updateServer(t, "v1.1.0", map[string][]byte{"moa_1.1.0_linux_amd64.tar.gz": archive})
	defer s.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "moa")
	if err := os.WriteFile(target, []byte("old moa"), 0755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	opts := UpdateOptions{
		Info:     Info{Version: "1.0.0"},
		Out:      &out,
		Checker:  &Checker{Info: Info{Version: "1.0.0"}, Client: s.Client(), URL: s.URL + "/api/latest", CachePath: filepath.Join(t.TempDir(), "update.json")},
		Client:   s.Client(),
		BaseURL:  s.URL + "/releases/download",
		ExecPath: target,
		GOOS:     "linux",
		GOARCH:   "amd64",
	}
	if err := Update(context.Background(), opts); err != nil {
		t.Fatalf("Update() = %v", err)
	}
	if data, err := os.ReadFile(target); err != nil || !bytes.Equal(data, binary) {
		t.Fatalf("installed binary = %q, %v", data, err)
	}
	if !strings.Contains(out.String(), "v1.0.0 → v1.1.0") || !strings.Contains(out.String(), "Restart moa") {
		t.Fatalf("output = %q, want old → new plus a restart reminder", out.String())
	}
}

func TestUpdateRejectsTamperedArchive(t *testing.T) {
	archive := tarGzFixture(t, "moa", []byte("new moa"))
	sums := checksumsFixture(map[string][]byte{"moa_1.1.0_linux_amd64.tar.gz": []byte("something else")})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch filepath.Base(r.URL.Path) {
		case "latest":
			_, _ = fmt.Fprint(w, `{"tag_name":"v1.1.0"}`)
		case "checksums.txt":
			_, _ = w.Write(sums)
		default:
			_, _ = w.Write(archive)
		}
	}))
	defer s.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "moa")
	if err := os.WriteFile(target, []byte("old moa"), 0755); err != nil {
		t.Fatal(err)
	}
	err := Update(context.Background(), UpdateOptions{
		Info:     Info{Version: "1.0.0"},
		Out:      &bytes.Buffer{},
		Checker:  &Checker{Info: Info{Version: "1.0.0"}, Client: s.Client(), URL: s.URL + "/api/latest", CachePath: filepath.Join(t.TempDir(), "update.json")},
		Client:   s.Client(),
		BaseURL:  s.URL + "/releases/download",
		ExecPath: target,
		GOOS:     "linux",
		GOARCH:   "amd64",
	})
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("Update() = %v, want a checksum mismatch", err)
	}
	if data, _ := os.ReadFile(target); string(data) != "old moa" {
		t.Fatalf("binary must be untouched, got %q", data)
	}
}

func TestUpdateCheckOnlyAndUpToDate(t *testing.T) {
	s := updateServer(t, "v1.1.0", nil)
	defer s.Close()
	newChecker := func(version string) *Checker {
		return &Checker{Info: Info{Version: version}, Client: s.Client(), URL: s.URL + "/api/latest", CachePath: filepath.Join(t.TempDir(), "update.json")}
	}
	target := filepath.Join(t.TempDir(), "moa")
	if err := os.WriteFile(target, []byte("old moa"), 0755); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := Update(context.Background(), UpdateOptions{
		Info: Info{Version: "1.0.0"}, CheckOnly: true, Out: &out,
		Checker: newChecker("1.0.0"), Client: s.Client(), BaseURL: s.URL, ExecPath: target, GOOS: "linux", GOARCH: "amd64",
	}); err != nil {
		t.Fatalf("Update(--check) = %v", err)
	}
	if !strings.Contains(out.String(), "v1.0.0 → v1.1.0 available") {
		t.Fatalf("--check output = %q", out.String())
	}
	if data, _ := os.ReadFile(target); string(data) != "old moa" {
		t.Fatalf("--check must not download, got %q", data)
	}

	out.Reset()
	if err := Update(context.Background(), UpdateOptions{
		Info: Info{Version: "1.1.0"}, Out: &out,
		Checker: newChecker("1.1.0"), Client: s.Client(), BaseURL: s.URL, ExecPath: target, GOOS: "linux", GOARCH: "amd64",
	}); err != nil {
		t.Fatalf("Update() = %v", err)
	}
	if !strings.Contains(out.String(), "already the latest release") {
		t.Fatalf("up-to-date output = %q", out.String())
	}
}

func TestLatestVersionIgnoresCacheIntervalAndOptOut(t *testing.T) {
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = fmt.Fprint(w, `{"tag_name":"v2.0.0"}`)
	}))
	defer s.Close()
	t.Setenv("MOA_NO_UPDATE_CHECK", "1")
	c := &Checker{Info: Info{Version: "1.0.0"}, Client: s.Client(), URL: s.URL, CachePath: filepath.Join(t.TempDir(), "update.json")}
	for i := 0; i < 2; i++ {
		latest, err := c.LatestVersion(context.Background())
		if err != nil || latest != "v2.0.0" {
			t.Fatalf("LatestVersion() = %q, %v", latest, err)
		}
	}
	if calls != 2 {
		t.Fatalf("explicit update must bypass the cache interval: calls=%d", calls)
	}
}

func TestExtractTarGzRejectsOversizedEntry(t *testing.T) {
	// A header that declares an absurd size must be rejected before reading,
	// so a truncated binary can never be installed.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "moa", Typeflag: tar.TypeReg, Mode: 0755, Size: maxDownloadSize + 1}); err != nil {
		t.Fatal(err)
	}
	// Close writers without the payload: extract must fail on the size check
	// before it ever reads the (missing) body.
	_ = tw.Close()
	_ = gz.Close()
	if _, err := extractTarGz(buf.Bytes(), "moa"); err == nil || !strings.Contains(err.Error(), "unreasonably large") {
		t.Fatalf("extractTarGz() = %v, want size rejection", err)
	}
}

func TestReplaceBinaryRefusesConcurrentUpdate(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "moa")
	if err := os.WriteFile(target, []byte("old"), 0755); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(dir, ".moa-update.lock")
	if err := os.WriteFile(lock, nil, 0644); err != nil {
		t.Fatal(err)
	}
	err := replaceBinary(target, []byte("new"), "linux")
	if err == nil || !strings.Contains(err.Error(), "another moa update") {
		t.Fatalf("replaceBinary() = %v, want concurrent-update refusal", err)
	}
	if data, _ := os.ReadFile(target); string(data) != "old" {
		t.Fatalf("target must be untouched, got %q", data)
	}
	// After the stale lock is removed the update proceeds.
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if err := replaceBinary(target, []byte("new"), "linux"); err != nil {
		t.Fatalf("replaceBinary() after lock removal = %v", err)
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatal("lock file must be removed after a successful update")
	}
}
