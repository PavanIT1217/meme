package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

const (
	sessionCookie = "session"
	sessionTTL    = 30 * 24 * time.Hour
	oauthCookie   = "oauth_state"
	oauthPath     = "/auth/google"
)

// GoogleConfig holds OAuth client credentials from the Google Cloud console.
// The endpoint URLs only need setting in tests.
type GoogleConfig struct {
	ClientID     string
	ClientSecret string
	AuthURL      string
	TokenURL     string
	UserInfoURL  string
}

type User struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	AvatarURL string `json:"avatar_url"`
	IsAdmin   bool   `json:"is_admin"`
}

var usernameRe = regexp.MustCompile(`^[A-Za-z0-9_]{3,24}$`)

func (s *Server) googleEnabled() bool {
	return s.cfg.Google.ClientID != "" && s.cfg.Google.ClientSecret != ""
}

func (s *Server) oauthConfig(r *http.Request) *oauth2.Config {
	g := s.cfg.Google
	ep := oauth2.Endpoint{
		AuthURL:   "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL:  "https://oauth2.googleapis.com/token",
		AuthStyle: oauth2.AuthStyleInParams,
	}
	if g.AuthURL != "" {
		ep.AuthURL = g.AuthURL
	}
	if g.TokenURL != "" {
		ep.TokenURL = g.TokenURL
	}
	return &oauth2.Config{
		ClientID:     g.ClientID,
		ClientSecret: g.ClientSecret,
		Endpoint:     ep,
		RedirectURL:  s.baseURL(r) + oauthPath + "/callback",
		Scopes:       []string{"openid", "email", "profile"},
	}
}

// oauthState is kept in a short-lived HttpOnly cookie between the redirect to Google and the callback.
type oauthState struct {
	State    string `json:"s"`
	Verifier string `json:"v"` // PKCE code verifier
	Next     string `json:"n"` // where to send the user afterwards
}

// handleGoogleLogin starts the OAuth flow: GET /auth/google/login?next=/#/feed
func (s *Server) handleGoogleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.googleEnabled() {
		http.Error(w, "Google sign-in is not configured on this server.", http.StatusNotFound)
		return
	}
	st := oauthState{State: randomToken(), Verifier: oauth2.GenerateVerifier(), Next: safeNext(r.URL.Query().Get("next"))}
	raw, _ := json.Marshal(st)
	http.SetCookie(w, &http.Cookie{
		Name: oauthCookie, Value: base64.RawURLEncoding.EncodeToString(raw), Path: oauthPath,
		MaxAge: 600, HttpOnly: true, Secure: s.cfg.SecureCookies, SameSite: http.SameSiteLaxMode,
	})
	url := s.oauthConfig(r).AuthCodeURL(st.State,
		oauth2.S256ChallengeOption(st.Verifier),
		oauth2.SetAuthURLParam("prompt", "select_account"))
	http.Redirect(w, r, url, http.StatusFound)
}

// handleGoogleCallback finishes the flow, creates or updates the account and starts a session.
func (s *Server) handleGoogleCallback(w http.ResponseWriter, r *http.Request) {
	fail := func(reason string, err error) {
		if err != nil {
			logf("google login failed (%s): %v", reason, err)
		}
		http.Redirect(w, r, "/?login=failed", http.StatusFound)
	}
	if !s.googleEnabled() {
		http.NotFound(w, r)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: oauthCookie, Value: "", Path: oauthPath, MaxAge: -1,
		HttpOnly: true, Secure: s.cfg.SecureCookies, SameSite: http.SameSiteLaxMode})

	var st oauthState
	c, err := r.Cookie(oauthCookie)
	if err == nil {
		var raw []byte
		if raw, err = base64.RawURLEncoding.DecodeString(c.Value); err == nil {
			err = json.Unmarshal(raw, &st)
		}
	}
	q := r.URL.Query()
	if err != nil || st.State == "" || subtle.ConstantTimeCompare([]byte(st.State), []byte(q.Get("state"))) != 1 {
		fail("state mismatch", err)
		return
	}
	if q.Get("error") != "" { // e.g. the user pressed Cancel on Google's screen
		http.Redirect(w, r, st.Next, http.StatusFound)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	cfg := s.oauthConfig(r)
	tok, err := cfg.Exchange(ctx, q.Get("code"), oauth2.VerifierOption(st.Verifier))
	if err != nil {
		fail("code exchange", err)
		return
	}
	info, err := s.fetchGoogleUser(ctx, cfg.Client(ctx, tok))
	if err != nil {
		fail("userinfo", err)
		return
	}
	id, err := s.upsertGoogleUser(info)
	if err != nil {
		fail("save user", err)
		return
	}
	if err := s.startSession(w, id); err != nil {
		fail("session", err)
		return
	}
	http.Redirect(w, r, st.Next, http.StatusFound)
}

type googleUser struct {
	Sub     string `json:"sub"`
	Email   string `json:"email"`
	Name    string `json:"name"`
	Picture string `json:"picture"`
}

func (s *Server) fetchGoogleUser(ctx context.Context, client *http.Client) (googleUser, error) {
	url := s.cfg.Google.UserInfoURL
	if url == "" {
		url = "https://openidconnect.googleapis.com/v1/userinfo"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return googleUser{}, err
	}
	res, err := client.Do(req)
	if err != nil {
		return googleUser{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return googleUser{}, fmt.Errorf("userinfo status %d", res.StatusCode)
	}
	var u googleUser
	if err := json.NewDecoder(res.Body).Decode(&u); err != nil {
		return u, err
	}
	if u.Sub == "" {
		return u, errors.New("userinfo has no sub")
	}
	return u, nil
}

// upsertGoogleUser returns the user ID for a Google account, creating the user on first sign-in.
func (s *Server) upsertGoogleUser(g googleUser) (int64, error) {
	if !strings.HasPrefix(g.Picture, "https://") {
		g.Picture = ""
	}
	base := usernameBase(g.Name, g.Email)
	for attempt := 0; attempt < 10; attempt++ {
		var id int64
		err := s.db.QueryRow(`SELECT id FROM users WHERE google_sub = ?`, g.Sub).Scan(&id)
		if err == nil {
			_, err = s.db.Exec(`UPDATE users SET email = ?, avatar_url = ? WHERE id = ?`, g.Email, g.Picture, id)
			return id, err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		username := base
		if attempt > 0 { // name taken: add a random suffix
			username = truncate(base, 19) + "_" + randomDigits(4)
		}
		res, err := s.db.Exec(`INSERT INTO users (username, google_sub, email, avatar_url, created_at) VALUES (?, ?, ?, ?, ?)`,
			username, g.Sub, g.Email, g.Picture, now())
		if err == nil {
			return res.LastInsertId()
		}
		if !isUniqueViolation(err) {
			return 0, err
		}
		// Either the username is taken, or a concurrent sign-in created this Google user; loop handles both.
	}
	return 0, errors.New("could not pick a unique username")
}

var nonWordRe = regexp.MustCompile(`[^A-Za-z0-9]+`)

// usernameBase derives a valid username from a Google display name or email address.
func usernameBase(name, email string) string {
	for _, cand := range []string{name, strings.SplitN(email, "@", 2)[0]} {
		u := strings.Trim(nonWordRe.ReplaceAllString(cand, "_"), "_")
		if u = truncate(u, 24); len(u) >= 3 {
			return u
		}
	}
	return "user_" + randomDigits(4)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return strings.TrimRight(s[:n], "_")
	}
	return s
}

// safeNext only allows same-site relative paths, to avoid open redirects.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return "/"
	}
	return next
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
	writeJSON(w, http.StatusOK, map[string]any{"user": u, "google_login": s.googleEnabled()})
}

// handleUpdateMe lets users change their public username: PATCH /api/me {"username": "..."}
func (s *Server) handleUpdateMe(w http.ResponseWriter, r *http.Request, u *User) {
	var req struct {
		Username string `json:"username"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if !usernameRe.MatchString(req.Username) {
		writeError(w, http.StatusBadRequest, "username must be 3-24 letters, digits or underscores")
		return
	}
	_, err := s.db.Exec(`UPDATE users SET username = ? WHERE id = ?`, req.Username, u.ID)
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "that username is taken")
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}
	updated, err := s.userFrom(r)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": updated})
}

func (s *Server) startSession(w http.ResponseWriter, userID int64) error {
	token := randomToken()
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
	u := &User{}
	err = s.db.QueryRow(`SELECT u.id, u.username, u.avatar_url FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > ?`, hashToken(c.Value), now()).Scan(&u.ID, &u.Username, &u.AvatarURL)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	u.IsAdmin = s.admins[strings.ToLower(u.Username)]
	return u, nil
}

func (s *Server) requireUser(h func(http.ResponseWriter, *http.Request, *User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, err := s.userFrom(r)
		if err != nil {
			serverError(w, err)
			return
		}
		if u == nil {
			writeError(w, http.StatusUnauthorized, "sign in first")
			return
		}
		h(w, r, u)
	}
}

func randomToken() string {
	buf := make([]byte, 32)
	rand.Read(buf)
	return base64.RawURLEncoding.EncodeToString(buf)
}

func randomDigits(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		d, _ := rand.Int(rand.Reader, big.NewInt(10))
		b.WriteByte(byte('0' + d.Int64()))
	}
	return b.String()
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
