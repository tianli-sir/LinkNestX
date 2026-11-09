package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/linknestx/linknestx/server/internal/auth"
)

type AuthHandlers struct{ Service *auth.AuthService }

func NewAuthHandlers(service *auth.AuthService) *AuthHandlers { return &AuthHandlers{Service: service} }
func (h *AuthHandlers) Register(mux *http.ServeMux) {
	mux.Handle("POST /api/v1/auth/login", http.HandlerFunc(h.Login))
	mux.Handle("POST /api/v1/auth/refresh", http.HandlerFunc(h.Refresh))
	mux.Handle("POST /api/v1/auth/logout", RequireAuth(h.Service, http.HandlerFunc(h.Logout)))
	mux.Handle("GET /api/v1/me", RequireAuth(h.Service, http.HandlerFunc(h.Me)))
	mux.Handle("POST /api/v1/users", RequireAuth(h.Service, http.HandlerFunc(h.CreateUser)))
}
func (h *AuthHandlers) CreateUser(w http.ResponseWriter, r *http.Request) {
	var in auth.CreateUserInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err)
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
	var in auth.LoginInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err)
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
	var body struct {
		RefreshToken string `json:"refreshToken"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
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
func decodeJSON(r *http.Request, v any) error {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		return errors.New("content type must be application/json")
	}
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
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
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials), errors.Is(err, auth.ErrUnauthorized), errors.Is(err, auth.ErrSessionExpired), errors.Is(err, auth.ErrSessionRevoked):
		writeError(w, http.StatusUnauthorized, err)
	case errors.Is(err, auth.ErrInvalidDevice), errors.Is(err, auth.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, auth.ErrDisabledAccount), errors.Is(err, auth.ErrAdminRequired):
		writeError(w, http.StatusForbidden, err)
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}
