// Package meme holds the embedded frontend for the Meme Forge server.
package meme

import "embed"

// Web is the frontend: static/ (JS, CSS, icons) and templates/ (server-rendered page templates).
//
//go:embed web
var Web embed.FS
