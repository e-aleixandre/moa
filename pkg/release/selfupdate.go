package release

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// downloadBaseURL is the release asset root of the public repository.
	downloadBaseURL = "https://github.com/e-aleixandre/moa/releases/download"
	// downloadTimeout bounds a full archive download, which is much slower
	// than the metadata request the passive checker makes.
	downloadTimeout = 3 * time.Minute
	// maxDownloadSize caps both the archive and the extracted binary so a
	// hostile or corrupt response cannot exhaust memory.
	maxDownloadSize = 256 << 20
)

// UpdateOptions configures a self-update run. The zero value targets the
// running binary and the public GitHub releases; the remaining fields are
// seams that keep the flow testable without network or install access.
type UpdateOptions struct {
	Info      Info      // build metadata of the running binary
	CheckOnly bool      // report what would happen, download nothing
	Out       io.Writer // defaults to os.Stdout
	Checker   *Checker  // defaults to NewChecker(Info)
	Client    *http.Client
	BaseURL   string // release download root, defaults to downloadBaseURL
	ExecPath  string // binary to replace, defaults to the running executable
	GOOS      string // defaults to runtime.GOOS
	GOARCH    string // defaults to runtime.GOARCH
}

// Update replaces the running binary with the latest stable release. It never
// restarts anything: the caller is told to restart Moa however it runs.
func Update(ctx context.Context, opts UpdateOptions) error {
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}
	goos, goarch := opts.GOOS, opts.GOARCH
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}

	target := opts.ExecPath
	if target == "" {
		resolved, err := currentExecutable()
		if err != nil {
			return err
		}
		target = resolved
	}
	if manager := packageManager(target); manager != "" {
		return fmt.Errorf("%s", packageManagerMessage(manager, target))
	}

	checker := opts.Checker
	if checker == nil {
		checker = NewChecker(opts.Info)
	}
	if opts.Client != nil {
		checker.Client = opts.Client
	}
	latest, err := checker.LatestVersion(ctx)
	if err != nil {
		return fmt.Errorf("could not determine the latest release: %w", err)
	}
	latestVer, ok := ParseSemver(latest)
	if !ok {
		return fmt.Errorf("latest release %q is not a valid version", latest)
	}
	latest = "v" + strings.TrimPrefix(latest, "v")
	current := opts.Info.DisplayVersion()

	if cur, ok := ParseSemver(opts.Info.Version); ok && cur.Compare(latestVer) >= 0 {
		_, _ = fmt.Fprintf(out, "moa %s is already the latest release.\n", current)
		return nil
	}
	if opts.CheckOnly {
		_, _ = fmt.Fprintf(out, "moa %s → %s available.\nRun `moa update` to install it.\n", current, latest)
		return nil
	}

	base := opts.BaseURL
	if base == "" {
		base = downloadBaseURL
	}
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: downloadTimeout}
	}
	version := strings.TrimPrefix(latest, "v")
	name := archiveName(version, goos, goarch)
	assetBase := strings.TrimSuffix(base, "/") + "/" + latest

	_, _ = fmt.Fprintf(out, "Downloading %s...\n", name)
	archive, err := download(ctx, client, assetBase+"/"+name)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", name, err)
	}
	sums, err := download(ctx, client, assetBase+"/checksums.txt")
	if err != nil {
		return fmt.Errorf("downloading checksums.txt: %w", err)
	}
	if err := verifyChecksum(archive, sums, name); err != nil {
		return err
	}
	binary, err := extractBinary(archive, goos)
	if err != nil {
		return err
	}
	if err := replaceBinary(target, binary, goos); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(out, "Updated moa %s → %s at %s.\n", current, latest, target)
	_, _ = fmt.Fprintf(out, "Restart moa for the new version to take effect (however you run it: terminal, systemd, ...).\n")
	return nil
}

// currentExecutable resolves the running binary through symlinks, so an update
// replaces the real file rather than a link pointing at it.
func currentExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot locate the moa binary: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("cannot resolve %s: %w", exe, err)
	}
	return resolved, nil
}

// packageManager reports the package manager that owns a binary path, if any.
// Those installs must be updated through the manager: replacing the file would
// leave the manager's own metadata lying about what is installed.
func packageManager(execPath string) string {
	p := filepath.ToSlash(execPath)
	switch {
	case strings.Contains(p, "/Cellar/"), strings.Contains(p, "/homebrew/"):
		return "homebrew"
	case strings.Contains(p, "/nix/store/"):
		return "nix"
	}
	return ""
}

func packageManagerMessage(manager, execPath string) string {
	switch manager {
	case "homebrew":
		return fmt.Sprintf("this moa was installed with Homebrew (%s)\nupdate it with: brew upgrade moa", execPath)
	case "nix":
		return fmt.Sprintf("this moa comes from the Nix store (%s)\nupdate it through your Nix configuration or flake instead", execPath)
	}
	return fmt.Sprintf("this moa is managed by a package manager (%s)", execPath)
}

// archiveName mirrors the goreleaser name_template for release archives.
func archiveName(version, goos, goarch string) string {
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("moa_%s_%s_%s.%s", version, goos, goarch, ext)
}

func download(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "moa-update")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownloadSize))
	if err != nil {
		return nil, err
	}
	return data, nil
}

// verifyChecksum matches the archive against its entry in checksums.txt. A
// missing entry is as fatal as a mismatch: both mean the download is unproven.
func verifyChecksum(archive, checksums []byte, name string) error {
	want, err := checksumFor(checksums, name)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(archive)
	if got := hex.EncodeToString(sum[:]); got != want {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", name, got, want)
	}
	return nil
}

// checksumFor parses the "<sha256>  <file>" lines goreleaser publishes.
func checksumFor(checksums []byte, name string) (string, error) {
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if strings.TrimPrefix(fields[1], "*") != name {
			continue
		}
		sum := strings.ToLower(fields[0])
		if len(sum) != hex.EncodedLen(sha256.Size) {
			return "", fmt.Errorf("malformed checksum for %s", name)
		}
		if _, err := hex.DecodeString(sum); err != nil {
			return "", fmt.Errorf("malformed checksum for %s", name)
		}
		return sum, nil
	}
	return "", fmt.Errorf("no checksum entry for %s", name)
}

// extractBinary pulls the moa binary out of a release archive: tar.gz
// everywhere, zip on windows.
func extractBinary(archive []byte, goos string) ([]byte, error) {
	if goos == "windows" {
		return extractZip(archive, "moa.exe")
	}
	return extractTarGz(archive, "moa")
}

func extractTarGz(archive []byte, name string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("reading archive: %w", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading archive: %w", err)
		}
		if header.Typeflag != tar.TypeReg || path.Base(header.Name) != name {
			continue
		}
		if header.Size > maxDownloadSize {
			return nil, fmt.Errorf("%s in archive is unreasonably large (%d bytes)", name, header.Size)
		}
		data, err := io.ReadAll(io.LimitReader(tr, maxDownloadSize))
		if err != nil {
			return nil, fmt.Errorf("reading %s from archive: %w", name, err)
		}
		return data, nil
	}
	return nil, fmt.Errorf("archive does not contain %s", name)
}

func extractZip(archive []byte, name string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, fmt.Errorf("reading archive: %w", err)
	}
	for _, f := range zr.File {
		if path.Base(f.Name) != name {
			continue
		}
		if f.UncompressedSize64 > maxDownloadSize {
			return nil, fmt.Errorf("%s in archive is unreasonably large (%d bytes)", name, f.UncompressedSize64)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("reading %s from archive: %w", name, err)
		}
		defer func() { _ = rc.Close() }()
		data, err := io.ReadAll(io.LimitReader(rc, maxDownloadSize))
		if err != nil {
			return nil, fmt.Errorf("reading %s from archive: %w", name, err)
		}
		return data, nil
	}
	return nil, fmt.Errorf("archive does not contain %s", name)
}

// replaceBinary installs data over target. The temporary file lives in the
// target's own directory because os.Rename is only atomic within a filesystem.
// A same-directory lock file serializes concurrent updaters; the loser exits
// with a clear message instead of racing the rename.
func replaceBinary(target string, data []byte, goos string) error {
	dir := filepath.Dir(target)
	lock := filepath.Join(dir, ".moa-update.lock")
	lf, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("another moa update appears to be running (remove %s if it is stale)", lock)
		}
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("cannot write to %s: rerun `moa update` with permission to write there (or reinstall moa somewhere you own)", dir)
		}
		return fmt.Errorf("cannot stage the new binary in %s: %w", dir, err)
	}
	_ = lf.Close()
	defer func() { _ = os.Remove(lock) }()

	tmp, err := os.CreateTemp(dir, ".moa-update-*")
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("cannot write to %s: rerun `moa update` with permission to write there (or reinstall moa somewhere you own)", dir)
		}
		return fmt.Errorf("cannot stage the new binary in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing the new binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing the new binary: %w", err)
	}
	if err := os.Chmod(tmpName, 0755); err != nil {
		return fmt.Errorf("making the new binary executable: %w", err)
	}

	if goos == "windows" {
		// Windows locks a running executable against deletion and overwrite,
		// but renaming it within the same volume is allowed. Moving the old
		// binary aside and dropping the new one in its place is the standard
		// self-update dance (rclone, syncthing do the same); the .old file can
		// be deleted after the next restart.
		old := target + ".old"
		_ = os.Remove(old)
		if err := os.Rename(target, old); err != nil {
			return fmt.Errorf("cannot move the current binary aside: %w", err)
		}
		if err := os.Rename(tmpName, target); err != nil {
			_ = os.Rename(old, target) // put the working binary back
			return fmt.Errorf("installing the new binary: %w", err)
		}
		return nil
	}

	if err := os.Rename(tmpName, target); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("cannot replace %s: rerun `moa update` with permission to write there", target)
		}
		return fmt.Errorf("installing the new binary: %w", err)
	}
	return nil
}
