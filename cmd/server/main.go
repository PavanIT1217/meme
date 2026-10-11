// Command server runs the Meme Forge web app: API, share pages and frontend.
package main

import (
	"context"
	"errors"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/pavanit1217/meme"
	"github.com/pavanit1217/meme/internal/server"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	addr := flag.String("addr", env("ADDR", ":"+env("PORT", "8080")), "listen address (env ADDR or PORT)")
	dbPath := flag.String("db", env("DB_PATH", "data/meme.db"), "SQLite database path (env DB_PATH)")
	uploadDir := flag.String("uploads", env("UPLOAD_DIR", "data/uploads"), "directory for uploaded images (env UPLOAD_DIR)")
	webDir := flag.String("web", "", "serve the frontend from this directory instead of the embedded copy (for development)")
	seed := flag.Bool("seed", true, "import Imgflip's popular templates when the library is empty")
	flag.Parse()

	baseURL := os.Getenv("BASE_URL")
	var admins []string
	if v := os.Getenv("ADMIN_USERS"); v != "" {
		admins = strings.Split(v, ",")
	}

	var static fs.FS
	if *webDir != "" {
		static = os.DirFS(*webDir)
	} else {
		sub, err := fs.Sub(meme.Web, "web")
		if err != nil {
			log.Fatal(err)
		}
		static = sub
	}

	srv, err := server.New(server.Config{
		DBPath:        *dbPath,
		UploadDir:     *uploadDir,
		Admins:        admins,
		BaseURL:       baseURL,
		SecureCookies: strings.HasPrefix(baseURL, "https://") || os.Getenv("SECURE_COOKIES") == "1",
		Static:        static,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer srv.Close()

	if *seed {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		n, err := srv.SeedImgflip(ctx)
		cancel()
		switch {
		case err != nil:
			log.Printf("seeding templates from Imgflip failed (continuing without them): %v", err)
		case n > 0:
			log.Printf("seeded %d templates from Imgflip", n)
		}
	}

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		log.Printf("Meme Forge listening on %s", *addr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Print("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	httpSrv.Shutdown(ctx)
}
