package http

import (
	"errors"
	"github.com/google/uuid"
	"github.com/linknestx/linknestx/server/internal/files"
	"io"
	"mime"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Streams are keyed by the persisted session, not by a caller-supplied device.
type WebDownloads struct {
	Service *files.FileService
	mu      sync.Mutex
	active  map[string]bool
}

func (h *WebDownloads) acquire(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.active == nil {
		h.active = map[string]bool{}
	}
	if h.active[id] {
		return false
	}
	h.active[id] = true
	return true
}
func (h *WebDownloads) release(id string) { h.mu.Lock(); delete(h.active, id); h.mu.Unlock() }
func (h *WebDownloads) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	actor, session, version := UserIDFromContext(r.Context()), SessionIDFromContext(r.Context()), r.PathValue("versionID")
	if r.Header.Get("Range") != "" {
		writeError(w, 416, errors.New("ranges are not supported"))
		return
	}
	m, e := h.Service.Manifest(r.Context(), actor, version)
	if e != nil {
		fileError(w, e)
		return
	}
	if m.Size > WebMaxDownloadBytes {
		writeError(w, 413, errors.New("use the desktop client for files larger than 1 GB"))
		return
	}
	if r.Method != http.MethodHead {
		if !h.acquire(session) {
			writeError(w, 409, errors.New("a download is already active for this session"))
			return
		}
		defer h.release(session)
	}
	var first io.ReadCloser
	var firstInfo files.ChunkInfo
	if r.Method != http.MethodHead && len(m.Chunks) > 0 {
		first, firstInfo, e = h.Service.OpenChunk(r.Context(), actor, version, m.Chunks[0].Hash)
		if e != nil {
			fileError(w, e)
			return
		}
		defer first.Close()
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": m.Name}))
	w.Header().Set("Content-Length", strconv.FormatInt(m.Size, 10))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Accept-Ranges", "none")
	if r.Method == http.MethodHead {
		return
	}
	controller := http.NewResponseController(w)
	defer controller.SetWriteDeadline(time.Time{})
	buffer := make([]byte, 32*1024)
	var total int64
	task := "web:" + uuid.NewString()
	for index, chunk := range m.Chunks {
		reader, info := first, firstInfo
		if index > 0 {
			reader, info, e = h.Service.OpenChunk(r.Context(), actor, version, chunk.Hash)
		}
		if e != nil {
			return
		}
		remaining := info.Size
		for remaining > 0 {
			if e = h.Service.CheckDownload(r.Context(), actor, version); e != nil {
				reader.Close()
				return
			}
			want := int64(len(buffer))
			if remaining < want {
				want = remaining
			}
			n, readErr := io.ReadFull(reader, buffer[:want])
			if readErr != nil {
				reader.Close()
				return
			}
			_ = controller.SetWriteDeadline(time.Now().Add(30 * time.Second))
			written, writeErr := w.Write(buffer[:n])
			total += int64(written)
			remaining -= int64(written)
			if writeErr != nil || written != n {
				reader.Close()
				return
			}
		}
		reader.Close()
	}
	if total == m.Size {
		_ = h.Service.RecordServerDelivery(r.Context(), actor, version, task, total)
	}
}
