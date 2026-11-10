package http

import (
	"context"
	"net/http"
	"strings"

	"github.com/linknestx/linknestx/server/internal/auth"
)

func UserFromContext(ctx context.Context) (auth.User, bool) { return auth.UserFromContext(ctx) }
func UserIDFromContext(ctx context.Context) string          { user, _ := UserFromContext(ctx); return user.ID }
func SessionIDFromContext(ctx context.Context) string       { return auth.SessionIDFromContext(ctx) }
func DeviceIDFromContext(ctx context.Context) string        { return auth.DeviceIDFromContext(ctx) }

// RequireAuth adds a verified actor to the request context. The actor is checked
// again by privileged service methods before accepting account changes.
func RequireAuth(service *auth.AuthService, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers := r.Header.Values("Authorization")
		if len(headers) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		scheme, token, ok := strings.Cut(headers[0], " ")
		token = strings.TrimLeft(token, " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" || strings.ContainsAny(token, " \t\r\n") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ctx, err := service.AuthenticateContext(r.Context(), token)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		user, _ := auth.UserFromContext(ctx)
		allowInitial := (r.Method == "GET" && r.URL.Path == "/api/v1/me") || (r.Method == "POST" && (r.URL.Path == "/api/v1/auth/password" || r.URL.Path == "/api/v1/auth/logout"))
		if user.MustChangePassword && !allowInitial {
			authError(w, auth.ErrPasswordChangeRequired)
			return
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
func AuthMiddleware(service *auth.AuthService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler { return RequireAuth(service, next) }
}
