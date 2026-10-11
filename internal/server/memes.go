package server

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

type memeJSON struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	ImageURL     string `json:"image_url"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	Public       bool   `json:"public"`
	Likes        int    `json:"likes"`
	Liked        bool   `json:"liked"`
	Author       string `json:"author"`
	TemplateID   *int64 `json:"template_id"`
	TemplateName string `json:"template_name,omitempty"`
	CanDelete    bool   `json:"can_delete"`
	ShareURL     string `json:"share_url"`
	CreatedAt    int64  `json:"created_at"`
}

const memeSelect = `SELECT m.id, m.title, m.image_url, m.width, m.height, m.is_public, m.like_count,
	EXISTS (SELECT 1 FROM likes l WHERE l.meme_id = m.id AND l.user_id = ?),
	m.user_id, u.username, m.template_id, COALESCE(t.name, ''), m.created_at
	FROM memes m
	JOIN users u ON u.id = m.user_id
	LEFT JOIN templates t ON t.id = m.template_id`

func scanMeme(row interface{ Scan(...any) error }, viewer *User) (memeJSON, error) {
	var (
		m      memeJSON
		userID int64
		tmplID sql.NullInt64
	)
	err := row.Scan(&m.ID, &m.Title, &m.ImageURL, &m.Width, &m.Height, &m.Public, &m.Likes, &m.Liked,
		&userID, &m.Author, &tmplID, &m.TemplateName, &m.CreatedAt)
	if tmplID.Valid {
		m.TemplateID = &tmplID.Int64
	}
	m.ShareURL = "/m/" + m.ID
	m.CanDelete = viewer != nil && (viewer.IsAdmin || viewer.ID == userID)
	return m, err
}

func (s *Server) queryMemes(w http.ResponseWriter, viewer *User, where string, order string, args ...any) {
	var viewerID int64
	if viewer != nil {
		viewerID = viewer.ID
	}
	rows, err := s.db.Query(memeSelect+` WHERE `+where+` ORDER BY `+order+` LIMIT ? OFFSET ?`,
		append([]any{viewerID}, args...)...)
	if err != nil {
		serverError(w, err)
		return
	}
	defer rows.Close()
	list := []memeJSON{}
	for rows.Next() {
		m, err := scanMeme(rows, viewer)
		if err != nil {
			serverError(w, err)
			return
		}
		list = append(list, m)
	}
	if err := rows.Err(); err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"memes": list})
}

// handleListMemes is the public feed. sort=new (default) or sort=top.
func (s *Server) handleListMemes(w http.ResponseWriter, r *http.Request) {
	viewer, err := s.userFrom(r)
	if err != nil {
		serverError(w, err)
		return
	}
	limit, offset := pagination(r, 30, 100)
	order := "m.created_at DESC, m.id"
	if r.URL.Query().Get("sort") == "top" {
		order = "m.like_count DESC, m.created_at DESC, m.id"
	}
	s.queryMemes(w, viewer, "m.is_public = 1", order, limit, offset)
}

func (s *Server) handleMyMemes(w http.ResponseWriter, r *http.Request, u *User) {
	limit, offset := pagination(r, 30, 100)
	s.queryMemes(w, u, "m.user_id = ?", "m.created_at DESC, m.id", u.ID, limit, offset)
}

func (s *Server) getMeme(id string, viewer *User) (memeJSON, error) {
	var viewerID int64
	if viewer != nil {
		viewerID = viewer.ID
	}
	m, err := scanMeme(s.db.QueryRow(memeSelect+` WHERE m.id = ?`, viewerID, id), viewer)
	if errors.Is(err, sql.ErrNoRows) {
		return m, errNotFound
	}
	return m, err
}

func (s *Server) handleGetMeme(w http.ResponseWriter, r *http.Request) {
	viewer, err := s.userFrom(r)
	if err != nil {
		serverError(w, err)
		return
	}
	m, err := s.getMeme(r.PathValue("id"), viewer)
	if errors.Is(err, errNotFound) {
		writeError(w, http.StatusNotFound, "meme not found")
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// handleCreateMeme accepts multipart fields: image (PNG/JPEG), title, template_id, public.
// Unlisted memes (public=false) stay out of the feed but are viewable by anyone with the link.
func (s *Server) handleCreateMeme(w http.ResponseWriter, r *http.Request, u *User) {
	data, err := readImageUpload(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	title := strings.TrimSpace(r.FormValue("title"))
	if len([]rune(title)) > 140 {
		writeError(w, http.StatusBadRequest, "title must be at most 140 characters")
		return
	}
	public := true
	if v := r.FormValue("public"); v != "" {
		public = v == "1" || v == "true" || v == "on"
	}
	var templateID any // nil → NULL
	if v := r.FormValue("template_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		var exists bool
		if err == nil {
			err = s.db.QueryRow(`SELECT EXISTS (SELECT 1 FROM templates WHERE id = ?)`, id).Scan(&exists)
		}
		if err != nil || !exists {
			writeError(w, http.StatusBadRequest, "unknown template_id")
			return
		}
		templateID = id
	}

	img, err := s.saveImage(data, "memes", "png", "jpeg")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	id, err := s.insertMeme(u.ID, templateID, title, img, public)
	if err != nil {
		s.removeUpload(img.URL)
		serverError(w, err)
		return
	}
	m, err := s.getMeme(id, u)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

func (s *Server) insertMeme(userID int64, templateID any, title string, img savedImage, public bool) (string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var id string
	for attempt := 0; ; attempt++ {
		id = randomString(8)
		_, err = tx.Exec(`INSERT INTO memes (id, user_id, template_id, title, image_url, width, height, is_public, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, userID, templateID, title, img.URL, img.Width, img.Height, public, now())
		if !isUniqueViolation(err) || attempt == 3 {
			break
		}
	}
	if err != nil {
		return "", err
	}
	if templateID != nil {
		if _, err := tx.Exec(`UPDATE templates SET use_count = use_count + 1 WHERE id = ?`, templateID); err != nil {
			return "", err
		}
	}
	return id, tx.Commit()
}

func (s *Server) handleDeleteMeme(w http.ResponseWriter, r *http.Request, u *User) {
	m, err := s.getMeme(r.PathValue("id"), u)
	if errors.Is(err, errNotFound) {
		writeError(w, http.StatusNotFound, "meme not found")
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}
	if !m.CanDelete {
		writeError(w, http.StatusForbidden, "you can only delete your own memes")
		return
	}
	if _, err := s.db.Exec(`DELETE FROM memes WHERE id = ?`, m.ID); err != nil {
		serverError(w, err)
		return
	}
	s.removeUpload(m.ImageURL)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleLike(on bool) func(http.ResponseWriter, *http.Request, *User) {
	return func(w http.ResponseWriter, r *http.Request, u *User) {
		id := r.PathValue("id")
		tx, err := s.db.Begin()
		if err != nil {
			serverError(w, err)
			return
		}
		defer tx.Rollback()

		var exists bool
		if err := tx.QueryRow(`SELECT EXISTS (SELECT 1 FROM memes WHERE id = ?)`, id).Scan(&exists); err != nil {
			serverError(w, err)
			return
		}
		if !exists {
			writeError(w, http.StatusNotFound, "meme not found")
			return
		}

		var res sql.Result
		delta := 1
		if on {
			res, err = tx.Exec(`INSERT OR IGNORE INTO likes (user_id, meme_id, created_at) VALUES (?, ?, ?)`, u.ID, id, now())
		} else {
			delta = -1
			res, err = tx.Exec(`DELETE FROM likes WHERE user_id = ? AND meme_id = ?`, u.ID, id)
		}
		if err != nil {
			serverError(w, err)
			return
		}
		// Only adjust the counter if the like actually changed (makes PUT/DELETE idempotent).
		if n, _ := res.RowsAffected(); n == 1 {
			if _, err := tx.Exec(`UPDATE memes SET like_count = like_count + ? WHERE id = ?`, delta, id); err != nil {
				serverError(w, err)
				return
			}
		}
		var likes int
		if err := tx.QueryRow(`SELECT like_count FROM memes WHERE id = ?`, id).Scan(&likes); err != nil {
			serverError(w, err)
			return
		}
		if err := tx.Commit(); err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"liked": on, "likes": likes})
	}
}
