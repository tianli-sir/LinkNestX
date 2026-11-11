package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/linknestx/linknestx/server/internal/auth"
	"github.com/linknestx/linknestx/server/internal/chat"
	"github.com/linknestx/linknestx/server/internal/config"
	"github.com/linknestx/linknestx/server/internal/files"
	"github.com/linknestx/linknestx/server/internal/groups"
	"github.com/linknestx/linknestx/server/internal/p2p"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type socialFixture struct {
	a                 *auth.AuthService
	g                 *groups.GroupService
	c                 *chat.ChatService
	router            http.Handler
	admin, alice, bob auth.Session
	fs                *files.FileService
}

func newSocialFixture(t *testing.T) *socialFixture {
	t.Helper()
	a := testAuthHandler(t).Service
	admin, e := a.Login(context.Background(), auth.LoginInput{Username: "admin", Password: "correct horse battery staple"})
	if e != nil {
		t.Fatal(e)
	}
	ctx, e := a.AuthenticateContext(context.Background(), admin.AccessToken)
	if e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"alice", "bob"} {
		if _, e = a.CreateUser(ctx, auth.CreateUserInput{Username: name, Password: "password123"}); e != nil {
			t.Fatal(e)
		}
	}
	a.Database().Exec(`UPDATE users SET must_change_password=0`)
	alice, e := a.Login(context.Background(), auth.LoginInput{Username: "alice", Password: "password123"})
	if e != nil {
		t.Fatal(e)
	}
	bob, e := a.Login(context.Background(), auth.LoginInput{Username: "bob", Password: "password123"})
	if e != nil {
		t.Fatal(e)
	}
	g := groups.NewGroupService(a.Database(), a)
	c := chat.NewChatService(a.Database(), a, g)
	hub := chat.NewHub(c)
	t.Cleanup(hub.Close)
	store, e := files.NewChunkStore(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { store.Close() })
	fs := files.NewFileService(a.Database(), a, store, "secret", c)
	ps := p2p.NewService(a.Database(), a, fs, config.Default())
	ph := p2p.NewHub(ps)
	t.Cleanup(ph.Close)
	return &socialFixture{a, g, c, NewRouterWithPeers(config.Default(), a, g, c, hub, fs, ps, ph), admin, alice, bob, fs}
}
func (f *socialFixture) req(method, path, body string, s auth.Session) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+s.AccessToken)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	return w
}
func expectCode(t *testing.T, w *httptest.ResponseRecorder, n int) {
	t.Helper()
	if w.Code != n {
		t.Fatalf("status=%d want=%d body=%s", w.Code, n, w.Body)
	}
}
func (f *socialFixture) direct(t *testing.T) string {
	t.Helper()
	w := f.req("POST", "/api/v1/conversations/direct", `{"userId":"`+f.bob.User.ID+`"}`, f.alice)
	expectCode(t, w, 403)
	w = f.req("POST", "/api/v1/friend-requests", `{"accountNumber":"`+f.bob.User.AccountNumber+`"}`, f.alice)
	expectCode(t, w, 201)
	var r struct {
		ID string `json:"id"`
	}
	json.Unmarshal(w.Body.Bytes(), &r)
	expectCode(t, f.req("POST", "/api/v1/friend-requests/"+r.ID+"/accept", "{}", f.alice), 403)
	expectCode(t, f.req("POST", "/api/v1/friend-requests/"+r.ID+"/accept", "{}", f.bob), 200)
	w = f.req("POST", "/api/v1/conversations/direct", `{"userId":"`+f.bob.User.ID+`"}`, f.alice)
	expectCode(t, w, 200)
	json.Unmarshal(w.Body.Bytes(), &r)
	return r.ID
}
func TestSocialFriendDirectIsolationAndExactReads(t *testing.T) {
	f := newSocialFixture(t)
	id := f.direct(t)
	path := "/api/v1/conversations/" + id
	expectCode(t, f.req("GET", path+"/messages", "", f.admin), 403)
	expectCode(t, f.req("GET", "/api/v1/groups/"+id+"/members", "", f.admin), 403)
	expectCode(t, f.req("POST", "/api/v1/groups/"+id+"/members", `{"userId":"`+f.admin.User.ID+`","role":"owner"}`, f.admin), 403)
	w := f.req("GET", "/api/v1/groups", "", f.admin)
	expectCode(t, w, 200)
	if strings.Contains(w.Body.String(), id) {
		t.Fatal("direct disclosed to group admin")
	}
	ids := []string{}
	for i := 0; i < 3; i++ {
		w = f.req("POST", path+"/messages", fmt.Sprintf(`{"clientMessageId":"m%d","body":"hello%d"}`, i, i), f.alice)
		expectCode(t, w, 201)
		var m chat.Message
		json.Unmarshal(w.Body.Bytes(), &m)
		ids = append(ids, m.ID)
	}
	expectCode(t, f.req("POST", path+"/read", fmt.Sprintf(`{"messageIds":["%s","%s","%s"]}`, ids[0], ids[2], ids[2]), f.bob), 200)
	w = f.req("GET", "/api/v1/conversations", "", f.bob)
	expectCode(t, w, 200)
	if !strings.Contains(w.Body.String(), `"unreadCount":1`) {
		t.Fatal(w.Body)
	}
	w = f.req("GET", path+"/messages?limit=2", "", f.bob)
	expectCode(t, w, 200)
	var page chat.MessagePage
	json.Unmarshal(w.Body.Bytes(), &page)
	if len(page.Messages) != 2 || page.Messages[0].ID != ids[1] || !page.HasMore {
		t.Fatal(w.Body)
	}
	expectCode(t, f.req("POST", path+"/read", fmt.Sprintf(`{"messageIds":["%s","bad"]}`, ids[1]), f.bob), 400)
	w = f.req("GET", path+"/messages/"+ids[0]+"/readers", "", f.alice)
	expectCode(t, w, 200)
	if !strings.Contains(w.Body.String(), `"count":1`) {
		t.Fatal(w.Body)
	}
	expectCode(t, f.req("DELETE", "/api/v1/contacts/"+f.bob.User.ID, "", f.alice), 204)
	expectCode(t, f.req("POST", path+"/messages", `{"clientMessageId":"after","body":"blocked"}`, f.alice), 403)
	expectCode(t, f.req("POST", "/api/v1/groups/"+id+"/messages", `{"clientMessageId":"bypass","body":"blocked"}`, f.alice), 403)
	expectCode(t, f.req("GET", path+"/messages", "", f.alice), 200)
}

func TestDirectFilesDenyAdminManagementAndPeerSources(t *testing.T) {
	f := newSocialFixture(t)
	id := f.direct(t)
	v := f.publish(t, id, []byte("private file"))
	base := "/api/v1/files/" + v.FileID
	for _, route := range []struct{ method, path, body string }{{"GET", "/api/v1/files?groupId=" + id, ""}, {"GET", base + "/versions", ""}, {"GET", "/api/v1/file-versions/" + v.VersionID + "/manifest", ""}, {"DELETE", base, ""}, {"POST", base + "/promote", ""}, {"PUT", "/api/v1/sharing-policies/group/" + id, `{"enabled":false}`}, {"POST", "/api/v1/groups/" + id + "/messages", `{"clientMessageId":"admin","body":"no"}`}} {
		w := f.req(route.method, route.path, route.body, f.admin)
		if w.Code != 403 && w.Code != 404 {
			t.Fatalf("direct admin bypass %s %d %s", route.path, w.Code, w.Body)
		}
	}
	ctx, e := f.a.AuthenticateContext(context.Background(), f.bob.AccessToken)
	if e != nil {
		t.Fatal(e)
	}
	adminCtx, _ := f.a.AuthenticateContext(context.Background(), f.admin.AccessToken)
	ps := p2p.NewService(f.a.Database(), f.a, f.fs, config.Default())
	if _, e = ps.ListSources(adminCtx, f.admin.User.ID, v.VersionID); e == nil {
		t.Fatal("admin discovered direct sources")
	}
	// A stale/injected source still must be excluded by discovery's SQL ACL.
	_, e = f.a.Database().Exec(`INSERT INTO peer_sources(device_id,version_id,user_id,session_id,indexes_json,last_seen) VALUES(?,?,?,?,'[0]',?)`, f.admin.DeviceID, v.VersionID, f.admin.User.ID, f.admin.ID, time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z"))
	if e != nil {
		t.Fatal(e)
	}
	sources, e := ps.ListSources(ctx, f.bob.User.ID, v.VersionID)
	if e != nil {
		t.Fatal(e)
	}
	if len(sources.Sources) != 0 {
		t.Fatal("admin source escaped direct pair filter")
	}
	expectCode(t, f.req("DELETE", "/api/v1/contacts/"+f.bob.User.ID, "", f.alice), 204)
	payload, _ := json.Marshal(files.StartUploadInput{GroupID: id, Name: "no", Size: 0, SHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"})
	expectCode(t, f.req("POST", "/api/v1/files/uploads", string(payload), f.alice), 403)
	expectCode(t, f.req("GET", "/api/v1/file-versions/"+v.VersionID+"/manifest", "", f.alice), 200)
}

func TestAcceptedRequestCannotRestoreDeletedFriendship(t *testing.T) {
	f := newSocialFixture(t)
	f.direct(t)
	var id string
	if e := f.a.Database().QueryRow(`SELECT id FROM friend_requests WHERE status='accepted'`).Scan(&id); e != nil {
		t.Fatal(e)
	}
	expectCode(t, f.req("DELETE", "/api/v1/contacts/"+f.bob.User.ID, "", f.alice), 204)
	expectCode(t, f.req("POST", "/api/v1/friend-requests/"+id+"/accept", "{}", f.bob), 400)
	expectCode(t, f.req("POST", "/api/v1/conversations/direct", `{"userId":"`+f.bob.User.ID+`"}`, f.alice), 403)
}

func TestReceiptCountsFollowCurrentEntitlementAndRetention(t *testing.T) {
	f := newSocialFixture(t)
	ctx, _ := f.a.AuthenticateContext(context.Background(), f.admin.AccessToken)
	g, e := f.g.Create(ctx, f.admin.User.ID, groups.CreateInput{Name: "receipts", OwnerID: f.alice.User.ID})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.g.AddMember(ctx, f.admin.User.ID, g.ID, f.bob.User.ID, "member"); e != nil {
		t.Fatal(e)
	}
	path := "/api/v1/conversations/" + g.ID
	w := f.req("POST", path+"/messages", `{"clientMessageId":"receipt","body":"hello"}`, f.alice)
	expectCode(t, w, 201)
	var m chat.Message
	json.Unmarshal(w.Body.Bytes(), &m)
	body := `{"messageIds":["` + m.ID + `"]}`
	expectCode(t, f.req("POST", path+"/read", body, f.bob), 200)
	expectCode(t, f.req("POST", path+"/read", body, f.alice), 200)
	readers := func(want int) {
		t.Helper()
		w := f.req("GET", path+"/messages/"+m.ID+"/readers", "", f.alice)
		expectCode(t, w, 200)
		var r chat.Readers
		json.Unmarshal(w.Body.Bytes(), &r)
		if r.Count != want || len(r.Readers) != want {
			t.Fatal(w.Body)
		}
		w = f.req("GET", path+"/messages", "", f.alice)
		expectCode(t, w, 200)
		var p chat.MessagePage
		json.Unmarshal(w.Body.Bytes(), &p)
		found := false
		for _, entry := range p.Messages {
			if entry.ID == m.ID {
				found = true
				if entry.ReadCount != want {
					t.Fatal(w.Body)
				}
			}
		}
		if !found {
			t.Fatal(w.Body)
		}
	}
	readers(1)
	f.a.Database().Exec(`UPDATE users SET disabled=1 WHERE id=?`, f.bob.User.ID)
	readers(0)
	f.a.Database().Exec(`UPDATE users SET disabled=0 WHERE id=?`, f.bob.User.ID)
	readers(1)
	if e = f.g.RemoveMember(ctx, f.admin.User.ID, g.ID, f.bob.User.ID); e != nil {
		t.Fatal(e)
	}
	readers(0)
	expectCode(t, f.req("GET", path+"/messages/"+m.ID+"/readers", "", f.bob), 403)
	if e = f.g.AddMember(ctx, f.admin.User.ID, g.ID, f.bob.User.ID, "member"); e != nil {
		t.Fatal(e)
	}
	readers(1)
	f.a.Database().Exec(`UPDATE messages SET created_at='2020-01-01T00:00:00.000000000Z' WHERE id=?`, m.ID)
	if _, e = f.c.ExpireBeforeTwoYears(context.Background(), time.Now()); e != nil {
		t.Fatal(e)
	}
	var n int
	f.a.Database().QueryRow(`SELECT count(*) FROM message_reads WHERE message_id=?`, m.ID).Scan(&n)
	if n != 0 {
		t.Fatal("expired receipts retained")
	}
	expectCode(t, f.req("POST", path+"/read", body, f.bob), 400)
}

func TestProfileNormalizationNumberLoginAndFriendRequestRaces(t *testing.T) {
	f := newSocialFixture(t)
	session, e := f.a.Login(context.Background(), auth.LoginInput{Username: f.alice.User.AccountNumber, Password: "password123"})
	if e != nil || session.User.ID != f.alice.User.ID {
		t.Fatalf("number login %v", e)
	}
	w := f.req("PATCH", "/api/v1/profile", `{"displayName":"  阿丽丝  "}`, f.alice)
	expectCode(t, w, 200)
	if !strings.Contains(w.Body.String(), `"displayName":"阿丽丝"`) {
		t.Fatal(w.Body)
	}
	expectCode(t, f.req("PATCH", "/api/v1/profile", `{"displayName":"bad\nname"}`, f.alice), 400)
	img := image.NewRGBA(image.Rect(0, 0, 600, 300))
	var raw bytes.Buffer
	png.Encode(&raw, img)
	w = f.req("PUT", "/api/v1/profile/avatar", raw.String(), f.alice)
	expectCode(t, w, 200)
	w = f.req("GET", "/api/v1/profiles/"+f.alice.User.ID+"/avatar", "", f.bob)
	expectCode(t, w, 200)
	cfg, kind, e := image.DecodeConfig(w.Body)
	if e != nil || kind != "png" || cfg.Width != 256 || cfg.Height != 128 {
		t.Fatalf("avatar %v %s %v", cfg, kind, e)
	}
	expectCode(t, f.req("PUT", "/api/v1/profile/avatar", "<svg/>", f.alice), 400)
	done := make(chan *httptest.ResponseRecorder, 8)
	for i := 0; i < 8; i++ {
		go func(i int) {
			s, number := f.alice, f.bob.User.AccountNumber
			if i%2 == 1 {
				s, number = f.bob, f.alice.User.AccountNumber
			}
			done <- f.req("POST", "/api/v1/friend-requests", `{"accountNumber":"`+number+`"}`, s)
		}(i)
	}
	id := ""
	for i := 0; i < 8; i++ {
		w = <-done
		expectCode(t, w, 201)
		var r struct {
			ID string `json:"id"`
		}
		json.Unmarshal(w.Body.Bytes(), &r)
		if id != "" && id != r.ID {
			t.Fatal("duplicate/reversed pair raced")
		}
		id = r.ID
	}
	var n int
	f.a.Database().QueryRow(`SELECT count(*) FROM friend_requests WHERE status='pending'`).Scan(&n)
	if n != 1 {
		t.Fatal(n)
	}
}
