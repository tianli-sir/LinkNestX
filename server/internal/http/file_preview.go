package http

import (
	"bytes"
	"container/list"
	"errors"
	"image"
	"image/png"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	_ "image/gif"
	_ "image/jpeg"
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

// thumbLRU is an in-process thumbnail cache keyed by version ID. A single
// enterprise deployment keeps the working set small; overflow evicts the
// least-recently-used entry. sync.Map has no eviction semantics, hence the
// explicit list-backed map.
type thumbLRU struct {
	mu    sync.Mutex
	order *list.List
	items map[string]*list.Element
	limit int
}

type thumbValue struct {
	key   string
	bytes []byte
}

var attachmentThumbs = &thumbLRU{order: list.New(), items: map[string]*list.Element{}, limit: 256}

func (c *thumbLRU) get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.items[key]; ok {
		c.order.MoveToFront(element)
		return element.Value.(*thumbValue).bytes, true
	}
	return nil, false
}
func (c *thumbLRU) put(key string, value []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.items[key]; ok {
		c.order.MoveToFront(element)
		element.Value.(*thumbValue).bytes = value
		return
	}
	c.items[key] = c.order.PushFront(&thumbValue{key, value})
	for c.order.Len() > c.limit {
		oldest := c.order.Back()
		if oldest == nil {
			return
		}
		c.order.Remove(oldest)
		delete(c.items, oldest.Value.(*thumbValue).key)
	}
}
func (c *thumbLRU) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

const (
	thumbMaxEdge        = 480
	thumbMaxPixels      = 80_000_000
	thumbMaxSourceBytes = 64 << 20
)

// Thumb serves a PNG thumbnail with the longest edge capped at 480. Sources
// the standard library cannot decode (including webp), oversized payloads and
// absurd pixel counts fall back to a 302 so the client scales the original in
// CSS. Access checks mirror Preview; cache hits re-run the availability check
// so deleted, expired or revoked files stop serving immediately instead of
// serving until LRU eviction.
func (h *FileHandlers) Thumb(w http.ResponseWriter, r *http.Request) {
	actor := UserIDFromContext(r.Context())
	version := r.PathValue("versionID")
	m, e := h.Service.Manifest(r.Context(), actor, version)
	if e != nil {
		fileError(w, e)
		return
	}
	if cached, ok := attachmentThumbs.get(version); ok {
		serveThumb(w, cached)
		return
	}
	if m.Size == 0 || m.Size > thumbMaxSourceBytes || len(m.Chunks) == 0 {
		redirectPreview(w, r, version)
		return
	}
	reader := &documentImageReader{ctx: r.Context(), service: h.Service, actor: actor, version: version, chunks: m.Chunks}
	defer reader.Close()
	data, e := io.ReadAll(io.LimitReader(reader, thumbMaxSourceBytes+1))
	if e != nil || int64(len(data)) != m.Size {
		redirectPreview(w, r, version)
		return
	}
	cfg, _, e := image.DecodeConfig(bytes.NewReader(data))
	if e != nil || int64(cfg.Width)*int64(cfg.Height) > thumbMaxPixels {
		redirectPreview(w, r, version)
		return
	}
	source, _, e := image.Decode(bytes.NewReader(data))
	if e != nil {
		redirectPreview(w, r, version)
		return
	}
	encoded := scaleToThumbnail(source)
	if len(encoded) == 0 {
		redirectPreview(w, r, version)
		return
	}
	attachmentThumbs.put(version, encoded)
	serveThumb(w, encoded)
}

func redirectPreview(w http.ResponseWriter, r *http.Request, version string) {
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/api/v1/files/"+version+"/preview", http.StatusFound)
}

func serveThumb(w http.ResponseWriter, encoded []byte) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Length", strconv.Itoa(len(encoded)))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Write(encoded)
}

// scaleToThumbnail resizes with nearest-neighbour sampling (standard library
// only; no x/image dependency) and re-encodes as PNG so gif first frames and
// every decodable source converge on one output type.
func scaleToThumbnail(source image.Image) []byte {
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 {
		return nil
	}
	scale := float64(thumbMaxEdge) / float64(max(width, height))
	if scale > 1 {
		scale = 1
	}
	targetWidth, targetHeight := max(1, int(float64(width)*scale+0.5)), max(1, int(float64(height)*scale+0.5))
	target := image.NewRGBA(image.Rect(0, 0, targetWidth, targetHeight))
	for y := 0; y < targetHeight; y++ {
		sourceY := bounds.Min.Y + y*height/targetHeight
		for x := 0; x < targetWidth; x++ {
			target.Set(x, y, source.At(bounds.Min.X+x*width/targetWidth, sourceY))
		}
	}
	var out bytes.Buffer
	if png.Encode(&out, target) != nil {
		return nil
	}
	return out.Bytes()
}
