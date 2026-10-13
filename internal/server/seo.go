package server

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	siteName        = "Meme Forge"
	siteTagline     = "Free Meme Generator"
	sitemapMaxURLs  = 50000 // protocol limit per sitemap file
	themeColorLight = "#6c3cf5"
)

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// slugify turns "Drake Hotline Bling!" into "drake-hotline-bling".
func slugify(s string) string {
	s = strings.Trim(slugRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > 80 {
		s = strings.TrimRight(s[:80], "-")
	}
	if s == "" {
		return "meme"
	}
	return s
}

func templatePath(id int64, name string) string {
	return fmt.Sprintf("/templates/%d/%s", id, slugify(name))
}

func tagPath(tag string) string { return "/tags/" + url.PathEscape(tag) }

func isoDate(unix int64) string { return time.Unix(unix, 0).UTC().Format(time.RFC3339) }

// ---------- robots.txt ----------

func (s *Server) handleRobots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	fmt.Fprintf(w, `User-agent: *
Allow: /
Disallow: /api/
Disallow: /auth/
Disallow: /me/
Disallow: /editor

Sitemap: %s/sitemap.xml
`, s.baseURL(r))
}

// ---------- sitemap.xml ----------

type sitemapImage struct {
	Loc   string `xml:"image:loc"`
	Title string `xml:"image:title,omitempty"`
}

type sitemapURL struct {
	Loc        string        `xml:"loc"`
	LastMod    string        `xml:"lastmod,omitempty"`
	ChangeFreq string        `xml:"changefreq,omitempty"`
	Priority   string        `xml:"priority,omitempty"`
	Image      *sitemapImage `xml:"image:image,omitempty"`
}

type urlset struct {
	XMLName    xml.Name     `xml:"urlset"`
	Xmlns      string       `xml:"xmlns,attr"`
	XmlnsImage string       `xml:"xmlns:image,attr"`
	URLs       []sitemapURL `xml:"url"`
}

func (s *Server) handleSitemap(w http.ResponseWriter, r *http.Request) {
	base := s.baseURL(r)
	set := urlset{Xmlns: "http://www.sitemaps.org/schemas/sitemap/0.9", XmlnsImage: "http://www.google.com/schemas/sitemap-image/1.1"}
	add := func(u sitemapURL) bool {
		if len(set.URLs) >= sitemapMaxURLs {
			return false
		}
		set.URLs = append(set.URLs, u)
		return true
	}

	var latest int64
	s.db.QueryRow(`SELECT MAX(t) FROM (SELECT MAX(created_at) AS t FROM templates UNION ALL SELECT MAX(created_at) FROM memes WHERE is_public = 1)`).Scan(&latest)
	lastmod := ""
	if latest > 0 {
		lastmod = isoDate(latest)
	}
	add(sitemapURL{Loc: base + "/", LastMod: lastmod, ChangeFreq: "daily", Priority: "1.0"})
	add(sitemapURL{Loc: base + "/feed", LastMod: lastmod, ChangeFreq: "hourly", Priority: "0.8"})
	add(sitemapURL{Loc: base + "/feed/top", ChangeFreq: "daily", Priority: "0.6"})

	tags, err := s.topTags(0)
	if err != nil {
		serverError(w, err)
		return
	}
	for _, t := range tags {
		add(sitemapURL{Loc: base + tagPath(t.Tag), ChangeFreq: "weekly", Priority: "0.6"})
	}

	// Templates, with the most recent meme made from each as a freshness signal.
	rows, err := s.db.Query(`SELECT t.id, t.name, t.image_url,
		MAX(t.created_at, COALESCE((SELECT MAX(m.created_at) FROM memes m WHERE m.template_id = t.id AND m.is_public = 1), 0))
		FROM templates t ORDER BY t.use_count DESC, t.id`)
	if err != nil {
		serverError(w, err)
		return
	}
	for rows.Next() {
		var (
			id         int64
			name, img  string
			lastChange int64
		)
		if err := rows.Scan(&id, &name, &img, &lastChange); err != nil {
			rows.Close()
			serverError(w, err)
			return
		}
		add(sitemapURL{Loc: base + templatePath(id, name), LastMod: isoDate(lastChange), ChangeFreq: "weekly", Priority: "0.9",
			Image: &sitemapImage{Loc: absURL(base, img), Title: memeName(name) + " meme template"}})
	}
	rows.Close()

	// Public memes, newest first, filling whatever room is left.
	rows, err = s.db.Query(`SELECT m.id, m.title, m.image_url, m.created_at FROM memes m WHERE m.is_public = 1
		ORDER BY m.created_at DESC LIMIT ?`, max(sitemapMaxURLs-len(set.URLs), 0))
	if err != nil {
		serverError(w, err)
		return
	}
	for rows.Next() {
		var (
			id, title, img string
			created        int64
		)
		if err := rows.Scan(&id, &title, &img, &created); err != nil {
			rows.Close()
			serverError(w, err)
			return
		}
		add(sitemapURL{Loc: base + "/m/" + id, LastMod: isoDate(created), Priority: "0.5",
			Image: &sitemapImage{Loc: absURL(base, img), Title: title}})
	}
	rows.Close()

	out, err := xml.MarshalIndent(set, "", " ")
	if err != nil {
		serverError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=900")
	w.Write([]byte(xml.Header))
	w.Write(out)
}

// ---------- manifest.webmanifest ----------

func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/manifest+json")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	json.NewEncoder(w).Encode(map[string]any{
		"name":             siteName + " – " + siteTagline,
		"short_name":       siteName,
		"description":      "Make memes online for free with popular meme templates, then share them.",
		"start_url":        "/",
		"scope":            "/",
		"display":          "standalone",
		"background_color": "#f6f6f8",
		"theme_color":      themeColorLight,
		"icons": []map[string]string{
			{"src": s.assetURL("icon-192.png"), "sizes": "192x192", "type": "image/png"},
			{"src": s.assetURL("icon-512.png"), "sizes": "512x512", "type": "image/png"},
			{"src": s.assetURL("icon-512.png"), "sizes": "512x512", "type": "image/png", "purpose": "maskable"},
			{"src": s.assetURL("icon.svg"), "sizes": "any", "type": "image/svg+xml"},
		},
	})
}
