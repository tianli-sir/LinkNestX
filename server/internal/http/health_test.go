package http

import (
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linknestx/linknestx/server/internal/chat"
	"github.com/linknestx/linknestx/server/internal/config"
	"github.com/linknestx/linknestx/server/internal/groups"
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

func TestRootPagePreservesComposedRouterBoundaries(t *testing.T) {
	cfg := config.Default()
	cfg.Version = "root-page-test"
	a := testAuthHandler(t).Service
	g := groups.NewGroupService(a.Database(), a)
	c := chat.NewChatService(a.Database(), a, g)
	hub := chat.NewHub(c)
	t.Cleanup(hub.Close)
	for name, router := range map[string]stdhttp.Handler{
		"public":   NewRouter(cfg),
		"auth":     NewRouterWithAuth(cfg, a),
		"services": NewRouterWithServices(cfg, a, g, c, hub),
	} {
		t.Run(name, func(t *testing.T) {
			root := httptest.NewRecorder()
			request := httptest.NewRequest(stdhttp.MethodGet, "/", nil)
			request.Host = "untrusted-host.example"
			router.ServeHTTP(root, request)
			if root.Code != stdhttp.StatusOK || !strings.HasPrefix(root.Header().Get("Content-Type"), "text/html") {
				t.Fatalf("root status=%d type=%q", root.Code, root.Header().Get("Content-Type"))
			}
			body := root.Body.String()
			if !strings.Contains(body, "LinkNestX") || !strings.Contains(body, "桌面客户端") || strings.Contains(body, request.Host) {
				t.Fatal("root must provide static desktop connection guidance without reflecting the request host")
			}
			for _, path := range []string{"/missing", "/api/v1/missing", "/healthz/missing"} {
				response := httptest.NewRecorder()
				router.ServeHTTP(response, httptest.NewRequest(stdhttp.MethodGet, path, nil))
				if response.Code != stdhttp.StatusNotFound {
					t.Errorf("%s status=%d, want 404", path, response.Code)
				}
			}
			health := httptest.NewRecorder()
			router.ServeHTTP(health, httptest.NewRequest(stdhttp.MethodGet, "/healthz", nil))
			var payload HealthResponse
			if health.Code != stdhttp.StatusOK || !strings.HasPrefix(health.Header().Get("Content-Type"), "application/json") || json.Unmarshal(health.Body.Bytes(), &payload) != nil || payload.Status != "ok" || payload.Version != cfg.Version {
				t.Fatalf("health contract changed: %d %s", health.Code, health.Body)
			}
			if name != "public" {
				unauthenticated := httptest.NewRecorder()
				router.ServeHTTP(unauthenticated, httptest.NewRequest(stdhttp.MethodGet, "/api/v1/me", nil))
				if unauthenticated.Code != stdhttp.StatusUnauthorized {
					t.Fatalf("protected API status=%d, want 401", unauthenticated.Code)
				}
			}
		})
	}
}
