package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	xdraw "golang.org/x/image/draw"
)

// Thumbnails keep gallery pages fast: cards show a small JPEG instead of the
// full-size template or meme image. They are generated on first request and
// cached on disk under UploadDir/thumbs.

const (
	templateThumbWidth = 320
	memeThumbWidth     = 480
	memeLargeWidth     = 960
	maxSourceBytes     = 10 << 20
)

var (
	thumbWidths = map[int]bool{templateThumbWidth: true, memeThumbWidth: true, memeLargeWidth: true}
	thumbNameRe = regexp.MustCompile(`^([A-Za-z0-9]+)-(\d+)\.jpg$`)
	thumbLocks  sync.Map // cache path → *sync.Mutex, so each thumbnail is generated once
)

func thumbURL(kind, id string, width int) string {
	return fmt.Sprintf("/thumbs/%s/%s-%d.jpg", kind, id, width)
}

func (s *Server) handleThumb(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	m := thumbNameRe.FindStringSubmatch(r.PathValue("name"))
	if m == nil {
		http.NotFound(w, r)
		return
	}
	id := m[1]
	width, _ := strconv.Atoi(m[2])
	if !thumbWidths[width] {
		http.NotFound(w, r)
		return
	}

	var src string
	switch kind {
	case "templates":
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		t, err := s.getTemplate(n, nil)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		src = t.ImageURL
	case "memes":
		mm, err := s.getMeme(id, nil)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		src = mm.ImageURL
	default:
		http.NotFound(w, r)
		return
	}

	// The source URL is part of the cache key, so a changed image never serves a stale thumbnail.
	sum := sha256.Sum256([]byte(src))
	path := filepath.Join(s.cfg.UploadDir, "thumbs", kind, fmt.Sprintf("%s-%d-%s.jpg", id, width, hex.EncodeToString(sum[:4])))
	// Detached from the request: if the visitor navigates away, the thumbnail still gets cached.
	if err := s.ensureThumb(context.WithoutCancel(r.Context()), path, src, width); err != nil {
		logf("thumbnail %s: %v", path, err)
		http.Redirect(w, r, src, http.StatusFound) // fall back to the full image
		return
	}
	f, err := os.Open(path)
	if err != nil {
		serverError(w, err)
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=604800")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	http.ServeContent(w, r, "", st.ModTime(), f)
}

func (s *Server) ensureThumb(ctx context.Context, path, src string, width int) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	mu, _ := thumbLocks.LoadOrStore(path, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	if _, err := os.Stat(path); err == nil { // made while we waited
		return nil
	}

	data, err := s.loadSource(ctx, src)
	if err != nil {
		return err
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return err
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > maxImageSide || cfg.Height > maxImageSide {
		return errors.New("source image has unsupported dimensions")
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return err
	}
	tw := min(width, cfg.Width)
	th := max(1, cfg.Height*tw/cfg.Width)
	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	draw.Draw(dst, dst.Bounds(), image.White, image.Point{}, draw.Src) // JPEG has no transparency
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Over, nil)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 80}); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// loadSource reads an image from our uploads directory or, for seeded templates, over HTTPS.
func (s *Server) loadSource(ctx context.Context, src string) ([]byte, error) {
	if rel, ok := strings.CutPrefix(src, "/uploads/"); ok {
		if strings.Contains(rel, "..") {
			return nil, errors.New("bad upload path")
		}
		return os.ReadFile(filepath.Join(s.cfg.UploadDir, filepath.FromSlash(rel)))
	}
	if !strings.HasPrefix(src, "https://") {
		return nil, errors.New("unsupported image source")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: status %d", src, res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxSourceBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSourceBytes {
		return nil, errors.New("source image too large")
	}
	return data, nil
}

// removeThumbs deletes cached thumbnails of a deleted template or meme.
func (s *Server) removeThumbs(kind, id string) {
	matches, _ := filepath.Glob(filepath.Join(s.cfg.UploadDir, "thumbs", kind, id+"-*.jpg"))
	for _, m := range matches {
		os.Remove(m)
	}
}
