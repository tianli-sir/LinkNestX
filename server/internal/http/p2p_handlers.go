package http

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/linknestx/linknestx/server/internal/auth"
	"github.com/linknestx/linknestx/server/internal/chat"
	"github.com/linknestx/linknestx/server/internal/config"
	"github.com/linknestx/linknestx/server/internal/files"
	"github.com/linknestx/linknestx/server/internal/groups"
	"github.com/linknestx/linknestx/server/internal/p2p"
)

type PeerHandlers struct{ Service p2p.PeerService }

func NewPeerHandlers(s p2p.PeerService) *PeerHandlers { return &PeerHandlers{Service: s} }
func (h *PeerHandlers) Register(mux *http.ServeMux, a *auth.AuthService) {
	mux.Handle("POST /api/v1/p2p/allocate", RequireAuth(a, http.HandlerFunc(h.Allocate)))
	mux.Handle("POST /api/v1/p2p/announce", RequireAuth(a, http.HandlerFunc(h.Announce)))
	mux.Handle("POST /api/v1/p2p/heartbeat", RequireAuth(a, http.HandlerFunc(h.Heartbeat)))
	mux.Handle("POST /api/v1/p2p/withdraw", RequireAuth(a, http.HandlerFunc(h.Withdraw)))
	mux.Handle("GET /api/v1/p2p/sources", RequireAuth(a, http.HandlerFunc(h.Sources)))
	mux.Handle("GET /api/v1/p2p/config", RequireAuth(a, http.HandlerFunc(h.Config)))
	mux.Handle("POST /api/v1/p2p/grants", RequireAuth(a, http.HandlerFunc(h.Grant)))
	mux.Handle("POST /api/v1/p2p/grants/validate", RequireAuth(a, http.HandlerFunc(h.ValidateGrant)))
}
func (h *PeerHandlers) Allocate(w http.ResponseWriter, r *http.Request) {
	var in p2p.AllocationInput
	if err := h.decode(w, r, &in); err != nil {
		peerError(w, err)
		return
	}
	result, err := h.Service.Allocate(r.Context(), UserIDFromContext(r.Context()), in)
	if err != nil {
		peerError(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (h *PeerHandlers) decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, p2p.MaxBodyBytes)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return p2p.ErrInvalidInput
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(v) != nil || decoder.Decode(new(any)) != io.EOF {
		return p2p.ErrInvalidInput
	}
	return nil
}
func (h *PeerHandlers) Announce(w http.ResponseWriter, r *http.Request) {
	var in p2p.AnnounceInput
	if e := h.decode(w, r, &in); e != nil {
		peerError(w, e)
		return
	}
	if e := h.Service.Announce(r.Context(), UserIDFromContext(r.Context()), in); e != nil {
		peerError(w, e)
		return
	}
	w.WriteHeader(204)
}
func (h *PeerHandlers) Heartbeat(w http.ResponseWriter, r *http.Request) {
	var in struct {
		VersionID string `json:"versionId"`
	}
	if e := h.decode(w, r, &in); e != nil {
		peerError(w, e)
		return
	}
	if e := h.Service.Heartbeat(r.Context(), UserIDFromContext(r.Context()), in.VersionID); e != nil {
		peerError(w, e)
		return
	}
	w.WriteHeader(204)
}
func (h *PeerHandlers) Withdraw(w http.ResponseWriter, r *http.Request) {
	var in struct {
		VersionID string `json:"versionId"`
	}
	if e := h.decode(w, r, &in); e != nil {
		peerError(w, e)
		return
	}
	if e := h.Service.Withdraw(r.Context(), UserIDFromContext(r.Context()), in.VersionID); e != nil {
		peerError(w, e)
		return
	}
	w.WriteHeader(204)
}
func (h *PeerHandlers) Sources(w http.ResponseWriter, r *http.Request) {
	version := r.URL.Query().Get("versionId")
	result, e := h.Service.ListSources(r.Context(), UserIDFromContext(r.Context()), version)
	if e != nil {
		peerError(w, e)
		return
	}
	writeJSON(w, 200, result)
}
func (h *PeerHandlers) Config(w http.ResponseWriter, r *http.Request) {
	result, e := h.Service.Config(r.Context(), UserIDFromContext(r.Context()))
	if e != nil {
		peerError(w, e)
		return
	}
	writeJSON(w, 200, result)
}
func (h *PeerHandlers) Grant(w http.ResponseWriter, r *http.Request) {
	var in struct {
		VersionID string `json:"versionId"`
		ChunkHash string `json:"chunkHash"`
	}
	if e := h.decode(w, r, &in); e != nil {
		peerError(w, e)
		return
	}
	g, e := h.Service.CreateChunkGrant(r.Context(), UserIDFromContext(r.Context()), in.VersionID, in.ChunkHash)
	if e != nil {
		peerError(w, e)
		return
	}
	writeJSON(w, 200, g)
}
func (h *PeerHandlers) ValidateGrant(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Grant files.ChunkGrant `json:"grant"`
	}
	if e := h.decode(w, r, &in); e != nil {
		peerError(w, e)
		return
	}
	if e := h.Service.ValidateChunkGrant(r.Context(), in.Grant); e != nil {
		peerError(w, e)
		return
	}
	w.WriteHeader(204)
}
func peerError(w http.ResponseWriter, e error) {
	var large *http.MaxBytesError
	if errors.As(e, &large) {
		writeError(w, 413, errors.New("request body too large"))
		return
	}
	switch {
	case errors.Is(e, files.ErrSharingDisabled):
		writeError(w, 409, e)
	case errors.Is(e, auth.ErrUnauthorized), errors.Is(e, auth.ErrSessionRevoked), errors.Is(e, auth.ErrSessionExpired):
		writeError(w, 401, e)
	case errors.Is(e, files.ErrForbidden), errors.Is(e, auth.ErrDisabledAccount):
		writeError(w, 403, e)
	case errors.Is(e, files.ErrUnavailable):
		writeError(w, 404, e)
	case errors.Is(e, p2p.ErrInvalidInput), errors.Is(e, files.ErrInvalidGrant):
		writeError(w, 400, e)
	case errors.Is(e, p2p.ErrLimit):
		writeError(w, 429, e)
	default:
		writeError(w, 500, errors.New("internal server error"))
	}
}

// NewRouterWithPeers extends the established constructor without changing its
// call signature; existing chat/file callers continue to compile unchanged.
func NewRouterWithPeers(cfg config.Config, a *auth.AuthService, g *groups.GroupService, c *chat.ChatService, ch *chat.Hub, f *files.FileService, p p2p.PeerService, ph *p2p.Hub) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/", NewRouterWithServices(cfg, a, g, c, ch, f))
	NewPeerHandlers(p).Register(mux, a)
	mux.Handle("GET /api/v1/p2p/ws", RequireAuth(a, ph))
	return withTrustedWebScheme(cfg.TrustedProxyCIDRs, mux)
}
