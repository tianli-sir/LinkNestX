package chat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/linknestx/linknestx/server/internal/auth"
)

func socket(t *testing.T, server *httptest.Server, s auth.Session) *websocket.Conn {
	t.Helper()
	h := http.Header{}
	h.Set("Authorization", "Bearer "+s.AccessToken)
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	readEvent(t, c, "session.ready", "")
	return c
}
func readEvent(t *testing.T, c *websocket.Conn, kind, request string) Event {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	for i := 0; i < 30; i++ {
		var event Event
		if err := c.ReadJSON(&event); err != nil {
			t.Fatal(err)
		}
		if event.Type == kind && (request == "" || event.RequestID == request) {
			return event
		}
	}
	t.Fatalf("no %s event", kind)
	return Event{}
}
func socketServer(t *testing.T, f fixture) (*Hub, *httptest.Server) {
	t.Helper()
	hub := NewHub(f.service)
	t.Cleanup(hub.Close)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		ctx, err := f.auth.AuthenticateContext(r.Context(), token)
		if err != nil {
			http.Error(w, "unauthorized", 401)
			return
		}
		hub.ServeHTTP(w, r.WithContext(ctx))
	}))
	t.Cleanup(server.Close)
	return hub, server
}
func TestWebSocketMessagesACKPresenceAndRemoval(t *testing.T) {
	f := chatFixture(t)
	member, memberCtx := f.user(t, "member")
	outsider, _ := f.user(t, "outsider")
	if err := f.groups.AddMember(f.ctx, f.session.User.ID, f.group.ID, member.User.ID, "member"); err != nil {
		t.Fatal(err)
	}
	_, server := socketServer(t, f)
	ownerConn := socket(t, server, f.session)
	readEvent(t, ownerConn, "presence.changed", "")
	outsiderConn := socket(t, server, outsider)
	memberConn := socket(t, server, member)
	presence := readEvent(t, ownerConn, "presence.changed", "")
	if presence.GroupID != f.group.ID || !presence.Online || presence.UserID != member.User.ID {
		t.Fatalf("presence=%+v", presence)
	}
	request := ClientEvent{Type: "message.send", RequestID: "r1", GroupID: f.group.ID, ClientMessageID: "client-1", Body: "online"}
	if err := memberConn.WriteJSON(request); err != nil {
		t.Fatal(err)
	}
	ack := readEvent(t, memberConn, "message.ack", "r1")
	created := readEvent(t, ownerConn, "message.created", "")
	if ack.Message == nil || created.Message == nil || ack.Message.ID != created.Message.ID || ack.ClientMessageID != "client-1" {
		t.Fatalf("ack=%+v created=%+v", ack, created)
	}
	if err := outsiderConn.WriteJSON(ClientEvent{Type: "ping", RequestID: "barrier"}); err != nil {
		t.Fatal(err)
	}
	outsiderConn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var barrier Event
	if err := outsiderConn.ReadJSON(&barrier); err != nil {
		t.Fatal(err)
	}
	if barrier.Type != "pong" {
		t.Fatalf("outsider received private event: %+v", barrier)
	}
	if err := memberConn.WriteJSON(request); err != nil {
		t.Fatal(err)
	}
	again := readEvent(t, memberConn, "message.ack", "r1")
	if again.Message.ID != ack.Message.ID {
		t.Fatal("retry created a duplicate")
	}
	if err := f.groups.RemoveMember(f.ctx, f.session.User.ID, f.group.ID, member.User.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Append(f.ctx, f.session.User.ID, AppendInput{GroupID: f.group.ID, ClientMessageID: "after-removal", Body: "private"}); err != nil {
		t.Fatal(err)
	}
	if err := memberConn.WriteJSON(ClientEvent{Type: "message.send", RequestID: "denied", GroupID: f.group.ID, ClientMessageID: "no", Body: "removed"}); err != nil {
		t.Fatal(err)
	}
	memberConn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var denial Event
	if err := memberConn.ReadJSON(&denial); err != nil {
		t.Fatal(err)
	}
	if denial.Type != "error" || denial.Code != "forbidden" {
		t.Fatalf("removed member received %+v", denial)
	}
	if _, err := f.service.List(memberCtx, member.User.ID, f.group.ID, "", 10); err == nil {
		t.Fatal("removed member read history")
	}
}
func TestWebSocketRevokedSessionCannotReceiveOrSend(t *testing.T) {
	f := chatFixture(t)
	member, _ := f.user(t, "member")
	if err := f.groups.AddMember(f.ctx, f.session.User.ID, f.group.ID, member.User.ID, "member"); err != nil {
		t.Fatal(err)
	}
	_, server := socketServer(t, f)
	c := socket(t, server, member)
	// Drain join presence before revocation.
	readEvent(t, c, "presence.changed", "")
	if err := f.auth.RevokeSession(context.Background(), member.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Append(f.ctx, f.session.User.ID, AppendInput{GroupID: f.group.ID, ClientMessageID: "secret", Body: "hidden"}); err != nil {
		t.Fatal(err)
	}
	_ = c.WriteJSON(ClientEvent{Type: "message.send", RequestID: "revoked", GroupID: f.group.ID, ClientMessageID: "revoked", Body: "invalid"})
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	var e Event
	if err := c.ReadJSON(&e); err == nil {
		t.Fatalf("revoked session received %+v", e)
	}
	page, err := f.service.List(f.ctx, f.session.User.ID, f.group.ID, "", 100)
	if err != nil || len(page.Messages) != 1 {
		t.Fatalf("revoked send persisted: %+v err=%v", page, err)
	}
}

func TestWebSocketMembershipChangesAndOfflineCatchup(t *testing.T) {
	f := chatFixture(t)
	member, _ := f.user(t, "late-member")
	var clock atomic.Int64
	clock.Store(time.Now().Unix())
	f.auth.SetClock(func() time.Time { return time.Unix(clock.Load(), 0) })
	_, server := socketServer(t, f)
	c := socket(t, server, member)
	// Existing sockets must begin receiving after membership is granted.
	if err := f.groups.AddMember(f.ctx, f.session.User.ID, f.group.ID, member.User.ID, "member"); err != nil {
		t.Fatal(err)
	}
	first, err := f.service.Append(f.ctx, f.session.User.ID, AppendInput{GroupID: f.group.ID, ClientMessageID: "live", Body: "live"})
	if err != nil {
		t.Fatal(err)
	}
	live := readEvent(t, c, "message.created", "")
	if live.Message == nil || live.Message.ID != first.ID {
		t.Fatalf("live=%+v", live)
	}
	c.Close()
	offline, err := f.service.Append(f.ctx, f.session.User.ID, AppendInput{GroupID: f.group.ID, ClientMessageID: "offline", Body: "offline"})
	if err != nil {
		t.Fatal(err)
	}
	reconnected := socket(t, server, member)
	ctx, err := f.auth.AuthenticateContext(context.Background(), member.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	page, err := f.service.List(ctx, member.User.ID, f.group.ID, live.Message.Cursor, 100)
	if err != nil || len(page.Messages) != 1 || page.Messages[0].ID != offline.ID {
		t.Fatalf("catchup=%+v err=%v", page, err)
	}
	// Access token expiry closes the old connection before another event is delivered.
	clock.Add(3600)
	_ = reconnected.WriteJSON(ClientEvent{Type: "ping", RequestID: "expired"})
	reconnected.SetReadDeadline(time.Now().Add(3 * time.Second))
	for i := 0; i < 3; i++ {
		var event Event
		err := reconnected.ReadJSON(&event)
		if err != nil {
			return
		}
		if event.Type == "pong" {
			t.Fatal("expired token continued using socket")
		}
	}
	t.Fatal("expired socket remained open")
}
