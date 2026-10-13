package httpserver

import (
	"compress/gzip"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

var gzipPool = sync.Pool{New: func() any {
	w, _ := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed)
	return w
}}

// gzipJSON compresses JSON answers under /api/ for clients that accept gzip. Media, ranges, HEAD
// and the torrent stream routes (video, proxies, thumbnails) are left alone; whether a response
// is JSON is only known once the handler starts writing, so the writer decides then.
func gzipJSON() gin.HandlerFunc {
	return func(c *gin.Context) {
		r := c.Request
		if r.Method == http.MethodHead || r.Header.Get("Range") != "" || !acceptsGzip(r.Header.Get("Accept-Encoding")) ||
			!strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/api/torrent-stream/") {
			c.Next()
			return
		}
		w := &gzipWriter{ResponseWriter: c.Writer}
		c.Writer = w
		defer func() {
			if w.gz != nil {
				w.gz.Close()
				w.gz.Reset(io.Discard)
				gzipPool.Put(w.gz)
			}
			c.Writer = w.ResponseWriter
		}()
		c.Next()
	}
}

// acceptsGzip reports whether an Accept-Encoding value allows gzip (and does not refuse it with q=0).
func acceptsGzip(v string) bool {
	for _, part := range strings.Split(v, ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(name), "gzip") {
			continue
		}
		if q, ok := strings.CutPrefix(strings.TrimSpace(params), "q="); ok {
			if f, err := strconv.ParseFloat(strings.TrimSpace(q), 64); err == nil && f == 0 {
				return false
			}
		}
		return true
	}
	return false
}

// addVary adds v to the Vary header unless it is already listed.
func addVary(h http.Header, v string) {
	for _, line := range h.Values("Vary") {
		for _, f := range strings.Split(line, ",") {
			if strings.EqualFold(strings.TrimSpace(f), v) {
				return
			}
		}
	}
	h.Add("Vary", v)
}

type gzipWriter struct {
	gin.ResponseWriter
	gz      *gzip.Writer // nil: written as is
	decided bool
}

// decide runs right before the headers go out.
func (w *gzipWriter) decide() {
	if w.decided {
		return
	}
	w.decided = true
	h := w.Header()
	if !strings.HasPrefix(strings.ToLower(h.Get("Content-Type")), "application/json") || h.Get("Content-Encoding") != "" {
		return
	}
	switch w.Status() {
	case http.StatusNoContent, http.StatusPartialContent, http.StatusNotModified:
		return
	}
	h.Set("Content-Encoding", "gzip")
	addVary(h, "Accept-Encoding")
	h.Del("Content-Length")
	w.gz = gzipPool.Get().(*gzip.Writer)
	w.gz.Reset(w.ResponseWriter)
}

func (w *gzipWriter) Write(b []byte) (int, error) {
	w.decide()
	if w.gz != nil {
		return w.gz.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

func (w *gzipWriter) WriteString(s string) (int, error) {
	w.decide()
	if w.gz != nil {
		return io.WriteString(w.gz, s)
	}
	return w.ResponseWriter.WriteString(s)
}

func (w *gzipWriter) WriteHeaderNow() {
	w.decide()
	w.ResponseWriter.WriteHeaderNow()
}

func (w *gzipWriter) Flush() {
	w.decide()
	if w.gz != nil {
		w.gz.Flush()
	}
	w.ResponseWriter.Flush()
}
