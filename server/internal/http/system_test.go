package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/linknestx/linknestx/server/internal/audit"
	"github.com/linknestx/linknestx/server/internal/auth"
	"github.com/linknestx/linknestx/server/internal/chat"
	"github.com/linknestx/linknestx/server/internal/config"
	"github.com/linknestx/linknestx/server/internal/db"
	"github.com/linknestx/linknestx/server/internal/files"
	"github.com/linknestx/linknestx/server/internal/groups"
	"github.com/linknestx/linknestx/server/internal/p2p"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Exercises the actual composed HTTP handlers and real services/storage. It is
// an in-process server system test, not a native-client or network smoke test.
func TestSystemPasswordAccountsGroupsFilesPeersAndAudit(t *testing.T) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "system.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err = db.ApplyMigrations(context.Background(), conn); err != nil {
		t.Fatal(err)
	}
	a := auth.NewAuthService(conn, "sign", "refresh")
	if _, err = a.BootstrapAdmin(context.Background(), auth.CreateUserInput{Username: "admin", Password: "initial-password"}); err != nil {
		t.Fatal(err)
	}
	gs := groups.NewGroupService(conn, a)
	cs := chat.NewChatService(conn, a, gs)
	hub := chat.NewHub(cs)
	defer hub.Close()
	store, err := files.NewChunkStore(filepath.Join(t.TempDir(), "storage"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fs := files.NewFileService(conn, a, store, "grants", cs)
	ps := p2p.NewService(conn, a, fs, config.Default())
	ph := p2p.NewHub(ps)
	defer ph.Close()
	router := NewRouterWithPeers(config.Default(), a, gs, cs, hub, fs, ps, ph)
	request := func(method, path, token string, body any, want int) *httptest.ResponseRecorder {
		t.Helper()
		var raw []byte
		switch v := body.(type) {
		case []byte:
			raw = v
		case nil:
		default:
			raw, _ = json.Marshal(v)
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		r.RemoteAddr = "192.0.2.10:1234"
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s status=%d want=%d body=%s", method, path, w.Code, want, w.Body)
		}
		return w
	}
	decode := func(w *httptest.ResponseRecorder, out any) {
		t.Helper()
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatal(err)
		}
	}
	login := func(name string) auth.Session {
		t.Helper()
		var s auth.Session
		decode(request("POST", "/api/v1/auth/login", "", map[string]string{"username": name, "password": "initial-password"}, 200), &s)
		return s
	}
	change := func(s auth.Session) {
		t.Helper()
		var u auth.User
		decode(request("POST", "/api/v1/auth/password", s.AccessToken, map[string]string{"currentPassword": "initial-password", "newPassword": "changed-password"}, 200), &u)
		if u.MustChangePassword || u.ID != s.User.ID {
			t.Fatalf("password result %+v", u)
		}
	}
	admin := login("admin")
	if !admin.User.MustChangePassword {
		t.Fatal("bootstrap password not gated")
	}
	request("GET", "/api/v1/me", admin.AccessToken, nil, 200)
	for _, path := range []string{"/api/v1/admin/users", "/api/v1/users", "/api/v1/groups", "/api/v1/files?scope=pool", "/api/v1/p2p/config", "/api/v1/ws", "/api/v1/me/devices"} {
		request("GET", path, admin.AccessToken, nil, 403)
	}
	change(admin)
	create := func(name string) auth.Session {
		t.Helper()
		request("POST", "/api/v1/users", admin.AccessToken, map[string]any{"username": name, "password": "initial-password", "roles": []string{"user"}}, 201)
		s := login(name)
		request("GET", "/api/v1/groups", s.AccessToken, nil, 403)
		change(s)
		return s
	}
	owner, member, outsider := create("owner"), create("member"), create("outsider")
	var directory []auth.DirectoryUser
	decode(request("GET", "/api/v1/users", owner.AccessToken, nil, 200), &directory)
	if len(directory) != 4 {
		t.Fatalf("directory=%+v", directory)
	}
	request("GET", "/api/v1/admin/audit", owner.AccessToken, nil, 403)
	request("GET", "/api/v1/admin/audit/export?format=json", owner.AccessToken, nil, 403)
	var g groups.Group
	decode(request("POST", "/api/v1/groups", admin.AccessToken, map[string]string{"name": "system", "ownerId": owner.User.ID}, 201), &g)
	membersPath := "/api/v1/groups/" + g.ID + "/members"
	request("POST", membersPath, owner.AccessToken, map[string]string{"userId": member.User.ID, "role": "member"}, 204)
	messages := "/api/v1/groups/" + g.ID + "/messages"
	request("POST", messages, member.AccessToken, map[string]string{"clientMessageId": "system-message", "body": "hello system"}, 201)
	request("GET", messages, owner.AccessToken, nil, 200)
	request("GET", messages, outsider.AccessToken, nil, 403)
	digest := func(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
	data := []byte("abcdefgh")
	hash := digest(data)
	upload := func(name string) files.UploadSession {
		t.Helper()
		var u files.UploadSession
		in := files.StartUploadInput{GroupID: g.ID, Name: name, Size: 8, SHA256: hash, ChunkSize: 4, Chunks: []files.Chunk{{Index: 0, Hash: digest(data[:4]), Size: 4}, {Index: 1, Hash: digest(data[4:]), Size: 4}}}
		decode(request("POST", "/api/v1/files/uploads", owner.AccessToken, in, 201), &u)
		for i, b := range [][]byte{data[:4], data[4:]} {
			request("PUT", "/api/v1/uploads/"+u.UploadID+"/chunks/"+string(rune('0'+i)), owner.AccessToken, b, 204)
		}
		request("POST", "/api/v1/uploads/"+u.UploadID+"/complete", owner.AccessToken, nil, 200)
		return u
	}
	groupFile, poolFile := upload("group.bin"), upload("pool.bin")
	manifest := func(u files.UploadSession) string { return "/api/v1/file-versions/" + u.VersionID + "/manifest" }
	request("GET", manifest(groupFile), outsider.AccessToken, nil, 403)
	request("POST", "/api/v1/files/"+poolFile.FileID+"/promote", admin.AccessToken, nil, 200)
	request("GET", manifest(poolFile), outsider.AccessToken, nil, 200)
	request("POST", "/api/v1/p2p/announce", owner.AccessToken, map[string]any{"versionId": groupFile.VersionID, "indexes": []int{0}}, 204)
	var sources p2p.SourceResult
	decode(request("GET", "/api/v1/p2p/sources?versionId="+groupFile.VersionID, member.AccessToken, nil, 200), &sources)
	if len(sources.Sources) != 1 || len(sources.Sources[0].Indexes) != 1 || sources.Sources[0].Indexes[0] != 0 {
		t.Fatalf("partial sources=%+v", sources)
	}
	var grant files.ChunkGrant
	decode(request("POST", "/api/v1/p2p/grants", member.AccessToken, map[string]string{"versionId": groupFile.VersionID, "chunkHash": digest(data[:4])}, 200), &grant)
	request("POST", "/api/v1/p2p/grants/validate", owner.AccessToken, map[string]any{"grant": grant}, 204)
	report := audit.TransferInput{TaskID: uuid.NewString(), VersionID: groupFile.VersionID, NodeID: owner.DeviceID, Action: "peer.received", Bytes: 4}
	request("POST", "/api/v1/p2p/transfers", member.AccessToken, report, 204)
	request("POST", "/api/v1/p2p/transfers", member.AccessToken, report, 204)
	request("POST", "/api/v1/p2p/transfers", outsider.AccessToken, report, 403)
	request("DELETE", membersPath+"/"+member.User.ID, owner.AccessToken, nil, 204)
	request("GET", manifest(groupFile), member.AccessToken, nil, 403)
	request("POST", "/api/v1/p2p/grants/validate", owner.AccessToken, map[string]any{"grant": grant}, 403)
	request("POST", "/api/v1/p2p/transfers", member.AccessToken, report, 403)
	request("GET", manifest(poolFile), member.AccessToken, nil, 200)
	request("POST", "/api/v1/me/devices/"+owner.DeviceID+"/revoke", member.AccessToken, map[string]any{}, 404)
	request("POST", "/api/v1/admin/devices/"+owner.DeviceID+"/revoke", admin.AccessToken, map[string]any{}, 204)
	request("GET", manifest(poolFile), owner.AccessToken, nil, 401)
	request("POST", membersPath, admin.AccessToken, map[string]string{"userId": member.User.ID, "role": "member"}, 204)
	decode(request("GET", "/api/v1/p2p/sources?versionId="+groupFile.VersionID, member.AccessToken, nil, 200), &sources)
	if len(sources.Sources) != 0 {
		t.Fatal("revoked source remained listed")
	}
	var devices []auth.Device
	decode(request("GET", "/api/v1/admin/devices", admin.AccessToken, nil, 200), &devices)
	if len(devices) != 4 {
		t.Fatalf("devices=%+v", devices)
	}
	sweep := time.Now().UTC().Add(31 * 24 * time.Hour)
	reportExpiry, err := fs.DeleteExpired(context.Background(), sweep)
	if err != nil || reportExpiry.VersionsExpired != 1 {
		t.Fatalf("expiry=%+v %v", reportExpiry, err)
	}
	request("GET", manifest(groupFile), member.AccessToken, nil, 404)
	request("GET", manifest(poolFile), outsider.AccessToken, nil, 200)
	request("GET", "/api/v1/files/"+poolFile.VersionID+"/chunks/"+digest(data[:4]), outsider.AccessToken, nil, 200)
	var page audit.Page
	decode(request("GET", "/api/v1/admin/audit?limit=2&offset=0", admin.AccessToken, nil, 200), &page)
	if len(page.Events) != 2 || page.Total <= 2 {
		t.Fatalf("audit page=%+v", page)
	}
	var full audit.Page
	w := request("GET", "/api/v1/admin/audit/export?format=json&limit=1&offset=1", admin.AccessToken, nil, 200)
	decode(w, &full)
	if len(full.Events) <= 2 || full.Total != int64(len(full.Events)) || !strings.Contains(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("full export=%+v headers=%v", full, w.Header())
	}
	decode(request("GET", "/api/v1/admin/audit?action=peer.received&limit=1", admin.AccessToken, nil, 200), &page)
	if page.Total != 1 || page.Events[0].Bytes != 4 || page.Events[0].Source != "client-reported" {
		t.Fatalf("client report=%+v", page)
	}
	for _, action := range []string{"account.created", "account.password_changed", "group.created", "group.member_removed", "device.revoked", "file.promoted", "server.sent"} {
		decode(request("GET", "/api/v1/admin/audit?action="+action, admin.AccessToken, nil, 200), &page)
		if page.Total == 0 {
			t.Fatalf("missing audit action %s", action)
		}
	}
	request("GET", "/api/v1/admin/audit/export?format=csv", admin.AccessToken, nil, 200)
	request("POST", "/api/v1/admin/users/"+outsider.User.ID+"/password", admin.AccessToken, map[string]string{"password": "reset-password"}, 204)
	request("GET", "/api/v1/me", outsider.AccessToken, nil, 401)
	var reset auth.Session
	decode(request("POST", "/api/v1/auth/login", "", map[string]string{"username": "outsider", "password": "reset-password"}, 200), &reset)
	if !reset.User.MustChangePassword {
		t.Fatal("reset password not gated")
	}
	request("GET", manifest(poolFile), reset.AccessToken, nil, 403)
	var integrity string
	if err := conn.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity=%s %v", integrity, err)
	}
	rows, err := conn.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign key violation")
	}
}
