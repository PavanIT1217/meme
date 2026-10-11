package server

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	sessionCookie = "session"
	sessionTTL    = 30 * 24 * time.Hour
)

type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	IsAdmin  bool   `json:"is_admin"`
}

var usernameRe = regexp.MustCompile(`^[A-Za-z0-9_]{3,24}$`)

// Compared against when a username doesn't exist, so login timing doesn't reveal which usernames are taken.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("not-a-real-password"), bcrypt.DefaultCost)

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if !decodeJSON(w, r, &c) {
		return
	}
	c.Username = strings.TrimSpace(c.Username)
	if !usernameRe.MatchString(c.Username) {
		writeError(w, http.StatusBadRequest, "username must be 3-24 letters, digits or underscores")
		return
	}
	if len(c.Password) < 8 || len(c.Password) > 72 {
		writeError(w, http.StatusBadRequest, "password must be 8-72 characters")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(c.Password), bcrypt.DefaultCost)
	if err != nil {
		serverError(w, err)
		return
	}
	res, err := s.db.Exec(`INSERT INTO users (username, password_hash, created_at) VALUES (?, ?, ?)`,
		c.Username, string(hash), now())
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "that username is taken")
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}
	id, _ := res.LastInsertId()
	if err := s.startSession(w, id); err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"user": s.makeUser(id, c.Username)})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if !decodeJSON(w, r, &c) {
		return
	}
	var (
		id       int64
		username string
		hash     string
	)
	err := s.db.QueryRow(`SELECT id, username, password_hash FROM users WHERE username = ?`,
		strings.TrimSpace(c.Username)).Scan(&id, &username, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		bcrypt.CompareHashAndPassword(dummyHash, []byte(c.Password))
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(c.Password)) != nil {
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	if err := s.startSession(w, id); err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": s.makeUser(id, username)})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, hashToken(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.cfg.SecureCookies, SameSite: http.SameSiteLaxMode})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u, err := s.userFrom(r)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": u})
}

func (s *Server) startSession(w http.ResponseWriter, userID int64) error {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return err
	}
	token := base64.RawURLEncoding.EncodeToString(buf)
	expires := time.Now().Add(sessionTTL)
	// Opportunistically clear out expired sessions.
	s.db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, now())
	if _, err := s.db.Exec(`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES (?, ?, ?)`,
		hashToken(token), userID, expires.Unix()); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", Expires: expires,
		HttpOnly: true, Secure: s.cfg.SecureCookies, SameSite: http.SameSiteLaxMode,
	})
	return nil
}

// userFrom returns the logged-in user, or nil if the request has no valid session.
func (s *Server) userFrom(r *http.Request) (*User, error) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil, nil
	}
	var (
		id       int64
		username string
	)
	err = s.db.QueryRow(`SELECT u.id, u.username FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > ?`, hashToken(c.Value), now()).Scan(&id, &username)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.makeUser(id, username), nil
}

func (s *Server) makeUser(id int64, username string) *User {
	return &User{ID: id, Username: username, IsAdmin: s.admins[strings.ToLower(username)]}
}

func (s *Server) requireUser(h func(http.ResponseWriter, *http.Request, *User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, err := s.userFrom(r)
		if err != nil {
			serverError(w, err)
			return
		}
		if u == nil {
			writeError(w, http.StatusUnauthorized, "log in first")
			return
		}
		h(w, r, u)
	}
}

// userID returns the current user's id, or 0 when logged out (used for "liked"/"favorite" flags).
func (s *Server) userID(r *http.Request) int64 {
	if u, _ := s.userFrom(r); u != nil {
		return u.ID
	}
	return 0
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
