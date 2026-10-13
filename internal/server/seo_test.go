package server

import (
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"image"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

type response struct {
	status int
	header http.Header
	body   string
}

// fetch performs a GET without following redirects or transparently decompressing.
func fetch(t *testing.T, url string, headers ...string) response {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	client := &http.Client{
		Transport:     &http.Transport{DisableCompression: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var r io.Reader = res.Body
	if res.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		r = gz
	}
	b, _ := io.ReadAll(r)
	return response{res.StatusCode, res.Header, string(b)}
}

func attr(t *testing.T, body, pattern string) string {
	t.Helper()
	m := regexp.MustCompile(pattern).FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	return m[1]
}

func canonical(t *testing.T, body string) string {
	return attr(t, body, `<link rel="canonical" href="([^"]+)">`)
}

func robots(t *testing.T, body string) string {
	return attr(t, body, `<meta name="robots" content="([^"]+)">`)
}

// jsonLD parses every JSON-LD block on the page, keyed by @type.
func jsonLD(t *testing.T, body string) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, m := range regexp.MustCompile(`(?s)<script type="application/ld\+json">(.*?)</script>`).FindAllStringSubmatch(body, -1) {
		var v map[string]any
		if err := json.Unmarshal([]byte(m[1]), &v); err != nil {
			t.Fatalf("invalid JSON-LD %q: %v", m[1], err)
		}
		out[v["@type"].(string)] = v
	}
	return out
}

// seoFixture: alice uploads two tagged templates and saves a public and an unlisted meme.
type seoFixture struct {
	ts                       string
	tmplURL, tmplID          string
	publicMeme, unlistedMeme string
}

func newSEOFixture(t *testing.T, opts ...func(*Config)) seoFixture {
	t.Helper()
	ts := newTestServer(t, opts...)
	alice := newClient(t, ts)
	alice.login("alice")
	_, tmpl := alice.upload("/api/templates", pngBytes(60, 40), map[string]string{
		"name": "Cat Stare", "tags": "cats, reaction", "description": "A cat staring into the void."})
	alice.upload("/api/templates", pngBytes(40, 40), map[string]string{"name": "Dog Smile", "tags": "dogs"})
	tmplID := itoa(int(tmpl["id"].(float64)))
	_, pub := alice.upload("/api/memes", pngBytes(60, 40), map[string]string{"title": "When the cat stares", "template_id": tmplID})
	_, unl := alice.upload("/api/memes", pngBytes(60, 40), map[string]string{"title": "secret", "public": "false"})
	return seoFixture{ts: ts.URL, tmplURL: tmpl["url"].(string), tmplID: tmplID,
		publicMeme: pub["id"].(string), unlistedMeme: unl["id"].(string)}
}

func TestHomePageSEO(t *testing.T) {
	f := newSEOFixture(t)
	r := fetch(t, f.ts+"/")
	if r.status != 200 || !strings.HasPrefix(r.header.Get("Content-Type"), "text/html") {
		t.Fatalf("home: %d %s", r.status, r.header.Get("Content-Type"))
	}
	for _, want := range []string{
		`<html lang="en">`,
		`<title>Meme Generator – Make Memes Online Free | Meme Forge</title>`,
		`<meta name="description" content="Free online meme generator with 2 popular meme templates like Cat Stare, Dog Smile.`,
		`<h1>Meme Generator</h1>`,
		`href="` + f.tmplURL + `"`,                    // crawlable link to the template page
		`alt="Cat Stare meme template"`,               // descriptive alt text
		`<a class="chip" href="/tags/cats">#cats</a>`, // tag pages are linked
		`<meta property="og:image" content="` + f.ts + `/static/og-default.png?v=`,
		`<link rel="manifest" href="/manifest.webmanifest">`,
		`How to make a meme`,
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("home page is missing %q", want)
		}
	}
	if got := canonical(t, r.body); got != f.ts+"/" {
		t.Errorf("canonical = %q", got)
	}
	if got := robots(t, r.body); !strings.HasPrefix(got, "index, follow") {
		t.Errorf("robots = %q", got)
	}
	ld := jsonLD(t, r.body)
	site := ld["WebSite"]
	if site == nil || site["potentialAction"].(map[string]any)["target"].(map[string]any)["urlTemplate"] != f.ts+"/?q={search_term_string}" {
		t.Errorf("WebSite/SearchAction JSON-LD: %v", site)
	}
	if ld["Organization"] == nil || ld["ItemList"] == nil {
		t.Errorf("missing Organization or ItemList JSON-LD: %v", ld)
	}

	// Search results are crawlable but not indexed, and canonicalise to the home page.
	r = fetch(t, f.ts+"/?q=cat")
	if r.status != 200 || robots(t, r.body) != "noindex, follow" || r.header.Get("X-Robots-Tag") != "noindex, follow" ||
		canonical(t, r.body) != f.ts+"/" || !strings.Contains(r.body, "Cat Stare") || strings.Contains(r.body, "Dog Smile") {
		t.Errorf("search page: robots=%q canonical=%q", robots(t, r.body), canonical(t, r.body))
	}
}

func TestTemplatePageSEO(t *testing.T) {
	f := newSEOFixture(t)
	if f.tmplURL != "/templates/"+f.tmplID+"/cat-stare" {
		t.Fatalf("template url = %q", f.tmplURL)
	}
	r := fetch(t, f.ts+f.tmplURL)
	if r.status != 200 {
		t.Fatalf("template page: %d", r.status)
	}
	for _, want := range []string{
		`<title>Cat Stare Meme Template &amp; Generator | Meme Forge</title>`,
		`<meta name="description" content="A cat staring into the void.">`,
		`<h1>Cat Stare Meme Template</h1>`,
		`<meta property="og:image" content="` + f.ts + `/uploads/templates/`,
		`<meta property="og:image:width" content="60">`,
		`id="editor-placeholder"`,
		`<a href="/tags/cats">#cats</a>`,
		`How to make a Cat Stare meme`,
		`Cat Stare memes</h2>`,     // memes made with it
		`href="/m/` + f.publicMeme, // …linked
		`Related meme templates`,
		`<nav class="breadcrumbs" aria-label="Breadcrumb">`,
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("template page is missing %q", want)
		}
	}
	if strings.Contains(r.body, f.unlistedMeme) {
		t.Error("template page links an unlisted meme")
	}
	if got := canonical(t, r.body); got != f.ts+f.tmplURL {
		t.Errorf("canonical = %q", got)
	}
	ld := jsonLD(t, r.body)
	if img := ld["ImageObject"]; img == nil || img["name"] != "Cat Stare meme template" || !strings.HasPrefix(img["contentUrl"].(string), f.ts+"/uploads/") {
		t.Errorf("ImageObject JSON-LD: %v", img)
	}
	crumbs := ld["BreadcrumbList"]["itemListElement"].([]any)
	if len(crumbs) != 3 || crumbs[1].(map[string]any)["item"] != f.ts+"/tags/cats" || crumbs[2].(map[string]any)["item"] != f.ts+f.tmplURL {
		t.Errorf("breadcrumbs: %v", crumbs)
	}

	// One canonical URL per template.
	for _, u := range []string{"/templates/" + f.tmplID, "/templates/" + f.tmplID + "/old-name"} {
		if r := fetch(t, f.ts+u); r.status != http.StatusMovedPermanently || r.header.Get("Location") != f.tmplURL {
			t.Errorf("%s: %d → %q", u, r.status, r.header.Get("Location"))
		}
	}
	if r := fetch(t, f.ts+"/templates/9999/nope"); r.status != 404 || robots(t, r.body) != "noindex" {
		t.Errorf("missing template: %d robots=%q", r.status, robots(t, r.body))
	}
}

func TestTagFeedAndMemePagesSEO(t *testing.T) {
	f := newSEOFixture(t)

	r := fetch(t, f.ts+"/tags/cats")
	if r.status != 200 || !strings.Contains(r.body, `<h1>Cats Meme Templates</h1>`) || canonical(t, r.body) != f.ts+"/tags/cats" ||
		!strings.Contains(r.body, "Cat Stare") || strings.Contains(r.body, "Dog Smile") || jsonLD(t, r.body)["CollectionPage"] == nil {
		t.Errorf("tag page: %d canonical=%q", r.status, canonical(t, r.body))
	}
	if r := fetch(t, f.ts+"/tags/Cats"); r.status != http.StatusMovedPermanently || r.header.Get("Location") != "/tags/cats" {
		t.Errorf("tag case redirect: %d %q", r.status, r.header.Get("Location"))
	}
	if r := fetch(t, f.ts+"/tags/unknown"); r.status != 404 {
		t.Errorf("empty tag: %d", r.status)
	}

	r = fetch(t, f.ts+"/feed")
	if r.status != 200 || !strings.Contains(r.body, `<h1 class="view-title">Latest Memes</h1>`) ||
		!strings.Contains(r.body, "/m/"+f.publicMeme) || strings.Contains(r.body, f.unlistedMeme) {
		t.Errorf("feed page: %d", r.status)
	}
	if r := fetch(t, f.ts+"/feed/top"); r.status != 200 || canonical(t, r.body) != f.ts+"/feed/top" {
		t.Errorf("top feed: %d", r.status)
	}
	if r := fetch(t, f.ts+"/feed?page=5"); r.status != 404 {
		t.Errorf("feed page past the end: %d", r.status)
	}

	r = fetch(t, f.ts+"/m/"+f.publicMeme)
	if r.status != 200 || !strings.HasPrefix(robots(t, r.body), "index") || !strings.Contains(r.body, `<meta property="og:type" content="article">`) ||
		!strings.Contains(r.body, `<h1>When the cat stares</h1>`) || !strings.Contains(r.body, `href="`+f.tmplURL+`"`) {
		t.Errorf("public meme page: %d robots=%q", r.status, robots(t, r.body))
	}
	img := jsonLD(t, r.body)["ImageObject"]
	if img == nil || img["isBasedOn"] != f.ts+f.tmplURL || img["author"].(map[string]any)["name"] != "alice" {
		t.Errorf("meme JSON-LD: %v", img)
	}
	if share := "https://twitter.com/intent/tweet?url=" + url.QueryEscape(f.ts+"/m/"+f.publicMeme) + "&amp;text=When%20the%20cat%20stares"; !strings.Contains(strings.ToLower(r.body), strings.ToLower(share)) { // percent-encoding hex case may differ
		t.Errorf("share link not encoded as expected, want %s\ngot %s", share, attr(t, r.body, `(https://twitter.com/intent/tweet[^"]*)`))
	}
	r = fetch(t, f.ts+"/m/"+f.unlistedMeme)
	if r.status != 200 || robots(t, r.body) != "noindex, follow" || r.header.Get("X-Robots-Tag") != "noindex, follow" {
		t.Errorf("unlisted meme should be noindex: robots=%q", robots(t, r.body))
	}

	for _, p := range []string{"/me/memes", "/editor"} {
		if r := fetch(t, f.ts+p); r.status != 200 || !strings.HasPrefix(robots(t, r.body), "noindex") {
			t.Errorf("%s: %d robots=%q", p, r.status, robots(t, r.body))
		}
	}
	if r := fetch(t, f.ts+"/no/such/page"); r.status != 404 || robots(t, r.body) != "noindex" || !strings.Contains(r.body, "Page not found") {
		t.Errorf("unknown path: %d", r.status)
	}
}

func TestRobotsSitemapManifest(t *testing.T) {
	f := newSEOFixture(t)

	r := fetch(t, f.ts+"/robots.txt")
	if r.status != 200 || !strings.Contains(r.body, "Sitemap: "+f.ts+"/sitemap.xml") || !strings.Contains(r.body, "Disallow: /api/") {
		t.Errorf("robots.txt: %d\n%s", r.status, r.body)
	}

	r = fetch(t, f.ts+"/sitemap.xml")
	if r.status != 200 || !strings.HasPrefix(r.header.Get("Content-Type"), "application/xml") {
		t.Fatalf("sitemap: %d %s", r.status, r.header.Get("Content-Type"))
	}
	var sm struct {
		URLs []struct {
			Loc    string `xml:"loc"`
			Images []struct {
				Loc string `xml:"loc"`
			} `xml:"image"`
		} `xml:"url"`
	}
	if err := xml.Unmarshal([]byte(r.body), &sm); err != nil {
		t.Fatalf("sitemap XML: %v\n%s", err, r.body)
	}
	locs := map[string]bool{}
	images := 0
	for _, u := range sm.URLs {
		locs[u.Loc] = true
		images += len(u.Images)
	}
	for _, want := range []string{"/", "/feed", "/tags/cats", "/tags/dogs", f.tmplURL, "/m/" + f.publicMeme} {
		if !locs[f.ts+want] {
			t.Errorf("sitemap is missing %s", want)
		}
	}
	if locs[f.ts+"/m/"+f.unlistedMeme] {
		t.Error("sitemap lists an unlisted meme")
	}
	if images != 3 { // two templates + one public meme
		t.Errorf("sitemap images = %d, want 3", images)
	}
	if !strings.Contains(r.body, `xmlns:image="http://www.google.com/schemas/sitemap-image/1.1"`) {
		t.Error("sitemap is missing the image namespace")
	}

	r = fetch(t, f.ts+"/manifest.webmanifest")
	var manifest struct {
		Name  string `json:"name"`
		Icons []struct {
			Src string `json:"src"`
		} `json:"icons"`
	}
	if err := json.Unmarshal([]byte(r.body), &manifest); err != nil || manifest.Name == "" || len(manifest.Icons) < 2 {
		t.Fatalf("manifest: %v %s", err, r.body)
	}
	for _, icon := range manifest.Icons {
		if r := fetch(t, f.ts+icon.Src); r.status != 200 {
			t.Errorf("manifest icon %s: %d", icon.Src, r.status)
		}
	}
	for _, p := range []string{"/favicon.ico", "/apple-touch-icon.png"} {
		if r := fetch(t, f.ts+p); r.status != 200 {
			t.Errorf("%s: %d", p, r.status)
		}
	}
}

func TestCompressionAndCaching(t *testing.T) {
	f := newSEOFixture(t)

	r := fetch(t, f.ts+"/", "Accept-Encoding", "gzip")
	if r.header.Get("Content-Encoding") != "gzip" || !strings.Contains(r.body, "Meme Generator") {
		t.Errorf("HTML not gzipped: %v", r.header)
	}
	if r := fetch(t, f.ts+"/"); r.header.Get("Content-Encoding") != "" {
		t.Error("gzip sent to a client that did not ask for it")
	}

	asset := attr(t, r.body, `<link rel="stylesheet" href="([^"]+)">`)
	r = fetch(t, f.ts+asset, "Accept-Encoding", "gzip")
	if r.status != 200 || r.header.Get("Cache-Control") != "public, max-age=31536000, immutable" ||
		r.header.Get("Content-Encoding") != "gzip" || !strings.Contains(r.body, ":root") {
		t.Errorf("fingerprinted CSS: %d %v", r.status, r.header)
	}
	if r := fetch(t, f.ts+asset, "If-None-Match", r.header.Get("ETag")); r.status != http.StatusNotModified {
		t.Errorf("ETag revalidation: %d", r.status)
	}
	if r := fetch(t, f.ts+"/static/style.css?v=stale"); r.header.Get("Cache-Control") != "public, max-age=3600" {
		t.Errorf("stale fingerprint should get a short cache: %q", r.header.Get("Cache-Control"))
	}
	if r := fetch(t, f.ts+"/static/nope.js"); r.status != 404 {
		t.Errorf("missing asset: %d", r.status)
	}

	// Images are already compressed: served as-is.
	page := fetch(t, f.ts+"/m/"+f.publicMeme)
	img := attr(t, page.body, `<meta property="og:image" content="[^"]*(/uploads/[^"]+)">`)
	if r := fetch(t, f.ts+img, "Accept-Encoding", "gzip"); r.status != 200 || r.header.Get("Content-Encoding") != "" {
		t.Errorf("image %s: %d encoding=%q", img, r.status, r.header.Get("Content-Encoding"))
	}
}

func TestPagination(t *testing.T) {
	ts := newTestServer(t, func(c *Config) { c.Limits.Template = Limit{} })
	alice := newClient(t, ts)
	alice.login("alice")
	for i := 0; i < templatesPerPage+5; i++ {
		alice.upload("/api/templates", pngBytes(8, 8), map[string]string{"name": "Template " + itoa(i)})
	}
	r := fetch(t, ts.URL+"/")
	if attr(t, r.body, `<link rel="next" href="([^"]+)">`) != ts.URL+"/?page=2" || strings.Contains(r.body, `rel="prev"`) {
		t.Errorf("page 1 pagination links wrong")
	}
	r = fetch(t, ts.URL+"/?page=2")
	if r.status != 200 || canonical(t, r.body) != ts.URL+"/?page=2" ||
		attr(t, r.body, `<link rel="prev" href="([^"]+)">`) != ts.URL+"/" || strings.Contains(r.body, `rel="next"`) ||
		strings.Count(r.body, `class="card-link"`) != 5 || !strings.Contains(r.body, "– Page 2</title>") {
		t.Errorf("page 2: %d canonical=%q", r.status, canonical(t, r.body))
	}
	if r := fetch(t, ts.URL+"/?page=3"); r.status != 404 {
		t.Errorf("page past the end: %d", r.status)
	}
}

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"Drake Hotline Bling":           "drake-hotline-bling",
		"  Surprised Pikachu!!  ":       "surprised-pikachu",
		"Bernie I Am Once Again Asking": "bernie-i-am-once-again-asking",
		"€€€":                           "meme",
		strings.Repeat("ab ", 50):       strings.TrimRight(strings.Repeat("ab-", 27), "-"),
	} {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDescriptionMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE templates (id INTEGER PRIMARY KEY, name TEXT NOT NULL, image_url TEXT NOT NULL,
		width INTEGER NOT NULL, height INTEGER NOT NULL, box_count INTEGER NOT NULL DEFAULT 2, tags TEXT NOT NULL DEFAULT '',
		uploaded_by INTEGER, external_id TEXT UNIQUE, use_count INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL);
		INSERT INTO templates (name, image_url, width, height, created_at) VALUES ('Old', 'https://example.com/x.png', 1, 1, 1);`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{DBPath: path, UploadDir: filepath.Join(t.TempDir(), "u"), Web: webFS(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.getTemplate(1, nil)
	if err != nil || got.Name != "Old" || got.Description != "" {
		t.Fatalf("after migration: %+v %v", got, err)
	}
}

func TestMemeNameAndTruncate(t *testing.T) {
	for in, want := range map[string]string{
		"Types of Headaches meme": "Types of Headaches",
		"Drake Hotline Bling":     "Drake Hotline Bling",
		"Meme":                    "Meme",
		"Dank Memes":              "Dank",
	} {
		if got := memeName(in); got != want {
			t.Errorf("memeName(%q) = %q, want %q", in, got, want)
		}
	}
	got := truncateText("Add text, change fonts and colors, then download or share", 40)
	if got != "Add text, change fonts and colors…" {
		t.Errorf("truncateText = %q", got)
	}
}

func TestThumbnails(t *testing.T) {
	ts := newTestServer(t)
	alice := newClient(t, ts)
	alice.login("alice")
	_, tmpl := alice.upload("/api/templates", pngBytes(1000, 500), map[string]string{"name": "Wide"})
	_, meme := alice.upload("/api/memes", pngBytes(100, 50), nil)

	check := func(url string, wantW, wantH int) {
		t.Helper()
		r := fetch(t, ts.URL+url)
		if r.status != 200 || r.header.Get("Content-Type") != "image/jpeg" {
			t.Fatalf("%s: %d %s", url, r.status, r.header.Get("Content-Type"))
		}
		cfg, format, err := image.DecodeConfig(strings.NewReader(r.body))
		if err != nil || format != "jpeg" || cfg.Width != wantW || cfg.Height != wantH {
			t.Fatalf("%s: %s %dx%d %v, want %dx%d", url, format, cfg.Width, cfg.Height, err, wantW, wantH)
		}
	}
	check(tmpl["thumb_url"].(string), 320, 160) // scaled down, aspect ratio kept
	check(tmpl["thumb_url"].(string), 320, 160) // served from the disk cache
	check(meme["thumb_url"].(string), 100, 50)  // never upscaled

	for _, bad := range []string{"/thumbs/templates/" + itoa(int(tmpl["id"].(float64))) + "-123.jpg",
		"/thumbs/templates/9999-320.jpg", "/thumbs/other/1-320.jpg", "/thumbs/memes/a.b-320.jpg"} {
		if r := fetch(t, ts.URL+bad); r.status != 404 {
			t.Errorf("%s: %d, want 404", bad, r.status)
		}
	}

	alice.json("DELETE", "/api/memes/"+meme["id"].(string), nil)
	if r := fetch(t, ts.URL+meme["thumb_url"].(string)); r.status != 404 {
		t.Errorf("thumbnail of deleted meme: %d", r.status)
	}
}
