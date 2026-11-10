package http

import (
	"net/http"
	"os"
	"path"
	"strings"
)

func webAssets(directory string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if directory == "" {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/app/")
		if name == "" {
			name = "index.html"
		}
		if strings.Contains(name, "\\") || strings.Contains(name, "\x00") || path.IsAbs(name) || path.Clean(name) != name {
			http.NotFound(w, r)
			return
		}
		for _, segment := range strings.Split(name, "/") {
			if strings.HasPrefix(segment, ".") {
				http.NotFound(w, r)
				return
			}
		}
		root, e := os.OpenRoot(directory)
		if e != nil {
			http.NotFound(w, r)
			return
		}
		defer root.Close()
		file, e := root.Open(name)
		if e != nil && path.Ext(name) == "" && !strings.HasPrefix(name, "assets/") {
			name = "index.html"
			file, e = root.Open(name)
		}
		if e != nil {
			http.NotFound(w, r)
			return
		}
		defer file.Close()
		info, e := file.Stat()
		if e != nil || !info.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; connect-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'self'")
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeContent(w, r, name, info.ModTime(), file)
	})
}
