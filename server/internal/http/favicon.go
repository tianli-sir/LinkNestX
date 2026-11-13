package http

import (
	"embed"
	stdhttp "net/http"
)

//go:embed favicon.svg favicon.ico
var faviconFiles embed.FS

// Favicon assets ship with the binary so every deployment serves the brand mark.
func faviconHandler() stdhttp.Handler {
	svg, _ := faviconFiles.ReadFile("favicon.svg")
	ico, _ := faviconFiles.ReadFile("favicon.ico")
	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		w.Header().Set("Cache-Control", "public, max-age=86400")
		if r.URL.Path == "/favicon.ico" {
			w.Header().Set("Content-Type", "image/x-icon")
			w.Write(ico)
			return
		}
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Write(svg)
	})
}
