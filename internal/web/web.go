// Package web serves the embedded frontend.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:static
var staticFS embed.FS

// Handler returns an http.Handler for the SPA frontend. Known pages map to
// their HTML files; unknown extension-less paths fall back to index.html.
func Handler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err) // embed layout is fixed at compile time
	}
	files := http.StripPrefix("/static/", http.FileServerFS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		switch p {
		case "", "index.html":
			serveWithCache(w, sub, "index.html")
		case "login":
			serveWithCache(w, sub, "login.html")
		default:
			if strings.HasPrefix(p, "static/") {
				files.ServeHTTP(w, r)
				return
			}
			if strings.HasPrefix(p, "note/") {
				serveWithCache(w, sub, "note.html")
				return
			}
			serveWithCache(w, sub, "index.html")
		}
	})
}

func serveWithCache(w http.ResponseWriter, fsys fs.FS, name string) {
	// HTML is never cached so deploys show up immediately.
	w.Header().Set("Cache-Control", "no-cache")
	f, err := fsys.Open(name)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		http.Error(w, "read error", http.StatusInternalServerError)
		return
	}
	switch {
	case strings.HasSuffix(name, ".html"):
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	}
	_, _ = w.Write(data)
}
