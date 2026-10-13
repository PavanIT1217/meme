package server

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"
)

// staticFile is a frontend asset held in memory with a content hash used for
// cache-busting URLs (/static/app.js?v=<hash>) and ETags.
type staticFile struct {
	data []byte
	hash string
}

func loadStatic(web fs.FS) (map[string]staticFile, error) {
	files := map[string]staticFile{}
	err := fs.WalkDir(web, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(web, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		files[strings.TrimPrefix(p, "static/")] = staticFile{data: data, hash: hex.EncodeToString(sum[:])[:12]}
		return nil
	})
	return files, err
}

// assetURL returns the cache-busting URL for a static file.
func (s *Server) assetURL(name string) string {
	if f, ok := s.static[name]; ok {
		return "/static/" + name + "?v=" + f.hash
	}
	return "/static/" + name
}

// serveStatic serves an in-memory asset. Fingerprinted URLs are cached for a year;
// everything else revalidates via ETag after maxAge seconds.
func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request, name string, maxAge string) {
	f, ok := s.static[name]
	if !ok {
		s.renderNotFound(w, r)
		return
	}
	if v := r.URL.Query().Get("v"); v != "" && v == f.hash {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "public, max-age="+maxAge)
	}
	w.Header().Set("ETag", `"`+f.hash+`"`)
	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(f.data))
}

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	s.serveStatic(w, r, r.PathValue("file"), "3600")
}

// staticAt serves a fixed asset at a well-known root path (e.g. /favicon.ico).
func (s *Server) staticAt(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { s.serveStatic(w, r, name, "86400") }
}

// ---------- gzip ----------

var gzipPool = sync.Pool{New: func() any { w, _ := gzip.NewWriterLevel(io.Discard, gzip.DefaultCompression); return w }}

func compressible(contentType string) bool {
	ct, _, _ := mime.ParseMediaType(contentType)
	return strings.HasPrefix(ct, "text/") || ct == "application/json" || ct == "application/javascript" ||
		ct == "application/xml" || ct == "image/svg+xml" || ct == "application/manifest+json"
}

// gzipWriter compresses full (200 OK) responses of text-like content types.
type gzipWriter struct {
	http.ResponseWriter
	gz      *gzip.Writer
	decided bool
}

func (g *gzipWriter) WriteHeader(code int) {
	if !g.decided {
		g.decided = true
		h := g.Header()
		if code == http.StatusOK && h.Get("Content-Encoding") == "" && compressible(h.Get("Content-Type")) {
			h.Set("Content-Encoding", "gzip")
			h.Del("Content-Length")
			h.Add("Vary", "Accept-Encoding")
			gz := gzipPool.Get().(*gzip.Writer)
			gz.Reset(g.ResponseWriter)
			g.gz = gz
		}
	}
	g.ResponseWriter.WriteHeader(code)
}

func (g *gzipWriter) Write(b []byte) (int, error) {
	if !g.decided {
		if g.Header().Get("Content-Type") == "" {
			g.Header().Set("Content-Type", http.DetectContentType(b))
		}
		g.WriteHeader(http.StatusOK)
	}
	if g.gz != nil {
		return g.gz.Write(b)
	}
	return g.ResponseWriter.Write(b)
}

func (g *gzipWriter) close() {
	if g.gz != nil {
		g.gz.Close()
		gzipPool.Put(g.gz)
	}
}

func withGzip(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") || r.Method == http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipWriter{ResponseWriter: w}
		defer gw.close()
		next.ServeHTTP(gw, r)
	})
}
