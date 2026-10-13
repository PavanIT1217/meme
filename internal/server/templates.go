package server

import (
	"database/sql"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type templateJSON struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	URL         string   `json:"url"` // canonical page, e.g. /templates/12/drake-hotline-bling
	ImageURL    string   `json:"image_url"`
	ThumbURL    string   `json:"thumb_url"` // small JPEG for gallery cards
	Width       int      `json:"width"`
	Height      int      `json:"height"`
	BoxCount    int      `json:"box_count"`
	Tags        []string `json:"tags"`
	UseCount    int      `json:"use_count"`
	UploadedBy  string   `json:"uploaded_by,omitempty"`
	Favorite    bool     `json:"favorite"`
	CanDelete   bool     `json:"can_delete"`
	CreatedAt   int64    `json:"created_at"`
}

const templateCols = `t.id, t.name, t.description, t.image_url, t.width, t.height, t.box_count, t.tags, t.use_count,
	t.created_at, t.uploaded_by, COALESCE(u.username, ''),
	EXISTS (SELECT 1 FROM favorites f WHERE f.template_id = t.id AND f.user_id = ?)`

func scanTemplate(row interface{ Scan(...any) error }, viewer *User) (templateJSON, error) {
	var (
		t        templateJSON
		tags     string
		uploader sql.NullInt64
	)
	err := row.Scan(&t.ID, &t.Name, &t.Description, &t.ImageURL, &t.Width, &t.Height, &t.BoxCount, &tags, &t.UseCount,
		&t.CreatedAt, &uploader, &t.UploadedBy, &t.Favorite)
	t.Tags = splitTags(tags)
	t.URL = templatePath(t.ID, t.Name)
	t.ThumbURL = thumbURL("templates", strconv.FormatInt(t.ID, 10), templateThumbWidth)
	t.CanDelete = viewer != nil && (viewer.IsAdmin || (uploader.Valid && uploader.Int64 == viewer.ID))
	return t, err
}

func viewerID(u *User) int64 {
	if u == nil {
		return 0
	}
	return u.ID
}

type templateQuery struct {
	Q, Tag, Sort    string
	Favorites, Mine bool
	Limit, Offset   int
	ExcludeID       int64
}

// queryTemplates returns one page of templates matching tq plus the total number of matches.
func (s *Server) queryTemplates(viewer *User, tq templateQuery) ([]templateJSON, int, error) {
	where := []string{"1=1"}
	args := []any{}
	if search := strings.TrimSpace(tq.Q); search != "" {
		pat := "%" + escapeLike(strings.ToLower(search)) + "%"
		where = append(where, `(LOWER(t.name) LIKE ? ESCAPE '\' OR t.tags LIKE ? ESCAPE '\')`)
		args = append(args, pat, pat)
	}
	if tag := normalizeTag(tq.Tag); tag != "" {
		where = append(where, `(',' || t.tags || ',') LIKE ? ESCAPE '\'`)
		args = append(args, "%,"+escapeLike(tag)+",%")
	}
	if tq.Favorites {
		where = append(where, `EXISTS (SELECT 1 FROM favorites f WHERE f.template_id = t.id AND f.user_id = ?)`)
		args = append(args, viewerID(viewer))
	}
	if tq.Mine {
		where = append(where, `t.uploaded_by = ?`)
		args = append(args, viewerID(viewer))
	}
	if tq.ExcludeID != 0 {
		where = append(where, `t.id != ?`)
		args = append(args, tq.ExcludeID)
	}
	whereSQL := strings.Join(where, " AND ")

	order := "t.use_count DESC, t.id ASC"
	switch tq.Sort {
	case "new":
		order = "t.created_at DESC, t.id DESC"
	case "name":
		order = "t.name COLLATE NOCASE ASC"
	}

	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM templates t WHERE `+whereSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query(`SELECT `+templateCols+` FROM templates t LEFT JOIN users u ON u.id = t.uploaded_by
		WHERE `+whereSQL+` ORDER BY `+order+` LIMIT ? OFFSET ?`,
		append(append([]any{viewerID(viewer)}, args...), tq.Limit, tq.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	list := []templateJSON{}
	for rows.Next() {
		t, err := scanTemplate(rows, viewer)
		if err != nil {
			return nil, 0, err
		}
		list = append(list, t)
	}
	return list, total, rows.Err()
}

func (s *Server) handleListTemplates(w http.ResponseWriter, r *http.Request) {
	viewer, err := s.userFrom(r)
	if err != nil {
		serverError(w, err)
		return
	}
	q := r.URL.Query()
	limit, offset := pagination(r, 60, 200)
	list, total, err := s.queryTemplates(viewer, templateQuery{
		Q: q.Get("q"), Tag: q.Get("tag"), Sort: q.Get("sort"),
		Favorites: q.Get("favorites") == "1", Mine: q.Get("mine") == "1",
		Limit: limit, Offset: offset,
	})
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": list, "total": total})
}

func (s *Server) getTemplate(id int64, viewer *User) (templateJSON, error) {
	row := s.db.QueryRow(`SELECT `+templateCols+` FROM templates t LEFT JOIN users u ON u.id = t.uploaded_by
		WHERE t.id = ?`, viewerID(viewer), id)
	t, err := scanTemplate(row, viewer)
	if errors.Is(err, sql.ErrNoRows) {
		return t, errNotFound
	}
	return t, err
}

func (s *Server) handleGetTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "template not found")
		return
	}
	viewer, err := s.userFrom(r)
	if err != nil {
		serverError(w, err)
		return
	}
	t, err := s.getTemplate(id, viewer)
	if errors.Is(err, errNotFound) {
		writeError(w, http.StatusNotFound, "template not found")
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// handleCreateTemplate accepts multipart fields: image, name, description, tags (comma-separated), box_count.
func (s *Server) handleCreateTemplate(w http.ResponseWriter, r *http.Request, u *User) {
	data, err := readImageUpload(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" || len([]rune(name)) > 100 {
		writeError(w, http.StatusBadRequest, "name is required (max 100 characters)")
		return
	}
	description := strings.TrimSpace(r.FormValue("description"))
	if len([]rune(description)) > 500 {
		writeError(w, http.StatusBadRequest, "description must be at most 500 characters")
		return
	}
	boxCount := 2
	if v := r.FormValue("box_count"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 10 {
			writeError(w, http.StatusBadRequest, "box_count must be between 1 and 10")
			return
		}
		boxCount = n
	}
	tags := normalizeTags(r.FormValue("tags"))

	img, err := s.saveImage(data, "templates", "png", "jpeg", "gif")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := s.db.Exec(`INSERT INTO templates (name, description, image_url, width, height, box_count, tags, uploaded_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, name, description, img.URL, img.Width, img.Height, boxCount, tags, u.ID, now())
	if err != nil {
		s.removeUpload(img.URL)
		serverError(w, err)
		return
	}
	id, _ := res.LastInsertId()
	t, err := s.getTemplate(id, u)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (s *Server) handleDeleteTemplate(w http.ResponseWriter, r *http.Request, u *User) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "template not found")
		return
	}
	t, err := s.getTemplate(id, u)
	if errors.Is(err, errNotFound) {
		writeError(w, http.StatusNotFound, "template not found")
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}
	if !t.CanDelete {
		writeError(w, http.StatusForbidden, "you can only delete templates you uploaded")
		return
	}
	if _, err := s.db.Exec(`DELETE FROM templates WHERE id = ?`, id); err != nil {
		serverError(w, err)
		return
	}
	s.removeUpload(t.ImageURL)
	s.removeThumbs("templates", strconv.FormatInt(id, 10))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleFavorite(on bool) func(http.ResponseWriter, *http.Request, *User) {
	return func(w http.ResponseWriter, r *http.Request, u *User) {
		id, ok := pathID(r)
		if !ok {
			writeError(w, http.StatusNotFound, "template not found")
			return
		}
		var err error
		if on {
			var exists bool
			if err = s.db.QueryRow(`SELECT EXISTS (SELECT 1 FROM templates WHERE id = ?)`, id).Scan(&exists); err == nil && !exists {
				writeError(w, http.StatusNotFound, "template not found")
				return
			}
			if err == nil {
				_, err = s.db.Exec(`INSERT OR IGNORE INTO favorites (user_id, template_id, created_at) VALUES (?, ?, ?)`, u.ID, id, now())
			}
		} else {
			_, err = s.db.Exec(`DELETE FROM favorites WHERE user_id = ? AND template_id = ?`, u.ID, id)
		}
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"favorite": on})
	}
}

type tagCount struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

// topTags returns the most used tags with their template counts (all of them when limit <= 0).
func (s *Server) topTags(limit int) ([]tagCount, error) {
	rows, err := s.db.Query(`SELECT tags FROM templates WHERE tags != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var tags string
		if err := rows.Scan(&tags); err != nil {
			return nil, err
		}
		for _, t := range splitTags(tags) {
			counts[t]++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []tagCount{}
	for t, c := range counts {
		out = append(out, tagCount{t, c})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Tag < out[j].Tag
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *Server) handleTags(w http.ResponseWriter, r *http.Request) {
	tags, err := s.topTags(30)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tags": tags})
}

var tagCleanRe = regexp.MustCompile(`[^a-z0-9 -]+`)

func normalizeTag(t string) string {
	t = tagCleanRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(t)), "")
	t = strings.Join(strings.Fields(t), " ")
	if len(t) > 24 {
		t = t[:24]
	}
	return strings.TrimSpace(t)
}

// normalizeTags turns user input like "Cats, reaction ,CATS" into "cats,reaction" (max 10 tags).
func normalizeTags(raw string) string {
	seen := map[string]bool{}
	var out []string
	for _, t := range strings.Split(raw, ",") {
		if t = normalizeTag(t); t != "" && !seen[t] && len(out) < 10 {
			seen[t] = true
			out = append(out, t)
		}
	}
	return strings.Join(out, ",")
}

func splitTags(s string) []string {
	if s == "" {
		return []string{}
	}
	return strings.Split(s, ",")
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
