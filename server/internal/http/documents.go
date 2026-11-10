package http

import (
	"context"
	"errors"
	"github.com/linknestx/linknestx/server/internal/auth"
	"github.com/linknestx/linknestx/server/internal/files"
	"github.com/linknestx/linknestx/server/internal/notes"
	"io"
	"net/http"
	"strconv"
	"time"
)

type DocumentHandlers struct {
	Service *notes.Service
	Files   *files.FileService
}

func (h *DocumentHandlers) Register(mux *http.ServeMux, a *auth.AuthService) {
	for route, handler := range map[string]http.HandlerFunc{
		"GET /api/v1/documents": h.List, "POST /api/v1/documents": h.Create, "GET /api/v1/documents/{documentID}": h.Get, "PATCH /api/v1/documents/{documentID}": h.Update, "DELETE /api/v1/documents/{documentID}": h.Delete, "POST /api/v1/documents/{documentID}/curation": h.Curate, "GET /api/v1/documents/{documentID}/attachments/{versionID}/preview": h.Preview,
	} {
		mux.Handle(route, RequireAuth(a, handler))
	}
}
func documentError(w http.ResponseWriter, e error) {
	switch {
	case errors.Is(e, notes.ErrInvalid):
		writeError(w, 400, e)
	case errors.Is(e, notes.ErrNotFound):
		writeError(w, 404, e)
	case errors.Is(e, notes.ErrForbidden):
		writeError(w, 403, e)
	case errors.Is(e, notes.ErrConflict):
		writeError(w, 409, e)
	default:
		authError(w, e)
	}
}
func (h *DocumentHandlers) List(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		var e error
		limit, e = strconv.Atoi(raw)
		if e != nil || limit < 1 {
			documentError(w, notes.ErrInvalid)
			return
		}
	}
	p, e := h.Service.List(r.Context(), UserIDFromContext(r.Context()), notes.ListInput{Scope: r.URL.Query().Get("scope"), Q: r.URL.Query().Get("q"), Cursor: r.URL.Query().Get("cursor"), Limit: limit})
	if e != nil {
		documentError(w, e)
		return
	}
	writeJSON(w, 200, p)
}
func (h *DocumentHandlers) Get(w http.ResponseWriter, r *http.Request) {
	d, e := h.Service.Get(r.Context(), UserIDFromContext(r.Context()), r.PathValue("documentID"))
	if e != nil {
		documentError(w, e)
		return
	}
	writeJSON(w, 200, d)
}
func (h *DocumentHandlers) write(w http.ResponseWriter, r *http.Request, create bool) {
	var body struct {
		Title      *string `json:"title"`
		Body       *string `json:"body"`
		Visibility *string `json:"visibility"`
		Status     *string `json:"status"`
		Revision   int64   `json:"revision"`
	}
	if decodeJSONLimit(r, &body, 2*1024*1024) != nil || body.Title == nil || body.Body == nil || body.Visibility == nil || body.Status == nil {
		documentError(w, notes.ErrInvalid)
		return
	}
	in := notes.Input{Title: *body.Title, Body: *body.Body, Visibility: *body.Visibility, Status: *body.Status, Revision: body.Revision}
	var d notes.Document
	var e error
	code := 200
	if create {
		d, e = h.Service.Create(r.Context(), UserIDFromContext(r.Context()), in)
		code = 201
	} else {
		d, e = h.Service.Update(r.Context(), UserIDFromContext(r.Context()), r.PathValue("documentID"), in)
	}
	if e != nil {
		documentError(w, e)
		return
	}
	writeJSON(w, code, d)
}
func (h *DocumentHandlers) Create(w http.ResponseWriter, r *http.Request) { h.write(w, r, true) }
func (h *DocumentHandlers) Update(w http.ResponseWriter, r *http.Request) { h.write(w, r, false) }
func (h *DocumentHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	revision, e := strconv.ParseInt(r.URL.Query().Get("revision"), 10, 64)
	if e != nil || revision < 1 {
		documentError(w, notes.ErrInvalid)
		return
	}
	if e = h.Service.Delete(r.Context(), UserIDFromContext(r.Context()), r.PathValue("documentID"), revision); e != nil {
		documentError(w, e)
		return
	}
	w.WriteHeader(204)
}
func (h *DocumentHandlers) Curate(w http.ResponseWriter, r *http.Request) {
	if auth.ClientTypeFromContext(r.Context()) == "web" {
		documentError(w, notes.ErrForbidden)
		return
	}
	var in struct {
		Revision int64 `json:"revision"`
		Curated  *bool `json:"curated"`
	}
	if decodeJSONLimit(r, &in, 4096) != nil || in.Curated == nil {
		documentError(w, notes.ErrInvalid)
		return
	}
	d, e := h.Service.Curate(r.Context(), UserIDFromContext(r.Context()), r.PathValue("documentID"), in.Revision, *in.Curated)
	if e != nil {
		documentError(w, e)
		return
	}
	writeJSON(w, 200, d)
}
func (h *DocumentHandlers) Preview(w http.ResponseWriter, r *http.Request) {
	if h.Files == nil {
		documentError(w, notes.ErrNotFound)
		return
	}
	actor := UserIDFromContext(r.Context())
	version := r.PathValue("versionID")
	m, e := h.Files.Manifest(r.Context(), actor, version)
	if e != nil {
		fileError(w, e)
		return
	}
	if m.DocumentID != r.PathValue("documentID") {
		documentError(w, notes.ErrNotFound)
		return
	}
	if m.Size > 10*1024*1024 {
		writeError(w, 413, errors.New("image preview exceeds 10 MiB"))
		return
	}
	if m.Size == 0 || len(m.Chunks) == 0 {
		writeError(w, 415, errors.New("unsupported image type"))
		return
	}
	// The prefix may cross manifest chunks. Each bounded read rechecks the
	// current session and document ACL before reading the next payload bytes.
	reader := &documentImageReader{ctx: r.Context(), service: h.Files, actor: actor, version: version, chunks: m.Chunks}
	defer reader.Close()
	prefix := make([]byte, 512)
	n, e := io.ReadFull(reader, prefix)
	if e != nil && e != io.EOF && e != io.ErrUnexpectedEOF {
		fileError(w, e)
		return
	}
	prefix = prefix[:n]
	kind := http.DetectContentType(prefix)
	switch kind {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		writeError(w, 415, errors.New("unsupported image type"))
		return
	}
	w.Header().Set("Content-Type", kind)
	w.Header().Set("Content-Length", strconv.FormatInt(m.Size, 10))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	if r.Method == "HEAD" {
		return
	}
	controller := http.NewResponseController(w)
	defer controller.SetWriteDeadline(time.Time{})
	buffer := make([]byte, 32*1024)
	write := func(b []byte) bool {
		if h.Files.CheckDownload(r.Context(), actor, version) != nil {
			return false
		}
		_ = controller.SetWriteDeadline(time.Now().Add(30 * time.Second))
		n, e := w.Write(b)
		return e == nil && n == len(b)
	}
	if !write(prefix) {
		return
	}
	for {
		n, e := reader.Read(buffer)
		if n > 0 && !write(buffer[:n]) {
			return
		}
		if e != nil {
			return
		}
	}
}

type documentImageReader struct {
	ctx            context.Context
	service        *files.FileService
	actor, version string
	chunks         []files.Chunk
	index          int
	reader         io.ReadCloser
	remaining      int64
}

func (r *documentImageReader) Close() error {
	if r.reader != nil {
		return r.reader.Close()
	}
	return nil
}
func (r *documentImageReader) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	if e := r.service.CheckDownload(r.ctx, r.actor, r.version); e != nil {
		return 0, e
	}
	if r.remaining == 0 {
		if r.reader != nil {
			r.reader.Close()
			r.reader = nil
		}
		if r.index == len(r.chunks) {
			return 0, io.EOF
		}
		c := r.chunks[r.index]
		reader, info, e := r.service.OpenChunk(r.ctx, r.actor, r.version, c.Hash)
		if e != nil {
			return 0, e
		}
		r.reader = reader
		r.remaining = info.Size
		r.index++
	}
	want := len(buffer)
	if int64(want) > r.remaining {
		want = int(r.remaining)
	}
	n, e := io.ReadFull(r.reader, buffer[:want])
	r.remaining -= int64(n)
	return n, e
}
