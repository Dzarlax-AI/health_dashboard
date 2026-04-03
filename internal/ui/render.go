package ui

import (
	"bytes"
	"embed"
	"html/template"
	"io"
	"log"
	"net/http"
	"strings"
)

//go:embed templates/*.html templates/**/*.html
var templateFS embed.FS

//go:embed static/*.js
var staticFS embed.FS

// pageTemplates maps page names to parsed templates.
// Each page template is parsed with the shared base.html and nav.html.
var pageTemplates map[string]*template.Template

var funcMap = template.FuncMap{
	"T":           T,
	"CSS":         func() template.CSS { return template.CSS(cssStyle) },
	"MetricName":  MetricName,
	"LangOptions": LangOptions,
	"HasPrefix":   strings.HasPrefix,
	"ToLower":     strings.ToLower,
}

func init() {
	pageTemplates = make(map[string]*template.Template)

	// Shared templates that every page needs
	sharedFiles := []string{
		"templates/base.html",
		"templates/nav.html",
	}

	// Each page template is parsed independently with the shared base
	pages := []string{
		"templates/pages/dashboard.html",
		"templates/pages/section.html",
		"templates/pages/metrics.html",
		"templates/pages/metric_detail.html",
		"templates/pages/admin.html",
		"templates/pages/login.html",
	}

	// Partials that some pages need
	partials := []string{
		"templates/partials/metrics_list.html",
		"templates/partials/admin_status.html",
	}

	for _, page := range pages {
		files := make([]string, 0, len(sharedFiles)+len(partials)+1)
		files = append(files, sharedFiles...)
		files = append(files, partials...)
		files = append(files, page)
		t, err := template.New("").Funcs(funcMap).ParseFS(templateFS, files...)
		if err != nil {
			log.Fatalf("parse template %s: %v", page, err)
		}
		// Key is the page filename without extension
		name := page[strings.LastIndex(page, "/")+1:]
		name = strings.TrimSuffix(name, ".html")
		pageTemplates[name] = t
	}
}

// renderPage executes a page template by name (e.g., "dashboard", "section", "login").
// Renders to a buffer first so errors produce a clean 500 instead of partial output.
func renderPage(w http.ResponseWriter, name string, data any) {
	tmplName := "base"
	if name == "login" {
		tmplName = "login.html"
	}

	t := pageTemplates[name]
	if t == nil {
		log.Printf("render %s: template not found (available: %v)", name, templateKeys())
		http.Error(w, "template not found: "+name, http.StatusInternalServerError)
		return
	}

	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, tmplName, data); err != nil {
		log.Printf("render %s: %v", name, err)
		http.Error(w, "render error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func templateKeys() []string {
	keys := make([]string, 0, len(pageTemplates))
	for k := range pageTemplates {
		keys = append(keys, k)
	}
	return keys
}

// renderFragment executes a partial template (no base layout).
func renderFragment(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	t := pageTemplates[name]
	if t == nil {
		http.Error(w, "fragment not found: "+name, http.StatusInternalServerError)
		return
	}
	if err := t.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("render fragment %s: %v", name, err)
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}

// serveStatic serves embedded JS files.
func serveStatic(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	data, err := staticFS.ReadFile(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	io.WriteString(w, string(data))
}
