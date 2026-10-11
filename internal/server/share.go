package server

import (
	"bytes"
	"errors"
	"net/http"
)

// handleSharePage renders /m/{id}: a standalone page with Open Graph tags so
// shared links unfurl with the meme image on social media and chat apps.
func (s *Server) handleSharePage(w http.ResponseWriter, r *http.Request) {
	m, err := s.getMeme(r.PathValue("id"), nil)
	if errors.Is(err, errNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}
	base := s.baseURL(r)
	title := m.Title
	if title == "" {
		title = "A meme by " + m.Author
	}
	var buf bytes.Buffer
	err = s.shareTmpl.Execute(&buf, map[string]any{
		"Meme":     m,
		"Title":    title,
		"ImageAbs": absURL(base, m.ImageURL),
		"PageAbs":  base + m.ShareURL,
	})
	if err != nil {
		serverError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(buf.Bytes())
}

const sharePageHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{.Title}} · Meme Forge</title>
  <meta property="og:type" content="website">
  <meta property="og:site_name" content="Meme Forge">
  <meta property="og:title" content="{{.Title}}">
  <meta property="og:url" content="{{.PageAbs}}">
  <meta property="og:image" content="{{.ImageAbs}}">
  <meta property="og:image:width" content="{{.Meme.Width}}">
  <meta property="og:image:height" content="{{.Meme.Height}}">
  <meta name="twitter:card" content="summary_large_image">
  <link rel="icon" href="data:image/svg+xml,%3Csvg xmlns=%22http://www.w3.org/2000/svg%22 viewBox=%220 0 100 100%22%3E%3Ctext y=%22.9em%22 font-size=%2290%22%3E%F0%9F%98%82%3C/text%3E%3C/svg%3E">
  <link rel="stylesheet" href="/style.css">
</head>
<body>
  <header class="topbar">
    <a class="brand" href="/">Meme Forge</a>
  </header>
  <main class="share-page">
    <h1>{{.Title}}</h1>
    <img src="{{.Meme.ImageURL}}" alt="{{.Title}}" width="{{.Meme.Width}}" height="{{.Meme.Height}}">
    <p class="meta">by <strong>{{.Meme.Author}}</strong> · ♥ {{.Meme.Likes}}{{if .Meme.TemplateName}} · template: {{.Meme.TemplateName}}{{end}}</p>
    <p class="actions">
      {{if .Meme.TemplateID}}<a class="btn primary" href="/#/editor/{{.Meme.TemplateID}}">Make your own with this template</a>{{else}}<a class="btn primary" href="/">Make your own meme</a>{{end}}
      <a class="btn secondary" href="/#/feed">Browse the feed</a>
    </p>
  </main>
</body>
</html>
`
