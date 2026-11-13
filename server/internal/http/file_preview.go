package http

import (
	"errors"
	"io"
	"net/http"
	"time"
)

// Preview streams original attachment image bytes under the same visibility
// rules as chunk downloads, without a size cap: the stream is chunked and each
// bounded write rechecks the current session and file ACL. Content is sniffed
// from the leading bytes so a renamed non-picture is rejected as 415 instead of
// becoming inline content.
func (h *FileHandlers) Preview(w http.ResponseWriter, r *http.Request) {
	actor := UserIDFromContext(r.Context())
	version := r.PathValue("versionID")
	m, e := h.Service.Manifest(r.Context(), actor, version)
	if e != nil {
		fileError(w, e)
		return
	}
	if m.Size == 0 || len(m.Chunks) == 0 {
		writeError(w, 415, errors.New("unsupported image type"))
		return
	}
	reader := &documentImageReader{ctx: r.Context(), service: h.Service, actor: actor, version: version, chunks: m.Chunks}
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
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	controller := http.NewResponseController(w)
	defer controller.SetWriteDeadline(time.Time{})
	buffer := make([]byte, 32*1024)
	write := func(b []byte) bool {
		if h.Service.CheckDownload(r.Context(), actor, version) != nil {
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
