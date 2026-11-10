package http

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"

	"github.com/linknestx/linknestx/server/internal/auth"
)

type AuthHandlers struct {
	Service        *auth.AuthService
	limiter        *authLimiter
	trustedProxies []netip.Prefix
}

func NewAuthHandlers(service *auth.AuthService) *AuthHandlers {
	return &AuthHandlers{Service: service, limiter: newAuthLimiter()}
}
func (h *AuthHandlers) Register(mux *http.ServeMux) {
	h.registerAccounts(mux)
	mux.Handle("POST /api/v1/auth/login", http.HandlerFunc(h.Login))
	mux.Handle("POST /api/v1/auth/refresh", http.HandlerFunc(h.Refresh))
	mux.Handle("POST /api/v1/auth/logout", RequireAuth(h.Service, http.HandlerFunc(h.Logout)))
	mux.Handle("GET /api/v1/me", RequireAuth(h.Service, http.HandlerFunc(h.Me)))
	mux.Handle("POST /api/v1/users", RequireAuth(h.Service, http.HandlerFunc(h.CreateUser)))
}
func (h *AuthHandlers) CreateUser(w http.ResponseWriter, r *http.Request) {
	var in auth.CreateUserInput
	if err := decodeJSON(r, &in); err != nil {
		authError(w, err)
		return
	}
	user, err := h.Service.CreateUser(r.Context(), in)
	if err != nil {
		authError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, user)
}

func (h *AuthHandlers) Login(w http.ResponseWriter, r *http.Request) {
	if !h.allowAuthentication(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	var in auth.LoginInput
	if err := decodeJSON(r, &in); err != nil {
		authError(w, err)
		return
	}
	session, err := h.Service.Login(r.Context(), in)
	if err != nil {
		authError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}
func (h *AuthHandlers) Refresh(w http.ResponseWriter, r *http.Request) {
	if !h.allowAuthentication(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	var body struct {
		RefreshToken string `json:"refreshToken"`
	}
	if err := decodeJSON(r, &body); err != nil {
		authError(w, err)
		return
	}
	session, err := h.Service.Refresh(r.Context(), body.RefreshToken)
	if err != nil {
		authError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}
func (h *AuthHandlers) Logout(w http.ResponseWriter, r *http.Request) {
	if err := h.Service.RevokeSession(r.Context(), SessionIDFromContext(r.Context())); err != nil {
		authError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h *AuthHandlers) Me(w http.ResponseWriter, r *http.Request) {
	u, ok := UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

var errRateLimit = errors.New("too many authentication attempts")

func decodeJSON(r *http.Request, v any) error {
	return decodeJSONLimit(r, v, 1048576)
}
func decodeJSONLimit(r *http.Request, v any, limit int64) error {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		return fmt.Errorf("%w: content type must be application/json", auth.ErrInvalidInput)
	}
	defer r.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return err
	}
	if int64(len(raw)) > limit {
		return &http.MaxBytesError{Limit: limit}
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return auth.ErrInvalidInput
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: invalid JSON body", auth.ErrInvalidInput)
	}
	if dec.Decode(new(any)) != io.EOF {
		return auth.ErrInvalidInput
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
func authError(w http.ResponseWriter, err error) {
	var large *http.MaxBytesError
	if errors.As(err, &large) {
		writeError(w, 413, errors.New("request body too large"))
		return
	}
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials), errors.Is(err, auth.ErrUnauthorized), errors.Is(err, auth.ErrSessionExpired), errors.Is(err, auth.ErrSessionRevoked):
		writeError(w, http.StatusUnauthorized, err)
	case errors.Is(err, auth.ErrInvalidDevice), errors.Is(err, auth.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, auth.ErrDisabledAccount), errors.Is(err, auth.ErrAdminRequired), errors.Is(err, auth.ErrPasswordChangeRequired):
		writeError(w, http.StatusForbidden, err)
	case errors.Is(err, auth.ErrLastAdmin):
		writeError(w, 409, err)
	case errors.Is(err, auth.ErrUserNotFound), errors.Is(err, auth.ErrDeviceNotFound):
		writeError(w, 404, err)
	default:
		writeError(w, http.StatusInternalServerError, errors.New("internal server error"))
	}
}
