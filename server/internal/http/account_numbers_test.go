package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linknestx/linknestx/server/internal/auth"
	"github.com/linknestx/linknestx/server/internal/config"
)

func TestAdminAccountNumberREST(t *testing.T) {
	h := testAuthHandler(t)
	router := NewRouterWithAuth(config.Default(), h.Service)
	admin, err := h.Service.Login(context.Background(), auth.LoginInput{Username: "admin", Password: "correct horse battery staple"})
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path string, body any, token string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	create := request("POST", "/api/v1/users", map[string]string{"username": "employee", "password": "password123", "accountNumber": "765432"}, admin.AccessToken)
	if create.Code != http.StatusCreated {
		t.Fatalf("create=%d %s", create.Code, create.Body)
	}
	var user auth.User
	if err = json.Unmarshal(create.Body.Bytes(), &user); err != nil {
		t.Fatal(err)
	}
	if user.AccountNumber != "765432" {
		t.Fatalf("number=%s", user.AccountNumber)
	}
	path := "/api/v1/admin/users/" + user.ID + "/account-number"
	for _, tc := range []struct {
		number, token string
		status        int
	}{{"876543", "", 401}, {"012345", admin.AccessToken, 400}, {admin.User.AccountNumber, admin.AccessToken, 409}, {"876543", admin.AccessToken, 200}} {
		got := request("PATCH", path, map[string]string{"accountNumber": tc.number}, tc.token)
		if got.Code != tc.status {
			t.Fatalf("edit number %q=%d want=%d body=%s", tc.number, got.Code, tc.status, got.Body)
		}
	}
	employee, err := h.Service.Login(context.Background(), auth.LoginInput{Username: "876543", Password: "password123"})
	if err != nil {
		t.Fatal(err)
	}
	if employee.User.ID != user.ID {
		t.Fatal("number update changed internal identity")
	}
	ctx, err := h.Service.AuthenticateContext(context.Background(), employee.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.Service.ChangePassword(ctx, auth.ChangePasswordInput{CurrentPassword: "password123", NewPassword: "changed-password"}); err != nil {
		t.Fatal(err)
	}
	got := request("PATCH", path, map[string]string{"accountNumber": "987654"}, employee.AccessToken)
	if got.Code != http.StatusForbidden {
		t.Fatalf("regular member edit=%d %s", got.Code, got.Body)
	}
	duplicate := request("POST", "/api/v1/users", map[string]string{"username": "duplicate", "password": "password123", "accountNumber": "876543"}, admin.AccessToken)
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate create=%d %s", duplicate.Code, duplicate.Body)
	}
}
