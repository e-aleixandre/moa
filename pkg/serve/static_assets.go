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
	return staticServer{
		handler: withBuildRouting(withRevalidation(http.FileServer(http.FS(sub)), assetETags(sub)), buildIDProvider),
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
		target := buildAssetPath(r.URL.Path, buildID())
		if target == r.URL.Path {
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
		if len(parts) == 2 {
			switch parts[1] {
			case "shell.html", "app.js", "app.css", "app.js.map", "app.css.map":
				return prefix + parts[1]
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
