package http

import (
	"context"
	"encoding/json"
	"strings"
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

func TestSetMemberNicknameSelfOnlyAndValidated(t *testing.T) {
	f := newSocialFixture(t)
	f.a.Database().Exec(`UPDATE profiles SET display_name='爱丽丝' WHERE user_id=?`, f.alice.User.ID)
	group := fixtureGroup(t, f)
	self := "/api/v1/groups/" + group + "/members/" + f.alice.User.ID

	var events [][2]string
	f.g.SetNicknameObserver(func(groupID, userID string) { events = append(events, [2]string{groupID, userID}) })

	// 本人可设置；观察者收到 (group, user)
	expectCode(t, f.req("PATCH", self, `{"nickname":"群里的我"}`, f.alice), 204)
	if len(events) != 1 || events[0] != [2]string{group, f.alice.User.ID} {
		t.Fatalf("observer events = %v", events)
	}

	// 成员名单生效名变为昵称
	w := f.req("GET", "/api/v1/groups/"+group+"/members", "", f.alice)
	expectCode(t, w, 200)
	var members []struct {
		UserID      string  `json:"userId"`
		Nickname    *string `json:"nickname"`
		DisplayName string  `json:"displayName"`
	}
	if err := json.NewDecoder(w.Body).Decode(&members); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range members {
		if m.UserID == f.alice.User.ID {
			found = true
			if m.Nickname == nil || *m.Nickname != "群里的我" || m.DisplayName != "群里的我" {
				t.Fatalf("nickname resolution wrong: %+v", m)
			}
		}
	}
	if !found {
		t.Fatal("alice missing from member list")
	}

	// 空串清除昵称
	expectCode(t, f.req("PATCH", self, `{"nickname":""}`, f.alice), 204)
	w = f.req("GET", "/api/v1/groups/"+group+"/members", "", f.alice)
	expectCode(t, w, 200)
	members = nil
	if err := json.NewDecoder(w.Body).Decode(&members); err != nil {
		t.Fatal(err)
	}
	for _, m := range members {
		if m.UserID == f.alice.User.ID && (m.Nickname != nil || m.DisplayName != "爱丽丝") {
			t.Fatalf("nickname not cleared: %+v", m)
		}
	}

	// 仅限本人：管理员和其他成员都不能替 alice 改
	expectCode(t, f.req("PATCH", self, `{"nickname":"越权"}`, f.admin), 403)
	expectCode(t, f.req("PATCH", self, `{"nickname":"越权"}`, f.bob), 403)
	// 校验：超长、控制字符
	expectCode(t, f.req("PATCH", self, `{"nickname":"`+strings.Repeat("长", 81)+`"}`, f.alice), 400)
	expectCode(t, f.req("PATCH", self, "{\"nickname\":\"坏\\u0000字符\"}", f.alice), 400)
	// 群不存在
	expectCode(t, f.req("PATCH", "/api/v1/groups/00000000-0000-0000-0000-000000000000/members/"+f.alice.User.ID, `{"nickname":"x"}`, f.alice), 404)
	// 非成员（无该群成员关系的账号）→ 403
	ctx, e := f.a.AuthenticateContext(context.Background(), f.admin.AccessToken)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.a.CreateUser(ctx, auth.CreateUserInput{Username: "carol", Password: "password123"}); e != nil {
		t.Fatal(e)
	}
	f.a.Database().Exec(`UPDATE users SET must_change_password=0 WHERE username='carol'`)
	carol, e := f.a.Login(context.Background(), auth.LoginInput{Username: "carol", Password: "password123"})
	if e != nil {
		t.Fatal(e)
	}
	expectCode(t, f.req("PATCH", self, `{"nickname":"外人"}`, carol), 403)
}

func TestSetMemberNicknameRejectedForDirectConversation(t *testing.T) {
	f := newSocialFixture(t)
	direct := f.direct(t)
	expectCode(t, f.req("PATCH", "/api/v1/groups/"+direct+"/members/"+f.alice.User.ID, `{"nickname":"私聊不许"}`, f.alice), 403)
}
