package http

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/linknestx/linknestx/server/internal/auth"
	"github.com/linknestx/linknestx/server/internal/chat"
	"github.com/linknestx/linknestx/server/internal/files"
)

type FileHandlers struct{ Service *files.FileService }

func NewFileHandlers(s *files.FileService) *FileHandlers { return &FileHandlers{s} }
func (h *FileHandlers) Register(mux *http.ServeMux, a *auth.AuthService) {
	routes := map[string]http.HandlerFunc{
		"PATCH /api/v1/files/{fileID}":                    h.EditPool,
		"GET /api/v1/sharing-policies/{scope}/{targetID}": h.GetSharingPolicy,
		"PUT /api/v1/sharing-policies/{scope}/{targetID}": h.SetSharingPolicy,
		"PUT /api/v1/uploads/{uploadID}/chunks/{index}":   h.PutChunk,
		"POST /api/v1/uploads/{uploadID}/complete":        h.Complete,
		"GET /api/v1/files/{versionID}/chunks/{hash}":     h.Chunk,
		"POST /api/v1/files/uploads":                      h.StartUpload,
		"GET /api/v1/uploads/{uploadID}":                  h.GetUpload,
		"GET /api/v1/files":                               h.List,
		"GET /api/v1/files/{fileID}/versions":             h.Versions,
		"POST /api/v1/files/{fileID}/versions":            h.CreateVersion,
		"GET /api/v1/file-versions/{versionID}/manifest":  h.Manifest,
		"POST /api/v1/files/{fileID}/promote":             h.Promote,
		"GET /api/v1/files/{versionID}/preview":           h.Preview,
		"GET /api/v1/files/{versionID}/thumb":             h.Thumb,
		"DELETE /api/v1/files/{fileID}":                   h.Delete,
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
	if err := decodeJSONLimit(r, &in, 4*1024*1024); err != nil {
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
	items, err := h.Service.List(r.Context(), UserIDFromContext(r.Context()), files.ListInput{DocumentID: r.URL.Query().Get("documentId"), GroupID: r.URL.Query().Get("groupId"), Scope: r.URL.Query().Get("scope")})
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
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, 413, errors.New("request body too large"))
		return
	}
	switch {
	case errors.Is(err, files.ErrInvalidInput), errors.Is(err, files.ErrInvalidGrant), errors.Is(err, files.ErrInvalidChunkHash), errors.Is(err, files.ErrInvalidChunkSize), errors.Is(err, files.ErrChunkSizeMismatch), errors.Is(err, files.ErrChunkHashMismatch):
		writeError(w, 400, err)
	case errors.Is(err, files.ErrForbidden):
		writeError(w, 403, err)
	case errors.Is(err, files.ErrUnavailable):
		writeError(w, 404, err)
	case errors.Is(err, files.ErrIncomplete), errors.Is(err, chat.ErrConflict):
		writeError(w, 409, err)
	default:
		groupError(w, err)
	}
}

func (h *FileHandlers) PutChunk(w http.ResponseWriter, r *http.Request) {
	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || index < 0 {
		fileError(w, files.ErrInvalidInput)
		return
	}
	// Bound stalled network bodies even when a peer remains connected. A
	// context alone cannot interrupt net/http's blocking body read.
	deadline := time.Now().Add(2 * time.Minute)
	controller := http.NewResponseController(w)
	if err = controller.SetReadDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
		fileError(w, err)
		return
	}
	defer controller.SetReadDeadline(time.Time{})
	ctx, cancel := context.WithDeadline(r.Context(), deadline)
	defer cancel()
	r = r.WithContext(ctx)
	r.Body = http.MaxBytesReader(w, r.Body, files.MaxChunkSize+1)
	if err = h.Service.PutChunk(r.Context(), UserIDFromContext(r.Context()), r.PathValue("uploadID"), index, r.Body); err != nil {
		fileError(w, err)
		return
	}
	w.WriteHeader(204)
}
func (h *FileHandlers) Complete(w http.ResponseWriter, r *http.Request) {
	m, err := h.Service.CompleteUpload(r.Context(), UserIDFromContext(r.Context()), r.PathValue("uploadID"))
	if err != nil {
		fileError(w, err)
		return
	}
	writeJSON(w, 200, m)
}
func (h *FileHandlers) Chunk(w http.ResponseWriter, r *http.Request) {
	actor := UserIDFromContext(r.Context())
	version := r.PathValue("versionID")
	task := r.Header.Get("X-Transfer-ID")
	if len(task) > 128 {
		fileError(w, files.ErrInvalidInput)
		return
	}
	reader, info, err := h.Service.OpenChunk(r.Context(), actor, version, r.PathValue("hash"))
	if err != nil {
		fileError(w, err)
		return
	}
	defer reader.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(200)
	if r.Method == http.MethodHead {
		return
	}
	controller := http.NewResponseController(w)
	defer controller.SetWriteDeadline(time.Time{})
	buffer := make([]byte, 32*1024)
	var delivered int64
	for delivered < info.Size {
		want := min(int64(len(buffer)), info.Size-delivered)
		n, err := io.ReadFull(reader, buffer[:want])
		if err != nil {
			return
		}
		// Permission can change while a chunk is in flight. Recheck immediately
		// before each bounded write without holding a database transaction open.
		if err = h.Service.CheckDownload(r.Context(), actor, version); err != nil {
			return
		}
		if err = controller.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return
		}
		written, err := w.Write(buffer[:n])
		if err != nil || written != n {
			return
		}
		delivered += int64(written)
	}
	if err = h.Service.RecordServerDelivery(r.Context(), actor, version, task, delivered); err != nil {
		slog.Error("record server transfer", "error", err)
	}
}

func (h *FileHandlers) EditPool(w http.ResponseWriter, r *http.Request) {
	var in files.PoolEdit
	if err := decodeJSONLimit(r, &in, 4096); err != nil {
		fileError(w, files.ErrInvalidInput)
		return
	}
	if err := h.Service.EditPool(r.Context(), UserIDFromContext(r.Context()), r.PathValue("fileID"), in); err != nil {
		fileError(w, err)
		return
	}
	w.WriteHeader(204)
}
func (h *FileHandlers) GetSharingPolicy(w http.ResponseWriter, r *http.Request) {
	p, err := h.Service.GetSharingPolicy(r.Context(), UserIDFromContext(r.Context()), r.PathValue("scope"), r.PathValue("targetID"))
	if err != nil {
		fileError(w, err)
		return
	}
	writeJSON(w, 200, p)
}
func (h *FileHandlers) SetSharingPolicy(w http.ResponseWriter, r *http.Request) {
	var in files.SharingPolicy
	if err := decodeJSONLimit(r, &in, 4096); err != nil {
		fileError(w, files.ErrInvalidInput)
		return
	}
	if err := h.Service.SetSharingPolicy(r.Context(), UserIDFromContext(r.Context()), r.PathValue("scope"), r.PathValue("targetID"), in); err != nil {
		fileError(w, err)
		return
	}
	w.WriteHeader(204)
}
