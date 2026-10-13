package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pavanit1217/meme"
)

func newTestServer(t *testing.T, opts ...func(*Config)) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	google := fakeGoogle(t)
	cfg := Config{
		DBPath:    filepath.Join(dir, "test.db"),
		UploadDir: filepath.Join(dir, "uploads"),
		Web:       webFS(t),
		Limits:    DefaultLimits(),
		Google: GoogleConfig{
			ClientID: "test-client", ClientSecret: "test-secret",
			AuthURL: google.URL + "/auth", TokenURL: google.URL + "/token", UserInfoURL: google.URL + "/userinfo",
		},
	}
	for _, o := range opts {
		o(&cfg)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s)
	t.Cleanup(func() { ts.Close(); s.Close() })
	return ts
}

// webFS is the real embedded frontend, so page templates are exercised by the tests.
func webFS(t *testing.T) fs.FS {
	sub, err := fs.Sub(meme.Web, "web")
	if err != nil {
		t.Fatal(err)
	}
	return sub
}

func withAdmins(names ...string) func(*Config) { return func(c *Config) { c.Admins = names } }

// fakeGoogle is a minimal OAuth provider. The authorization "code" is the base64 JSON
// of the Google profile to sign in as; the token endpoint echoes it back as the access token.
func fakeGoogle(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("client_secret") != "test-secret" || r.Form.Get("code_verifier") == "" {
			t.Errorf("token request missing client secret or PKCE verifier: %v", r.Form)
			http.Error(w, `{"error":"invalid_request"}`, 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": r.Form.Get("code"), "token_type": "Bearer", "expires_in": 3600})
	})
	mux.HandleFunc("GET /userinfo", func(w http.ResponseWriter, r *http.Request) {
		raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if err != nil {
			http.Error(w, "bad token", 401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(raw)
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

// client is a browser-like client with its own cookie jar. It does not follow redirects.
type client struct {
	t    *testing.T
	base string
	http *http.Client
}

func newClient(t *testing.T, ts *httptest.Server) *client {
	jar, _ := cookiejar.New(nil)
	return &client{t: t, base: ts.URL, http: &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (c *client) get(path string) *http.Response {
	c.t.Helper()
	res, err := c.http.Get(c.base + path)
	if err != nil {
		c.t.Fatal(err)
	}
	res.Body.Close()
	return res
}

func (c *client) do(method, path string, body io.Reader, contentType string) (int, map[string]any) {
	c.t.Helper()
	req, _ := http.NewRequest(method, c.base+path, body)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func (c *client) json(method, path string, v any) (int, map[string]any) {
	var body io.Reader
	if v != nil {
		b, _ := json.Marshal(v)
		body = bytes.NewReader(b)
	}
	return c.do(method, path, body, "application/json")
}

func (c *client) upload(path string, img []byte, fields map[string]string) (int, map[string]any) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	fw, _ := mw.CreateFormFile("image", "x.png")
	fw.Write(img)
	mw.Close()
	return c.do("POST", path, &buf, mw.FormDataContentType())
}

// startGoogleLogin follows /auth/google/login and returns the provider redirect URL.
func (c *client) startGoogleLogin(next string) *url.URL {
	c.t.Helper()
	res := c.get("/auth/google/login?next=" + url.QueryEscape(next))
	if res.StatusCode != http.StatusFound {
		c.t.Fatalf("login start: status %d", res.StatusCode)
	}
	loc, _ := url.Parse(res.Header.Get("Location"))
	return loc
}

func googleCode(g googleUser) string {
	b, _ := json.Marshal(g)
	return base64.RawURLEncoding.EncodeToString(b)
}

// loginAs completes the Google sign-in flow as the given profile and returns the final redirect.
func (c *client) loginAs(g googleUser, next string) string {
	c.t.Helper()
	loc := c.startGoogleLogin(next)
	res := c.get("/auth/google/callback?state=" + url.QueryEscape(loc.Query().Get("state")) + "&code=" + googleCode(g))
	if res.StatusCode != http.StatusFound {
		c.t.Fatalf("callback: status %d", res.StatusCode)
	}
	return res.Header.Get("Location")
}

// login signs in as a Google user whose display name (and thus username) is name.
func (c *client) login(name string) {
	c.t.Helper()
	c.loginAs(googleUser{Sub: "sub-" + name, Name: name, Email: name + "@example.com"}, "/")
	if u := c.me(); u == nil || u["username"] != name {
		c.t.Fatalf("login %s: got user %v", name, u)
	}
}

func (c *client) me() map[string]any {
	c.t.Helper()
	_, out := c.json("GET", "/api/me", nil)
	u, _ := out["user"].(map[string]any)
	return u
}

func pngBytes(w, h int) []byte {
	var buf bytes.Buffer
	png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h)))
	return buf.Bytes()
}

func expect(t *testing.T, what string, got, want int, body map[string]any) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: status %d, want %d (body %v)", what, got, want, body)
	}
}

func TestGoogleLogin(t *testing.T) {
	ts := newTestServer(t)
	c := newClient(t, ts)

	_, out := c.json("GET", "/api/me", nil)
	if out["user"] != nil || out["google_login"] != true {
		t.Fatalf("anonymous /api/me: %v", out)
	}

	// The redirect to Google carries PKCE, our callback URL and a state value.
	loc := c.startGoogleLogin("/#/feed")
	q := loc.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("state") == "" ||
		q.Get("redirect_uri") != ts.URL+"/auth/google/callback" || q.Get("client_id") != "test-client" {
		t.Fatalf("bad authorization URL: %s", loc)
	}

	// A callback with the wrong state is rejected and does not sign in.
	res := c.get("/auth/google/callback?state=forged&code=" + googleCode(googleUser{Sub: "1", Name: "Mallory"}))
	if res.Header.Get("Location") != "/?login=failed" || c.me() != nil {
		t.Fatalf("forged state accepted: %v", res.Header)
	}
	// The state cookie is single use: replaying the old state fails too.
	res = c.get("/auth/google/callback?state=" + q.Get("state") + "&code=" + googleCode(googleUser{Sub: "1", Name: "Mallory"}))
	if res.Header.Get("Location") != "/?login=failed" {
		t.Fatalf("state replay accepted: %v", res.Header)
	}

	alice := googleUser{Sub: "google-alice", Name: "Alice Smith", Email: "alice@example.com", Picture: "https://example.com/a.png"}
	if next := c.loginAs(alice, "/#/feed"); next != "/#/feed" {
		t.Fatalf("redirect after login: %q", next)
	}
	u := c.me()
	if u["username"] != "Alice_Smith" || u["avatar_url"] != "https://example.com/a.png" {
		t.Fatalf("new user: %v", u)
	}
	id := u["id"]

	// Signing in again with the same Google account reuses the account.
	c2 := newClient(t, ts)
	c2.loginAs(alice, "/")
	if c2.me()["id"] != id {
		t.Fatalf("second sign-in created a new account: %v", c2.me())
	}

	// Another person with the same name gets a distinct username.
	c3 := newClient(t, ts)
	c3.loginAs(googleUser{Sub: "google-alice-2", Name: "Alice Smith"}, "/")
	if other := c3.me(); other["id"] == id || !strings.HasPrefix(other["username"].(string), "Alice_Smith_") {
		t.Fatalf("name clash: %v", other)
	}

	// Open redirects are not allowed.
	if next := newClient(t, ts).loginAs(googleUser{Sub: "x", Name: "Xavier"}, "//evil.example"); next != "/" {
		t.Fatalf("open redirect: %q", next)
	}

	// Renaming.
	code, out := c.json("PATCH", "/api/me", map[string]string{"username": "a!"})
	expect(t, "invalid rename", code, 400, out)
	code, out = c3.json("PATCH", "/api/me", map[string]string{"username": "alice_smith"})
	expect(t, "rename to taken name (case-insensitive)", code, 409, out)
	code, out = c.json("PATCH", "/api/me", map[string]string{"username": "alice"})
	expect(t, "rename", code, 200, out)

	code, out = c.json("POST", "/api/auth/logout", nil)
	expect(t, "logout", code, 200, out)
	if c.me() != nil {
		t.Fatal("still signed in after logout")
	}
}

func TestGoogleLoginDisabled(t *testing.T) {
	ts := newTestServer(t, func(c *Config) { c.Google = GoogleConfig{} })
	c := newClient(t, ts)
	if res := c.get("/auth/google/login"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("login without config: %d", res.StatusCode)
	}
	if _, out := c.json("GET", "/api/me", nil); out["google_login"] != false {
		t.Fatalf("google_login flag: %v", out)
	}
}

func TestUsernameBase(t *testing.T) {
	cases := map[[2]string]string{
		{"José Ñandú", ""}:                      "Jos_and",
		{"李", "li.wei@example.com"}:             "li_wei",
		{"", "x@example.com"}:                   "",
		{"A Very Long Display Name Indeed", ""}: "A_Very_Long_Display_Name",
	}
	for in, want := range cases {
		got := usernameBase(in[0], in[1])
		if want == "" {
			if !strings.HasPrefix(got, "user_") {
				t.Errorf("usernameBase(%q,%q) = %q, want user_NNNN", in[0], in[1], got)
			}
		} else if got != want {
			t.Errorf("usernameBase(%q,%q) = %q, want %q", in[0], in[1], got, want)
		}
		if !usernameRe.MatchString(got) {
			t.Errorf("usernameBase(%q,%q) = %q is not a valid username", in[0], in[1], got)
		}
	}
}

func TestTemplatesAndFavorites(t *testing.T) {
	ts := newTestServer(t)
	alice, bob, anon := newClient(t, ts), newClient(t, ts), newClient(t, ts)
	alice.login("alice")
	bob.login("bob")

	code, out := anon.upload("/api/templates", pngBytes(10, 10), map[string]string{"name": "x"})
	expect(t, "anon upload", code, 401, out)

	code, out = alice.upload("/api/templates", []byte("not an image"), map[string]string{"name": "x"})
	expect(t, "bad image", code, 400, out)

	code, out = alice.upload("/api/templates", pngBytes(40, 30), map[string]string{
		"name": "Cat Stare", "tags": "Cats, reaction, cats", "box_count": "3"})
	expect(t, "upload", code, 201, out)
	id := int(out["id"].(float64))
	if out["width"].(float64) != 40 || out["box_count"].(float64) != 3 {
		t.Fatalf("unexpected template %v", out)
	}
	if tags := out["tags"].([]any); len(tags) != 2 || tags[0] != "cats" {
		t.Fatalf("tags not normalised: %v", tags)
	}
	imgURL := out["image_url"].(string)
	res, err := http.Get(ts.URL + imgURL)
	if err != nil || res.StatusCode != 200 || res.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("serving upload: %v %v", err, res)
	}
	if res, _ := http.Get(ts.URL + "/uploads/templates/"); res.StatusCode != 404 {
		t.Fatalf("directory listing should be hidden, got %d", res.StatusCode)
	}

	alice.upload("/api/templates", pngBytes(10, 10), map[string]string{"name": "Dog Smile", "tags": "dogs"})

	code, out = anon.json("GET", "/api/templates?q=cat", nil)
	expect(t, "search", code, 200, out)
	if out["total"].(float64) != 1 {
		t.Fatalf("search: %v", out)
	}
	code, out = anon.json("GET", "/api/templates?tag=dogs", nil)
	if out["total"].(float64) != 1 || out["templates"].([]any)[0].(map[string]any)["name"] != "Dog Smile" {
		t.Fatalf("tag filter: %v", out)
	}
	code, out = anon.json("GET", "/api/tags", nil)
	if len(out["tags"].([]any)) != 3 {
		t.Fatalf("tags: %v", out)
	}

	path := "/api/templates/" + itoa(id)
	code, out = bob.json("PUT", path+"/favorite", nil)
	expect(t, "favorite", code, 200, out)
	code, out = bob.json("GET", "/api/templates?favorites=1", nil)
	if out["total"].(float64) != 1 {
		t.Fatalf("favorites: %v", out)
	}
	code, out = bob.json("PUT", "/api/templates/9999/favorite", nil)
	expect(t, "favorite missing", code, 404, out)

	code, out = bob.json("DELETE", path, nil)
	expect(t, "delete someone else's template", code, 403, out)
	code, out = alice.json("DELETE", path, nil)
	expect(t, "delete own template", code, 200, out)
	if res, _ := http.Get(ts.URL + imgURL); res.StatusCode != 404 {
		t.Fatalf("upload file should be removed, got %d", res.StatusCode)
	}
}

func TestMemesFeedAndLikes(t *testing.T) {
	ts := newTestServer(t, withAdmins("mod"))
	alice, bob, mod, anon := newClient(t, ts), newClient(t, ts), newClient(t, ts), newClient(t, ts)
	alice.login("alice")
	bob.login("bob")
	mod.login("mod")

	_, tmpl := alice.upload("/api/templates", pngBytes(20, 20), map[string]string{"name": "Base"})
	tmplID := itoa(int(tmpl["id"].(float64)))

	code, out := anon.upload("/api/memes", pngBytes(20, 20), nil)
	expect(t, "anon meme", code, 401, out)

	code, out = alice.upload("/api/memes", pngBytes(20, 20), map[string]string{"title": "first", "template_id": tmplID})
	expect(t, "create meme", code, 201, out)
	first := out["id"].(string)
	if out["share_url"] != "/m/"+first || out["template_name"] != "Base" {
		t.Fatalf("unexpected meme %v", out)
	}
	code, out = alice.upload("/api/memes", pngBytes(20, 20), map[string]string{"title": "secret", "public": "false"})
	expect(t, "create unlisted", code, 201, out)
	unlisted := out["id"].(string)
	code, out = alice.upload("/api/memes", pngBytes(20, 20), map[string]string{"template_id": "424242"})
	expect(t, "unknown template", code, 400, out)

	code, out = anon.json("GET", "/api/memes", nil)
	if memes := out["memes"].([]any); len(memes) != 1 {
		t.Fatalf("feed should only list public memes: %v", out)
	}
	code, out = anon.json("GET", "/api/memes/"+unlisted, nil)
	expect(t, "unlisted by link", code, 200, out)
	code, out = alice.json("GET", "/api/me/memes", nil)
	if len(out["memes"].([]any)) != 2 {
		t.Fatalf("my memes: %v", out)
	}
	_, out = anon.json("GET", "/api/templates/"+tmplID, nil)
	if out["use_count"].(float64) != 1 {
		t.Fatalf("use_count not incremented: %v", out)
	}

	// Likes are idempotent per user.
	bob.json("PUT", "/api/memes/"+first+"/like", nil)
	code, out = bob.json("PUT", "/api/memes/"+first+"/like", nil)
	expect(t, "like", code, 200, out)
	if out["likes"].(float64) != 1 {
		t.Fatalf("double like counted twice: %v", out)
	}
	_, out = bob.json("GET", "/api/memes/"+first, nil)
	if out["liked"] != true {
		t.Fatalf("liked flag missing: %v", out)
	}
	_, out = bob.json("DELETE", "/api/memes/"+first+"/like", nil)
	_, out = bob.json("DELETE", "/api/memes/"+first+"/like", nil)
	if out["likes"].(float64) != 0 {
		t.Fatalf("unlike: %v", out)
	}
	code, out = bob.json("PUT", "/api/memes/nope/like", nil)
	expect(t, "like missing", code, 404, out)

	// Share page has Open Graph tags and escapes user content.
	alice.upload("/api/memes", pngBytes(20, 20), map[string]string{"title": `<script>alert(1)</script>`})
	_, out = anon.json("GET", "/api/memes", nil)
	xss := out["memes"].([]any)[0].(map[string]any)["id"].(string)
	res, _ := http.Get(ts.URL + "/m/" + xss)
	page, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || strings.Contains(string(page), "<script>alert") || !strings.Contains(string(page), `og:image`) {
		t.Fatalf("share page: %d\n%s", res.StatusCode, page)
	}
	if res, _ := http.Get(ts.URL + "/m/doesnotexist"); res.StatusCode != 404 {
		t.Fatalf("missing share page: %d", res.StatusCode)
	}

	// Permissions: bob can't delete alice's meme, an admin can.
	code, out = bob.json("DELETE", "/api/memes/"+first, nil)
	expect(t, "bob deletes alice's meme", code, 403, out)
	code, out = mod.json("DELETE", "/api/memes/"+first, nil)
	expect(t, "admin delete", code, 200, out)
	code, out = anon.json("GET", "/api/memes/"+first, nil)
	expect(t, "deleted meme", code, 404, out)
}

func TestCrossOriginRejected(t *testing.T) {
	ts := newTestServer(t)
	req, _ := http.NewRequest("POST", ts.URL+"/api/auth/logout", nil)
	req.Header.Set("Origin", "https://evil.example")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin POST: got %d, want 403", res.StatusCode)
	}
}

func TestStaticAndUnknownAPI(t *testing.T) {
	ts := newTestServer(t)
	res, _ := http.Get(ts.URL + "/")
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || !strings.Contains(string(body), "<h1>Meme Generator</h1>") {
		t.Fatalf("index not served: %s", body)
	}
	if res, _ := http.Get(ts.URL + "/api/nope"); res.StatusCode != 404 ||
		!strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("unknown API route: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestLimiter(t *testing.T) {
	clock := time.Unix(1_000_000, 0)
	l := newLimiter(Limit{Events: 1, Per: time.Second, Burst: 2}, func() time.Time { return clock })
	for i := 0; i < 2; i++ {
		if ok, _ := l.allow("a"); !ok {
			t.Fatalf("burst request %d denied", i)
		}
	}
	ok, wait := l.allow("a")
	if ok || wait <= 0 || wait > time.Second {
		t.Fatalf("over limit: ok=%v wait=%v", ok, wait)
	}
	if ok, _ := l.allow("b"); !ok {
		t.Fatal("keys must be independent")
	}
	clock = clock.Add(time.Second)
	if ok, _ := l.allow("a"); !ok {
		t.Fatal("token not refilled after 1s")
	}
	clock = clock.Add(time.Hour) // the sweep drops idle, full buckets
	l.allow("c")
	if len(l.buckets) != 1 {
		t.Fatalf("idle buckets not swept: %d left", len(l.buckets))
	}
	if ok, _ := (*limiter)(nil).allow("x"); !ok {
		t.Fatal("nil limiter (zero Limit) must allow everything")
	}
}

func TestRateLimits(t *testing.T) {
	ts := newTestServer(t, func(c *Config) {
		c.Limits.Meme = Limit{Events: 2, Per: time.Hour, Burst: 2}
	})
	alice, bob := newClient(t, ts), newClient(t, ts)
	alice.login("alice")
	bob.login("bob")
	for i := 0; i < 2; i++ {
		code, out := alice.upload("/api/memes", pngBytes(10, 10), nil)
		expect(t, "meme within limit", code, 201, out)
	}
	code, out := alice.upload("/api/memes", pngBytes(10, 10), nil)
	expect(t, "meme over limit", code, 429, out)
	if !strings.Contains(out["error"].(string), "min") {
		t.Fatalf("error should say when to retry: %v", out)
	}
	code, out = bob.upload("/api/memes", pngBytes(10, 10), nil)
	expect(t, "limits are per user", code, 201, out)
}

func TestAPIRateLimitPerIP(t *testing.T) {
	ts := newTestServer(t, func(c *Config) {
		c.Limits.API = Limit{Events: 3, Per: time.Minute, Burst: 3}
	})
	c := newClient(t, ts)
	for i := 0; i < 3; i++ {
		code, out := c.json("GET", "/api/tags", nil)
		expect(t, "within limit", code, 200, out)
	}
	res, err := http.Get(ts.URL + "/api/tags")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusTooManyRequests || res.Header.Get("Retry-After") == "" {
		t.Fatalf("over limit: %d, Retry-After=%q", res.StatusCode, res.Header.Get("Retry-After"))
	}
	// Static pages are not API-limited.
	if res, _ := http.Get(ts.URL + "/"); res.StatusCode != 200 {
		t.Fatalf("static page limited: %d", res.StatusCode)
	}
}

func TestClientIP(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "6.6.6.6, 203.0.113.9")
	if ip := (&Server{}).clientIP(r); ip != "10.0.0.1" {
		t.Fatalf("untrusted proxy header used: %s", ip)
	}
	if ip := (&Server{cfg: Config{TrustProxy: true}}).clientIP(r); ip != "203.0.113.9" {
		t.Fatalf("trusted proxy: got %s, want right-most entry", ip)
	}
}
