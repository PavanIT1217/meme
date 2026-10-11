package server

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

func newTestServer(t *testing.T, admins ...string) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	s, err := New(Config{
		DBPath:    filepath.Join(dir, "test.db"),
		UploadDir: filepath.Join(dir, "uploads"),
		Admins:    admins,
		Static:    fstest.MapFS{"index.html": {Data: []byte("<h1>home</h1>")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s)
	t.Cleanup(func() { ts.Close(); s.Close() })
	return ts
}

// client is a logged-in (or anonymous) browser-like client with its own cookie jar.
type client struct {
	t    *testing.T
	base string
	http *http.Client
}

func newClient(t *testing.T, ts *httptest.Server) *client {
	jar, _ := cookiejar.New(nil)
	return &client{t: t, base: ts.URL, http: &http.Client{Jar: jar}}
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

func (c *client) register(name string) {
	c.t.Helper()
	code, out := c.json("POST", "/api/auth/register", map[string]string{"username": name, "password": "correct horse"})
	if code != http.StatusCreated {
		c.t.Fatalf("register %s: %d %v", name, code, out)
	}
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

func TestAuthFlow(t *testing.T) {
	ts := newTestServer(t)
	c := newClient(t, ts)

	code, out := c.json("GET", "/api/me", nil)
	expect(t, "me anon", code, 200, out)
	if out["user"] != nil {
		t.Fatalf("expected no user, got %v", out["user"])
	}

	code, out = c.json("POST", "/api/auth/register", map[string]string{"username": "ab", "password": "correct horse"})
	expect(t, "short username", code, 400, out)
	code, out = c.json("POST", "/api/auth/register", map[string]string{"username": "alice", "password": "short"})
	expect(t, "short password", code, 400, out)

	c.register("alice")
	code, out = c.json("GET", "/api/me", nil)
	expect(t, "me", code, 200, out)
	if out["user"].(map[string]any)["username"] != "alice" {
		t.Fatalf("unexpected user %v", out)
	}

	other := newClient(t, ts)
	code, out = other.json("POST", "/api/auth/register", map[string]string{"username": "ALICE", "password": "correct horse"})
	expect(t, "duplicate username (case-insensitive)", code, 409, out)

	code, out = c.json("POST", "/api/auth/logout", nil)
	expect(t, "logout", code, 200, out)
	code, out = c.json("GET", "/api/me", nil)
	if out["user"] != nil {
		t.Fatalf("still logged in after logout: %v", out)
	}

	code, out = c.json("POST", "/api/auth/login", map[string]string{"username": "alice", "password": "wrong password"})
	expect(t, "bad login", code, 401, out)
	code, out = c.json("POST", "/api/auth/login", map[string]string{"username": "alice", "password": "correct horse"})
	expect(t, "login", code, 200, out)
}

func TestTemplatesAndFavorites(t *testing.T) {
	ts := newTestServer(t)
	alice, bob, anon := newClient(t, ts), newClient(t, ts), newClient(t, ts)
	alice.register("alice")
	bob.register("bob")

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
	ts := newTestServer(t, "mod")
	alice, bob, mod, anon := newClient(t, ts), newClient(t, ts), newClient(t, ts), newClient(t, ts)
	alice.register("alice")
	bob.register("bob")
	mod.register("mod")

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
	req, _ := http.NewRequest("POST", ts.URL+"/api/auth/register",
		strings.NewReader(`{"username":"mallory","password":"correct horse"}`))
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
	if !strings.Contains(string(body), "home") {
		t.Fatalf("index not served: %s", body)
	}
	if res, _ := http.Get(ts.URL + "/api/nope"); res.StatusCode != 404 ||
		!strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("unknown API route: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
