package serve

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// buildIDFile is written by the frontend build next to the bundle and carries
// the same id esbuild compiles into it. Serving one and stamping the other from
// a single build is what lets a running client tell whether its code is the
// code this binary ships.
const buildIDFile = "build-id.txt"

// staticServer is the frontend build as it is served: the tree embedded in the
// binary, or the build output on disk when MOA_SERVE_STATIC_DIR points at it
// (frontend watch mode).
type staticServer struct {
	handler http.Handler
	// buildID returns the bundle's identity for GET /api/version. It is dynamic
	// for a disk tree because watch mode replaces the build under the server.
	buildID func() string
}

// newStaticServer selects the asset tree and wraps it so every response carries
// a validator and asks to be revalidated.
//
// The assets previously shipped under fixed names (/app.js, /app.css) and the
// embedded filesystem reports a zero modtime, which makes net/http skip
// Last-Modified entirely — so the file server answered them with no validator
// and no cache directive at all, leaving the browser's heuristics in charge. A browser is
// then free to keep serving the shell it first saw, and an installed iOS PWA —
// which has its own cache container and no reload affordance — did exactly
// that: it kept the old interface until the icon was deleted and added again.
func newStaticServer() staticServer {
	if dir := os.Getenv("MOA_SERVE_STATIC_DIR"); dir != "" {
		assets := os.DirFS(dir)
		buildID := func() string { return readBuildID(assets) }
		// Watch mode rebuilds under the running server. Avoid filesystem
		// timestamp granularity and read the stamp on each version request.
		return staticServer{
			handler: withBuildRouting(withNoStore(http.FileServer(http.Dir(dir))), buildID),
			buildID: buildID,
		}
	}
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic("serve: embedded static filesystem missing 'static' subtree: " + err.Error())
	}
	buildID := readBuildID(sub)
	buildIDProvider := func() string { return buildID }
	etags := assetETags(sub)
	files := withStaticGzip(http.FileServer(http.FS(sub)), newGzipAssets(sub), etags)
	return staticServer{
		handler: withBuildRouting(withRevalidation(files, etags), buildIDProvider),
		buildID: buildIDProvider,
	}
}

// assetETags digests every embedded asset once, at startup: the frontend tree
// is small and cannot change under a running binary.
func assetETags(assets fs.FS) map[string]string {
	etags := make(map[string]string)
	err := fs.WalkDir(assets, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // an unreadable asset just goes without a validator
		}
		data, err := fs.ReadFile(assets, p)
		if err != nil {
			return nil //nolint:nilerr
		}
		sum := sha256.Sum256(data)
		etags[p] = `"sha256-` + hex.EncodeToString(sum[:]) + `"`
		return nil
	})
	if err != nil {
		return nil
	}
	return etags
}

// readBuildID returns the id the frontend build stamped into the bundle, or ""
// when the file is absent.
func readBuildID(assets fs.FS) string {
	data, err := fs.ReadFile(assets, buildIDFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// withRevalidation gives each asset a content ETag and asks the browser to
// revalidate before reusing it. no-cache still serves from cache — it only
// forbids doing so without asking — so an unchanged asset costs a 304 and the
// bytes are not resent.
//
// A file requested under the CURRENT build id (see currentBuildRequest) is the
// exception: its URL names its content, so it is cached for good.
func withRevalidation(next http.Handler, etags map[string]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		etag, known := etags[assetPath(r.URL.Path)]
		if known {
			// Set before delegating: net/http answers If-None-Match from the
			// ETag already on the response, so this is all the 304 needs.
			w.Header().Set("ETag", etag)
		}
		if known && isCurrentBuildRequest(r) {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		next.ServeHTTP(w, r)
	})
}

type currentBuildKey struct{}

// isCurrentBuildRequest reports whether the request named a file of the build
// this server is publishing, under that build's own id. The id covers the
// whole tree, so the bytes behind that URL can never change. A request that
// reached its file by rebasing a stale id (see buildAssetPath) is NOT one: it
// is answered with the current build's file, and caching that under the old id
// would pin the old URL to the wrong bytes.
func isCurrentBuildRequest(r *http.Request) bool {
	v, _ := r.Context().Value(currentBuildKey{}).(bool)
	return v
}

// gzipAssets compresses the embedded text assets the first time they are asked
// for and keeps the result: the tree cannot change under a running binary, so
// each file is compressed once, at the strongest level, instead of per request.
type gzipAssets struct {
	fsys  fs.FS
	mu    sync.Mutex
	cache map[string][]byte // nil value: not worth compressing
}

func newGzipAssets(fsys fs.FS) *gzipAssets {
	return &gzipAssets{fsys: fsys, cache: make(map[string][]byte)}
}

// compressibleAsset says which files gain from gzip. Images and fonts are
// already compressed formats.
func compressibleAsset(key string) bool {
	switch path.Ext(key) {
	case ".js", ".css", ".html", ".svg", ".json", ".webmanifest", ".wasm", ".map", ".txt":
		return true
	}
	return false
}

// minGzipBytes is the size below which the headers outweigh the saving.
const minGzipBytes = 1024

func (g *gzipAssets) get(key string) []byte {
	g.mu.Lock()
	defer g.mu.Unlock()
	if data, ok := g.cache[key]; ok {
		return data
	}
	var out []byte
	if raw, err := fs.ReadFile(g.fsys, key); err == nil && len(raw) >= minGzipBytes {
		var buf bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		_, werr := zw.Write(raw)
		if cerr := zw.Close(); werr == nil && cerr == nil && buf.Len() < len(raw) {
			out = buf.Bytes()
		}
	}
	g.cache[key] = out
	return out
}

// withStaticGzip answers a plain GET of a compressible embedded asset with its
// gzip form when the client accepts it. It only ever sees the file server's
// routes: sockets and event streams never pass through here.
//
// The gzip form carries its own ETag (the plain one plus a suffix) because a
// strong validator names exact bytes, and a client that stored the compressed
// body must revalidate it against the compressed variant. Range requests and
// anything not in the tree fall through to the plain file server.
func withStaticGzip(next http.Handler, assets *gzipAssets, etags map[string]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := assetPath(r.URL.Path)
		if _, known := etags[key]; !known || !compressibleAsset(key) || strings.HasSuffix(r.URL.Path, "/") {
			next.ServeHTTP(w, r)
			return
		}
		h := w.Header()
		h.Add("Vary", "Accept-Encoding")
		if (r.Method != http.MethodGet && r.Method != http.MethodHead) ||
			r.Header.Get("Range") != "" || !acceptsGzip(r.Header.Get("Accept-Encoding")) {
			next.ServeHTTP(w, r)
			return
		}
		data := assets.get(key)
		if data == nil {
			next.ServeHTTP(w, r)
			return
		}
		h.Set("Content-Encoding", "gzip")
		if etag := h.Get("ETag"); etag != "" {
			h.Set("ETag", strings.TrimSuffix(etag, `"`)+`-gzip"`)
		}
		if h.Get("Content-Type") == "" {
			if ct := mime.TypeByExtension(path.Ext(key)); ct != "" {
				h.Set("Content-Type", ct)
			}
		}
		// ServeContent leaves the length out when a Content-Encoding is set.
		h.Set("Content-Length", strconv.Itoa(len(data)))
		http.ServeContent(w, r, key, time.Time{}, bytes.NewReader(data))
	})
}

// withNoStore keeps a mutable development tree out of the HTTP cache. Watch
// rebuilds can happen more than once within Last-Modified's one-second
// resolution, so validators derived from filesystem timestamps are unsafe.
func withNoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// withBuildRouting resolves the shell and bundle aliases through build-id.txt,
// the single publication pointer written after a complete frontend build. The
// files themselves live under /build/<id>/, so an old shell and a new deploy
// can be served concurrently without either receiving the other's bundle.
func withBuildRouting(next http.Handler, buildID func() string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := buildID()
		target := buildAssetPath(r.URL.Path, id)
		if target == r.URL.Path {
			if validBuildID(id) && strings.HasPrefix(r.URL.Path, "/build/"+id+"/") {
				r = r.WithContext(context.WithValue(r.Context(), currentBuildKey{}, true))
			}
			next.ServeHTTP(w, r)
			return
		}
		routed := new(http.Request)
		*routed = *r
		u := *r.URL
		u.Path = target
		u.RawPath = ""
		routed.URL = &u
		next.ServeHTTP(w, routed)
	})
}

func buildAssetPath(requestPath, buildID string) string {
	if !validBuildID(buildID) {
		return requestPath
	}
	prefix := "/build/" + buildID + "/"
	switch requestPath {
	case "/", "/index.html":
		return prefix + "shell.html"
	case "/app.js":
		return prefix + "app.js"
	case "/app.css":
		return prefix + "app.css"
	}
	if strings.HasPrefix(requestPath, "/build/") {
		parts := strings.SplitN(strings.TrimPrefix(requestPath, "/build/"), "/", 2)
		// A response remains at its requested URL: when a stale app.js is
		// rebased, its relative imports still resolve below the stale build ID.
		// Rebase the complete tree, not a fixed entry-file allowlist, so every
		// split chunk, lazy catalog entry, stylesheet, and source map resolves
		// from the same current build. This is intentionally generic because
		// esbuild owns output names and may add more split assets at any time.
		if len(parts) == 2 && validBuildID(parts[0]) {
			asset := strings.TrimPrefix(path.Clean("/"+parts[1]), "/")
			if asset != "" && asset != "." {
				return prefix + asset
			}
		}
	}
	return requestPath
}

func validBuildID(id string) bool {
	if len(id) != 12 {
		return false
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// assetPath maps a request path to its key in the asset tree, resolving
// directories to the shell the file server would serve for them.
func assetPath(p string) string {
	clean := strings.TrimPrefix(path.Clean("/"+p), "/")
	if clean == "" || clean == "." || strings.HasSuffix(p, "/") {
		return path.Join(clean, "index.html")
	}
	return clean
}
