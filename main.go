package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

//go:embed app.html
var appHTML embed.FS

// ============================================================
// Types
// ============================================================

type TreeNode struct {
	Name     string     `json:"name"`
	Path     string     `json:"path"`
	IsDir    bool       `json:"isDir"`
	HasPage  bool       `json:"hasPage"`
	Children []TreeNode `json:"children,omitempty"`
}

type SearchResult struct {
	Path    string `json:"path"`
	Title   string `json:"title"`
	Context string `json:"context"`
}

type listItem struct {
	depth    int
	listType string // "bullet", "numbered", "checkbox"
	checked  bool
	text     string
}

// ============================================================
// Compiled regexps
// ============================================================

var (
	reHeading   = regexp.MustCompile(`^(={2,6})\s+(.+?)\s+(={2,6})\s*$`)
	reHRule     = regexp.MustCompile(`^-{4,}\s*$`)
	reCodeStart = regexp.MustCompile(`^\s*\{\{\{(?:\s*code:\s*(\w+))?\s*$`)
	reCodeEnd   = regexp.MustCompile(`^\s*\}\}\}\s*$`)
	reBullet    = regexp.MustCompile(`^(\t*)[\*\-]\s+(.*)`)
	reNumbered  = regexp.MustCompile(`^(\t*)\d+\.\s+(.*)`)
	reCheckbox  = regexp.MustCompile(`^(\t*)\[([x ])\]\s+(.*)`)
	reTableRow  = regexp.MustCompile(`^\|`)
	reVerbatim  = regexp.MustCompile(`''(.+?)''`)
	reBold      = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reItalic    = regexp.MustCompile(`//(.+?)//`)
	reUnderline = regexp.MustCompile(`__(.+?)__`)
	reStrike    = regexp.MustCompile(`~~(.+?)~~`)
	reImage     = regexp.MustCompile(`\{\{([^}]+)\}\}`)
	reLink      = regexp.MustCompile(`\[\[([^\]]+)\]\]`)
	reBareURL   = regexp.MustCompile(`(?:https?|ftp)://[^\s<>"')\]]+[^\s<>"')\].,]`)
	reAnchorID  = regexp.MustCompile(`[^\w\-]`)
	reTag        = regexp.MustCompile(`@(\w+)`)
)

// ============================================================
// Zim markup → HTML
// ============================================================

func parseInline(text, notebookPath, pagePath string) string {
	var protected []string
	protect := func(s string) string {
		idx := len(protected)
		protected = append(protected, s)
		return fmt.Sprintf("\x00p%d\x00", idx)
	}

	// Protect verbatim first (before escaping)
	text = reVerbatim.ReplaceAllStringFunc(text, func(m string) string {
		inner := m[2 : len(m)-2]
		return protect("<code>" + html.EscapeString(inner) + "</code>")
	})

	// Protect images
	text = reImage.ReplaceAllStringFunc(text, func(m string) string {
		return protect(buildImageHTML(m[2:len(m)-2], pagePath))
	})

	// Protect links
	text = reLink.ReplaceAllStringFunc(text, func(m string) string {
		return protect(buildLinkHTML(m[2 : len(m)-2]))
	})

	// HTML-escape remaining plain text (placeholders use \x00 which is safe)
	text = html.EscapeString(text)

	// Protect bare URLs (after escaping; // is not escaped so URLs still match)
	text = reBareURL.ReplaceAllStringFunc(text, func(m string) string {
		raw := html.UnescapeString(m)
		return protect(`<a href="` + html.EscapeString(raw) + `" target="_blank" rel="noopener">` + html.EscapeString(raw) + `</a>`)
	})

	// Inline formatting (now safe: text is escaped, URLs/links are protected)
	text = reBold.ReplaceAllString(text, "<strong>$1</strong>")
	text = reUnderline.ReplaceAllString(text, "<u>$1</u>")
	text = reStrike.ReplaceAllString(text, "<s>$1</s>")
	// Italic: avoid matching :// by replacing protocol markers temporarily
	text = strings.ReplaceAll(text, "://", "\x00PROTO\x00")
	text = reItalic.ReplaceAllString(text, "<em>$1</em>")
	text = strings.ReplaceAll(text, "\x00PROTO\x00", "://")

	// Restore protected spans
	for i, v := range protected {
		text = strings.ReplaceAll(text, fmt.Sprintf("\x00p%d\x00", i), v)
	}

	return text
}

func buildImageHTML(inner, pagePath string) string {
	src := inner
	alt := ""
	style := ""
	if i := strings.Index(src, "|"); i >= 0 {
		alt = strings.TrimSpace(src[i+1:])
		src = strings.TrimSpace(src[:i])
	}
	if i := strings.Index(src, "?"); i >= 0 {
		params := src[i+1:]
		src = strings.TrimSpace(src[:i])
		for _, p := range strings.Split(params, "&") {
			kv := strings.SplitN(p, "=", 2)
			if len(kv) == 2 && (kv[0] == "width" || kv[0] == "w") {
				style = "max-width:" + kv[1] + "px;"
			}
		}
	}
	src = strings.TrimSpace(src)
	imgURL := src
	if !strings.HasPrefix(src, "http://") && !strings.HasPrefix(src, "https://") {
		dir := filepath.ToSlash(filepath.Dir(pagePath))
		if dir == "." {
			dir = ""
		}
		if dir != "" {
			imgURL = "/attachment/" + dir + "/" + src
		} else {
			imgURL = "/attachment/" + src
		}
	}
	sa := ""
	if style != "" {
		sa = ` style="` + style + `"`
	}
	return fmt.Sprintf(`<img src="%s" alt="%s"%s>`, html.EscapeString(imgURL), html.EscapeString(alt), sa)
}

func buildLinkHTML(inner string) string {
	target := inner
	linkText := inner
	if i := strings.Index(inner, "|"); i >= 0 {
		target = strings.TrimSpace(inner[:i])
		linkText = strings.TrimSpace(inner[i+1:])
	}
	target = strings.TrimSpace(target)
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") ||
		strings.HasPrefix(target, "ftp://") || strings.HasPrefix(target, "mailto:") {
		return fmt.Sprintf(`<a href="%s" target="_blank" rel="noopener">%s</a>`,
			html.EscapeString(target), html.EscapeString(linkText))
	}
	// Internal: colons are Zim namespace separators, convert to /
	pageLink := strings.ReplaceAll(target, ":", "/")
	return fmt.Sprintf(`<a href="/page/%s" class="internal-link">%s</a>`,
		pageLink, html.EscapeString(linkText))
}

func zimToHTML(content, notebookPath, pagePath string) string {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	i := 0

	// Skip Zim file header block
	if len(lines) > 0 && strings.HasPrefix(lines[0], "Content-Type: text/x-zim-wiki") {
		for i < len(lines) && strings.TrimSpace(lines[i]) != "" {
			i++
		}
		if i < len(lines) {
			i++
		}
	}

	var out strings.Builder
	var para []string
	inCode := false
	var codeLines []string
	codeLang := ""

	flushPara := func() {
		if len(para) == 0 {
			return
		}
		out.WriteString("<p>")
		for j, l := range para {
			if j > 0 {
				out.WriteString("<br>")
			}
			out.WriteString(parseInline(l, notebookPath, pagePath))
		}
		out.WriteString("</p>\n")
		para = nil
	}

	for i < len(lines) {
		line := lines[i]

		if !inCode && reCodeStart.MatchString(line) {
			flushPara()
			m := reCodeStart.FindStringSubmatch(line)
			codeLang = ""
			if len(m) > 1 {
				codeLang = m[1]
			}
			inCode = true
			codeLines = nil
			i++
			continue
		}

		if inCode {
			if reCodeEnd.MatchString(line) {
				inCode = false
				code := html.EscapeString(strings.Join(codeLines, "\n"))
				lc := ""
				if codeLang != "" {
					lc = ` class="language-` + codeLang + `"`
				}
				out.WriteString("<pre><code" + lc + ">" + code + "</code></pre>\n")
				codeLines = nil
			} else {
				codeLines = append(codeLines, line)
			}
			i++
			continue
		}

		if m := reHeading.FindStringSubmatch(line); m != nil && len(m[1]) == len(m[3]) {
			flushPara()
			level := 7 - len(m[1])
			if level < 1 {
				level = 1
			}
			anchor := strings.ToLower(reAnchorID.ReplaceAllString(m[2], "-"))
			out.WriteString(fmt.Sprintf("<h%d id=%q>%s</h%d>\n",
				level, anchor, parseInline(m[2], notebookPath, pagePath), level))
			i++
			continue
		}

		if reHRule.MatchString(line) {
			flushPara()
			out.WriteString("<hr>\n")
			i++
			continue
		}

		if reTableRow.MatchString(line) {
			flushPara()
			var tbl []string
			for i < len(lines) && reTableRow.MatchString(lines[i]) {
				tbl = append(tbl, lines[i])
				i++
			}
			out.WriteString(parseTable(tbl, notebookPath, pagePath))
			continue
		}

		if reBullet.MatchString(line) || reNumbered.MatchString(line) || reCheckbox.MatchString(line) {
			flushPara()
			var items []listItem
			for i < len(lines) {
				l := lines[i]
				if m := reCheckbox.FindStringSubmatch(l); m != nil {
					items = append(items, listItem{
						depth: len(m[1]), listType: "checkbox",
						checked: strings.ToLower(m[2]) == "x",
						text:    parseInline(m[3], notebookPath, pagePath),
					})
					i++
				} else if m := reBullet.FindStringSubmatch(l); m != nil {
					items = append(items, listItem{
						depth: len(m[1]), listType: "bullet",
						text: parseInline(m[2], notebookPath, pagePath),
					})
					i++
				} else if m := reNumbered.FindStringSubmatch(l); m != nil {
					items = append(items, listItem{
						depth: len(m[1]), listType: "numbered",
						text: parseInline(m[2], notebookPath, pagePath),
					})
					i++
				} else {
					break
				}
			}
			out.WriteString(listToHTML(items, 0))
			out.WriteString("\n")
			continue
		}

		if strings.TrimSpace(line) == "" {
			flushPara()
			i++
			continue
		}

		// Check for blockquote
		if strings.HasPrefix(line, ">") {
			flushPara()
			var qlines []string
			for i < len(lines) && strings.HasPrefix(lines[i], ">") {
				qlines = append(qlines, strings.TrimPrefix(strings.TrimPrefix(lines[i], ">"), " "))
				i++
			}
			out.WriteString("<blockquote>")
			out.WriteString(zimToHTML(strings.Join(qlines, "\n"), notebookPath, pagePath))
			out.WriteString("</blockquote>\n")
			continue
		}

		para = append(para, line)
		i++
	}

	flushPara()

	if inCode && len(codeLines) > 0 {
		out.WriteString("<pre><code>" + html.EscapeString(strings.Join(codeLines, "\n")) + "</code></pre>\n")
	}

	return out.String()
}

func listToHTML(items []listItem, depth int) string {
	if len(items) == 0 {
		return ""
	}
	tag := "ul"
	for _, it := range items {
		if it.depth == depth {
			if it.listType == "numbered" {
				tag = "ol"
			}
			break
		}
	}
	var sb strings.Builder
	sb.WriteString("<" + tag + ">")
	i := 0
	for i < len(items) {
		if items[i].depth != depth {
			i++
			continue
		}
		it := items[i]
		i++
		// Collect children
		j := i
		for j < len(items) && items[j].depth > depth {
			j++
		}
		sb.WriteString("<li>")
		if it.listType == "checkbox" {
			if it.checked {
				sb.WriteString(`<input type="checkbox" checked disabled> `)
			} else {
				sb.WriteString(`<input type="checkbox" disabled> `)
			}
		}
		sb.WriteString(it.text)
		if j > i {
			sb.WriteString(listToHTML(items[i:j], depth+1))
		}
		sb.WriteString("</li>")
		i = j
	}
	sb.WriteString("</" + tag + ">")
	return sb.String()
}

func parseTable(lines []string, notebookPath, pagePath string) string {
	reSep := regexp.MustCompile(`^\|[-| :]+\|\s*$`)
	var sb strings.Builder
	sb.WriteString(`<table class="wiki-table">`)
	isHeader := true
	for _, line := range lines {
		if reSep.MatchString(line) {
			isHeader = false
			continue
		}
		cells := strings.Split(strings.Trim(line, "| \t"), "|")
		tag := "td"
		if isHeader {
			tag = "th"
			isHeader = false
		}
		sb.WriteString("<tr>")
		for _, c := range cells {
			sb.WriteString("<" + tag + ">" + parseInline(strings.TrimSpace(c), notebookPath, pagePath) + "</" + tag + ">")
		}
		sb.WriteString("</tr>")
	}
	sb.WriteString("</table>")
	return sb.String()
}

// ============================================================
// Notebook / filesystem helpers
// ============================================================

func displayName(stem string) string {
	return strings.ReplaceAll(stem, "_", " ")
}

func toFileName(name string) string {
	return strings.ReplaceAll(name, " ", "_")
}

func getPageFilePath(notebookPath, pagePath string) string {
	parts := strings.Split(pagePath, "/")
	for k, p := range parts {
		parts[k] = toFileName(p)
	}
	elems := append([]string{notebookPath}, parts...)
	return filepath.Join(elems...) + ".txt"
}

func getPageTitle(content string) string {
	for _, line := range strings.Split(content, "\n") {
		if m := reHeading.FindStringSubmatch(line); m != nil && len(m[1]) == 6 && len(m[3]) == 6 {
			return m[2]
		}
	}
	return ""
}

func newPageContent(title string) string {
	return fmt.Sprintf(
		"Content-Type: text/x-zim-wiki\nWiki-Format: zim 0.6\nCreation-Date: %s\n\n====== %s ======\n",
		time.Now().Format("2006-01-02T15:04:05-07:00"), title,
	)
}

func getNotebookName(notebookPath string) string {
	data, err := os.ReadFile(filepath.Join(notebookPath, "notebook.zim"))
	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "name=") {
				return strings.TrimSpace(strings.TrimPrefix(line, "name="))
			}
		}
	}
	return filepath.Base(notebookPath)
}

func buildTree(dirPath, relPath string) []TreeNode {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil
	}

	var dirs, files []os.DirEntry
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || name == "notebook.zim" {
			continue
		}
		if e.IsDir() {
			dirs = append(dirs, e)
		} else if strings.HasSuffix(name, ".txt") {
			files = append(files, e)
		}
	}

	dirSet := make(map[string]bool)
	for _, d := range dirs {
		dirSet[d.Name()] = true
	}

	var nodes []TreeNode

	for _, d := range dirs {
		childRel := d.Name()
		if relPath != "" {
			childRel = relPath + "/" + d.Name()
		}
		childPath := filepath.Join(dirPath, d.Name())
		_, err := os.Stat(childPath + ".txt")
		nodes = append(nodes, TreeNode{
			Name:     displayName(d.Name()),
			Path:     childRel,
			IsDir:    true,
			HasPage:  err == nil,
			Children: buildTree(childPath, childRel),
		})
	}

	for _, f := range files {
		stem := strings.TrimSuffix(f.Name(), ".txt")
		if dirSet[stem] {
			continue // already represented as dir node with HasPage=true
		}
		filePath := stem
		if relPath != "" {
			filePath = relPath + "/" + stem
		}
		nodes = append(nodes, TreeNode{
			Name: displayName(stem),
			Path: filePath,
		})
	}

	sort.Slice(nodes, func(i, j int) bool {
		ni := strings.ToLower(nodes[i].Name)
		nj := strings.ToLower(nodes[j].Name)
		if ni == "home" {
			return true
		}
		if nj == "home" {
			return false
		}
		// Dirs before pages at same level
		if nodes[i].IsDir != nodes[j].IsDir {
			return nodes[i].IsDir
		}
		return ni < nj
	})

	return nodes
}

func extractTags(content string) []string {
	seen := make(map[string]bool)
	var tags []string
	for _, m := range reTag.FindAllStringSubmatch(content, -1) {
		t := strings.ToLower(m[1])
		if !seen[t] {
			seen[t] = true
			tags = append(tags, t)
		}
	}
	return tags
}

func findBrokenLinks(notebookPath, content string) []string {
	var broken []string
	for _, m := range reLink.FindAllStringSubmatch(content, -1) {
		inner := m[1]
		target := inner
		if i := strings.Index(inner, "|"); i >= 0 {
			target = strings.TrimSpace(inner[:i])
		}
		target = strings.TrimSpace(target)
		if strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
			continue
		}
		targetPath := strings.ReplaceAll(target, ":", "/")
		fp := getPageFilePath(notebookPath, targetPath)
		if _, err := os.Stat(fp); os.IsNotExist(err) {
			broken = append(broken, targetPath)
		}
	}
	return broken
}

func findBacklinks(notebookPath, pagePath string) []SearchResult {
	parts := strings.Split(pagePath, "/")
	pageName := parts[len(parts)-1]

	seen := make(map[string]bool)
	var results []SearchResult
	filepath.Walk(notebookPath, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".txt") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		content := string(data)
		for _, m := range reLink.FindAllStringSubmatch(content, -1) {
			inner := m[1]
			target := inner
			if i := strings.Index(inner, "|"); i >= 0 {
				target = strings.TrimSpace(inner[:i])
			}
			target = strings.TrimSpace(target)
			if strings.Contains(target, "://") {
				continue
			}
			targetPath := strings.ReplaceAll(target, ":", "/")
			if targetPath == pagePath || target == pageName {
				rel, _ := filepath.Rel(notebookPath, path)
				rel = filepath.ToSlash(strings.TrimSuffix(rel, ".txt"))
				if rel != pagePath && !seen[rel] {
					seen[rel] = true
					title := getPageTitle(content)
					if title == "" {
						p := strings.Split(rel, "/")
						title = displayName(p[len(p)-1])
					}
					results = append(results, SearchResult{Path: rel, Title: title})
				}
				break
			}
		}
		return nil
	})
	return results
}

func searchPages(notebookPath, query string) []SearchResult {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil
	}
	var results []SearchResult
	filepath.Walk(notebookPath, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".txt") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		lower := strings.ToLower(string(data))
		if !strings.Contains(lower, query) {
			return nil
		}
		rel, _ := filepath.Rel(notebookPath, path)
		rel = filepath.ToSlash(strings.TrimSuffix(rel, ".txt"))

		title := getPageTitle(string(data))
		if title == "" {
			parts := strings.Split(rel, "/")
			title = displayName(parts[len(parts)-1])
		}

		idx := strings.Index(lower, query)
		start := idx - 80
		if start < 0 {
			start = 0
		}
		end := idx + len(query) + 80
		if end > len(lower) {
			end = len(lower)
		}
		ctx := strings.ReplaceAll(string(data)[start:end], "\n", " ")
		if start > 0 {
			ctx = "…" + ctx
		}
		if end < len(lower) {
			ctx += "…"
		}
		results = append(results, SearchResult{Path: rel, Title: title, Context: ctx})
		return nil
	})
	return results
}

// ============================================================
// HTTP server
// ============================================================

type srv struct{ notebookPath string }

func (s *srv) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	switch {
	case p == "/" || p == "/index.html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		f, _ := appHTML.Open("app.html")
		defer f.Close()
		io.Copy(w, f)
	case strings.HasPrefix(p, "/api/"):
		s.api(w, r)
	case strings.HasPrefix(p, "/attachment/"):
		s.attachment(w, r)
	default:
		http.NotFound(w, r)
	}
}

func jsonOK(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func jsonErr(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func (s *srv) api(w http.ResponseWriter, r *http.Request) {
	sub := strings.TrimPrefix(r.URL.Path, "/api/")

	switch sub {

	case "tree":
		nodes := buildTree(s.notebookPath, "")
		if nodes == nil {
			nodes = []TreeNode{}
		}
		jsonOK(w, map[string]interface{}{
			"name":     getNotebookName(s.notebookPath),
			"children": nodes,
		})

	case "page":
		pagePath := r.URL.Query().Get("path")
		if pagePath == "" {
			jsonErr(w, "missing path"); return
		}
		fp := getPageFilePath(s.notebookPath, pagePath)
		data, err := os.ReadFile(fp)
		if err != nil {
			parts := strings.Split(pagePath, "/")
			title := displayName(parts[len(parts)-1])
			jsonOK(w, map[string]interface{}{
				"path": pagePath, "exists": false, "raw": "", "title": title,
				"html":         "<p style='color:#888'><em>This page does not exist yet. Click <strong>Edit</strong> to create it.</em></p>",
				"tags":         []string{},
				"brokenLinks":  []string{},
			})
			return
		}
		content := string(data)
		title := getPageTitle(content)
		if title == "" {
			parts := strings.Split(pagePath, "/")
			title = displayName(parts[len(parts)-1])
		}
		tags := extractTags(content)
		broken := findBrokenLinks(s.notebookPath, content)
		if tags == nil { tags = []string{} }
		if broken == nil { broken = []string{} }
		jsonOK(w, map[string]interface{}{
			"path": pagePath, "exists": true, "raw": content, "title": title,
			"html":        zimToHTML(content, s.notebookPath, pagePath),
			"tags":        tags,
			"brokenLinks": broken,
		})

	case "save":
		if r.Method != "POST" {
			http.Error(w, "POST required", http.StatusMethodNotAllowed); return
		}
		pagePath := r.URL.Query().Get("path")
		if pagePath == "" {
			jsonErr(w, "missing path"); return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			jsonErr(w, err.Error()); return
		}
		fp := getPageFilePath(s.notebookPath, pagePath)
		if err := os.MkdirAll(filepath.Dir(fp), 0755); err != nil {
			jsonErr(w, err.Error()); return
		}
		if err := os.WriteFile(fp, body, 0644); err != nil {
			jsonErr(w, err.Error()); return
		}
		content := string(body)
		title := getPageTitle(content)
		if title == "" {
			parts := strings.Split(pagePath, "/")
			title = displayName(parts[len(parts)-1])
		}
		tags := extractTags(content)
		broken := findBrokenLinks(s.notebookPath, content)
		if tags == nil { tags = []string{} }
		if broken == nil { broken = []string{} }
		jsonOK(w, map[string]interface{}{
			"ok": true, "title": title,
			"html":        zimToHTML(content, s.notebookPath, pagePath),
			"tags":        tags,
			"brokenLinks": broken,
		})

	case "create":
		if r.Method != "POST" {
			http.Error(w, "POST required", http.StatusMethodNotAllowed); return
		}
		var req struct {
			Path  string `json:"path"`
			IsDir bool   `json:"isDir"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonErr(w, err.Error()); return
		}
		if req.Path == "" {
			jsonErr(w, "missing path"); return
		}
		if req.IsDir {
			dp := filepath.Join(s.notebookPath, filepath.FromSlash(req.Path))
			if err := os.MkdirAll(dp, 0755); err != nil {
				jsonErr(w, err.Error()); return
			}
			jsonOK(w, map[string]bool{"ok": true})
		} else {
			fp := getPageFilePath(s.notebookPath, req.Path)
			if _, err := os.Stat(fp); err == nil {
				jsonErr(w, "page already exists"); return
			}
			if err := os.MkdirAll(filepath.Dir(fp), 0755); err != nil {
				jsonErr(w, err.Error()); return
			}
			parts := strings.Split(req.Path, "/")
			title := displayName(parts[len(parts)-1])
			if err := os.WriteFile(fp, []byte(newPageContent(title)), 0644); err != nil {
				jsonErr(w, err.Error()); return
			}
			jsonOK(w, map[string]interface{}{"ok": true, "path": req.Path})
		}

	case "delete":
		if r.Method != "POST" {
			http.Error(w, "POST required", http.StatusMethodNotAllowed); return
		}
		var req struct {
			Path  string `json:"path"`
			IsDir bool   `json:"isDir"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonErr(w, err.Error()); return
		}
		if req.IsDir {
			dp := filepath.Join(s.notebookPath, filepath.FromSlash(req.Path))
			os.RemoveAll(dp)
		}
		fp := getPageFilePath(s.notebookPath, req.Path)
		os.Remove(fp)
		jsonOK(w, map[string]bool{"ok": true})

	case "search":
		q := r.URL.Query().Get("q")
		results := searchPages(s.notebookPath, q)
		if results == nil {
			results = []SearchResult{}
		}
		jsonOK(w, results)

	case "render":
		if r.Method != "POST" {
			http.Error(w, "POST required", http.StatusMethodNotAllowed); return
		}
		pagePath := r.URL.Query().Get("path")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			jsonErr(w, err.Error()); return
		}
		jsonOK(w, map[string]string{
			"html": zimToHTML(string(body), s.notebookPath, pagePath),
		})

	case "backlinks":
		pagePath := r.URL.Query().Get("path")
		if pagePath == "" {
			jsonErr(w, "missing path"); return
		}
		results := findBacklinks(s.notebookPath, pagePath)
		if results == nil {
			results = []SearchResult{}
		}
		jsonOK(w, results)

	case "rename":
		if r.Method != "POST" {
			http.Error(w, "POST required", http.StatusMethodNotAllowed); return
		}
		var req struct {
			OldPath string `json:"oldPath"`
			NewPath string `json:"newPath"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonErr(w, err.Error()); return
		}
		if req.OldPath == "" || req.NewPath == "" {
			jsonErr(w, "missing path"); return
		}
		oldFile := getPageFilePath(s.notebookPath, req.OldPath)
		newFile := getPageFilePath(s.notebookPath, req.NewPath)
		// Guard against path traversal
		absNew, _ := filepath.Abs(newFile)
		if !strings.HasPrefix(absNew, s.notebookPath+string(os.PathSeparator)) {
			jsonErr(w, "invalid path"); return
		}
		if _, err := os.Stat(newFile); err == nil {
			jsonErr(w, "destination already exists"); return
		}
		if err := os.MkdirAll(filepath.Dir(newFile), 0755); err != nil {
			jsonErr(w, err.Error()); return
		}
		if err := os.Rename(oldFile, newFile); err != nil {
			jsonErr(w, err.Error()); return
		}
		// Also rename the associated attachment/sub-page directory if present
		oldDir := strings.TrimSuffix(oldFile, ".txt")
		newDir := strings.TrimSuffix(newFile, ".txt")
		if info, err := os.Stat(oldDir); err == nil && info.IsDir() {
			os.MkdirAll(filepath.Dir(newDir), 0755)
			os.Rename(oldDir, newDir)
		}
		jsonOK(w, map[string]interface{}{"ok": true, "newPath": req.NewPath})

	case "upload":
		if r.Method != "POST" {
			http.Error(w, "POST required", http.StatusMethodNotAllowed); return
		}
		pageDir := r.URL.Query().Get("dir")
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			jsonErr(w, err.Error()); return
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			jsonErr(w, err.Error()); return
		}
		defer file.Close()
		safeBase := filepath.Base(header.Filename)
		var targetDir string
		if pageDir != "" {
			targetDir = filepath.Join(s.notebookPath, filepath.FromSlash(pageDir))
		} else {
			targetDir = s.notebookPath
		}
		// Guard against path traversal
		absTarget, _ := filepath.Abs(targetDir)
		if !strings.HasPrefix(absTarget, s.notebookPath) {
			jsonErr(w, "invalid path"); return
		}
		if err := os.MkdirAll(targetDir, 0755); err != nil {
			jsonErr(w, err.Error()); return
		}
		dst, err := os.Create(filepath.Join(targetDir, safeBase))
		if err != nil {
			jsonErr(w, err.Error()); return
		}
		defer dst.Close()
		if _, err := io.Copy(dst, file); err != nil {
			jsonErr(w, err.Error()); return
		}
		attachPath := safeBase
		if pageDir != "" {
			attachPath = pageDir + "/" + safeBase
		}
		jsonOK(w, map[string]string{"url": "/attachment/" + attachPath, "name": safeBase})

	default:
		http.NotFound(w, r)
	}
}

func (s *srv) attachment(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/attachment/")
	http.ServeFile(w, r, filepath.Join(s.notebookPath, filepath.FromSlash(rel)))
}

// ============================================================
// Main
// ============================================================

func openBrowser(url string) {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "windows":
		cmd, args = "cmd", []string{"/c", "start", "", url}
	case "darwin":
		cmd, args = "open", []string{url}
	default:
		cmd, args = "xdg-open", []string{url}
	}
	exec.Command(cmd, args...).Start()
}

func findFreePort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 8081
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

func main() {
	notebookPath := "."
	port := 8080

	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		a := args[i]
		if (a == "--port" || a == "-p") && i+1 < len(args) {
			fmt.Sscanf(args[i+1], "%d", &port)
			i++
		} else if !strings.HasPrefix(a, "-") {
			notebookPath = a
		}
	}

	abs, err := filepath.Abs(notebookPath)
	if err != nil {
		log.Fatalf("Invalid path: %v", err)
	}
	notebookPath = abs

	if info, err := os.Stat(notebookPath); err != nil || !info.IsDir() {
		log.Fatalf("Notebook path is not a directory: %s", notebookPath)
	}

	// Check port availability
	if l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err != nil {
		port = findFreePort()
	} else {
		l.Close()
	}

	url := fmt.Sprintf("http://localhost:%d", port)
	fmt.Printf("Zim Wiki Alternative\n")
	fmt.Printf("Notebook : %s\n", notebookPath)
	fmt.Printf("URL      : %s\n", url)
	fmt.Printf("Press Ctrl+C to stop.\n\n")

	go func() {
		time.Sleep(400 * time.Millisecond)
		openBrowser(url)
	}()

	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", port), &srv{notebookPath: notebookPath}))
}
