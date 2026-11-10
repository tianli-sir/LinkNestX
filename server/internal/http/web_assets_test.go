package http

import (
	"github.com/linknestx/linknestx/server/internal/config"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestWebAssetsSPAKeepsAPIAndMissingAssets404(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("web-index"), 0600)
	os.Mkdir(filepath.Join(dir, "assets"), 0700)
	os.WriteFile(filepath.Join(dir, "assets", "app.js"), []byte("web-script"), 0600)
	outside := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(outside, []byte("secret"), 0600)
	os.Symlink(outside, filepath.Join(dir, "assets", "escape.js"))
	cfg := config.Default()
	cfg.WebAssetsDir = dir
	router := NewRouterWithAuth(cfg, testAuthHandler(t).Service)
	for _, tc := range []struct {
		path   string
		status int
		body   string
	}{{"/app/", 200, "web-index"}, {"/app/conversation/123", 200, "web-index"}, {"/app/assets/app.js", 200, "web-script"}, {"/app/assets/missing.js", 404, ""}, {"/app/assets/escape.js", 404, ""}, {"/api/v1/unknown", 404, ""}} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", tc.path, nil))
		if w.Code != tc.status || (tc.body != "" && w.Body.String() != tc.body) {
			t.Fatalf("%s %d %s", tc.path, w.Code, w.Body)
		}
	}
}
