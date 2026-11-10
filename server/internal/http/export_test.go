package http

import (
	"context"
	"errors"
	"github.com/linknestx/linknestx/server/internal/auth"
	"github.com/linknestx/linknestx/server/internal/config"
	"net/http"
	"net/http/httptest"
	"testing"
)

type revokeOnWrite struct {
	*httptest.ResponseRecorder
	once   bool
	revoke func()
}

func (w *revokeOnWrite) Write(b []byte) (int, error) {
	if !w.once {
		w.once = true
		w.revoke()
	}
	return w.ResponseRecorder.Write(b)
}
func TestExportAbortsHTTPResponseAfterRevocation(t *testing.T) {
	h := testAuthHandler(t)
	session, err := h.Service.Login(context.Background(), auth.LoginInput{Username: "admin", Password: "correct horse battery staple"})
	if err != nil {
		t.Fatal(err)
	}
	w := &revokeOnWrite{ResponseRecorder: httptest.NewRecorder(), revoke: func() {
		if err := h.Service.RevokeSession(context.Background(), session.ID); err != nil {
			t.Fatal(err)
		}
	}}
	req := httptest.NewRequest("GET", "/api/v1/admin/audit/export?format=json", nil)
	req.Header.Set("Authorization", "Bearer "+session.AccessToken)
	defer func() {
		if got := recover(); !errors.Is(asError(got), http.ErrAbortHandler) {
			t.Fatalf("export did not abort HTTP stream: %v", got)
		}
	}()
	NewRouterWithAuth(config.Default(), h.Service).ServeHTTP(w, req)
}
func asError(value any) error { err, _ := value.(error); return err }
