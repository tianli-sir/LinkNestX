package http

import (
	"errors"
	"github.com/linknestx/linknestx/server/internal/auth"
	"github.com/linknestx/linknestx/server/internal/chat"
	"github.com/linknestx/linknestx/server/internal/social"
	"io"
	"net/http"
	"strconv"
)

type SocialHandlers struct {
	Service *social.Service
	Chat    *chat.ChatService
}

func (h *SocialHandlers) Register(mux *http.ServeMux, a *auth.AuthService) {
	for path, handler := range map[string]http.HandlerFunc{
		"GET /api/v1/profiles/{userID}": h.Profile, "PATCH /api/v1/profile": h.PatchProfile, "PUT /api/v1/profile/avatar": h.PutAvatar, "GET /api/v1/profiles/{userID}/avatar": h.Avatar,
		"GET /api/v1/contacts": h.Contacts, "GET /api/v1/contacts/search": h.Search, "POST /api/v1/friend-requests": h.Request, "POST /api/v1/friend-requests/{id}/{action}": h.Act, "DELETE /api/v1/contacts/{userID}": h.Delete,
		"GET /api/v1/conversations": h.Conversations, "POST /api/v1/conversations/direct": h.Direct, "GET /api/v1/conversations/{id}/messages": h.Messages, "POST /api/v1/conversations/{id}/messages": h.Append, "POST /api/v1/conversations/{id}/read": h.Read, "GET /api/v1/conversations/{id}/messages/{messageID}/readers": h.Readers,
	} {
		mux.Handle(path, RequireAuth(a, handler))
	}
}
func socialError(w http.ResponseWriter, e error) {
	switch {
	case errors.Is(e, social.ErrInvalid):
		writeError(w, 400, e)
	case errors.Is(e, social.ErrNotFound):
		writeError(w, 404, e)
	default:
		chatError(w, e)
	}
}
func (h *SocialHandlers) Profile(w http.ResponseWriter, r *http.Request) {
	v, e := h.Service.Profile(r.Context(), UserIDFromContext(r.Context()), r.PathValue("userID"))
	if e != nil {
		socialError(w, e)
		return
	}
	writeJSON(w, 200, v)
}
func (h *SocialHandlers) PatchProfile(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DisplayName string `json:"displayName"`
	}
	if e := decodeJSON(r, &in); e != nil {
		socialError(w, social.ErrInvalid)
		return
	}
	v, e := h.Service.UpdateProfile(r.Context(), UserIDFromContext(r.Context()), in.DisplayName)
	if e != nil {
		socialError(w, e)
		return
	}
	writeJSON(w, 200, v)
}
func (h *SocialHandlers) PutAvatar(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2*1024*1024)
	b, e := io.ReadAll(r.Body)
	if e != nil {
		writeError(w, 413, social.ErrInvalid)
		return
	}
	v, e := h.Service.SetAvatar(r.Context(), UserIDFromContext(r.Context()), b)
	if e != nil {
		socialError(w, e)
		return
	}
	writeJSON(w, 200, v)
}
func (h *SocialHandlers) Avatar(w http.ResponseWriter, r *http.Request) {
	b, e := h.Service.Avatar(r.Context(), UserIDFromContext(r.Context()), r.PathValue("userID"))
	if e != nil {
		socialError(w, e)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(b)
}
func (h *SocialHandlers) Search(w http.ResponseWriter, r *http.Request) {
	v, e := h.Service.Search(r.Context(), UserIDFromContext(r.Context()), r.URL.Query().Get("accountNumber"))
	if e != nil {
		socialError(w, e)
		return
	}
	writeJSON(w, 200, v)
}
func (h *SocialHandlers) Contacts(w http.ResponseWriter, r *http.Request) {
	v, e := h.Service.Contacts(r.Context(), UserIDFromContext(r.Context()))
	if e != nil {
		socialError(w, e)
		return
	}
	writeJSON(w, 200, v)
}
func (h *SocialHandlers) Request(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AccountNumber string `json:"accountNumber"`
	}
	if e := decodeJSON(r, &in); e != nil {
		socialError(w, social.ErrInvalid)
		return
	}
	v, e := h.Service.Request(r.Context(), UserIDFromContext(r.Context()), in.AccountNumber)
	if e != nil {
		socialError(w, e)
		return
	}
	writeJSON(w, 201, v)
}
func (h *SocialHandlers) Act(w http.ResponseWriter, r *http.Request) {
	var in struct{}
	if e := decodeJSON(r, &in); e != nil {
		socialError(w, social.ErrInvalid)
		return
	}
	v, e := h.Service.Act(r.Context(), UserIDFromContext(r.Context()), r.PathValue("id"), r.PathValue("action"))
	if e != nil {
		socialError(w, e)
		return
	}
	writeJSON(w, 200, v)
}
func (h *SocialHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	if e := h.Service.Delete(r.Context(), UserIDFromContext(r.Context()), r.PathValue("userID")); e != nil {
		socialError(w, e)
		return
	}
	w.WriteHeader(204)
}
func (h *SocialHandlers) Conversations(w http.ResponseWriter, r *http.Request) {
	v, e := h.Service.Conversations(r.Context(), UserIDFromContext(r.Context()))
	if e != nil {
		socialError(w, e)
		return
	}
	writeJSON(w, 200, v)
}
func (h *SocialHandlers) Direct(w http.ResponseWriter, r *http.Request) {
	var in struct {
		UserID string `json:"userId"`
	}
	if e := decodeJSON(r, &in); e != nil {
		socialError(w, social.ErrInvalid)
		return
	}
	v, e := h.Service.OpenDirect(r.Context(), UserIDFromContext(r.Context()), in.UserID)
	if e != nil {
		socialError(w, e)
		return
	}
	writeJSON(w, 200, v)
}
func (h *SocialHandlers) Messages(w http.ResponseWriter, r *http.Request) {
	n := 50
	var e error
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, e = strconv.Atoi(raw)
		if e != nil {
			chatError(w, chat.ErrInvalidInput)
			return
		}
	}
	v, e := h.Chat.ConversationPage(r.Context(), UserIDFromContext(r.Context()), r.PathValue("id"), r.URL.Query().Get("before"), r.URL.Query().Get("after"), n)
	if e != nil {
		socialError(w, e)
		return
	}
	writeJSON(w, 200, v)
}
func (h *SocialHandlers) Append(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ClientMessageID string `json:"clientMessageId"`
		Body            string `json:"body"`
	}
	if e := decodeJSON(r, &in); e != nil {
		chatError(w, chat.ErrInvalidInput)
		return
	}
	v, e := h.Chat.Append(r.Context(), UserIDFromContext(r.Context()), chat.AppendInput{GroupID: r.PathValue("id"), ClientMessageID: in.ClientMessageID, Body: in.Body})
	if e != nil {
		socialError(w, e)
		return
	}
	writeJSON(w, 201, v)
}
func (h *SocialHandlers) Read(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MessageIDs []string `json:"messageIds"`
	}
	if e := decodeJSON(r, &in); e != nil {
		chatError(w, chat.ErrInvalidInput)
		return
	}
	v, e := h.Chat.Read(r.Context(), UserIDFromContext(r.Context()), r.PathValue("id"), in.MessageIDs)
	if e != nil {
		socialError(w, e)
		return
	}
	writeJSON(w, 200, map[string]any{"messageIds": v})
}
func (h *SocialHandlers) Readers(w http.ResponseWriter, r *http.Request) {
	v, e := h.Chat.Readers(r.Context(), UserIDFromContext(r.Context()), r.PathValue("id"), r.PathValue("messageID"))
	if e != nil {
		socialError(w, e)
		return
	}
	writeJSON(w, 200, v)
}
