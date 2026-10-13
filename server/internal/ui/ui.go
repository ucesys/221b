// Package ui serves the browser app built into the binary.
package ui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// The build copies web/dist here before compiling; a checkout without a web
// build holds only .gitkeep, and the server says so instead of an app.
//
//go:embed all:dist
var dist embed.FS

// Handler serves the app: hashed assets are cached for a year, index.html
// never, and unknown paths fall back to index.html so links into the app
// work.
func Handler() http.Handler {
	files, _ := fs.Sub(dist, "dist")
	return handler(files)
}

func handler(files fs.FS) http.Handler {
	index, err := fs.ReadFile(files, "index.html")
	server := http.FileServerFS(files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err != nil {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("The browser app is not built into this binary. Run `make web` and rebuild, or use `make image`.\n"))
			return
		}
		p := path.Clean("/" + r.URL.Path)
		if p != "/" && p != "/index.html" {
			if st, statErr := fs.Stat(files, strings.TrimPrefix(p, "/")); statErr == nil && !st.IsDir() {
				if strings.HasPrefix(p, "/assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					w.Header().Set("Cache-Control", "no-cache")
				}
				server.ServeHTTP(w, r)
				return
			}
			// A missing file with an extension is a real 404, not a page.
			if path.Ext(p) != "" {
				http.NotFound(w, r)
				return
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(index)
	})
}
