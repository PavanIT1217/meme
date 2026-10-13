# Meme Forge

A meme generator with a Go backend: browse a template library, add draggable text, then save memes with a shareable link, post them to a public feed and collect likes.

## Features
- **Template library** in SQLite, seeded with Imgflip's ~100 most popular templates on first run. Logged-in users can upload their own templates with names, tags and text-box counts. Templates can be searched, filtered by tag and sorted (most used, newest, A–Z).
- **Editor** (in the browser, canvas-based): multiple draggable text boxes, per-box size, font, colours, ALL CAPS; download as PNG or copy to clipboard.
- **Save & share**: saved memes get a short link (`/m/abc123`) with Open Graph tags, so they unfurl with a preview in chat apps and social media. Memes can be *public* (in the feed) or *unlisted* (link only).
- **Public feed** with New / Top sorting and likes.
- **Sign in with Google**: accounts are created on first sign-in (server-side OAuth code flow with PKCE, HttpOnly session cookies). Usernames start from the Google name and can be changed. "My memes", favourite templates, delete your own memes and templates. Usernames listed in `ADMIN_USERS` can delete anything. Browsing works without signing in.
- **SEO-ready, server-rendered pages**: every template has its own indexable page (`/templates/12/drake-hotline-bling`) with an editor, description, how-to and related templates; plus tag pages, feeds and meme pages. Titles, meta descriptions, canonical URLs, Open Graph/Twitter cards, JSON-LD structured data, `robots.txt`, an image sitemap, gzip, cached thumbnails and fingerprinted assets (see [SEO](#seo)).
- **Rate limiting** per client IP (all API calls, sign-in) and per user (saving memes, uploading templates, likes and other writes). Over-limit requests get `429` with a `Retry-After` header.

## Set up Google sign-in
1. In the [Google Cloud console](https://console.cloud.google.com/apis/credentials), configure the OAuth consent screen (External, scopes `openid`, `email`, `profile`).
2. Create an **OAuth client ID** of type *Web application*, and add these **Authorized redirect URIs**:
   - `http://localhost:8080/auth/google/callback` (local development)
   - `https://YOUR-DOMAIN/auth/google/callback` (production)
3. Pass the credentials to the server:
   ```sh
   export GOOGLE_CLIENT_ID=xxxx.apps.googleusercontent.com
   export GOOGLE_CLIENT_SECRET=xxxx
   ```
Without them the site still runs, but signing in is disabled.

## Run locally
Requires Go 1.24+. No C compiler is needed (uses the pure-Go `modernc.org/sqlite` driver).

```sh
go run ./cmd/server            # http://localhost:8080
go run ./cmd/server -web web   # load frontend files from disk instead of the embedded copy (restart to pick up edits)
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
| `GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_SECRET` | none | Google OAuth credentials (see above) |
| `TRUST_PROXY=1` | off | Use the right-most `X-Forwarded-For` entry as the client IP for rate limiting. Enable only behind a reverse proxy / load balancer that sets it |

### Rate limits
| What | Limit | Keyed by |
|---|---|---|
| Any `/api` or `/auth` request | 300/min, burst 100 | IP |
| Starting Google sign-in | 20/min | IP |
| Likes, favourites, deletes, renames | 120/min, burst 60 | user |
| Saving memes | 60/hour, burst 10 | user |
| Uploading templates | 20/hour, burst 5 | user |

Defaults live in `DefaultLimits()` in `internal/server/ratelimit.go`. Counters are in memory, so they reset on restart and are per server instance.

## SEO
Pages are rendered on the server, so search engines see the full content without running JavaScript; `web/static/app.js` then adds the interactive parts.

| URL | Page | Indexed |
|---|---|---|
| `/` | Template gallery with "how to make a meme" copy; `?page=N` pagination | yes (`?q=` / `?sort=` searches are `noindex`) |
| `/templates/{id}/{slug}` | Template page: editor, description, how-to, memes made with it, related templates. `/templates/{id}` and outdated slugs 301 to the canonical URL | yes |
| `/tags/{tag}` | Templates with a tag | yes |
| `/feed`, `/feed/top` | Latest / most-liked public memes | yes |
| `/m/{id}` | A meme: image, author, likes, share links, more memes | public memes yes, unlisted `noindex` |
| `/editor`, `/me/memes` | Edit your own image, your memes | `noindex` |
| anything else | Real `404` status with links back into the site | `noindex` |

Every page has a unique `<title>` and meta description, a canonical URL, `rel=prev/next` pagination links, Open Graph and Twitter card tags (meme and template images become the preview image), breadcrumbs, and schema.org JSON-LD (`WebSite` with a sitelinks search box, `Organization`, `BreadcrumbList`, `ItemList`, `CollectionPage`, and `ImageObject` for templates and memes).

- `GET /robots.txt` keeps crawlers out of `/api/`, `/auth/`, `/me/` and `/editor`, and points at the sitemap.
- `GET /sitemap.xml` lists the home and feed pages, every tag and template page, and public memes (newest first, up to 50,000 URLs), with Google image-sitemap entries.
- `GET /manifest.webmanifest`, `/favicon.ico`, `/apple-touch-icon.png` and a default social preview image (`web/static/og-default.png`).
- Performance (Core Web Vitals): gzip for text responses; `/static/*` assets are fingerprinted (`?v=<hash>`) and cached for a year; galleries use cached JPEG thumbnails from `/thumbs/...` (generated on first request into `UPLOAD_DIR/thumbs`); images carry width/height to avoid layout shift. Lighthouse (mobile) scores 100 for performance, accessibility and SEO on the home, template, feed and meme pages.

**Set `BASE_URL` in production** so canonical URLs, the sitemap and preview images use your real domain. Then submit `https://YOUR-DOMAIN/sitemap.xml` in Google Search Console and Bing Webmaster Tools.

## Deploy
The frontend is embedded in the binary, so you deploy one executable plus a persistent data directory.

```sh
docker build -t meme-forge .
docker run -p 8080:8080 -v meme-data:/data \
  -e BASE_URL=https://memes.example.com -e TRUST_PROXY=1 \
  -e GOOGLE_CLIENT_ID=... -e GOOGLE_CLIENT_SECRET=... meme-forge
```

Works on any host that offers a persistent disk/volume (Fly.io, Railway, Render with a disk, a VPS). Mount it at `/data`.

## API
All responses are JSON. Write endpoints need a session cookie (sign in first).

| Method & path | Description |
|---|---|
| `GET /auth/google/login?next=/path` | Redirects to Google; after sign-in returns to `next` with a session cookie |
| `POST /api/auth/logout` | Sign out |
| `GET /api/me` | `{user, google_login}`; `user` is `null` when signed out |
| `PATCH /api/me` | `{username}`: change your username |
| `GET /api/templates?q=&tag=&sort=popular\|new\|name&favorites=1&mine=1&limit=&offset=` | List/search templates |
| `GET /api/templates/{id}` | One template |
| `POST /api/templates` | Multipart: `image`, `name`, `description`, `tags`, `box_count` |
| `DELETE /api/templates/{id}` | Uploader or admin |
| `PUT` / `DELETE /api/templates/{id}/favorite` | Favourite / unfavourite |
| `GET /api/tags` | Most used tags |
| `GET /api/memes?sort=new\|top&limit=&offset=` | Public feed |
| `POST /api/memes` | Multipart: `image` (PNG/JPEG), `title`, `template_id`, `public` |
| `GET /api/memes/{id}`, `DELETE /api/memes/{id}` | View; delete (owner or admin) |
| `PUT` / `DELETE /api/memes/{id}/like` | Like / unlike |
| `GET /api/me/memes` | Your memes, including unlisted |
| `GET /m/{id}` | Shareable HTML page for a meme (see [SEO](#seo) for all pages) |

## Project layout
```
cmd/server/        entry point (flags, seeding, graceful shutdown)
internal/server/   HTTP handlers, server-rendered pages and SEO, SQLite schema, auth, uploads, thumbnails, tests
web/templates/     server-rendered page templates (page.html)
web/static/        app.js, style.css, icons, default social preview image; embedded via web.go
```
