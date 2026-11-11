package chat

import (
	"github.com/gorilla/websocket"
	"github.com/linknestx/linknestx/server/internal/groups"
	"testing"
	"time"
)

func TestGroupSystemSocketDeliveryUsesCurrentMembershipAndPersistentIDs(t *testing.T) {
	f := chatFixture(t)
	member, memberCtx := f.user(t, "member")
	outsider, _ := f.user(t, "outsider")
	_, server := socketServer(t, f)
	ownerConn := socket(t, server, f.session)
	readEvent(t, ownerConn, "presence.changed", "")
	memberConn := socket(t, server, member)
	outsiderConn := socket(t, server, outsider)
	if err := f.groups.AddMember(f.ctx, f.session.User.ID, f.group.ID, member.User.ID, "member"); err != nil {
		t.Fatal(err)
	}
	for _, conn := range []*websocket.Conn{ownerConn, memberConn} {
		event := readEvent(t, conn, "message.created", "")
		if event.Message == nil || event.Message.Kind != "system" || event.Message.SystemType != "group.member_added" || event.EventID != event.Message.ID || event.ConversationID != f.group.ID {
			t.Fatalf("join event %+v", event)
		}
		var persisted int
		if err := f.db.QueryRow("SELECT count(*) FROM messages WHERE id=? AND kind='system'", event.Message.ID).Scan(&persisted); err != nil || persisted != 1 {
			t.Fatalf("not persisted %d %v", persisted, err)
		}
	}
	noMessageBeforePong := func(conn *websocket.Conn, request string) {
		t.Helper()
		if err := conn.WriteJSON(ClientEvent{Type: "ping", RequestID: request}); err != nil {
			t.Fatal(err)
		}
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		for i := 0; i < 20; i++ {
			var event Event
			if err := conn.ReadJSON(&event); err != nil {
				t.Fatal(err)
			}
			if event.Type == "message.created" {
				t.Fatalf("unauthorized system message %+v", event)
			}
			if event.Type == "pong" && event.RequestID == request {
				return
			}
		}
		t.Fatal("missing pong")
	}
	noMessageBeforePong(outsiderConn, "outsider")
	if err := f.groups.RemoveMember(f.ctx, f.session.User.ID, f.group.ID, member.User.ID); err != nil {
		t.Fatal(err)
	}
	removed := readEvent(t, ownerConn, "message.created", "")
	if removed.Message == nil || removed.Message.SystemType != "group.member_removed" {
		t.Fatalf("removal event %+v", removed)
	}
	noMessageBeforePong(memberConn, "removed")
	if _, err := f.service.ConversationPage(memberCtx, member.User.ID, f.group.ID, "", "", 50); err != groups.ErrForbidden {
		t.Fatalf("removed history %v", err)
	}
	newGroup, err := f.groups.Create(f.ctx, f.session.User.ID, groups.CreateInput{Name: "new group", OwnerID: member.User.ID})
	if err != nil {
		t.Fatal(err)
	}
	created := readEvent(t, memberConn, "message.created", "")
	if created.GroupID != newGroup.ID || created.Message == nil || created.Message.SystemType != "group.created" {
		t.Fatalf("creation event %+v", created)
	}
	noMessageBeforePong(ownerConn, "nonmember-admin")
}
