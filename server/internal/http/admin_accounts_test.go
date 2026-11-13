package http

import (
	"encoding/json"
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
