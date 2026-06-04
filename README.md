# Zim Wiki Alternative

A self-contained viewer and editor for [Zim Desktop Wiki](https://github.com/zim-desktop-wiki/zim-desktop-wiki) notebooks. Runs as a local web server with no installation required — copy the `.exe` to any Windows machine and point it at your notebook folder.

All content is written in standard Zim markup format, so the notebook remains fully compatible with Zim Desktop Wiki.

## Usage

```
zimalt.exe C:\path\to\your\notebook
```

The browser opens automatically. Press **Ctrl+C** in the terminal to stop.

```
zimalt.exe C:\path\to\notebook --port 9090   # use a specific port
zimalt.exe .                                  # use current directory
```

## Features

- **View** — renders Zim markup to HTML: headings, bold/italic/underline/strikethrough, nested lists, checkboxes, code blocks (with language highlighting class), tables, blockquotes, horizontal rules, internal and external links, images
- **Edit** — raw Zim markup editor; **Ctrl+S** saves, **Esc** cancels
- **Create** — new pages and folders via the sidebar buttons; defaults to the current page's location
- **Delete** — removes a page or an entire folder tree (with confirmation)
- **Search** — full-text search across all pages, live as you type
- **Navigate** — collapsible folder tree; internal `[[links]]` are clickable; URL hash tracks the current page so browser back/forward work

## Zim format compatibility

Every file written by this app includes the standard Zim file header:

```
Content-Type: text/x-zim-wiki
Wiki-Format: zim 0.6
Creation-Date: 2024-01-01T00:00:00+00:00
```

Files and folders follow the same naming conventions Zim uses (spaces stored as underscores). The notebook can be copied back to any machine running Zim Desktop Wiki and opened without modification.

## Building from source

Requires [Go 1.21+](https://go.dev/dl/).

```bash
# Linux / macOS binary
go build -o zimalt .

# Windows .exe (cross-compile from Linux or macOS)
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o zimalt.exe .
```

No external dependencies — only the Go standard library is used.

## Project structure

```
zimalt.exe       Windows binary (copy this to your Windows machine)
main.go          HTTP server and Zim markup parser
app.html         Frontend (embedded into the binary at build time)
go.mod           Go module file
```
