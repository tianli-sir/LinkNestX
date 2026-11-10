package http

import (
	"bytes"
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linknestx/linknestx/server/internal/auth"
	"github.com/linknestx/linknestx/server/internal/chat"
	"github.com/linknestx/linknestx/server/internal/config"
	"github.com/linknestx/linknestx/server/internal/db"
	"github.com/linknestx/linknestx/server/internal/files"
	"github.com/linknestx/linknestx/server/internal/groups"
	"github.com/linknestx/linknestx/server/internal/p2p"
)

func TestPeerRoutesAuthenticateBoundBodiesAndIsolateChat(t *testing.T) {
	conn, e := db.Open(filepath.Join(t.TempDir(), "api.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	if e = db.ApplyMigrations(context.Background(), conn); e != nil {
		t.Fatal(e)
	}
	a := auth.NewAuthService(conn, "sign", "refresh")
	if _, e = a.BootstrapAdmin(context.Background(), auth.CreateUserInput{Username: "admin", Password: "password123"}); e != nil {
		t.Fatal(e)
	}
	// This fixture models accounts whose initial password was already changed.
	if _, err := a.Database().Exec(`UPDATE users SET must_change_password=0`); err != nil {
		t.Fatal(err)
	}
	session, e := a.Login(context.Background(), auth.LoginInput{Username: "admin", Password: "password123"})
	if e != nil {
		t.Fatal(e)
	}
	gs := groups.NewGroupService(conn, a)
	cs := chat.NewChatService(conn, a, gs)
	ch := chat.NewHub(cs)
	defer ch.Close()
	store, e := files.NewChunkStore(filepath.Join(t.TempDir(), "chunks"))
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	fs := files.NewFileService(conn, a, store, "grants")
	ps := p2p.NewService(conn, a, fs, config.Default())
	ph := p2p.NewHub(ps)
	defer ph.Close()
	router := NewRouterWithPeers(config.Default(), a, gs, cs, ch, fs, ps, ph)
	request := func(method, path, token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/api/v1/p2p/config", "/api/v1/p2p/sources?versionId=bad", "/api/v1/p2p/ws"} {
		if w := request("GET", path, "", ""); w.Code != 401 {
			t.Fatalf("anonymous %s: %d", path, w.Code)
		}
	}
	if w := request("GET", "/api/v1/p2p/config", session.AccessToken, ""); w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"iceServers":[]}` {
		t.Fatalf("config %d %s", w.Code, w.Body)
	}
	for _, path := range []string{"announce", "heartbeat", "withdraw", "grants", "grants/validate"} {
		if w := request("POST", "/api/v1/p2p/"+path, session.AccessToken, `{"unexpected":"`+strings.Repeat("x", 65536)+`"}`); w.Code != 413 {
			t.Fatalf("body bound %s %d %s", path, w.Code, w.Body)
		}
	}
	if w := request("POST", "/api/v1/p2p/withdraw", session.AccessToken, `{}`); w.Code != 204 {
		t.Fatalf("withdraw %d %s", w.Code, w.Body)
	}
	if w := request("POST", "/api/v1/p2p/withdraw", session.AccessToken, `null`); w.Code != 400 {
		t.Fatalf("null body accepted %d", w.Code)
	}
	if w := request("POST", "/api/v1/p2p/announce", session.AccessToken, `{"versionId":"bad","indexes":[],"deviceId":"forged"}`); w.Code != 400 {
		t.Fatalf("spoof %d", w.Code)
	}
	if w := request("GET", "/api/v1/groups", session.AccessToken, ""); w.Code != 200 {
		t.Fatalf("existing route broken %d", w.Code)
	}
}
