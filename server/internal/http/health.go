// Package http exposes the server's HTTP handlers.
package http

import (
	"encoding/json"
	stdhttp "net/http"
	"os"

	"strings"

	"github.com/linknestx/linknestx/server/internal/auth"
	"github.com/linknestx/linknestx/server/internal/chat"
	"github.com/linknestx/linknestx/server/internal/config"
	"github.com/linknestx/linknestx/server/internal/groups"
)

// HealthResponse is the stable response returned by GET /healthz.
type HealthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

// NewHandler creates the HTTP router for a server configured with cfg.
// The health endpoint is intentionally available without authentication so
// process supervisors can probe it during startup and rolling upgrades.
func NewHandler(cfg config.Config) stdhttp.Handler {
	mux := stdhttp.NewServeMux()
	version := cfg.Version
	if strings.TrimSpace(version) == "" {
		version = applicationVersion()
	}
	mux.Handle("GET /healthz", healthHandler(version))
	return mux
}

// NewRouter creates the health router without opening resources. Server startup
// supplies its database-backed service through NewRouterWithAuth.
func NewRouter(cfg config.Config) stdhttp.Handler {
	return NewHandler(cfg)
}

// NewRouterWithAuth builds a router with a caller-owned authentication service.
// It is useful for tests and embedded deployments where database lifecycle is
// managed by the caller.
func NewRouterWithAuth(cfg config.Config, service *auth.AuthService) stdhttp.Handler {
	mux := stdhttp.NewServeMux()
	mux.Handle("/healthz", NewHandler(cfg))
	if service != nil {
		NewAuthHandlers(service).Register(mux)
	}
	return mux
}

// NewRouterWithServices composes caller-owned feature services. The caller must
// close hub during shutdown because upgraded sockets are not owned by HTTP.
func NewRouterWithServices(cfg config.Config, a *auth.AuthService, g *groups.GroupService, c *chat.ChatService, hub *chat.Hub) stdhttp.Handler {
	mux := stdhttp.NewServeMux()
	mux.Handle("/", NewRouterWithAuth(cfg, a))
	NewGroupHandlers(g).Register(mux, a)
	ch := NewChatHandlers(c)
	mux.Handle("GET /api/v1/groups/{groupID}/messages", RequireAuth(a, stdhttp.HandlerFunc(ch.List)))
	mux.Handle("POST /api/v1/groups/{groupID}/messages", RequireAuth(a, stdhttp.HandlerFunc(ch.Append)))
	mux.Handle("GET /api/v1/ws", RequireAuth(a, hub))
	return mux
}

// NewHealthHandler returns just the health handler, useful for focused tests
// and for embedding the endpoint in another router.
func NewHealthHandler(version string) stdhttp.Handler {
	return healthHandler(version)
}

// HealthHandler is an alias for NewHealthHandler for callers that prefer a
// handler-style constructor name.
func HealthHandler(version string) stdhttp.Handler {
	return NewHealthHandler(version)
}

func healthHandler(version string) stdhttp.Handler {
	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		if r.Method != stdhttp.MethodGet {
			w.Header().Set("Allow", stdhttp.MethodGet)
			stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusMethodNotAllowed), stdhttp.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(stdhttp.StatusOK)
		_ = json.NewEncoder(w).Encode(HealthResponse{Status: "ok", Version: version})
	})
}

func applicationVersion() string {
	// Keep the default useful in local builds while allowing release tooling to
	// provide the version at build time through LINKNESTX_VERSION.
	if value := strings.TrimSpace(getenv("LINKNESTX_VERSION")); value != "" {
		return value
	}
	return "dev"
}

var getenv = func(name string) string {
	// Isolated behind a variable so tests can control the version without
	// mutating process-global environment state.
	return os.Getenv(name)
}
