package http

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/linknestx/linknestx/server/internal/auth"
	"github.com/linknestx/linknestx/server/internal/chat"
	"github.com/linknestx/linknestx/server/internal/groups"
)

type ChatHandlers struct {
	Service *chat.ChatService
}

func NewChatHandlers(service *chat.ChatService) *ChatHandlers {
	return &ChatHandlers{service}
}
func parseLimit(raw string) int {
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 1 {
		return -1
	}
	return n
}
func (h *ChatHandlers) List(w http.ResponseWriter, r *http.Request) {
	page, err := h.Service.List(r.Context(), UserIDFromContext(r.Context()), r.PathValue("groupID"), r.URL.Query().Get("cursor"), parseLimit(r.URL.Query().Get("limit")))
	if err != nil {
		chatError(w, err)
		return
	}
	writeJSON(w, 200, page)
}
func (h *ChatHandlers) Append(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ClientMessageID string `json:"clientMessageId"`
		Body            string `json:"body"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32768)
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, err)
		return
	}
	in := chat.AppendInput{GroupID: r.PathValue("groupID"), ClientMessageID: body.ClientMessageID, Body: body.Body}
	m, err := h.Service.Append(r.Context(), UserIDFromContext(r.Context()), in)
	if err != nil {
		chatError(w, err)
		return
	}
	writeJSON(w, 201, m)
}
func chatError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrUnauthorized), errors.Is(err, auth.ErrSessionRevoked), errors.Is(err, auth.ErrSessionExpired):
		writeError(w, 401, err)
	case errors.Is(err, groups.ErrForbidden), errors.Is(err, auth.ErrDisabledAccount):
		writeError(w, 403, err)
	case errors.Is(err, chat.ErrConflict):
		writeError(w, 409, err)
	case errors.Is(err, chat.ErrInvalidInput):
		writeError(w, 400, err)
	default:
		writeError(w, 500, errors.New("internal server error"))
	}
}
