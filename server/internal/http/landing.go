package http

import (
	_ "embed"
	stdhttp "net/http"
)

//go:embed landing.html
var landingHTML string

func landingHandler() stdhttp.Handler {
	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(stdhttp.StatusOK)
		if r.Method != stdhttp.MethodHead {
			_, _ = w.Write([]byte(landingHTML))
		}
	})
}
