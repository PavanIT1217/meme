package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// SeedImgflip fills an empty template library with Imgflip's ~100 most popular
// templates. Images stay hosted on Imgflip (which allows cross-origin canvas use).
// It does nothing if the library already has templates.
func (s *Server) SeedImgflip(ctx context.Context) (int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM templates`).Scan(&count); err != nil {
		return 0, err
	}
	if count > 0 {
		return 0, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.imgflip.com/get_memes", nil)
	if err != nil {
		return 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var body struct {
		Success bool `json:"success"`
		Data    struct {
			Memes []struct {
				ID       string `json:"id"`
				Name     string `json:"name"`
				URL      string `json:"url"`
				Width    int    `json:"width"`
				Height   int    `json:"height"`
				BoxCount int    `json:"box_count"`
			} `json:"memes"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, fmt.Errorf("decode imgflip response: %w", err)
	}
	if !body.Success {
		return 0, fmt.Errorf("imgflip API returned success=false")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	ts := now()
	for _, m := range body.Data.Memes {
		boxes := min(max(m.BoxCount, 1), 10)
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO templates
			(name, image_url, width, height, box_count, external_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			m.Name, m.URL, m.Width, m.Height, boxes, "imgflip:"+m.ID, ts); err != nil {
			return 0, err
		}
	}
	return len(body.Data.Memes), tx.Commit()
}
