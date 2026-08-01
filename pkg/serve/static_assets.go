package serve

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
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
	// buildID is the bundle's identity, reported by GET /api/version. Empty
	// when the build predates the stamp, which the client reads as "cannot
	// tell" and leaves alone.
	buildID string
}

// newStaticServer selects the asset tree and wraps it so every response carries
// a validator and asks to be revalidated.
//
// The assets ship under fixed names (/app.js, /app.css) and the embedded
// filesystem reports a zero modtime, which makes net/http skip Last-Modified
// entirely — so the file server answered them with no validator and no cache
// directive at all, leaving the browser's heuristics in charge. A browser is
// then free to keep serving the shell it first saw, and an installed iOS PWA —
// which has its own cache container and no reload affordance — did exactly
// that: it kept the old interface until the icon was deleted and added again.
func newStaticServer() staticServer {
	if dir := os.Getenv("MOA_SERVE_STATIC_DIR"); dir != "" {
		// Watch mode rebuilds under the running server, so digesting the tree
		// once would pin stale validators. There the file server derives
		// Last-Modified from each entry, which revalidates on its own.
		return staticServer{
			handler: withRevalidation(http.FileServer(http.Dir(dir)), nil),
			buildID: readBuildID(os.DirFS(dir)),
		}
	}
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic("serve: embedded static filesystem missing 'static' subtree: " + err.Error())
	}
	return staticServer{
		handler: withRevalidation(http.FileServer(http.FS(sub)), assetETags(sub)),
		buildID: readBuildID(sub),
	}
}

// assetETags digests every embedded asset once, at startup: the tree is the
// frontend build (under a megabyte) and cannot change under a running binary.
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
func withRevalidation(next http.Handler, etags map[string]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if etag, ok := etags[assetPath(r.URL.Path)]; ok {
			// Set before delegating: net/http answers If-None-Match from the
			// ETag already on the response, so this is all the 304 needs.
			w.Header().Set("ETag", etag)
		}
		w.Header().Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
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
