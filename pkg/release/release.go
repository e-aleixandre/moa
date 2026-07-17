// Package release contains build metadata and best-effort release update checks.
package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	githubLatestURL = "https://api.github.com/repos/ealeixandre/moa/releases/latest"
	// CheckInterval bounds both persistent-cache freshness and UI refreshes.
	// It deliberately keeps GitHub traffic low for short-lived TUI processes.
	CheckInterval  = 6 * time.Hour
	requestTimeout = 5 * time.Second
)

// Info is build metadata injected by the release build. Development builds use
// the default "dev" version and deliberately never make update requests.
type Info struct {
	Version string
	Commit  string
	Date    string
}

// DisplayVersion returns the conventional v-prefixed version, or the raw build
// label when it is not a release version.
func (i Info) DisplayVersion() string {
	if _, ok := ParseSemver(i.Version); ok {
		return "v" + strings.TrimPrefix(i.Version, "v")
	}
	return i.Version
}

// String is a concise human-readable build identifier.
func (i Info) String() string {
	commit, date := i.Commit, i.Date
	if commit == "" {
		commit = "none"
	}
	if date == "" {
		date = "unknown"
	}
	return fmt.Sprintf("%s (commit %s, built %s)", i.DisplayVersion(), commit, date)
}

// IsRelease reports whether this build has a valid semantic release version.
func (i Info) IsRelease() bool { _, ok := ParseSemver(i.Version); return ok }

// Semver is a parsed semantic version. Build metadata is ignored for ordering.
type Semver struct {
	Major, Minor, Patch int
	Pre                 string
}

// ParseSemver parses SemVer 2.0.0 versions with an optional v prefix.
func ParseSemver(s string) (Semver, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if build := strings.IndexByte(s, '+'); build >= 0 {
		if !validIdentifiers(s[build+1:]) || strings.Count(s, "+") != 1 {
			return Semver{}, false
		}
		s = s[:build]
	}
	main, pre, hasPre := strings.Cut(s, "-")
	parts := strings.Split(main, ".")
	if len(parts) != 3 || (hasPre && !validPrereleaseIdentifiers(pre)) {
		return Semver{}, false
	}
	values := [3]int{}
	for n, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return Semver{}, false
		}
		v, err := strconv.Atoi(part)
		if err != nil || v < 0 || strconv.Itoa(v) != part {
			return Semver{}, false
		}
		values[n] = v
	}
	return Semver{Major: values[0], Minor: values[1], Patch: values[2], Pre: pre}, true
}

func validIdentifiers(s string) bool {
	if s == "" {
		return false
	}
	for _, id := range strings.Split(s, ".") {
		if id == "" {
			return false
		}
		for _, r := range id {
			if (r < '0' || r > '9') && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && r != '-' {
				return false
			}
		}
	}
	return true
}

func validPrereleaseIdentifiers(s string) bool {
	if !validIdentifiers(s) {
		return false
	}
	for _, id := range strings.Split(s, ".") {
		if len(id) > 1 && id[0] == '0' && numeric(id) {
			return false
		}
	}
	return true
}

func numeric(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Compare returns -1, 0, or 1 according to SemVer precedence.
func (v Semver) Compare(other Semver) int {
	for _, pair := range [][2]int{{v.Major, other.Major}, {v.Minor, other.Minor}, {v.Patch, other.Patch}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	if v.Pre == other.Pre {
		return 0
	}
	if v.Pre == "" {
		return 1
	}
	if other.Pre == "" {
		return -1
	}
	a, b := strings.Split(v.Pre, "."), strings.Split(other.Pre, ".")
	for n := 0; n < len(a) && n < len(b); n++ {
		if a[n] == b[n] {
			continue
		}
		an, bn := numeric(a[n]), numeric(b[n])
		if an && bn {
			ai, _ := strconv.Atoi(a[n])
			bi, _ := strconv.Atoi(b[n])
			if ai < bi {
				return -1
			}
			return 1
		}
		if an {
			return -1
		}
		if bn {
			return 1
		}
		if a[n] < b[n] {
			return -1
		}
		return 1
	}
	if len(a) < len(b) {
		return -1
	}
	return 1
}

// Result is safe to display even when the check was unavailable.
type Result struct {
	Current         string `json:"current"`
	Latest          string `json:"latest,omitempty"`
	UpdateAvailable bool   `json:"update_available"`
}

type diskCache struct {
	Latest  string    `json:"latest"`
	ETag    string    `json:"etag,omitempty"`
	Checked time.Time `json:"checked"`
}

// Checker checks GitHub's public releases API. It has no telemetry: the only
// network request is to api.github.com, and can be disabled by callers.
type Checker struct {
	Info      Info
	Client    *http.Client
	URL       string // exported for deterministic tests; empty uses GitHub.
	CachePath string
	Now       func() time.Time
	mu        sync.Mutex
}

// NewChecker creates a checker using ~/.config/moa/update.json (or
// MOA_CONFIG_DIR) as its shared persistent cache.
func NewChecker(info Info) *Checker {
	dir := os.Getenv("MOA_CONFIG_DIR")
	if dir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, ".config", "moa")
		}
	}
	cachePath := ""
	if dir != "" {
		cachePath = filepath.Join(dir, "update.json")
	}
	return &Checker{Info: info, Client: &http.Client{Timeout: requestTimeout}, URL: githubLatestURL, CachePath: cachePath, Now: time.Now}
}

// Check returns a release result. Errors are intentionally returned to the
// caller so an interactive frontend can ignore them silently.
func (c *Checker) Check(ctx context.Context) (Result, error) {
	result := Result{Current: c.Info.DisplayVersion()}
	current, ok := ParseSemver(c.Info.Version)
	if !ok || os.Getenv("MOA_NO_UPDATE_CHECK") == "1" {
		return result, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.Now
	if now == nil {
		now = time.Now
	}
	cache := c.readCache()
	cached := compareResult(result, current, cache.Latest)
	if cache.Latest != "" && now().Sub(cache.Checked) < CheckInterval {
		return cached, nil
	}
	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, c.url(), nil)
	if err != nil {
		return result, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "moa/"+c.Info.DisplayVersion())
	if cache.ETag != "" {
		req.Header.Set("If-None-Match", cache.ETag)
	}
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: requestTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return cached, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotModified && cache.Latest != "" {
		cache.Checked = now()
		c.writeCache(cache)
		return compareResult(result, current, cache.Latest), nil
	}
	if resp.StatusCode != http.StatusOK {
		return cached, fmt.Errorf("release check: %s", resp.Status)
	}
	var payload struct {
		TagName    string `json:"tag_name"`
		Prerelease bool   `json:"prerelease"`
		Draft      bool   `json:"draft"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return cached, err
	}
	latest, valid := ParseSemver(payload.TagName)
	if !valid || latest.Pre != "" || payload.Prerelease || payload.Draft {
		return cached, errors.New("release check: invalid stable release")
	}
	cache = diskCache{Latest: "v" + strings.TrimPrefix(payload.TagName, "v"), ETag: resp.Header.Get("ETag"), Checked: now()}
	c.writeCache(cache)
	return compareResult(result, current, cache.Latest), nil
}

func (c *Checker) url() string {
	if c.URL != "" {
		return c.URL
	}
	return githubLatestURL
}
func (c *Checker) readCache() (cache diskCache) {
	if c.CachePath == "" {
		return
	}
	data, err := os.ReadFile(c.CachePath)
	if err == nil {
		_ = json.Unmarshal(data, &cache)
	}
	return
}
func (c *Checker) writeCache(cache diskCache) {
	if c.CachePath == "" {
		return
	}
	if os.MkdirAll(filepath.Dir(c.CachePath), 0700) != nil {
		return
	}
	data, err := json.Marshal(cache)
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(c.CachePath), ".update-*")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after a successful rename
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	_ = os.Rename(tmpName, c.CachePath)
}
func compareResult(result Result, current Semver, latest string) Result {
	if v, ok := ParseSemver(latest); ok && current.Compare(v) < 0 {
		result.Latest = "v" + strings.TrimPrefix(latest, "v")
		result.UpdateAvailable = true
	}
	return result
}
