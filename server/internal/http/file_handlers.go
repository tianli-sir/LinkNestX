package http

import (
	"errors"
	"net/http"

	"github.com/linknestx/linknestx/server/internal/auth"
	"github.com/linknestx/linknestx/server/internal/files"
)

type FileHandlers struct{ Service *files.FileService }

func NewFileHandlers(s *files.FileService) *FileHandlers { return &FileHandlers{s} }
func (h *FileHandlers) Register(mux *http.ServeMux, a *auth.AuthService) {
	routes := map[string]http.HandlerFunc{
		"POST /api/v1/files/uploads":                     h.StartUpload,
		"GET /api/v1/uploads/{uploadID}":                 h.GetUpload,
		"GET /api/v1/files":                              h.List,
		"GET /api/v1/files/{fileID}/versions":            h.Versions,
		"POST /api/v1/files/{fileID}/versions":           h.CreateVersion,
		"GET /api/v1/file-versions/{versionID}/manifest": h.Manifest,
		"POST /api/v1/files/{fileID}/promote":            h.Promote,
		"DELETE /api/v1/files/{fileID}":                  h.Delete,
	}
	for path, handler := range routes {
		mux.Handle(path, RequireAuth(a, handler))
	}
}
func (h *FileHandlers) StartUpload(w http.ResponseWriter, r *http.Request) { h.start(w, r, "") }
func (h *FileHandlers) CreateVersion(w http.ResponseWriter, r *http.Request) {
	h.start(w, r, r.PathValue("fileID"))
}
func (h *FileHandlers) start(w http.ResponseWriter, r *http.Request, id string) {
	r.Body = http.MaxBytesReader(w, r.Body, 4*1024*1024)
	var in files.StartUploadInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, 400, files.ErrInvalidInput)
		return
	}
	if id != "" {
		if in.FileID != "" && in.FileID != id {
			fileError(w, files.ErrInvalidInput)
			return
		}
		in.FileID = id
	}
	u, err := h.Service.StartUpload(r.Context(), UserIDFromContext(r.Context()), in)
	if err != nil {
		fileError(w, err)
		return
	}
	writeJSON(w, 201, u)
}
func (h *FileHandlers) GetUpload(w http.ResponseWriter, r *http.Request) {
	u, err := h.Service.GetUpload(r.Context(), UserIDFromContext(r.Context()), r.PathValue("uploadID"))
	if err != nil {
		fileError(w, err)
		return
	}
	writeJSON(w, 200, u)
}
func (h *FileHandlers) List(w http.ResponseWriter, r *http.Request) {
	items, err := h.Service.List(r.Context(), UserIDFromContext(r.Context()), files.ListInput{GroupID: r.URL.Query().Get("groupId"), Scope: r.URL.Query().Get("scope")})
	if err != nil {
		fileError(w, err)
		return
	}
	writeJSON(w, 200, items)
}
func (h *FileHandlers) Versions(w http.ResponseWriter, r *http.Request) {
	items, err := h.Service.Versions(r.Context(), UserIDFromContext(r.Context()), r.PathValue("fileID"))
	if err != nil {
		fileError(w, err)
		return
	}
	writeJSON(w, 200, items)
}
func (h *FileHandlers) Manifest(w http.ResponseWriter, r *http.Request) {
	m, err := h.Service.Manifest(r.Context(), UserIDFromContext(r.Context()), r.PathValue("versionID"))
	if err != nil {
		fileError(w, err)
		return
	}
	writeJSON(w, 200, m)
}
func (h *FileHandlers) Promote(w http.ResponseWriter, r *http.Request) {
	f, err := h.Service.PromoteToPool(r.Context(), UserIDFromContext(r.Context()), r.PathValue("fileID"))
	if err != nil {
		fileError(w, err)
		return
	}
	writeJSON(w, 200, f)
}
func (h *FileHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	if err := h.Service.Delete(r.Context(), UserIDFromContext(r.Context()), r.PathValue("fileID")); err != nil {
		fileError(w, err)
		return
	}
	w.WriteHeader(204)
}
func fileError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, files.ErrInvalidInput), errors.Is(err, files.ErrInvalidGrant), errors.Is(err, files.ErrInvalidChunkHash), errors.Is(err, files.ErrInvalidChunkSize), errors.Is(err, files.ErrChunkSizeMismatch), errors.Is(err, files.ErrChunkHashMismatch):
		writeError(w, 400, err)
	case errors.Is(err, files.ErrForbidden):
		writeError(w, 403, err)
	case errors.Is(err, files.ErrUnavailable):
		writeError(w, 404, err)
	case errors.Is(err, files.ErrIncomplete):
		writeError(w, 409, err)
	default:
		groupError(w, err)
	}
}
