# Meme Forge

A browser-based meme generator: browse popular meme templates, add and drag text, style it, and download or copy the result.

## Features
- ~100 popular templates loaded from the public [Imgflip API](https://imgflip.com/api), with search
- Upload your own image as a template
- Multiple text boxes (defaults to the template's box count), drag to position, per-box size
- Font, text colour, outline colour, ALL CAPS toggle
- Download as PNG or copy to clipboard — everything is rendered locally in the browser

## Run locally
No build step. Serve the folder with any static server:

```sh
python3 -m http.server 8000
# open http://localhost:8000
```

It can be hosted as-is on GitHub Pages, Netlify, Vercel, etc.
