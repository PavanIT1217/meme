// Package meme holds the embedded frontend for the Meme Forge server.
package meme

import "embed"

// Web is the static frontend (index.html, app.js, style.css).
//
//go:embed web
var Web embed.FS
