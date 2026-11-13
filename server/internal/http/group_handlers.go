package http

import (
	"errors"
	"net/http"

	"github.com/linknestx/linknestx/server/internal/auth"
	"github.com/linknestx/linknestx/server/internal/groups"
)

type GroupHandlers struct{ Service *groups.GroupService }

func NewGroupHandlers(service *groups.GroupService) *GroupHandlers { return &GroupHandlers{service} }
func (h *GroupHandlers) Register(mux *http.ServeMux, a *auth.AuthService) {
	mux.Handle("GET /api/v1/groups", RequireAuth(a, http.HandlerFunc(h.List)))
	mux.Handle("POST /api/v1/groups", RequireAuth(a, http.HandlerFunc(h.Create)))
	mux.Handle("GET /api/v1/groups/{groupID}/members", RequireAuth(a, http.HandlerFunc(h.ListMembers)))
	mux.Handle("POST /api/v1/groups/{groupID}/members", RequireAuth(a, http.HandlerFunc(h.AddMember)))
	mux.Handle("DELETE /api/v1/groups/{groupID}/members/{userID}", RequireAuth(a, http.HandlerFunc(h.RemoveMember)))
	mux.Handle("PATCH /api/v1/groups/{groupID}/members/{userID}", RequireAuth(a, http.HandlerFunc(h.SetMemberNickname)))
}
func (h *GroupHandlers) List(w http.ResponseWriter, r *http.Request) {
	actor := UserIDFromContext(r.Context())
	items, err := h.Service.List(r.Context(), actor)
	if err != nil {
		groupError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}
func (h *GroupHandlers) Create(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	var in groups.CreateInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, 400, err)
		return
	}
	g, err := h.Service.Create(r.Context(), UserIDFromContext(r.Context()), in)
	if err != nil {
		groupError(w, err)
		return
	}
	writeJSON(w, 201, g)
}
func (h *GroupHandlers) ListMembers(w http.ResponseWriter, r *http.Request) {
	g, err := h.Service.ListMembers(r.Context(), UserIDFromContext(r.Context()), r.PathValue("groupID"))
	if err != nil {
		groupError(w, err)
		return
	}
	writeJSON(w, 200, g)
}
func (h *GroupHandlers) AddMember(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	var in struct {
		UserID string `json:"userId"`
		Role   string `json:"role"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, 400, err)
		return
	}
	err := h.Service.AddMember(r.Context(), UserIDFromContext(r.Context()), r.PathValue("groupID"), in.UserID, in.Role)
	if err != nil {
		groupError(w, err)
		return
	}
	w.WriteHeader(204)
}
func (h *GroupHandlers) RemoveMember(w http.ResponseWriter, r *http.Request) {
	err := h.Service.RemoveMember(r.Context(), UserIDFromContext(r.Context()), r.PathValue("groupID"), r.PathValue("userID"))
	if err != nil {
		groupError(w, err)
		return
	}
	w.WriteHeader(204)
}
func (h *GroupHandlers) SetMemberNickname(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("userID") != UserIDFromContext(r.Context()) {
		writeError(w, 403, groups.ErrForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	var in struct {
		Nickname string `json:"nickname"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, 400, err)
		return
	}
	if err := h.Service.SetMemberNickname(r.Context(), UserIDFromContext(r.Context()), r.PathValue("groupID"), in.Nickname); err != nil {
		groupError(w, err)
		return
	}
	w.WriteHeader(204)
}
func groupError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrUnauthorized), errors.Is(err, auth.ErrSessionRevoked), errors.Is(err, auth.ErrSessionExpired):
		writeError(w, 401, err)
	case errors.Is(err, auth.ErrAdminRequired), errors.Is(err, groups.ErrForbidden), errors.Is(err, auth.ErrDisabledAccount):
		writeError(w, 403, err)
	case errors.Is(err, groups.ErrNotFound):
		writeError(w, 404, err)
	case errors.Is(err, groups.ErrInvalidInput), errors.Is(err, groups.ErrLastOwner):
		writeError(w, 400, err)
	default:
		writeError(w, 500, errors.New("internal server error"))
	}
}
