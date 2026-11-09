package http

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linknestx/linknestx/server/internal/auth"
	"github.com/linknestx/linknestx/server/internal/chat"
	"github.com/linknestx/linknestx/server/internal/config"
	"github.com/linknestx/linknestx/server/internal/db"
	"github.com/linknestx/linknestx/server/internal/groups"
)

func TestCollaborationRESTComposition(t *testing.T) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "rest.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := db.ApplyMigrations(context.Background(), conn); err != nil {
		t.Fatal(err)
	}
	a := auth.NewAuthService(conn, "access", "refresh")
	if _, err := a.BootstrapAdmin(context.Background(), auth.CreateUserInput{Username: "admin", Password: "password123"}); err != nil {
		t.Fatal(err)
	}
	session, err := a.Login(context.Background(), auth.LoginInput{Username: "admin", Password: "password123"})
	if err != nil {
		t.Fatal(err)
	}
	gs := groups.NewGroupService(conn, a)
	cs := chat.NewChatService(conn, a, gs)
	hub := chat.NewHub(cs)
	defer hub.Close()
	router := NewRouterWithServices(config.Default(), a, gs, cs, hub)
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
	if w := request("POST", "/api/v1/groups", `{"name":"team"}`, ""); w.Code != 401 {
		t.Fatalf("anonymous group create=%d", w.Code)
	}
	created := request("POST", "/api/v1/groups", `{"name":"team"}`, session.AccessToken)
	if created.Code != 201 {
		t.Fatalf("create=%d %s", created.Code, created.Body)
	}
	var g groups.Group
	if err := json.Unmarshal(created.Body.Bytes(), &g); err != nil {
		t.Fatal(err)
	}
	if w := request("GET", "/api/v1/groups", "", session.AccessToken); w.Code != 200 || !strings.Contains(w.Body.String(), g.ID) {
		t.Fatalf("list=%d %s", w.Code, w.Body)
	}
	user := request("POST", "/api/v1/users", `{"username":"member","password":"password123"}`, session.AccessToken)
	if user.Code != 201 {
		t.Fatalf("create member=%d %s", user.Code, user.Body)
	}
	var u auth.User
	json.Unmarshal(user.Body.Bytes(), &u)
	if w := request("POST", "/api/v1/groups/"+g.ID+"/members", `{"userId":"`+u.ID+`","role":"member"}`, session.AccessToken); w.Code != 204 {
		t.Fatalf("add=%d %s", w.Code, w.Body)
	}
	if w := request("GET", "/api/v1/groups/"+g.ID+"/members", "", session.AccessToken); w.Code != 200 || !strings.Contains(w.Body.String(), u.ID) {
		t.Fatalf("members=%d %s", w.Code, w.Body)
	}
	path := "/api/v1/groups/" + g.ID + "/messages"
	sent := request("POST", path, `{"clientMessageId":"rest-1","body":"hello"}`, session.AccessToken)
	if sent.Code != 201 {
		t.Fatalf("append=%d %s", sent.Code, sent.Body)
	}
	if w := request("POST", path, `{"clientMessageId":"rest-1","body":"changed"}`, session.AccessToken); w.Code != 409 {
		t.Fatalf("retry conflict=%d %s", w.Code, w.Body)
	}
	if w := request("POST", path, `{"groupId":"different","clientMessageId":"rest-2","body":"hello"}`, session.AccessToken); w.Code != 400 {
		t.Fatalf("path spoof=%d %s", w.Code, w.Body)
	}
	if w := request("GET", path+"?limit=bad", "", session.AccessToken); w.Code != 400 {
		t.Fatalf("invalid limit=%d %s", w.Code, w.Body)
	}
	if w := request("GET", path, "", session.AccessToken); w.Code != 200 || !strings.Contains(w.Body.String(), "hello") {
		t.Fatalf("history=%d %s", w.Code, w.Body)
	}
	if w := request("DELETE", "/api/v1/groups/"+g.ID+"/members/"+u.ID, "", session.AccessToken); w.Code != 204 {
		t.Fatalf("remove=%d %s", w.Code, w.Body)
	}
	if w := request("GET", "/api/v1/ws", "", ""); w.Code != 401 {
		t.Fatalf("anonymous socket=%d", w.Code)
	}
	if w := request("GET", "/api/v1/ws", "", session.AccessToken); w.Code != 400 {
		t.Fatalf("nonupgrade socket=%d", w.Code)
	}
}
