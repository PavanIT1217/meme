// Package server implements the Meme Forge HTTP API, share pages and static file serving.
package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config controls how the server stores data and identifies itself.
type Config struct {
	DBPath        string   // SQLite database file
	UploadDir     string   // where uploaded templates and memes are written
	Admins        []string // usernames that may delete any template or meme
	BaseURL       string   // public URL, e.g. https://memes.example.com (optional)
	SecureCookies bool     // set the Secure flag on session cookies (use behind HTTPS)
	Static        fs.FS    // frontend files
}

type Server struct {
	cfg       Config
	db        *sql.DB
	admins    map[string]bool
	handler   http.Handler
	shareTmpl *template.Template
}

func New(cfg Config) (*Server, error) {
	db, err := openDB(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	for _, kind := range []string{"templates", "memes"} {
		if err := os.MkdirAll(filepath.Join(cfg.UploadDir, kind), 0o755); err != nil {
			db.Close()
			return nil, fmt.Errorf("create upload dir: %w", err)
		}
	}
	s := &Server{
		cfg:       cfg,
		db:        db,
		admins:    map[string]bool{},
		shareTmpl: template.Must(template.New("share").Parse(sharePageHTML)),
	}
	for _, a := range cfg.Admins {
		if a = strings.ToLower(strings.TrimSpace(a)); a != "" {
			s.admins[a] = true
		}
	}
	s.handler = s.middleware(s.routes())
	return s, nil
}

func (s *Server) Close() error { return s.db.Close() }

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

func (s *Server) routes() http.Handler {
	m := http.NewServeMux()

	m.HandleFunc("POST /api/auth/register", s.handleRegister)
	m.HandleFunc("POST /api/auth/login", s.handleLogin)
	m.HandleFunc("POST /api/auth/logout", s.handleLogout)
	m.HandleFunc("GET /api/me", s.handleMe)
	m.HandleFunc("GET /api/me/memes", s.requireUser(s.handleMyMemes))

	m.HandleFunc("GET /api/templates", s.handleListTemplates)
	m.HandleFunc("POST /api/templates", s.requireUser(s.handleCreateTemplate))
	m.HandleFunc("GET /api/templates/{id}", s.handleGetTemplate)
	m.HandleFunc("DELETE /api/templates/{id}", s.requireUser(s.handleDeleteTemplate))
	m.HandleFunc("PUT /api/templates/{id}/favorite", s.requireUser(s.handleFavorite(true)))
	m.HandleFunc("DELETE /api/templates/{id}/favorite", s.requireUser(s.handleFavorite(false)))
	m.HandleFunc("GET /api/tags", s.handleTags)

	m.HandleFunc("GET /api/memes", s.handleListMemes)
	m.HandleFunc("POST /api/memes", s.requireUser(s.handleCreateMeme))
	m.HandleFunc("GET /api/memes/{id}", s.handleGetMeme)
	m.HandleFunc("DELETE /api/memes/{id}", s.requireUser(s.handleDeleteMeme))
	m.HandleFunc("PUT /api/memes/{id}/like", s.requireUser(s.handleLike(true)))
	m.HandleFunc("DELETE /api/memes/{id}/like", s.requireUser(s.handleLike(false)))

	m.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})

	m.HandleFunc("GET /m/{id}", s.handleSharePage)

	uploads := http.StripPrefix("/uploads/", http.FileServer(http.Dir(s.cfg.UploadDir)))
	m.Handle("GET /uploads/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") { // no directory listings
			http.NotFound(w, r)
			return
		}
		// Uploaded files are user content: never let them run as a document.
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		uploads.ServeHTTP(w, r)
	}))

	m.Handle("/", http.FileServerFS(s.cfg.Static))
	return m
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")

		// CSRF defence in addition to SameSite cookies: state-changing requests
		// from a browser must come from our own origin.
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || u.Host != r.Host {
					writeError(w, http.StatusForbidden, "cross-origin request rejected")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// ---------- helpers ----------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func serverError(w http.ResponseWriter, err error) {
	logf("internal error: %v", err)
	writeError(w, http.StatusInternalServerError, "internal server error")
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

// pagination reads limit/offset query params with sane bounds.
func pagination(r *http.Request, def, max int) (limit, offset int) {
	limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ = strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 {
		limit = def
	}
	if limit > max {
		limit = max
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

// baseURL returns the public origin used for absolute links in share pages.
func (s *Server) baseURL(r *http.Request) string {
	if s.cfg.BaseURL != "" {
		return strings.TrimRight(s.cfg.BaseURL, "/")
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func absURL(base, u string) string {
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
		return u
	}
	return base + u
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

var errNotFound = errors.New("not found")
