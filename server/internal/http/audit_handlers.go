package http

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/linknestx/linknestx/server/internal/audit"
	"github.com/linknestx/linknestx/server/internal/auth"
)

type AuditHandlers struct{ Service *audit.Service }

func (h *AuditHandlers) Register(mux *http.ServeMux, a *auth.AuthService) {
	mux.Handle("GET /api/v1/admin/audit", RequireAuth(a, http.HandlerFunc(h.List)))
	mux.Handle("GET /api/v1/admin/audit/export", RequireAuth(a, http.HandlerFunc(h.Export)))
	mux.Handle("POST /api/v1/p2p/transfers", RequireAuth(a, http.HandlerFunc(h.Transfer)))
}
func auditFilter(r *http.Request) (audit.Filter, error) {
	if len(r.URL.RawQuery) > 4096 {
		return audit.Filter{}, audit.ErrInvalidInput
	}
	q := r.URL.Query()
	f := audit.Filter{Action: q.Get("action"), ActorID: q.Get("actorId"), DeviceID: q.Get("deviceId"), FileID: q.Get("fileId"), VersionID: q.Get("versionId")}
	for key, values := range q {
		if len(values) != 1 {
			return f, audit.ErrInvalidInput
		}
		switch key {
		case "action", "actorId", "deviceId", "fileId", "versionId", "from", "before", "limit", "offset", "format":
		default:
			return f, audit.ErrInvalidInput
		}
	}
	for _, item := range []struct {
		name  string
		value *int
	}{{"limit", &f.Limit}, {"offset", &f.Offset}} {
		if raw := q.Get(item.name); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil {
				return f, audit.ErrInvalidInput
			}
			*item.value = n
			if item.name == "limit" && n == 0 {
				return f, audit.ErrInvalidInput
			}
		}
	}
	for _, item := range []struct {
		name  string
		value *time.Time
	}{{"from", &f.From}, {"before", &f.Before}} {
		if raw := q.Get(item.name); raw != "" {
			at, err := time.Parse(time.RFC3339Nano, raw)
			if err != nil {
				return f, audit.ErrInvalidInput
			}
			*item.value = at
		}
	}
	return f, nil
}
func auditError(w http.ResponseWriter, err error) {
	if errors.Is(err, audit.ErrInvalidInput) {
		writeError(w, 400, err)
	} else if errors.Is(err, audit.ErrTransferForbidden) {
		writeError(w, 403, err)
	} else {
		authError(w, err)
	}
}
func (h *AuditHandlers) List(w http.ResponseWriter, r *http.Request) {
	f, err := auditFilter(r)
	if err != nil {
		auditError(w, err)
		return
	}
	page, err := h.Service.Page(r.Context(), f)
	if err != nil {
		auditError(w, err)
		return
	}
	writeJSON(w, 200, page)
}

type exportWriter struct {
	http.ResponseWriter
	started bool
}

func (w *exportWriter) Write(data []byte) (int, error) {
	w.started = true
	return w.ResponseWriter.Write(data)
}
func (h *AuditHandlers) Export(w http.ResponseWriter, r *http.Request) {
	f, err := auditFilter(r)
	if err != nil {
		auditError(w, err)
		return
	}
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "csv"
	}
	if format != "csv" && format != "json" {
		auditError(w, audit.ErrInvalidInput)
		return
	}
	contentType := "text/csv; charset=utf-8"
	if format == "json" {
		contentType = "application/json; charset=utf-8"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", `attachment; filename="linknestx-audit.`+format+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	out := &exportWriter{ResponseWriter: w}
	if err := h.Service.Export(r.Context(), f, format, out); err != nil {
		if out.started {
			panic(http.ErrAbortHandler)
		}
		w.Header().Del("Content-Disposition")
		auditError(w, err)
	}
}
func (h *AuditHandlers) Transfer(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	var in audit.TransferInput
	if err := decodeJSON(r, &in); err != nil {
		auditError(w, err)
		return
	}
	if err := h.Service.RecordClientTransfer(r.Context(), in); err != nil {
		auditError(w, err)
		return
	}
	w.WriteHeader(204)
}
