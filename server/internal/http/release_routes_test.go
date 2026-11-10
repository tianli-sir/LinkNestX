package http

import (
	"github.com/linknestx/linknestx/server/internal/config"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublicDownloadRoutesAndWebEntry(t *testing.T) {
	cfg := config.Default()
	for _, router := range []http.Handler{NewHandler(cfg), NewRouterWithAuth(cfg, nil)} {
		for _, path := range []string{"/downloads", "/api/v1/releases"} {
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
			if w.Code != 200 {
				t.Fatalf("%s status %d", path, w.Code)
			}
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
		if !strings.Contains(w.Body.String(), `href="/app/"`) || !strings.Contains(w.Body.String(), `href="/downloads"`) {
			t.Fatal("missing Web/client entry")
		}
	}
}
