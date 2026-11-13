package http

import (
	"github.com/linknestx/linknestx/server/internal/auth"
	"net/http"
)

func (h *AuthHandlers) registerAccounts(mux *http.ServeMux) {
	routes := map[string]http.HandlerFunc{
		"POST /api/v1/auth/password":                    h.ChangePassword,
		"GET /api/v1/users":                             h.Directory,
		"GET /api/v1/admin/users":                       h.AdminUsers,
		"PATCH /api/v1/admin/users/{id}/account-number": h.UpdateAccountNumber,
		"PATCH /api/v1/admin/users/{id}/username":       h.UpdateUsername,
		"POST /api/v1/admin/users/{id}/disable":         h.DisableUser,
		"POST /api/v1/admin/users/{id}/password":        h.ResetPassword,
		"POST /api/v1/admin/users/{id}/revoke-sessions": h.RevokeUserSessions,
		"GET /api/v1/me/devices":                        h.Devices,
		"GET /api/v1/admin/devices":                     h.AdminDevices,
		"POST /api/v1/me/devices/{id}/revoke":           h.RevokeDevice,
		"POST /api/v1/admin/devices/{id}/revoke":        h.AdminRevokeDevice,
	}
	for path, handler := range routes {
		mux.Handle(path, RequireAuth(h.Service, handler))
	}
}
func (h *AuthHandlers) UpdateAccountNumber(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AccountNumber string `json:"accountNumber"`
	}
	if err := decodeJSON(r, &in); err != nil {
		authError(w, err)
		return
	}
	user, err := h.Service.UpdateAccountNumber(r.Context(), r.PathValue("id"), in.AccountNumber)
	if err != nil {
		authError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, user)
}
func (h *AuthHandlers) UpdateUsername(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
	}
	if err := decodeJSON(r, &in); err != nil {
		authError(w, err)
		return
	}
	user, err := h.Service.UpdateUsername(r.Context(), r.PathValue("id"), in.Username)
	if err != nil {
		authError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, user)
}
func (h *AuthHandlers) ChangePassword(w http.ResponseWriter, r *http.Request) {
	var in auth.ChangePasswordInput
	if err := decodeJSON(r, &in); err != nil {
		authError(w, err)
		return
	}
	user, err := h.Service.ChangePassword(r.Context(), in)
	if err != nil {
		authError(w, err)
		return
	}
	writeJSON(w, 200, user)
}
func (h *AuthHandlers) Directory(w http.ResponseWriter, r *http.Request) {
	result, err := h.Service.Directory(r.Context())
	if err != nil {
		authError(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (h *AuthHandlers) AdminUsers(w http.ResponseWriter, r *http.Request) {
	result, err := h.Service.AdminUsers(r.Context())
	if err != nil {
		authError(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (h *AuthHandlers) DisableUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Disabled *bool `json:"disabled"`
	}
	if err := decodeJSON(r, &in); err != nil {
		authError(w, err)
		return
	}
	if in.Disabled == nil {
		authError(w, auth.ErrInvalidInput)
		return
	}
	if err := h.Service.SetUserDisabled(r.Context(), r.PathValue("id"), *in.Disabled); err != nil {
		authError(w, err)
		return
	}
	w.WriteHeader(204)
}
func (h *AuthHandlers) ResetPassword(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		authError(w, err)
		return
	}
	if err := h.Service.ResetUserPassword(r.Context(), r.PathValue("id"), in.Password); err != nil {
		authError(w, err)
		return
	}
	w.WriteHeader(204)
}
func (h *AuthHandlers) RevokeUserSessions(w http.ResponseWriter, r *http.Request) {
	if err := decodeJSON(r, &struct{}{}); err != nil {
		authError(w, err)
		return
	}
	if err := h.Service.RevokeUserSessions(r.Context(), r.PathValue("id")); err != nil {
		authError(w, err)
		return
	}
	w.WriteHeader(204)
}
func (h *AuthHandlers) Devices(w http.ResponseWriter, r *http.Request) {
	result, err := h.Service.Devices(r.Context())
	if err != nil {
		authError(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (h *AuthHandlers) AdminDevices(w http.ResponseWriter, r *http.Request) {
	result, err := h.Service.AdminDevices(r.Context())
	if err != nil {
		authError(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (h *AuthHandlers) RevokeDevice(w http.ResponseWriter, r *http.Request) {
	h.revokeDevice(w, r, false)
}
func (h *AuthHandlers) AdminRevokeDevice(w http.ResponseWriter, r *http.Request) {
	h.revokeDevice(w, r, true)
}
func (h *AuthHandlers) revokeDevice(w http.ResponseWriter, r *http.Request, admin bool) {
	if err := decodeJSON(r, &struct{}{}); err != nil {
		authError(w, err)
		return
	}
	var err error
	if admin {
		err = h.Service.AdminRevokeDevice(r.Context(), r.PathValue("id"))
	} else {
		err = h.Service.RevokeDevice(r.Context(), r.PathValue("id"))
	}
	if err != nil {
		authError(w, err)
		return
	}
	w.WriteHeader(204)
}
