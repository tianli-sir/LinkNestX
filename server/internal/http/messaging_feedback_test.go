package http

import (
	"context"
	"encoding/json"
	"github.com/linknestx/linknestx/server/internal/chat"
	"github.com/linknestx/linknestx/server/internal/groups"
	"testing"
	"time"
)

func TestReadersHTTPReturnsFlatProfilesAndReadTimesWithoutSenderOrOutsiders(t *testing.T) {
	f := newSocialFixture(t)
	id := f.direct(t)
	path := "/api/v1/conversations/" + id
	if _, err := f.a.Database().Exec("UPDATE profiles SET display_name='真实读者',avatar=x'0102' WHERE user_id=?", f.bob.User.ID); err != nil {
		t.Fatal(err)
	}
	sent := f.req("POST", path+"/messages", `{"clientMessageId":"readers-ui","body":"check readers"}`, f.alice)
	expectCode(t, sent, 201)
	var m chat.Message
	if err := json.Unmarshal(sent.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	body := `{"messageIds":["` + m.ID + `"]}`
	expectCode(t, f.req("POST", path+"/read", body, f.bob), 200)
	expectCode(t, f.req("POST", path+"/read", body, f.alice), 200)
	result := f.req("GET", path+"/messages/"+m.ID+"/readers", "", f.alice)
	expectCode(t, result, 200)
	var payload struct {
		Count   int `json:"count"`
		Readers []struct {
			ID     string `json:"id"`
			Name   string `json:"displayName"`
			Number string `json:"accountNumber"`
			Avatar string `json:"avatarUrl"`
			ReadAt string `json:"readAt"`
		} `json:"readers"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Count != 1 || len(payload.Readers) != 1 {
		t.Fatalf("readers payload %s", result.Body)
	}
	r := payload.Readers[0]
	if r.ID != f.bob.User.ID || r.Name != "真实读者" || r.Number != f.bob.User.AccountNumber || r.Avatar != "/api/v1/profiles/"+f.bob.User.ID+"/avatar" {
		t.Fatalf("profile shape %+v", r)
	}
	if _, err := time.Parse(time.RFC3339Nano, r.ReadAt); err != nil {
		t.Fatalf("readAt %q: %v", r.ReadAt, err)
	}
	expectCode(t, f.req("GET", path+"/messages/"+m.ID+"/readers", "", f.admin), 403)
	ctx, _ := f.a.AuthenticateContext(context.Background(), f.admin.AccessToken)
	other, err := f.g.Create(ctx, f.admin.User.ID, groups.CreateInput{Name: "other", OwnerID: f.alice.User.ID})
	if err != nil {
		t.Fatal(err)
	}
	expectCode(t, f.req("GET", "/api/v1/conversations/"+other.ID+"/messages/"+m.ID+"/readers", "", f.alice), 400)
}

func TestSystemMessagesPersistThroughRESTAndCannotBeForged(t *testing.T) {
	f := newSocialFixture(t)
	created := f.req("POST", "/api/v1/groups", `{"name":"持久系统消息","ownerId":"`+f.alice.User.ID+`"}`, f.admin)
	expectCode(t, created, 201)
	var group groups.Group
	if err := json.Unmarshal(created.Body.Bytes(), &group); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/conversations/" + group.ID
	expectCode(t, f.req("POST", "/api/v1/groups/"+group.ID+"/members", `{"userId":"`+f.bob.User.ID+`","role":"member"}`, f.alice), 204)
	pageResponse := f.req("GET", path+"/messages", "", f.bob)
	expectCode(t, pageResponse, 200)
	var page chat.MessagePage
	if err := json.Unmarshal(pageResponse.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 2 || page.Messages[0].Kind != "system" || page.Messages[0].SystemType != "group.created" || page.Messages[1].SystemType != "group.member_added" {
		t.Fatalf("persisted notices %s", pageResponse.Body)
	}
	for _, m := range page.Messages {
		if m.Body == nil || *m.Body == "" || m.Cursor == "" || m.ConversationID != group.ID {
			t.Fatalf("incomplete notice %+v", m)
		}
	}
	expectCode(t, f.req("POST", path+"/messages", `{"clientMessageId":"forge","body":"fake","kind":"system","systemType":"group.created"}`, f.alice), 400)
	expectCode(t, f.req("POST", path+"/messages", `{"clientMessageId":"`+page.Messages[1].ClientMessageID+`","body":"changed system"}`, f.alice), 409)
	expectCode(t, f.req("DELETE", "/api/v1/groups/"+group.ID+"/members/"+f.bob.User.ID, "", f.alice), 204)
	expectCode(t, f.req("GET", path+"/messages", "", f.bob), 403)
	expectCode(t, f.req("GET", path+"/messages", "", f.admin), 403)
	pageResponse = f.req("GET", path+"/messages", "", f.alice)
	expectCode(t, pageResponse, 200)
	if err := json.Unmarshal(pageResponse.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 3 || page.Messages[2].SystemType != "group.member_removed" {
		t.Fatalf("removal notice %s", pageResponse.Body)
	}
}
