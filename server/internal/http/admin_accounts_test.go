package http

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/linknestx/linknestx/server/internal/auth"
)

func TestAdminUpdateUsernameValidationMatrix(t *testing.T) {
	f := newSocialFixture(t)
	path := "/api/v1/admin/users/" + f.alice.User.ID + "/username"

	// Success: rename + audit + session stays valid.
	w := f.req("PATCH", path, `{"username":"alice-new"}`, f.admin)
	expectCode(t, w, 200)
	var user auth.User
	if e := json.Unmarshal(w.Body.Bytes(), &user); e != nil || user.Username != "alice-new" {
		t.Fatalf("rename response=%s err=%v", w.Body, e)
	}
	expectCode(t, f.req("GET", "/api/v1/profiles/"+f.alice.User.ID, "", f.alice), 200)
	var audit int
	if e := f.a.Database().QueryRow(`SELECT count(*) FROM audit_events WHERE action='account.username_changed' AND target_id=?`, f.alice.User.ID).Scan(&audit); e != nil || audit != 1 {
		t.Fatalf("audit=%d err=%v", audit, e)
	}

	// 400: empty / oversized / whitespace-only.
	expectCode(t, f.req("PATCH", path, `{"username":""}`, f.admin), 400)
	expectCode(t, f.req("PATCH", path, `{"username":"`+strings.Repeat("a", 257)+`"}`, f.admin), 400)
	expectCode(t, f.req("PATCH", path, `{"username":"   "}`, f.admin), 400)

	// 409: case-insensitive duplicate (bob -> ALICE-NEW).
	expectCode(t, f.req("PATCH", "/api/v1/admin/users/"+f.bob.User.ID+"/username", `{"username":"Alice-New"}`, f.admin), 409)
	// 409: collides with an existing numeric account.
	expectCode(t, f.req("PATCH", "/api/v1/admin/users/"+f.bob.User.ID+"/username", `{"username":"`+f.admin.User.AccountNumber+`"}`, f.admin), 409)
	// 403: non-admin caller.
	expectCode(t, f.req("PATCH", "/api/v1/admin/users/"+f.bob.User.ID+"/username", `{"username":"bob2"}`, f.alice), 403)
	// 404: target does not exist.
	expectCode(t, f.req("PATCH", "/api/v1/admin/users/"+uuid.NewString()+"/username", `{"username":"ghost"}`, f.admin), 404)

	// Administrators may rename themselves.
	expectCode(t, f.req("PATCH", "/api/v1/admin/users/"+f.admin.User.ID+"/username", `{"username":"root-admin"}`, f.admin), 200)
	// Restore, keeping later fixtures and audit counts predictable.
	expectCode(t, f.req("PATCH", "/api/v1/admin/users/"+f.admin.User.ID+"/username", `{"username":"admin"}`, f.admin), 200)
	// Renaming to the current name is an idempotent success without audit rows.
	before := 0
	if e := f.a.Database().QueryRow(`SELECT count(*) FROM audit_events WHERE action='account.username_changed' AND target_id=?`, f.admin.User.ID).Scan(&before); e != nil {
		t.Fatal(e)
	}
	expectCode(t, f.req("PATCH", "/api/v1/admin/users/"+f.admin.User.ID+"/username", `{"username":"admin"}`, f.admin), 200)
	after := 0
	if e := f.a.Database().QueryRow(`SELECT count(*) FROM audit_events WHERE action='account.username_changed' AND target_id=?`, f.admin.User.ID).Scan(&after); e != nil {
		t.Fatal(e)
	}
	if after != before {
		t.Fatalf("no-op rename wrote audit rows: %d -> %d", before, after)
	}
}

func TestAdminUpdateUsernameBroadcastsProfileChanged(t *testing.T) {
	f := newSocialFixture(t)
	// social.New inside the fixture router already wires profileChanged to the
	// hub (see the NewRouterWithServices composition path); after a rename the
	// peer's conversation list reflects the new name.
	f.direct(t)
	expectCode(t, f.req("PATCH", "/api/v1/admin/users/"+f.alice.User.ID+"/username", `{"username":"alice-renamed"}`, f.admin), 200)
	w := f.req("GET", "/api/v1/conversations", "", f.bob)
	expectCode(t, w, 200)
	if !strings.Contains(w.Body.String(), "alice-renamed") {
		t.Fatalf("peer conversation list does not reflect rename: %s", w.Body)
	}
}

func TestAdminDeleteUserAnonymizesAndCleansUp(t *testing.T) {
	f := newSocialFixture(t)
	// Admin-owned group with a message from alice.
	group := fixtureGroup(t, f)
	w := f.req("POST", "/api/v1/conversations/"+group+"/messages", `{"body":"hello","clientMessageId":"del-1"}`, f.alice)
	expectCode(t, w, 201)

	// A group created by alice (group creation is admin-only, so the owned
	// row is seeded directly), with bob as a member and a file record.
	db := f.a.Database()
	var owned string
	if e := db.QueryRow(`INSERT INTO groups(id,name,created_by,created_at) VALUES(lower(hex(randomblob(4))||'-'||hex(randomblob(2))||'-4'||substr(hex(randomblob(2)),2)||'-a'||substr(hex(randomblob(2)),2)||'-'||substr(hex(randomblob(6)),2)),'爱丽丝的项目群',?,'2026-01-01') RETURNING id`, f.alice.User.ID).Scan(&owned); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec(`INSERT INTO group_members(group_id,user_id,role,joined_at) VALUES(?,?,'owner','2026-01-01'),(?,?,'member','2026-01-01')`, owned, f.alice.User.ID, owned, f.bob.User.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec(`INSERT INTO files(id,group_id,name,created_by,visibility,created_at) VALUES('file-owned',?, '报告.pdf',?, 'group','2026-01-01')`, owned, f.alice.User.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec(`INSERT INTO file_versions(id,file_id,created_by,name,size,sha256,chunk_size,status,created_at) VALUES('version-owned','file-owned',?, '报告.pdf',4,'hash',4,'published','2026-01-01')`, f.alice.User.ID); e != nil {
		t.Fatal(e)
	}
	// A message inside alice's group (dissolution clears it).
	expectCode(t, f.req("POST", "/api/v1/conversations/"+owned+"/messages", `{"body":"群主留言","clientMessageId":"del-2"}`, f.alice), 201)

	// Friends + direct conversation.
	directID := f.direct(t)

	// Delete.
	expectCode(t, f.req("POST", "/api/v1/admin/users/"+f.alice.User.ID+"/delete", "{}", f.admin), 204)

	// History anonymisation: alice's messages in the admin group show the
	// tombstone identity.
	w = f.req("GET", "/api/v1/conversations/"+group+"/messages?limit=50", "", f.bob)
	expectCode(t, w, 200)
	if !strings.Contains(w.Body.String(), "已注销用户") || !strings.Contains(w.Body.String(), "deleted-user") {
		t.Fatalf("history not anonymised: %s", w.Body)
	}
	var n int
	if e := db.QueryRow(`SELECT count(*) FROM messages WHERE sender_id=?`, authDeletedUserID(t, f)).Scan(&n); e != nil || n == 0 {
		t.Fatalf("tombstone attribution=%d err=%v", n, e)
	}

	// Group dissolution: members and messages cleared, groups row retained,
	// file records attributed to the tombstone.
	if e := db.QueryRow(`SELECT count(*) FROM group_members WHERE group_id=?`, owned).Scan(&n); e != nil || n != 0 {
		t.Fatalf("dissolved members=%d err=%v", n, e)
	}
	if e := db.QueryRow(`SELECT count(*) FROM messages WHERE group_id=?`, owned).Scan(&n); e != nil || n != 0 {
		t.Fatalf("dissolved messages=%d err=%v", n, e)
	}
	if e := db.QueryRow(`SELECT count(*) FROM groups WHERE id=?`, owned).Scan(&n); e != nil || n != 1 {
		t.Fatalf("dissolved group row missing=%d err=%v", n, e)
	}
	if e := db.QueryRow(`SELECT count(*) FROM files WHERE id='file-owned' AND created_by=?`, authDeletedUserID(t, f)).Scan(&n); e != nil || n != 1 {
		t.Fatalf("dissolved group file retention=%d err=%v", n, e)
	}
	// Bob's conversation list: the dissolved group and direct chat both vanish.
	w = f.req("GET", "/api/v1/conversations", "", f.bob)
	expectCode(t, w, 200)
	if strings.Contains(w.Body.String(), owned) || strings.Contains(w.Body.String(), directID) {
		t.Fatalf("peer still sees removed conversations: %s", w.Body)
	}
	// Friendships cleaned both ways.
	if e := db.QueryRow(`SELECT count(*) FROM friendships WHERE ? IN(pair_low,pair_high)`, f.alice.User.ID).Scan(&n); e != nil || n != 0 {
		t.Fatalf("friendships=%d err=%v", n, e)
	}
	// The users row is truly gone; cascades removed devices, sessions, roles
	// and read receipts.
	if e := db.QueryRow(`SELECT count(*) FROM users WHERE id=?`, f.alice.User.ID).Scan(&n); e != nil || n != 0 {
		t.Fatalf("user row survived=%d err=%v", n, e)
	}
	// Audit trail.
	if e := db.QueryRow(`SELECT count(*) FROM audit_events WHERE action='account.deleted' AND target_id=?`, f.alice.User.ID).Scan(&n); e != nil || n != 1 {
		t.Fatalf("audit=%d err=%v", n, e)
	}
	// The tombstone account cannot log in (disabled).
	if _, e := f.a.Login(context.Background(), auth.LoginInput{Username: "deleted-user", Password: "whatever123"}); !errors.Is(e, auth.ErrDisabledAccount) {
		t.Fatalf("tombstone login=%v", e)
	}
}

// authDeletedUserID reads the tombstone constant through the auth package to
// keep this test independent of the literal.
func authDeletedUserID(t *testing.T, f *socialFixture) string {
	t.Helper()
	var id string
	if e := f.a.Database().QueryRow(`SELECT id FROM users WHERE username='deleted-user'`).Scan(&id); e != nil {
		t.Fatal(e)
	}
	return id
}

func TestAdminDeleteUserGuards(t *testing.T) {
	f := newSocialFixture(t)
	// Non-admin 403.
	expectCode(t, f.req("POST", "/api/v1/admin/users/"+f.bob.User.ID+"/delete", "{}", f.alice), 403)
	// Unknown target 404.
	expectCode(t, f.req("POST", "/api/v1/admin/users/"+uuid.NewString()+"/delete", "{}", f.admin), 404)
	// Cannot delete yourself 409.
	expectCode(t, f.req("POST", "/api/v1/admin/users/"+f.admin.User.ID+"/delete", "{}", f.admin), 409)
	// Tombstone row 400.
	fixtureGroup(t, f)
	f.a.Database().Exec(`INSERT INTO users(id,username,email,password_hash,disabled,must_change_password,created_at,updated_at) VALUES('e59ec6d2-8d67-4f14-9a1c-2b6d0f5a4c31','deleted-user','','',1,1,'2026-01-01','2026-01-01')`)
	var tombstone string
	f.a.Database().QueryRow(`SELECT id FROM users WHERE username='deleted-user'`).Scan(&tombstone)
	expectCode(t, f.req("POST", "/api/v1/admin/users/"+tombstone+"/delete", "{}", f.admin), 400)
	// Cannot delete another administrator 409.
	ctx, e := f.a.AuthenticateContext(context.Background(), f.admin.AccessToken)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.a.CreateUser(ctx, auth.CreateUserInput{Username: "second-admin", Password: "password123", Roles: []string{"admin"}}); e != nil {
		t.Fatal(e)
	}
	var secondID string
	f.a.Database().QueryRow(`SELECT id FROM users WHERE username='second-admin'`).Scan(&secondID)
	expectCode(t, f.req("POST", "/api/v1/admin/users/"+secondID+"/delete", "{}", f.admin), 409)
	// Deleting the same user twice: the second call finds nothing, 404.
	expectCode(t, f.req("POST", "/api/v1/admin/users/"+f.bob.User.ID+"/delete", "{}", f.admin), 204)
	expectCode(t, f.req("POST", "/api/v1/admin/users/"+f.bob.User.ID+"/delete", "{}", f.admin), 404)
}
