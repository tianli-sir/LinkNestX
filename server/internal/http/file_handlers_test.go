package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
)

func TestFileRoutesUploadPublishPromoteDownload(t *testing.T) {
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
	admin, e := a.Login(context.Background(), auth.LoginInput{Username: "admin", Password: "password123"})
	if e != nil {
		t.Fatal(e)
	}
	ctx, e := a.AuthenticateContext(context.Background(), admin.AccessToken)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = a.CreateUser(ctx, auth.CreateUserInput{Username: "other", Password: "password123"}); e != nil {
		t.Fatal(e)
	}
	other, e := a.Login(context.Background(), auth.LoginInput{Username: "other", Password: "password123"})
	if e != nil {
		t.Fatal(e)
	}
	gs := groups.NewGroupService(conn, a)
	g, e := gs.Create(ctx, admin.User.ID, groups.CreateInput{Name: "group"})
	if e != nil {
		t.Fatal(e)
	}
	cs := chat.NewChatService(conn, a, gs)
	hub := chat.NewHub(cs)
	defer hub.Close()
	store, e := files.NewChunkStore(filepath.Join(t.TempDir(), "store"))
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	fs := files.NewFileService(conn, a, store, "grant", cs)
	router := NewRouterWithServices(config.Default(), a, gs, cs, hub, fs)
	request := func(method, path, token string, body []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	if w := request("GET", "/api/v1/files?scope=pool", "", nil); w.Code != 401 {
		t.Fatalf("anonymous %d", w.Code)
	}
	data := bytes.Repeat([]byte("binary\x00data\n"), 10000)
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	body, _ := json.Marshal(files.StartUploadInput{GroupID: g.ID, Name: "report.dat", Size: int64(len(data)), SHA256: hash, Chunks: []files.Chunk{{Index: 0, Hash: hash, Size: int64(len(data))}}})
	w := request("POST", "/api/v1/files/uploads", admin.AccessToken, body)
	if w.Code != 201 {
		t.Fatalf("start %d %s", w.Code, w.Body)
	}
	var u files.UploadSession
	if e = json.Unmarshal(w.Body.Bytes(), &u); e != nil {
		t.Fatal(e)
	}
	if w = request("GET", "/api/v1/files?groupId="+g.ID, admin.AccessToken, nil); w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("pending list %d %s", w.Code, w.Body)
	}
	path := "/api/v1/uploads/" + u.UploadID
	if w = request("POST", path+"/complete", admin.AccessToken, nil); w.Code != 409 {
		t.Fatalf("incomplete %d %s", w.Code, w.Body)
	}
	if w = request("PUT", path+"/chunks/0", admin.AccessToken, data); w.Code != 204 {
		t.Fatalf("put %d %s", w.Code, w.Body)
	}
	for i := 0; i < 2; i++ {
		if w = request("POST", path+"/complete", admin.AccessToken, nil); w.Code != 200 {
			t.Fatalf("complete %d %s", w.Code, w.Body)
		}
	}
	if w = request("GET", path, admin.AccessToken, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"completed":true`) {
		t.Fatalf("resume %d %s", w.Code, w.Body)
	}
	if w = request("GET", "/api/v1/files/"+u.VersionID+"/chunks/"+hash, other.AccessToken, nil); w.Code != 403 {
		t.Fatalf("outsider %d", w.Code)
	}
	if w = request("POST", "/api/v1/files/"+u.FileID+"/promote", admin.AccessToken, nil); w.Code != 200 {
		t.Fatalf("promote %d %s", w.Code, w.Body)
	}
	if w = request("GET", "/api/v1/files?scope=pool", other.AccessToken, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"createdBy":"`+admin.User.ID+`"`) {
		t.Fatalf("pool %d %s", w.Code, w.Body)
	}
	if w = request("GET", "/api/v1/files/"+u.VersionID+"/chunks/"+hash, other.AccessToken, nil); w.Code != 200 || !bytes.Equal(w.Body.Bytes(), data) || w.Header().Get("Content-Length") == "" {
		t.Fatalf("bytes %d len%d", w.Code, w.Body.Len())
	}
	if w = request("GET", "/api/v1/groups/"+g.ID+"/messages", admin.AccessToken, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"fileVersionId":"`+u.VersionID+`"`) {
		t.Fatalf("attachment %d %s", w.Code, w.Body)
	}
	if w = request("DELETE", "/api/v1/files/"+u.FileID, admin.AccessToken, nil); w.Code != 204 {
		t.Fatalf("delete %d", w.Code)
	}
	if w = request("GET", "/api/v1/files/"+u.VersionID+"/chunks/"+hash, other.AccessToken, nil); w.Code != 404 {
		t.Fatalf("deleted %d", w.Code)
	}
	if w = request("GET", "/api/v1/groups/"+g.ID+"/messages", admin.AccessToken, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"attachmentStatus":"deleted"`) {
		t.Fatalf("attachment tombstone %d %s", w.Code, w.Body)
	}
	var received, sent int64
	if e = conn.QueryRow(`SELECT sum(bytes) FROM transfer_records WHERE action='server.received'`).Scan(&received); e != nil {
		t.Fatal(e)
	}
	if e = conn.QueryRow(`SELECT sum(bytes) FROM transfer_records WHERE action='server.sent'`).Scan(&sent); e != nil {
		t.Fatal(e)
	}
	if received != int64(len(data)) || sent != int64(len(data)) {
		t.Fatalf("audit received%d sent%d", received, sent)
	}
}
