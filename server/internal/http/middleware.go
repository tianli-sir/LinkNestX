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
		cookie, cookieErr := browserCookie(r)
		var ctx context.Context
		var err error
		if cookieErr == nil {
			if len(headers) != 0 {
				http.Error(w, "ambiguous credentials", 401)
				return
			}
			var session auth.Session
			ctx, session, err = service.AuthenticateWeb(r.Context(), cookie.Value)
			if err == nil {
				if !webAllowed(r) {
					http.Error(w, "desktop capability required", 403)
					return
				}
				if (r.Method != "GET" && r.Method != "HEAD") && !csrfValid(r, session.CSRFToken) {
					http.Error(w, "invalid origin or CSRF token", 403)
					return
				}
				if r.URL.Path == "/api/v1/ws" && !sameOrigin(r) {
					http.Error(w, "invalid websocket origin", 403)
					return
				}
				if r.Header.Get("Origin") != "" && !sameOrigin(r) {
					http.Error(w, "invalid origin", 403)
					return
				}
			}
		} else {
			if cookieErr != http.ErrNoCookie || len(headers) != 1 {
				http.Error(w, "unauthorized", 401)
				return
			}
			scheme, token, ok := strings.Cut(headers[0], " ")
			token = strings.TrimLeft(token, " ")
			if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" || strings.ContainsAny(token, " \t\r\n") {
				http.Error(w, "unauthorized", 401)
				return
			}
			ctx, err = service.AuthenticateContext(r.Context(), token)
			if err == nil && auth.ClientTypeFromContext(ctx) == "web" {
				http.Error(w, "browser cookie required", 401)
				return
			}
		}
		if err != nil {
			http.Error(w, "unauthorized", 401)
			return
		}
		user, _ := auth.UserFromContext(ctx)
		allowInitial := (r.Method == "GET" && (r.URL.Path == "/api/v1/me" || r.URL.Path == "/api/v1/web/session")) || (r.Method == "POST" && (r.URL.Path == "/api/v1/auth/password" || r.URL.Path == "/api/v1/auth/logout" || r.URL.Path == "/api/v1/web/logout" || r.URL.Path == "/api/v1/web/refresh"))
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
