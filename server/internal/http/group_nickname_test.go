package http

import (
	"encoding/json"
	"testing"

	"github.com/linknestx/linknestx/server/internal/auth"
)

// fixtureGroup 建一个含 admin(创建者)/alice/bob 的群，返回群 ID。
func fixtureGroup(t *testing.T, f *socialFixture) string {
	t.Helper()
	w := f.req("POST", "/api/v1/groups", `{"name":"项目群"}`, f.admin)
	expectCode(t, w, 201)
	var g struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(w.Body).Decode(&g); err != nil || g.ID == "" {
		t.Fatalf("group not created: %d %s err=%v", w.Code, w.Body.String(), err)
	}
	for _, s := range []auth.Session{f.alice, f.bob} {
		w = f.req("POST", "/api/v1/groups/"+g.ID+"/members", `{"userId":"`+s.User.ID+`","role":"member"}`, f.admin)
		expectCode(t, w, 204)
	}
	return g.ID
}

func TestGroupMemberListCarriesNicknameFields(t *testing.T) {
	f := newSocialFixture(t)
	f.a.Database().Exec(`UPDATE profiles SET display_name='爱丽丝' WHERE user_id=?`, f.alice.User.ID)
	group := fixtureGroup(t, f)
	w := f.req("GET", "/api/v1/groups/"+group+"/members", "", f.alice)
	expectCode(t, w, 200)
	var members []map[string]any
	if err := json.NewDecoder(w.Body).Decode(&members); err != nil {
		t.Fatal(err)
	}
	var mine map[string]any
	for _, m := range members {
		if m["userId"] == f.alice.User.ID {
			mine = m
		}
	}
	if mine == nil {
		t.Fatalf("alice missing from member list: %v", members)
	}
	if mine["displayName"] != "爱丽丝" {
		t.Fatalf("effective displayName = %v, want 爱丽丝", mine["displayName"])
	}
	if v, ok := mine["nickname"].(string); ok {
		t.Fatalf("nickname = %v, want null/absent", v)
	}
	if num, _ := mine["accountNumber"].(string); num == "" {
		t.Fatalf("accountNumber missing: %v", mine)
	}
	if mine["hasAvatar"] != false {
		t.Fatalf("hasAvatar = %v, want false", mine["hasAvatar"])
	}
	if mine["disabled"] != false {
		t.Fatalf("disabled = %v, want false", mine["disabled"])
	}
}
