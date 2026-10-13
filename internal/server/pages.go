package server

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	templatesPerPage = 60
	memesPerPage     = 30
)

// pageMeta is everything that goes in <head> for search engines and link previews.
type pageMeta struct {
	Title       string // full <title>
	Description string
	Canonical   string // absolute URL
	Robots      string // empty = index, follow
	OGType      string
	Image       string // absolute URL
	ImageAlt    string
	ImageWidth  int
	ImageHeight int
	Prev, Next  string // absolute pagination URLs
	JSONLD      []any
}

type crumb struct{ Name, URL string }

// page is the data for templates/page.html. Pages are rendered for an anonymous
// visitor so they can be cached and indexed; the frontend personalises them after load.
type page struct {
	Meta   pageMeta
	View   string // home, tag, template, editor, feed, mine, meme, notfound
	Nav    string // highlighted header link
	H1     string
	Intro  string
	Crumbs []crumb

	Templates []templateJSON
	Total     int
	Tags      []tagCount
	Tag       string
	Query     string
	Sort      string
	NextPage  string // relative URL of the next page, for "Load more"

	Template *templateJSON
	Related  []templateJSON

	Memes    []memeJSON
	FeedSort string
	Meme     *memeJSON
}

func (s *Server) loadPageTemplates(web fs.FS) error {
	funcs := template.FuncMap{
		"asset":   s.assetURL,
		"tagPath": tagPath,
		"date":    func(unix int64) string { return isoDate(unix)[:10] },
		"plural": func(n int, one, many string) string {
			if n == 1 {
				return one
			}
			return many
		},
		"add":        func(a, b int) int { return a + b },
		"iso":        isoDate,
		"memeName":   memeName,
		"largeThumb": func(id string) string { return thumbURL("memes", id, memeLargeWidth) },
		// layerRows is how many text boxes the editor starts with, to reserve their space before JS runs.
		"layerRows": func(n int) int { return max(n, 2) },
		// dict builds a map so sub-templates can take several values: (dict "T" $t "Eager" true).
		"dict": func(kv ...any) (map[string]any, error) {
			if len(kv)%2 != 0 {
				return nil, errors.New("dict needs key/value pairs")
			}
			m := map[string]any{}
			for i := 0; i < len(kv); i += 2 {
				k, ok := kv[i].(string)
				if !ok {
					return nil, errors.New("dict keys must be strings")
				}
				m[k] = kv[i+1]
			}
			return m, nil
		},
	}
	t, err := template.New("").Funcs(funcs).ParseFS(web, "templates/*.html")
	if err != nil {
		return err
	}
	s.pages = t
	return nil
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, p *page) {
	base := s.baseURL(r)
	m := &p.Meta
	if m.Canonical == "" {
		m.Canonical = base + r.URL.Path
	} else if strings.HasPrefix(m.Canonical, "/") {
		m.Canonical = base + m.Canonical
	}
	if m.Image == "" {
		m.Image, m.ImageWidth, m.ImageHeight = s.assetURL("og-default.png"), 1200, 630
		m.ImageAlt = siteName + " – " + siteTagline
	}
	m.Image = absURL(base, m.Image)
	if m.OGType == "" {
		m.OGType = "website"
	}
	if len(p.Crumbs) > 1 {
		m.JSONLD = append(m.JSONLD, breadcrumbLD(base, p.Crumbs))
	}
	var buf bytes.Buffer
	if err := s.pages.ExecuteTemplate(&buf, "page", p); err != nil {
		serverError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if m.Robots != "" {
		w.Header().Set("X-Robots-Tag", m.Robots)
	}
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}

func (s *Server) renderNotFound(w http.ResponseWriter, r *http.Request) {
	popular, _, _ := s.queryTemplates(nil, templateQuery{Limit: 12})
	s.render(w, r, http.StatusNotFound, &page{
		View: "notfound", H1: "Page not found",
		Intro:     "The page you were looking for doesn't exist or was deleted. Try one of these popular meme templates instead.",
		Templates: popular,
		Meta:      pageMeta{Title: "Page not found | " + siteName, Robots: "noindex", Canonical: "/"},
	})
}

func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) { s.renderNotFound(w, r) }

// pageNumber reads ?page=N (1-based).
func pageNumber(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || n < 1 {
		return 1
	}
	return n
}

// withPage builds a URL for page n of path, keeping other query parameters.
func withPage(path string, q url.Values, n int) string {
	q = cloneValues(q)
	if n <= 1 {
		q.Del("page")
	} else {
		q.Set("page", strconv.Itoa(n))
	}
	if enc := q.Encode(); enc != "" {
		return path + "?" + enc
	}
	return path
}

func cloneValues(v url.Values) url.Values {
	out := url.Values{}
	for k, vs := range v {
		out[k] = append([]string(nil), vs...)
	}
	return out
}

func pageSuffix(n int) string {
	if n > 1 {
		return fmt.Sprintf(" – Page %d", n)
	}
	return ""
}

// truncateText shortens s to at most n runes on a word boundary, for meta descriptions.
func truncateText(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	cut := string([]rune(s)[:n-1])
	if i := strings.LastIndex(cut, " "); i > len(cut)/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,;:–-") + "…"
}

// ---------- home & tag galleries ----------

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	n := pageNumber(r)
	search, sortBy := strings.TrimSpace(q.Get("q")), q.Get("sort")
	list, total, err := s.queryTemplates(nil, templateQuery{Q: search, Sort: sortBy,
		Limit: templatesPerPage, Offset: (n - 1) * templatesPerPage})
	if err != nil {
		serverError(w, err)
		return
	}
	if n > 1 && len(list) == 0 {
		s.renderNotFound(w, r)
		return
	}
	tags, err := s.topTags(30)
	if err != nil {
		serverError(w, err)
		return
	}
	base := s.baseURL(r)

	p := &page{View: "home", Nav: "templates", Templates: list, Total: total, Tags: tags, Query: search, Sort: sortBy,
		H1:    "Meme Generator",
		Intro: "Make memes online for free. Pick one of the most popular meme templates or upload your own image, add text, then download your meme or share it with a link.",
	}
	var names []string
	for _, t := range list[:min(3, len(list))] {
		names = append(names, t.Name)
	}
	p.Meta = pageMeta{
		Title: "Meme Generator – Make Memes Online Free | " + siteName + pageSuffix(n),
		Description: truncateText(fmt.Sprintf("Free online meme generator with %d popular meme templates like %s. Add text, change fonts and colors, then download or share your meme in seconds.",
			total, strings.Join(names, ", ")), 160),
		Canonical: withPage("/", nil, n),
		JSONLD:    []any{websiteLD(base), organizationLD(base, s.assetURL("icon-512.png"))},
	}
	if search != "" {
		p.H1 = fmt.Sprintf("Meme templates matching “%s”", search)
		p.Meta.Title = fmt.Sprintf("“%s” meme templates | %s", search, siteName)
		p.Meta.Robots = "noindex, follow" // search result pages shouldn't compete with real pages
	}
	if sortBy != "" && sortBy != "popular" {
		p.Meta.Robots = "noindex, follow"
	}
	s.paginate(p, base, "/", q, n, len(list), total)
	if len(list) > 0 {
		p.Meta.JSONLD = append(p.Meta.JSONLD, templateListLD(base, list))
	}
	s.render(w, r, http.StatusOK, p)
}

// paginate fills NextPage and the rel=prev/next links.
func (s *Server) paginate(p *page, base, path string, q url.Values, n, shown, total int) {
	if (n-1)*templatesPerPage+shown < total {
		p.NextPage = withPage(path, q, n+1)
		p.Meta.Next = base + p.NextPage
	}
	if n > 1 {
		p.Meta.Prev = base + withPage(path, q, n-1)
	}
}

func (s *Server) handleTagPage(w http.ResponseWriter, r *http.Request) {
	raw := r.PathValue("tag")
	tag := normalizeTag(raw)
	if tag == "" {
		s.renderNotFound(w, r)
		return
	}
	if tag != raw {
		http.Redirect(w, r, withPage(tagPath(tag), r.URL.Query(), pageNumber(r)), http.StatusMovedPermanently)
		return
	}
	n := pageNumber(r)
	list, total, err := s.queryTemplates(nil, templateQuery{Tag: tag, Limit: templatesPerPage, Offset: (n - 1) * templatesPerPage})
	if err != nil {
		serverError(w, err)
		return
	}
	if len(list) == 0 {
		s.renderNotFound(w, r)
		return
	}
	tags, err := s.topTags(30)
	if err != nil {
		serverError(w, err)
		return
	}
	base := s.baseURL(r)
	label := titleCase(tag)
	p := &page{View: "tag", Nav: "templates", Templates: list, Total: total, Tags: tags, Tag: tag,
		H1:     label + " Meme Templates",
		Intro:  fmt.Sprintf("%d %s %s. Pick one to add your own text and make a %s meme.", total, tag, plural(total, "meme template", "meme templates"), tag),
		Crumbs: []crumb{{"Meme Generator", "/"}, {label + " memes", tagPath(tag)}},
	}
	p.Meta = pageMeta{
		Title:       fmt.Sprintf("%s Meme Templates – Make %s Memes | %s%s", label, label, siteName, pageSuffix(n)),
		Description: truncateText(fmt.Sprintf("Browse %d %s meme templates and make your own %s memes online for free. Add text, customize it, then download or share.", total, tag, tag), 160),
		Canonical:   withPage(tagPath(tag), nil, n),
		JSONLD:      []any{collectionLD(base, tagPath(tag), label+" meme templates"), templateListLD(base, list)},
	}
	s.paginate(p, base, tagPath(tag), r.URL.Query(), n, len(list), total)
	s.render(w, r, http.StatusOK, p)
}

// ---------- template pages (the editor) ----------

func (s *Server) handleTemplatePage(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		s.renderNotFound(w, r)
		return
	}
	t, err := s.getTemplate(id, nil)
	if errors.Is(err, errNotFound) {
		s.renderNotFound(w, r)
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}
	if r.URL.Path != t.URL { // missing or outdated slug: one canonical URL per template
		http.Redirect(w, r, t.URL, http.StatusMovedPermanently)
		return
	}
	memes, err := s.listMemes(nil, "m.is_public = 1 AND m.template_id = ?", feedTop, t.ID, 12, 0)
	if err != nil {
		serverError(w, err)
		return
	}
	related, err := s.relatedTemplates(t)
	if err != nil {
		serverError(w, err)
		return
	}
	base := s.baseURL(r)
	crumbs := []crumb{{"Meme Generator", "/"}}
	if len(t.Tags) > 0 {
		crumbs = append(crumbs, crumb{titleCase(t.Tags[0]) + " memes", tagPath(t.Tags[0])})
	}
	crumbs = append(crumbs, crumb{t.Name, t.URL})

	name := memeName(t.Name)
	desc := t.Description
	if desc == "" {
		desc = fmt.Sprintf("Make a %s meme online for free. Add your own text to the %s meme template, change the font and colors, then download or share it in seconds.", name, name)
	}
	p := &page{View: "template", Nav: "templates", Template: &t, Memes: memes, Related: related, Crumbs: crumbs,
		H1: name + " Meme Template"}
	p.Meta = pageMeta{
		Title:       fmt.Sprintf("%s Meme Template & Generator | %s", name, siteName),
		Description: truncateText(desc, 160),
		Canonical:   t.URL,
		Image:       t.ImageURL, ImageWidth: t.Width, ImageHeight: t.Height,
		ImageAlt: name + " meme template",
		JSONLD:   []any{templateLD(base, t, desc)},
	}
	s.render(w, r, http.StatusOK, p)
}

func (s *Server) handleTemplateRedirect(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		s.renderNotFound(w, r)
		return
	}
	t, err := s.getTemplate(id, nil)
	if err != nil {
		s.renderNotFound(w, r)
		return
	}
	http.Redirect(w, r, t.URL, http.StatusMovedPermanently)
}

// relatedTemplates prefers templates sharing a tag, topped up with popular ones.
func (s *Server) relatedTemplates(t templateJSON) ([]templateJSON, error) {
	const want = 12
	var out []templateJSON
	seen := map[int64]bool{t.ID: true}
	for _, tag := range t.Tags {
		list, _, err := s.queryTemplates(nil, templateQuery{Tag: tag, ExcludeID: t.ID, Limit: want})
		if err != nil {
			return nil, err
		}
		for _, x := range list {
			if !seen[x.ID] && len(out) < want {
				seen[x.ID] = true
				out = append(out, x)
			}
		}
	}
	if len(out) < want {
		list, _, err := s.queryTemplates(nil, templateQuery{ExcludeID: t.ID, Limit: want * 2})
		if err != nil {
			return nil, err
		}
		for _, x := range list {
			if !seen[x.ID] && len(out) < want {
				seen[x.ID] = true
				out = append(out, x)
			}
		}
	}
	return out, nil
}

func (s *Server) handleEditorPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, &page{View: "editor", Nav: "templates", H1: "Make a Meme From Your Own Image",
		Meta: pageMeta{Title: "Upload Your Own Image – Meme Generator | " + siteName, Robots: "noindex, follow",
			Description: "Turn any picture into a meme: upload an image, add text and download it."}})
}

// ---------- feeds ----------

func (s *Server) handleFeedPage(sortBy string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := pageNumber(r)
		order, path := feedNew, "/feed"
		if sortBy == "top" {
			order, path = feedTop, "/feed/top"
		}
		memes, err := s.listMemes(nil, "m.is_public = 1", order, memesPerPage+1, (n-1)*memesPerPage)
		if err != nil {
			serverError(w, err)
			return
		}
		if n > 1 && len(memes) == 0 {
			s.renderNotFound(w, r)
			return
		}
		base := s.baseURL(r)
		p := &page{View: "feed", Nav: "feed", FeedSort: sortBy, Crumbs: []crumb{{"Meme Generator", "/"}}}
		if sortBy == "top" {
			p.H1 = "Top Memes"
			p.Intro = "The most liked memes made with " + siteName + "."
			p.Crumbs = append(p.Crumbs, crumb{"Top memes", path})
			p.Meta.Title = "Top Memes – Most Liked Memes | " + siteName + pageSuffix(n)
			p.Meta.Description = "The funniest, most liked memes made by the " + siteName + " community. Like your favourites or make your own meme."
		} else {
			p.H1 = "Latest Memes"
			p.Intro = "Fresh memes from the " + siteName + " community. Like your favourites or make your own."
			p.Crumbs = append(p.Crumbs, crumb{"Latest memes", path})
			p.Meta.Title = "Latest Memes – New Memes Every Day | " + siteName + pageSuffix(n)
			p.Meta.Description = "Browse the newest memes made by the " + siteName + " community, then make your own with free meme templates."
		}
		if len(memes) > memesPerPage {
			memes = memes[:memesPerPage]
			p.NextPage = withPage(path, nil, n+1)
			p.Meta.Next = base + p.NextPage
		}
		if n > 1 {
			p.Meta.Prev = base + withPage(path, nil, n-1)
		}
		p.Memes = memes
		p.Meta.Canonical = withPage(path, nil, n)
		p.Meta.JSONLD = []any{collectionLD(base, path, p.H1)}
		if len(memes) == 0 {
			p.Meta.Robots = "noindex, follow" // nothing to index yet
		}
		s.render(w, r, http.StatusOK, p)
	}
}

func (s *Server) handleMinePage(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, &page{View: "mine", Nav: "mine", H1: "My Memes",
		Meta: pageMeta{Title: "My Memes | " + siteName, Robots: "noindex, nofollow"}})
}

// ---------- meme share pages ----------

func (s *Server) handleMemePage(w http.ResponseWriter, r *http.Request) {
	m, err := s.getMeme(r.PathValue("id"), nil)
	if errors.Is(err, errNotFound) {
		s.renderNotFound(w, r)
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}
	base := s.baseURL(r)
	title := m.Title
	if title == "" {
		if m.TemplateName != "" {
			title = memeName(m.TemplateName) + " meme by " + m.Author
		} else {
			title = "Meme by " + m.Author
		}
	}
	var more []memeJSON
	if m.TemplateID != nil {
		more, err = s.listMemes(nil, "m.is_public = 1 AND m.template_id = ? AND m.id != ?", feedTop, *m.TemplateID, m.ID, 8, 0)
	} else {
		more, err = s.listMemes(nil, "m.is_public = 1 AND m.id != ?", feedTop, m.ID, 8, 0)
	}
	if err != nil {
		serverError(w, err)
		return
	}
	popular, _, err := s.queryTemplates(nil, templateQuery{Limit: 6})
	if err != nil {
		serverError(w, err)
		return
	}

	desc := fmt.Sprintf("A meme by %s", m.Author)
	if m.TemplateName != "" {
		desc = fmt.Sprintf("A %s meme by %s", memeName(m.TemplateName), m.Author)
	}
	desc += fmt.Sprintf(" on %s · %d %s. Make your own meme online for free.", siteName, m.Likes, plural(m.Likes, "like", "likes"))
	crumbs := []crumb{{"Meme Generator", "/"}}
	if m.TemplateURL != "" {
		crumbs = append(crumbs, crumb{m.TemplateName, m.TemplateURL})
	} else {
		crumbs = append(crumbs, crumb{"Latest memes", "/feed"})
	}
	crumbs = append(crumbs, crumb{title, m.ShareURL})

	p := &page{View: "meme", Nav: "feed", Meme: &m, Memes: more, Templates: popular, H1: title, Crumbs: crumbs}
	p.Meta = pageMeta{
		Title:       truncateText(title, 70) + " | " + siteName,
		Description: truncateText(desc, 160),
		Canonical:   m.ShareURL,
		OGType:      "article",
		Image:       m.ImageURL, ImageWidth: m.Width, ImageHeight: m.Height, ImageAlt: title,
		JSONLD: []any{memeLD(base, m, title)},
	}
	if !m.Public {
		p.Meta.Robots = "noindex, follow" // unlisted: viewable by link, kept out of search
	}
	s.render(w, r, http.StatusOK, p)
}

// ---------- small text helpers ----------

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// memeName drops a trailing "meme" from a template name, so copy like "%s meme template"
// doesn't read "Types of Headaches meme meme template".
func memeName(name string) string {
	trimmed := strings.TrimSpace(name)
	lower := strings.ToLower(trimmed)
	for _, suffix := range []string{" memes", " meme"} {
		if strings.HasSuffix(lower, suffix) && len(trimmed) > len(suffix) {
			return strings.TrimSpace(trimmed[:len(trimmed)-len(suffix)])
		}
	}
	return trimmed
}

// titleCase capitalises each word of a tag: "reaction gifs" → "Reaction Gifs".
func titleCase(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}
