# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build commands

```bash
# Native binary (Linux/macOS)
go build -o zimalt .

# Windows .exe (cross-compile)
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o zimalt.exe .

# Run locally against a notebook directory
./zimalt ./zimNotes --port 9090
```

No external dependencies — stdlib only.

## Architecture

The entire application is two files:

- **`main.go`** — Go HTTP server, Zim markup parser, and filesystem helpers.
- **`app.html`** — Single-page frontend, embedded into the binary at compile time via `//go:embed app.html`.

`app.html` is served from memory (no disk read at runtime) and contains all HTML, CSS, and JavaScript in one file. There is no build step for the frontend.

### Server (`main.go`)

The `srv` struct implements `http.Handler`. Routes:

| Path | Handler |
|---|---|
| `/` | Serves the embedded `app.html` |
| `/api/*` | `srv.api()` — dispatches to `tree`, `page`, `save`, `create`, `delete`, `search` |
| `/attachment/*` | Serves raw files (images, etc.) from the notebook directory |

Key functions:
- `zimToHTML` — line-by-line Zim markup → HTML parser. Handles headings, lists, code blocks, tables, blockquotes, and inline formatting.
- `parseInline` — inline formatting pass (bold, italic, links, images). Uses a protection/placeholder scheme to prevent double-escaping.
- `searchPages` — walks the notebook directory, case-insensitive substring match, returns path/title/context snippet.
- `buildTree` — recursively builds the sidebar tree. A directory and its same-named `.txt` file are merged into one node (`HasPage: true`).

### Frontend (`app.html`)

Vanilla JS, no framework. State lives in a single `S` object. The three main panes (`view-pane`, `editor-pane`, `search-pane`) are toggled by `showPane(which)` — each pane has `display:none` in CSS and is shown by setting an inline style override.

Page navigation is hash-based (`location.hash = encodeURIComponent(path)`), so browser back/forward work natively.

### Notebook format

Files are stored as `<NotebookRoot>/<path/with/underscores>.txt` following standard Zim conventions (spaces → underscores in filenames). The file header block (`Content-Type: text/x-zim-wiki` …) is skipped during rendering. Pages are fully compatible with Zim Desktop Wiki.
