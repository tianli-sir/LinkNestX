package http

import (
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linknestx/linknestx/server/internal/config"
)

func TestHealth(t *testing.T) {
	req := httptest.NewRequest(stdhttp.MethodGet, "/healthz", nil)
	res := httptest.NewRecorder()
	cfg := config.Default()
	cfg.SQLitePath = filepath.Join(t.TempDir(), "health.sqlite3")
	NewRouter(cfg).ServeHTTP(res, req)
	if _, err := os.Stat(cfg.SQLitePath); !os.IsNotExist(err) {
		t.Fatalf("router created a database: %v", err)
	}

	if res.Code != stdhttp.StatusOK {
		t.Fatalf("status = %d, want %d", res.Code, stdhttp.StatusOK)
	}
	if contentType := res.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", contentType)
	}
	var payload HealthResponse
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Status != "ok" {
		t.Fatalf("status = %q, want %q", payload.Status, "ok")
	}
	if payload.Version == "" {
		t.Fatal("version should be present")
	}
}
