package server

import (
	"bytes"
	"crypto/rand"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxImageBytes = 8 << 20 // 8 MB
	maxImageSide  = 6000
)

var (
	errImageTooLarge = errors.New("image is larger than 8 MB")
	errBadImage      = errors.New("file is not a supported image (PNG, JPEG or GIF)")
)

var imageExt = map[string]string{"png": ".png", "jpeg": ".jpg", "gif": ".gif"}

type savedImage struct {
	URL           string
	Width, Height int
}

// readImageUpload parses a multipart request and returns the bytes of its "image" field.
func readImageUpload(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxImageBytes+(1<<20))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, errImageTooLarge
		}
		return nil, errors.New("expected a multipart form upload")
	}
	f, _, err := r.FormFile("image")
	if err != nil {
		return nil, errors.New(`missing "image" file`)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxImageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxImageBytes {
		return nil, errImageTooLarge
	}
	return data, nil
}

// saveImage validates image bytes and writes them under UploadDir/kind with a random name.
func (s *Server) saveImage(data []byte, kind string, allowed ...string) (savedImage, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return savedImage{}, errBadImage
	}
	ok := false
	for _, a := range allowed {
		ok = ok || a == format
	}
	if !ok {
		return savedImage{}, errBadImage
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > maxImageSide || cfg.Height > maxImageSide {
		return savedImage{}, errors.New("image dimensions must be at most 6000×6000")
	}
	name := randomString(20) + imageExt[format]
	if err := os.WriteFile(filepath.Join(s.cfg.UploadDir, kind, name), data, 0o644); err != nil {
		return savedImage{}, err
	}
	return savedImage{URL: "/uploads/" + kind + "/" + name, Width: cfg.Width, Height: cfg.Height}, nil
}

// removeUpload deletes a file previously returned by saveImage. Remote URLs are ignored.
func (s *Server) removeUpload(url string) {
	rel, ok := strings.CutPrefix(url, "/uploads/")
	if !ok || strings.Contains(rel, "..") {
		return
	}
	if err := os.Remove(filepath.Join(s.cfg.UploadDir, filepath.FromSlash(rel))); err != nil && !os.IsNotExist(err) {
		logf("remove upload %s: %v", url, err)
	}
}

const idAlphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// randomString returns n characters drawn uniformly from idAlphabet.
func randomString(n int) string {
	out := make([]byte, n)
	buf := make([]byte, 1)
	for i := 0; i < n; {
		rand.Read(buf)
		// Reject values that would bias the distribution.
		if int(buf[0]) >= 256-256%len(idAlphabet) {
			continue
		}
		out[i] = idAlphabet[int(buf[0])%len(idAlphabet)]
		i++
	}
	return string(out)
}
