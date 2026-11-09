package http

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/linknestx/linknestx/server/internal/config"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linknestx/linknestx/server/internal/auth"
	"github.com/linknestx/linknestx/server/internal/db"
)

func testAuthHandler(t *testing.T) *AuthHandlers {
	d, e := db.Open(filepath.Join(t.TempDir(), "auth.sqlite3"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { d.Close() })
	if e = db.ApplyMigrations(context.Background(), d); e != nil {
		t.Fatal(e)
	}
	s := auth.NewAuthService(auth.NewRepository(d), "access", "refresh")
	if _, e = s.BootstrapAdmin(context.Background(), auth.CreateUserInput{Username: "admin", Password: "correct horse battery staple", Admin: true}); e != nil {
		t.Fatal(e)
	}
	return NewAuthHandlers(s)
}
func TestAuthLoginAndMe(t *testing.T) {
	h := testAuthHandler(t)
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "correct horse battery staple"})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Login(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", w.Code, w.Body)
	}
	var sess auth.Session
	if e := json.Unmarshal(w.Body.Bytes(), &sess); e != nil {
		t.Fatal(e)
	}
	if sess.AccessToken == "" {
		t.Fatal("missing access token")
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+sess.AccessToken)
	out := httptest.NewRecorder()
	RequireAuth(h.Service, http.HandlerFunc(h.Me)).ServeHTTP(out, req)
	if out.Code != http.StatusOK {
		t.Fatalf("me status=%d body=%s", out.Code, out.Body)
	}
}

func TestMalformedBearerRejected(t *testing.T) {
	h := testAuthHandler(t)
	session, err := h.Service.Login(context.Background(), auth.LoginInput{Username: "admin", Password: "correct horse battery staple"})
	if err != nil {
		t.Fatal(err)
	}
	for _, header := range []string{"Bearer" + session.AccessToken, "Bearer", "Bearer ", "Basic " + session.AccessToken, "Bearer " + session.AccessToken + " extra"} {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
		r.Header.Set("Authorization", header)
		w := httptest.NewRecorder()
		RequireAuth(h.Service, http.HandlerFunc(h.Me)).ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("malformed authorization returned %d", w.Code)
		}
	}
}

func TestBearerContextAllowsAccountCreation(t *testing.T) {
	h := testAuthHandler(t)
	session, err := h.Service.Login(context.Background(), auth.LoginInput{Username: "admin", Password: "correct horse battery staple"})
	if err != nil {
		t.Fatal(err)
	}
	handler := RequireAuth(h.Service, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, err := h.Service.CreateUser(r.Context(), auth.CreateUserInput{Username: "member", Password: "password123"})
		if err != nil {
			t.Fatalf("verified administrator context rejected: %v", err)
		}
		if user.ID == "" || UserIDFromContext(r.Context()) != session.User.ID || SessionIDFromContext(r.Context()) != session.ID || DeviceIDFromContext(r.Context()) != session.DeviceID {
			t.Fatal("request identifiers missing")
		}
		w.WriteHeader(http.StatusCreated)
	}))
	r := httptest.NewRequest(http.MethodPost, "/create-test", nil)
	r.Header.Set("Authorization", "bearer "+session.AccessToken)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestAuthRouterLogoutRevokesAccessAndRefresh(t *testing.T) {
	h := testAuthHandler(t)
	cfg := config.Default()
	cfg.SQLitePath = filepath.Join(t.TempDir(), "unused.sqlite3")
	router := NewRouterWithAuth(cfg, h.Service)
	request := func(method, path, body, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	login := request(http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"correct horse battery staple"}`, "")
	if login.Code != http.StatusOK {
		t.Fatalf("login=%d", login.Code)
	}
	var session auth.Session
	if err := json.Unmarshal(login.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	me := request(http.MethodGet, "/api/v1/me", "", session.AccessToken)
	if me.Code != http.StatusOK {
		t.Fatalf("me=%d", me.Code)
	}
	if strings.Contains(me.Body.String(), "password") || strings.Contains(me.Body.String(), session.RefreshToken) {
		t.Fatal("me leaked credentials")
	}
	if got := request(http.MethodPost, "/api/v1/auth/logout", "", session.AccessToken); got.Code != http.StatusNoContent {
		t.Fatalf("logout=%d", got.Code)
	}
	if got := request(http.MethodGet, "/api/v1/me", "", session.AccessToken); got.Code != http.StatusUnauthorized {
		t.Fatalf("logged-out access=%d", got.Code)
	}
	body, _ := json.Marshal(map[string]string{"refreshToken": session.RefreshToken})
	if got := request(http.MethodPost, "/api/v1/auth/refresh", string(body), ""); got.Code != http.StatusUnauthorized {
		t.Fatalf("logged-out refresh=%d", got.Code)
	}
	if got := request(http.MethodGet, "/healthz", "", ""); got.Code != http.StatusOK {
		t.Fatalf("health=%d", got.Code)
	}
}

func TestAdminAccountCreationREST(t *testing.T) {
	h := testAuthHandler(t)
	router := NewRouterWithAuth(config.Default(), h.Service)
	admin, err := h.Service.Login(context.Background(), auth.LoginInput{Username: "admin", Password: "correct horse battery staple"})
	if err != nil {
		t.Fatal(err)
	}
	create := func(token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/users", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	if got := create("", `{"username":"anon","password":"password123"}`); got.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous create=%d", got.Code)
	}
	created := create(admin.AccessToken, `{"username":"member","password":"password123"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("admin create=%d, %s", created.Code, created.Body)
	}
	var payload struct {
		ID                 string `json:"id"`
		MustChangePassword bool   `json:"mustChangePassword"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ID == "" || !payload.MustChangePassword {
		t.Fatalf("missing account/password-change state: %s", created.Body)
	}
	if strings.Contains(created.Body.String(), "password123") || strings.Contains(created.Body.String(), "$argon2id$") {
		t.Fatal("account response leaked password material")
	}
	regular, err := h.Service.Login(context.Background(), auth.LoginInput{Username: "member", Password: "password123"})
	if err != nil {
		t.Fatal(err)
	}
	if got := create(regular.AccessToken, `{"username":"forbidden","password":"password123","roles":["admin"]}`); got.Code != http.StatusForbidden {
		t.Fatalf("non-admin create=%d", got.Code)
	}
	if got := create(admin.AccessToken, `{"username":"injected","password":"password123","actorUserId":"forged","admin":true}`); got.Code != http.StatusBadRequest {
		t.Fatalf("caller privilege fields=%d", got.Code)
	}
}
