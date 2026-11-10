package http

import (
	"crypto/subtle"
	"github.com/linknestx/linknestx/server/internal/auth"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const webCookieName = "lnx_web"
const WebMaxDownloadBytes int64 = 1_000_000_000

type WebCapabilities struct {
	Upload           bool  `json:"upload"`
	P2P              bool  `json:"p2p"`
	AdminWrite       bool  `json:"adminWrite"`
	ProfileEdit      bool  `json:"profileEdit"`
	NativeTransfers  bool  `json:"nativeTransfers"`
	Updates          bool  `json:"updates"`
	MaxDownloadBytes int64 `json:"maxDownloadBytes"`
}
type WebSession struct {
	User         auth.User       `json:"user"`
	ExpiresAt    time.Time       `json:"expiresAt"`
	CSRFToken    string          `json:"csrfToken"`
	Capabilities WebCapabilities `json:"capabilities"`
}

func webDTO(s auth.Session) WebSession {
	return WebSession{s.User, s.ExpiresAt, s.CSRFToken, WebCapabilities{MaxDownloadBytes: WebMaxDownloadBytes}}
}
func sameOrigin(r *http.Request) bool {
	raw := r.Header.Values("Origin")
	if len(raw) != 1 {
		return false
	}
	u, e := url.Parse(raw[0])
	scheme := requestScheme(r)
	return e == nil && u.Scheme == scheme && strings.EqualFold(u.Host, r.Host) && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
}
func browserCookie(r *http.Request) (*http.Cookie, error) {
	var found *http.Cookie
	for _, c := range r.Cookies() {
		if c.Name == webCookieName {
			if found != nil {
				return nil, auth.ErrUnauthorized
			}
			found = c
		}
	}
	if found == nil {
		return nil, http.ErrNoCookie
	}
	return found, nil
}
func setWebCookie(w http.ResponseWriter, r *http.Request, s auth.Session) {
	http.SetCookie(w, &http.Cookie{Name: webCookieName, Value: s.RefreshToken, Path: "/", HttpOnly: true, Secure: requestScheme(r) == "https", SameSite: http.SameSiteStrictMode, Expires: s.ExpiresAt})
}
func clearWebCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: webCookieName, Value: "", Path: "/", HttpOnly: true, Secure: requestScheme(r) == "https", SameSite: http.SameSiteStrictMode, MaxAge: -1})
}
func csrfValid(r *http.Request, expected string) bool {
	return sameOrigin(r) && expected != "" && len(r.Header.Values("X-CSRF-Token")) == 1 && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(expected)) == 1
}
func (h *AuthHandlers) registerWeb(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/web/login", h.WebLogin)
	mux.Handle("GET /api/v1/web/session", RequireAuth(h.Service, http.HandlerFunc(h.WebSession)))
	mux.Handle("POST /api/v1/web/refresh", RequireAuth(h.Service, http.HandlerFunc(h.WebRefresh)))
	mux.Handle("POST /api/v1/web/logout", RequireAuth(h.Service, http.HandlerFunc(h.WebLogout)))
}
func (h *AuthHandlers) WebLogin(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) || len(r.Header.Values("Authorization")) != 0 {
		writeError(w, 403, auth.ErrUnauthorized)
		return
	}
	if !h.allowAuthentication(w, r) {
		return
	}
	var in auth.LoginInput
	if e := decodeJSONLimit(r, &in, 8192); e != nil {
		authError(w, e)
		return
	}
	in.DeviceID = ""
	s, e := h.Service.LoginWeb(r.Context(), in)
	if e != nil {
		authError(w, e)
		return
	}
	setWebCookie(w, r, s)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, webDTO(s))
}
func (h *AuthHandlers) WebSession(w http.ResponseWriter, r *http.Request) {
	cookie, e := browserCookie(r)
	if e != nil {
		authError(w, auth.ErrUnauthorized)
		return
	}
	_, s, e := h.Service.AuthenticateWeb(r.Context(), cookie.Value)
	if e != nil {
		authError(w, e)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, webDTO(s))
}
func (h *AuthHandlers) WebRefresh(w http.ResponseWriter, r *http.Request) {
	cookie, e := browserCookie(r)
	if e != nil {
		authError(w, auth.ErrUnauthorized)
		return
	}
	s, e := h.Service.RefreshWeb(r.Context(), cookie.Value)
	if e != nil {
		authError(w, e)
		return
	}
	setWebCookie(w, r, s)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, webDTO(s))
}
func (h *AuthHandlers) WebLogout(w http.ResponseWriter, r *http.Request) {
	if auth.ClientTypeFromContext(r.Context()) != "web" {
		authError(w, auth.ErrUnauthorized)
		return
	}
	if e := h.Service.RevokeSession(r.Context(), SessionIDFromContext(r.Context())); e != nil {
		authError(w, e)
		return
	}
	clearWebCookie(w, r)
	w.WriteHeader(204)
}

// Explicit routes keep newly added native capabilities closed to old browser sessions.
func webAllowed(r *http.Request) bool {
	p := r.URL.Path
	get := r.Method == http.MethodGet || r.Method == http.MethodHead
	if p == "/api/v1/documents" {
		return get || r.Method == http.MethodPost
	}
	if strings.HasPrefix(p, "/api/v1/documents/") {
		tail := strings.TrimPrefix(p, "/api/v1/documents/")
		if !strings.Contains(tail, "/") {
			return get || r.Method == http.MethodPatch || r.Method == http.MethodDelete
		}
		return get && strings.HasSuffix(tail, "/preview")
	}
	if get {
		return p == "/api/v1/me" || p == "/api/v1/web/session" || p == "/api/v1/ws" || p == "/api/v1/conversations" || strings.HasPrefix(p, "/api/v1/conversations/") || strings.HasPrefix(p, "/api/v1/profiles/") || p == "/api/v1/contacts" || p == "/api/v1/contacts/search" || p == "/api/v1/groups" || strings.HasPrefix(p, "/api/v1/groups/") || p == "/api/v1/files" || (strings.HasPrefix(p, "/api/v1/files/") && strings.HasSuffix(p, "/versions")) || strings.HasPrefix(p, "/api/v1/web/files/") || p == "/api/v1/me/devices"
	}
	if r.Method == http.MethodDelete {
		return strings.HasPrefix(p, "/api/v1/contacts/")
	}
	if r.Method != http.MethodPost {
		return false
	}
	return p == "/api/v1/web/refresh" || p == "/api/v1/web/logout" || p == "/api/v1/auth/password" || p == "/api/v1/auth/logout" || p == "/api/v1/friend-requests" || strings.HasPrefix(p, "/api/v1/friend-requests/") || p == "/api/v1/conversations/direct" || (strings.HasPrefix(p, "/api/v1/conversations/") && (strings.HasSuffix(p, "/messages") || strings.HasSuffix(p, "/read"))) || (strings.HasPrefix(p, "/api/v1/groups/") && strings.HasSuffix(p, "/messages")) || (strings.HasPrefix(p, "/api/v1/me/devices/") && strings.HasSuffix(p, "/revoke"))
}
