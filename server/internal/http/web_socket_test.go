package http

import (
	"context"
	"github.com/gorilla/websocket"
	"github.com/linknestx/linknestx/server/internal/chat"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWebSocketSameOriginCookieAndRevocation(t *testing.T) {
	f := newSocialFixture(t)
	id := f.direct(t)
	cookie := f.web(t)
	server := httptest.NewServer(f.router)
	defer server.Close()
	socketURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v1/ws"
	headers := http.Header{}
	headers.Set("Cookie", cookie.String())
	headers.Set("Origin", "http://evil.test")
	_, response, e := websocket.DefaultDialer.Dial(socketURL, headers)
	if e == nil || response.StatusCode != 403 {
		t.Fatalf("hostile origin %v", e)
	}
	headers.Del("Origin")
	_, response, e = websocket.DefaultDialer.Dial(socketURL, headers)
	if e == nil || response.StatusCode != 403 {
		t.Fatalf("missing origin %v", e)
	}
	headers.Set("Origin", server.URL)
	conn, _, e := websocket.DefaultDialer.Dial(socketURL, headers)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var event chat.Event
	if e = conn.ReadJSON(&event); e != nil || event.Type != "session.ready" {
		t.Fatalf("session %v %v", event, e)
	}
	if e = conn.WriteJSON(chat.ClientEvent{Type: "message.send", RequestID: "web", GroupID: id, ClientMessageID: "web-send", Body: "cookie message"}); e != nil {
		t.Fatal(e)
	}
	ack := false
	for i := 0; i < 5; i++ {
		if e = conn.ReadJSON(&event); e != nil {
			t.Fatal(e)
		}
		if event.Type == "message.ack" {
			ack = true
			if event.Message == nil || event.Message.ConversationID != id {
				t.Fatal(event)
			}
			break
		}
	}
	if !ack {
		t.Fatal("missing ack")
	}
	_, session, e := f.a.AuthenticateWeb(context.Background(), cookie.Value)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.a.RevokeSession(context.Background(), session.ID); e != nil {
		t.Fatal(e)
	}
	conn.WriteJSON(chat.ClientEvent{Type: "ping", RequestID: "revoked"})
	if e = conn.ReadJSON(&event); e == nil {
		t.Fatalf("revoked socket delivered %+v", event)
	}
}
