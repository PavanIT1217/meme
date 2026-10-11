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
	ID         int64    `json:"id"`
	Name       string   `json:"name"`
	ImageURL   string   `json:"image_url"`
	Width      int      `json:"width"`
	Height     int      `json:"height"`
	BoxCount   int      `json:"box_count"`
	Tags       []string `json:"tags"`
	UseCount   int      `json:"use_count"`
	UploadedBy string   `json:"uploaded_by,omitempty"`
	Favorite   bool     `json:"favorite"`
	CanDelete  bool     `json:"can_delete"`
	CreatedAt  int64    `json:"created_at"`
}

const templateCols = `t.id, t.name, t.image_url, t.width, t.height, t.box_count, t.tags, t.use_count,
	t.created_at, t.uploaded_by, COALESCE(u.username, ''),
	EXISTS (SELECT 1 FROM favorites f WHERE f.template_id = t.id AND f.user_id = ?)`

func scanTemplate(row interface{ Scan(...any) error }, viewer *User) (templateJSON, error) {
	var (
		t        templateJSON
		tags     string
		uploader sql.NullInt64
	)
	err := row.Scan(&t.ID, &t.Name, &t.ImageURL, &t.Width, &t.Height, &t.BoxCount, &tags, &t.UseCount,
		&t.CreatedAt, &uploader, &t.UploadedBy, &t.Favorite)
	t.Tags = splitTags(tags)
	t.CanDelete = viewer != nil && (viewer.IsAdmin || (uploader.Valid && uploader.Int64 == viewer.ID))
	return t, err
}

func (s *Server) handleListTemplates(w http.ResponseWriter, r *http.Request) {
	viewer, err := s.userFrom(r)
	if err != nil {
		serverError(w, err)
		return
	}
	var viewerID int64
	if viewer != nil {
		viewerID = viewer.ID
	}
	q := r.URL.Query()
	limit, offset := pagination(r, 60, 200)

	where := []string{"1=1"}
	args := []any{}
	if search := strings.TrimSpace(q.Get("q")); search != "" {
		pat := "%" + escapeLike(strings.ToLower(search)) + "%"
		where = append(where, `(LOWER(t.name) LIKE ? ESCAPE '\' OR t.tags LIKE ? ESCAPE '\')`)
		args = append(args, pat, pat)
	}
	if tag := normalizeTag(q.Get("tag")); tag != "" {
		where = append(where, `(',' || t.tags || ',') LIKE ? ESCAPE '\'`)
		args = append(args, "%,"+escapeLike(tag)+",%")
	}
	if q.Get("favorites") == "1" {
		where = append(where, `EXISTS (SELECT 1 FROM favorites f WHERE f.template_id = t.id AND f.user_id = ?)`)
		args = append(args, viewerID)
	}
	if q.Get("mine") == "1" {
		where = append(where, `t.uploaded_by = ?`)
		args = append(args, viewerID)
	}
	whereSQL := strings.Join(where, " AND ")

	order := "t.use_count DESC, t.id ASC"
	switch q.Get("sort") {
	case "new":
		order = "t.created_at DESC, t.id DESC"
	case "name":
		order = "t.name COLLATE NOCASE ASC"
	}

	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM templates t WHERE `+whereSQL, args...).Scan(&total); err != nil {
		serverError(w, err)
		return
	}

	rows, err := s.db.Query(`SELECT `+templateCols+` FROM templates t LEFT JOIN users u ON u.id = t.uploaded_by
		WHERE `+whereSQL+` ORDER BY `+order+` LIMIT ? OFFSET ?`,
		append(append([]any{viewerID}, args...), limit, offset)...)
	if err != nil {
		serverError(w, err)
		return
	}
	defer rows.Close()
	list := []templateJSON{}
	for rows.Next() {
		t, err := scanTemplate(rows, viewer)
		if err != nil {
			serverError(w, err)
			return
		}
		list = append(list, t)
	}
	if err := rows.Err(); err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": list, "total": total})
}

func (s *Server) getTemplate(id int64, viewer *User) (templateJSON, error) {
	var viewerID int64
	if viewer != nil {
		viewerID = viewer.ID
	}
	row := s.db.QueryRow(`SELECT `+templateCols+` FROM templates t LEFT JOIN users u ON u.id = t.uploaded_by
		WHERE t.id = ?`, viewerID, id)
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

// handleCreateTemplate accepts multipart fields: image, name, tags (comma-separated), box_count.
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
	res, err := s.db.Exec(`INSERT INTO templates (name, image_url, width, height, box_count, tags, uploaded_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, name, img.URL, img.Width, img.Height, boxCount, tags, u.ID, now())
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

// handleTags returns the most used tags with their template counts.
func (s *Server) handleTags(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(`SELECT tags FROM templates WHERE tags != ''`)
	if err != nil {
		serverError(w, err)
		return
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var tags string
		if err := rows.Scan(&tags); err != nil {
			serverError(w, err)
			return
		}
		for _, t := range splitTags(tags) {
			counts[t]++
		}
	}
	type tagCount struct {
		Tag   string `json:"tag"`
		Count int    `json:"count"`
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
	if len(out) > 30 {
		out = out[:30]
	}
	writeJSON(w, http.StatusOK, map[string]any{"tags": out})
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
