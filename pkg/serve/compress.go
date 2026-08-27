package serve

import (
	"compress/gzip"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// gzipWriterPool avoids allocating a compressor per request on the read routes
// a client polls (the session roster is fetched on every reconnect).
var gzipWriterPool = sync.Pool{
	New: func() any { return gzip.NewWriter(io.Discard) },
}

// withGzip compresses a JSON read route when the client advertises gzip. Only
// the large read responses use it — the session roster and the two history
// projections shrink by 64-82% — because compressing a 200-byte POST ack costs
// CPU for nothing. The WebSocket is deliberately NOT wrapped: it negotiates
// permessage-deflate on its own (see wsAcceptOptions), and gzipping the upgrade
// response would break it.
func withGzip(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Vary regardless of what this particular client accepts: the same URL
		// legitimately answers compressed and plain, and a shared cache must not
		// serve one to a client that asked for the other.
		w.Header().Add("Vary", "Accept-Encoding")
		if !acceptsGzip(r.Header.Get("Accept-Encoding")) {
			next(w, r)
			return
		}
		gz := gzipWriterPool.Get().(*gzip.Writer)
		gz.Reset(w)
		defer func() {
			_ = gz.Close()
			gzipWriterPool.Put(gz)
		}()
		w.Header().Set("Content-Encoding", "gzip")
		// A Content-Length computed for the plain body would describe the wrong
		// stream, so the wrapper drops it and the response is chunked.
		w.Header().Del("Content-Length")
		next(&gzipResponseWriter{ResponseWriter: w, writer: gz}, r)
	}
}

// acceptsGzip reports whether the header offers gzip with a non-zero quality.
// The q value is parsed as a number: "gzip;q=0" is an explicit refusal, but
// "gzip;q=0.01" is a (weak) acceptance, and comparing string prefixes gets that
// backwards. A malformed q is treated as acceptance, matching the permissive
// reading servers apply to a header they cannot parse.
func acceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		fields := strings.Split(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(fields[0]), "gzip") {
			continue
		}
		for _, param := range fields[1:] {
			key, value, found := strings.Cut(strings.TrimSpace(param), "=")
			if !found || !strings.EqualFold(strings.TrimSpace(key), "q") {
				continue
			}
			quality, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
			if err == nil && quality <= 0 {
				return false
			}
		}
		return true
	}
	return false
}

// gzipResponseWriter routes the body through the compressor while leaving
// headers and status code to the underlying writer.
type gzipResponseWriter struct {
	http.ResponseWriter
	writer *gzip.Writer
}

func (w *gzipResponseWriter) Write(p []byte) (int, error) { return w.writer.Write(p) }

// Flush pushes buffered compressed bytes to the client. Handlers wrapped here
// don't stream, but http.ResponseController-based flushing must not silently
// leave the tail of a response inside the compressor.
func (w *gzipResponseWriter) Flush() {
	_ = w.writer.Flush()
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Unwrap exposes the real writer to http.NewResponseController (used by
// bodyTimeoutMiddleware to set read deadlines).
func (w *gzipResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
