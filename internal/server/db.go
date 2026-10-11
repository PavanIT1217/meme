package server

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS users (
	id         INTEGER PRIMARY KEY,
	username   TEXT NOT NULL UNIQUE COLLATE NOCASE,
	google_sub TEXT NOT NULL UNIQUE,      -- stable Google account ID ("sub" claim)
	email      TEXT NOT NULL DEFAULT '',
	avatar_url TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
	token_hash TEXT PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	expires_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS templates (
	id          INTEGER PRIMARY KEY,
	name        TEXT NOT NULL,
	image_url   TEXT NOT NULL,
	width       INTEGER NOT NULL,
	height      INTEGER NOT NULL,
	box_count   INTEGER NOT NULL DEFAULT 2,
	tags        TEXT NOT NULL DEFAULT '',  -- comma-separated, lowercase
	uploaded_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
	external_id TEXT UNIQUE,                -- e.g. "imgflip:181913649" for seeded templates
	use_count   INTEGER NOT NULL DEFAULT 0,
	created_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS memes (
	id          TEXT PRIMARY KEY,           -- short random slug used in share links
	user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	template_id INTEGER REFERENCES templates(id) ON DELETE SET NULL,
	title       TEXT NOT NULL DEFAULT '',
	image_url   TEXT NOT NULL,
	width       INTEGER NOT NULL,
	height      INTEGER NOT NULL,
	is_public   INTEGER NOT NULL DEFAULT 1, -- listed in the public feed
	like_count  INTEGER NOT NULL DEFAULT 0,
	created_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS memes_feed_new ON memes(is_public, created_at);
CREATE INDEX IF NOT EXISTS memes_feed_top ON memes(is_public, like_count);
CREATE INDEX IF NOT EXISTS memes_user ON memes(user_id, created_at);

CREATE TABLE IF NOT EXISTS likes (
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	meme_id    TEXT NOT NULL REFERENCES memes(id) ON DELETE CASCADE,
	created_at INTEGER NOT NULL,
	PRIMARY KEY (user_id, meme_id)
);

CREATE TABLE IF NOT EXISTS favorites (
	user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	template_id INTEGER NOT NULL REFERENCES templates(id) ON DELETE CASCADE,
	created_at  INTEGER NOT NULL,
	PRIMARY KEY (user_id, template_id)
);
`

func openDB(path string) (*sql.DB, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite allows one writer at a time; a single connection keeps things simple and safe.
	db.SetMaxOpenConns(1)
	var legacy bool
	if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM pragma_table_info('users') WHERE name = 'password_hash')`).Scan(&legacy); err != nil {
		db.Close()
		return nil, err
	}
	if legacy {
		db.Close()
		return nil, fmt.Errorf("%s was created by an older version that used password accounts; delete it (and the uploads directory) to start fresh with Google sign-in", path)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return db, nil
}

func now() int64 { return time.Now().Unix() }

func logf(format string, args ...any) { log.Printf(format, args...) }
