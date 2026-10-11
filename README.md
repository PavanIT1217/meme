# Meme Forge

A meme generator with a Go backend: browse a template library, add draggable text, then save memes with a shareable link, post them to a public feed and collect likes.

## Features
- **Template library** in SQLite, seeded with Imgflip's ~100 most popular templates on first run. Logged-in users can upload their own templates with names, tags and text-box counts. Templates can be searched, filtered by tag and sorted (most used, newest, A–Z).
- **Editor** (in the browser, canvas-based): multiple draggable text boxes, per-box size, font, colours, ALL CAPS; download as PNG or copy to clipboard.
- **Save & share**: saved memes get a short link (`/m/abc123`) with Open Graph tags, so they unfurl with a preview in chat apps and social media. Memes can be *public* (in the feed) or *unlisted* (link only).
- **Public feed** with New / Top sorting and likes.
- **Accounts**: sign up / log in (bcrypt passwords, HttpOnly session cookies), "My memes", favourite templates, delete your own memes and templates. Usernames listed in `ADMIN_USERS` can delete anything.

## Run locally
Requires Go 1.24+. No C compiler is needed (uses the pure-Go `modernc.org/sqlite` driver).

```sh
go run ./cmd/server            # http://localhost:8080
go run ./cmd/server -web web   # serve frontend files from disk while editing them
go test ./...
```

Data lives in `data/` (`meme.db` plus `uploads/`) by default.

## Configuration
| Flag / env | Default | Purpose |
|---|---|---|
| `-addr` / `ADDR` or `PORT` | `:8080` | Listen address |
| `-db` / `DB_PATH` | `data/meme.db` | SQLite database file |
| `-uploads` / `UPLOAD_DIR` | `data/uploads` | Uploaded templates and memes |
| `-seed` | `true` | Import Imgflip templates when the library is empty |
| `BASE_URL` | derived from request | Public URL used in share-link previews, e.g. `https://memes.example.com`. An `https://` value also turns on Secure cookies |
| `SECURE_COOKIES=1` | off | Force Secure cookies (when behind HTTPS without setting `BASE_URL`) |
| `ADMIN_USERS` | none | Comma-separated usernames with moderation rights |

## Deploy
The frontend is embedded in the binary, so you deploy one executable plus a persistent data directory.

```sh
docker build -t meme-forge .
docker run -p 8080:8080 -v meme-data:/data -e BASE_URL=https://memes.example.com meme-forge
```

Works on any host that offers a persistent disk/volume (Fly.io, Railway, Render with a disk, a VPS). Mount it at `/data`.

## API
All responses are JSON. Write endpoints need a session cookie (log in first).

| Method & path | Description |
|---|---|
| `POST /api/auth/register`, `POST /api/auth/login` | `{username, password}` → sets session cookie |
| `POST /api/auth/logout`, `GET /api/me` | |
| `GET /api/templates?q=&tag=&sort=popular\|new\|name&favorites=1&mine=1&limit=&offset=` | List/search templates |
| `GET /api/templates/{id}` | One template |
| `POST /api/templates` | Multipart: `image`, `name`, `tags`, `box_count` |
| `DELETE /api/templates/{id}` | Uploader or admin |
| `PUT` / `DELETE /api/templates/{id}/favorite` | Favourite / unfavourite |
| `GET /api/tags` | Most used tags |
| `GET /api/memes?sort=new\|top&limit=&offset=` | Public feed |
| `POST /api/memes` | Multipart: `image` (PNG/JPEG), `title`, `template_id`, `public` |
| `GET /api/memes/{id}`, `DELETE /api/memes/{id}` | View; delete (owner or admin) |
| `PUT` / `DELETE /api/memes/{id}/like` | Like / unlike |
| `GET /api/me/memes` | Your memes, including unlisted |
| `GET /m/{id}` | Shareable HTML page for a meme |

## Project layout
```
cmd/server/        entry point (flags, seeding, graceful shutdown)
internal/server/   HTTP handlers, SQLite schema, auth, uploads, share pages, tests
web/               frontend (index.html, app.js, style.css), embedded via web.go
```
