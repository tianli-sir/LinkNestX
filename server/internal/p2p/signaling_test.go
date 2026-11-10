package p2p

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/linknestx/linknestx/server/internal/files"
)

func TestSignalDecoderRequiresObjectPayload(t *testing.T) {
	for _, raw := range []string{`{"type":"peer.ice"}`, `{"type":"peer.close","payload":null}`, `null`} {
		if _, err := decodeClientEvent([]byte(raw)); err == nil {
			t.Fatalf("missing object payload accepted: %s", raw)
		}
	}
	valid := ClientEvent{Type: "peer.ice", ToDeviceID: uuid.NewString(), VersionID: uuid.NewString(), ConnectionID: uuid.NewString(), Payload: Payload{}}
	raw, e := json.Marshal(valid)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = decodeClientEvent(raw); e != nil {
		t.Fatalf("end-of-candidates rejected: %v", e)
	}
}

func TestPeerWebSocketPasswordGateRechecksExistingConnection(t *testing.T) {
	f := setup(t)
	_, server := socketServer(t, f)
	c := connect(t, server, f.sessions[0].AccessToken)
	if _, err := f.db.Exec(`UPDATE users SET must_change_password=1 WHERE id=?`, f.sessions[0].User.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.WriteJSON(ClientEvent{Type: "ping", Payload: Payload{}}); err != nil {
		t.Fatal(err)
	}
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	var e Event
	if err := c.ReadJSON(&e); err == nil {
		t.Fatalf("initial-password peer connection remained usable: %+v", e)
	}
}

func socketServer(t *testing.T, f *fixture) (*Hub, *httptest.Server) {
	t.Helper()
	h := NewHub(f.service)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, e := f.auth.AuthenticateContext(r.Context(), strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if e != nil {
			http.Error(w, "unauthorized", 401)
			return
		}
		h.ServeHTTP(w, r.WithContext(ctx))
	}))
	t.Cleanup(func() { h.Close(); server.Close() })
	return h, server
}
func connect(t *testing.T, s *httptest.Server, token string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(s.URL, "http"), http.Header{"Authorization": []string{"Bearer " + token}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	event := readEvent(t, conn)
	if event.Type != "session.ready" {
		t.Fatalf("ready %+v", event)
	}
	return conn
}
func readEvent(t *testing.T, c *websocket.Conn) Event {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	var e Event
	if err := c.ReadJSON(&e); err != nil {
		t.Fatal(err)
	}
	return e
}
func writeFrame(t *testing.T, c *websocket.Conn, f ClientEvent) {
	t.Helper()
	if e := c.WriteJSON(f); e != nil {
		t.Fatal(e)
	}
}
func offer(f *fixture, to string) ClientEvent {
	return ClientEvent{Type: "peer.offer", ToDeviceID: to, ConnectionID: uuid.NewString(), VersionID: f.version.VersionID, Payload: Payload{SDP: "v=0\r\no=test"}}
}

func TestRealWebSocketNegotiationBindsTrustedPairAndVersion(t *testing.T) {
	f := setup(t)
	f.announce(t, 0)
	h, s := socketServer(t, f)
	source := connect(t, s, f.sessions[0].AccessToken)
	recipient := connect(t, s, f.sessions[1].AccessToken)
	outsider := connect(t, s, f.sessions[2].AccessToken)
	in := offer(f, f.sessions[0].DeviceID)
	writeFrame(t, recipient, in)
	got := readEvent(t, source)
	if got.Type != "peer.offer" || got.FromDeviceID != f.sessions[1].DeviceID || got.FromUserID != f.sessions[1].User.ID || got.ConnectionID != in.ConnectionID {
		t.Fatalf("untrusted origin %+v", got)
	}
	answer := in
	answer.Type = "peer.answer"
	answer.ToDeviceID = f.sessions[1].DeviceID
	answer.Payload.SDP = "v=0\r\no=answer"
	writeFrame(t, source, answer)
	if got = readEvent(t, recipient); got.Type != "peer.answer" || got.FromUserID != f.sessions[0].User.ID {
		t.Fatalf("answer %+v", got)
	}
	ice := in
	ice.Type = "peer.ice"
	ice.Payload = Payload{Candidate: "candidate:1 1 udp 1 127.0.0.1 1234 typ host"}
	writeFrame(t, recipient, ice)
	if got = readEvent(t, source); got.Type != "peer.ice" {
		t.Fatalf("ICE %+v", got)
	}
	writeFrame(t, outsider, ice)
	if got = readEvent(t, outsider); got.Code != "invalid_connection" {
		t.Fatalf("unrelated participant %+v", got)
	}
	wrong := ice
	wrong.VersionID = uuid.NewString()
	writeFrame(t, recipient, wrong)
	if got = readEvent(t, recipient); got.Code != "invalid_connection" {
		t.Fatalf("version substitution %+v", got)
	}
	if e := recipient.WriteJSON(map[string]any{"type": "peer.ice", "toDeviceId": in.ToDeviceID, "connectionId": in.ConnectionID, "versionId": in.VersionID, "fromDeviceId": f.sessions[2].DeviceID, "payload": map[string]string{"candidate": "forged"}}); e != nil {
		t.Fatal(e)
	}
	if got = readEvent(t, recipient); got.Code != "invalid_input" {
		t.Fatalf("forged origin %+v", got)
	}
	if e := recipient.WriteJSON(map[string]any{"type": "peer.ice", "toDeviceId": in.ToDeviceID, "connectionId": in.ConnectionID, "versionId": in.VersionID}); e != nil {
		t.Fatal(e)
	}
	if got = readEvent(t, recipient); got.Code != "invalid_input" {
		t.Fatalf("missing payload %+v", got)
	}
	close := in
	close.Type = "peer.close"
	close.Payload = Payload{}
	writeFrame(t, recipient, close)
	if got = readEvent(t, source); got.Type != "peer.close" {
		t.Fatalf("close %+v", got)
	}
	writeFrame(t, recipient, in)
	if got = readEvent(t, recipient); got.Code != "invalid_connection" {
		t.Fatalf("historical connection reused %+v", got)
	}
	writeFrame(t, recipient, ice)
	if got = readEvent(t, recipient); got.Code != "invalid_connection" {
		t.Fatalf("closed reuse %+v", got)
	}
	h.Close()
	recipient.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, e := recipient.ReadMessage(); e == nil {
		t.Fatal("hub shutdown kept socket alive")
	}
	var count int
	if e := f.db.QueryRow(`SELECT count(*) FROM peer_transfers WHERE outcome='closed' AND ended_at IS NOT NULL`).Scan(&count); e != nil || count != 1 {
		t.Fatalf("aggregate audit %d %v", count, e)
	}
}

func TestSignalingRejectsOfflineUnannouncedAndCurrentAccessLoss(t *testing.T) {
	f := setup(t)
	_, s := socketServer(t, f)
	recipient := connect(t, s, f.sessions[1].AccessToken)
	in := offer(f, f.sessions[0].DeviceID)
	writeFrame(t, recipient, in)
	if got := readEvent(t, recipient); got.Code != "offline" {
		t.Fatalf("offline %+v", got)
	}
	source := connect(t, s, f.sessions[0].AccessToken)
	writeFrame(t, recipient, in)
	if got := readEvent(t, recipient); got.Code != "unavailable" {
		t.Fatalf("unannounced %+v", got)
	}
	f.announce(t, 0)
	writeFrame(t, recipient, in)
	readEvent(t, source)
	if e := f.groups.RemoveMember(f.contexts[0], f.sessions[0].User.ID, f.group.ID, f.sessions[1].User.ID); e != nil {
		t.Fatal(e)
	}
	in.Type = "peer.ice"
	in.Payload = Payload{Candidate: "candidate:now-forbidden"}
	writeFrame(t, recipient, in)
	if got := readEvent(t, recipient); got.Code != "forbidden" {
		t.Fatalf("membership loss %+v", got)
	}
}

func TestSignalingRevocationOversizeAndRateLimit(t *testing.T) {
	f := setup(t)
	_, s := socketServer(t, f)
	c := connect(t, s, f.sessions[0].AccessToken)
	// The read bound must reject the entire oversized frame before forwarding.
	c.WriteMessage(websocket.TextMessage, []byte(`{"type":"peer.offer","payload":{"sdp":"`+strings.Repeat("x", 65536)+`"}}`))
	c.SetReadDeadline(time.Now().Add(time.Second))
	_, _, e := c.ReadMessage()
	if !websocket.IsCloseError(e, websocket.CloseMessageTooBig) {
		t.Fatalf("oversized frame %v", e)
	}
	c = connect(t, s, f.sessions[1].AccessToken)
	in := offer(f, f.sessions[1].DeviceID)
	for i := 0; i < 60; i++ {
		writeFrame(t, c, in)
		if got := readEvent(t, c); got.Code != "invalid_input" {
			t.Fatalf("frame %d %+v", i, got)
		}
	}
	writeFrame(t, c, in)
	if got := readEvent(t, c); got.Code != "rate_limited" {
		t.Fatalf("rate limit %+v", got)
	}
	c = connect(t, s, f.sessions[2].AccessToken)
	if e = f.auth.RevokeSession(context.Background(), f.sessions[2].ID); e != nil {
		t.Fatal(e)
	}
	writeFrame(t, c, in)
	c.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, e = c.ReadMessage(); e == nil {
		t.Fatal("revoked session still signals")
	}
}

func TestReconnectDropsOldConnectionsAndExpiredVersionStopsForwarding(t *testing.T) {
	f := setup(t)
	f.announce(t, 0)
	h, s := socketServer(t, f)
	source := connect(t, s, f.sessions[0].AccessToken)
	recipient := connect(t, s, f.sessions[1].AccessToken)
	in := offer(f, f.sessions[0].DeviceID)
	writeFrame(t, recipient, in)
	readEvent(t, source)
	fresh, e := f.auth.Refresh(context.Background(), f.sessions[0].RefreshToken)
	if e != nil {
		t.Fatal(e)
	}
	freshCtx, e := f.auth.AuthenticateContext(context.Background(), fresh.AccessToken)
	if e != nil {
		t.Fatal(e)
	}
	f.sessions[0], f.contexts[0] = fresh, freshCtx
	f.announce(t, 0)
	nextSource := connect(t, s, fresh.AccessToken)
	if got := readEvent(t, recipient); got.Type != "peer.close" || got.Code != "reconnected" {
		t.Fatalf("reconnect cleanup %+v", got)
	}
	source.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, e = source.ReadMessage(); e == nil {
		t.Fatal("old source socket remained")
	}
	next := offer(f, fresh.DeviceID)
	writeFrame(t, recipient, next)
	readEvent(t, nextSource)
	h.mu.Lock()
	pair := h.connections[next.ConnectionID]
	h.mu.Unlock()
	// Check the actual dequeue authorization boundary using the real pair and DB.
	q := queuedEvent{event: Event{Type: "peer.ice"}, pair: pair, sender: pair.requester}
	if !h.deliverable(pair.source, q) {
		t.Fatal("valid queued ICE denied")
	}
	if _, e = f.db.Exec(`UPDATE file_versions SET expires_at=? WHERE id=?`, time.Now().Add(-time.Second).UTC().Format(timestampLayout), f.version.VersionID); e != nil {
		t.Fatal(e)
	}
	if h.deliverable(pair.source, q) {
		t.Fatal("queued ICE leaked expired version")
	}
	next.Type = "peer.ice"
	next.Payload = Payload{Candidate: "candidate:expired"}
	writeFrame(t, recipient, next)
	if got := readEvent(t, recipient); got.Code != "unavailable" {
		t.Fatalf("expired file signaling %+v", got)
	}
}

func TestWebSocketHandshakeRequiresAuthenticationAndSafeOrigin(t *testing.T) {
	f := setup(t)
	_, s := socketServer(t, f)
	for _, headers := range []http.Header{nil, {"Authorization": []string{"Bearer " + f.sessions[0].AccessToken}, "Origin": []string{"https://untrusted.example"}}} {
		conn, response, e := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(s.URL, "http"), headers)
		if conn != nil {
			conn.Close()
		}
		if e == nil || response == nil || (response.StatusCode != 401 && response.StatusCode != 403) {
			t.Fatalf("unsafe handshake %v %+v", e, response)
		}
	}
}

func TestRealSocketShutdownCancelsDatabaseWait(t *testing.T) {
	f := setup(t)
	f.announce(t, 0)
	h, s := socketServer(t, f)
	source := connect(t, s, f.sessions[0].AccessToken)
	recipient := connect(t, s, f.sessions[1].AccessToken)
	var last ClientEvent
	for i := 0; i < 4; i++ {
		last = offer(f, f.sessions[0].DeviceID)
		writeFrame(t, recipient, last)
		readEvent(t, source)
	}
	held, e := f.db.Conn(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer held.Close()
	last.Type = "peer.ice"
	last.Payload = Payload{Candidate: "candidate:blocked"}
	writeFrame(t, recipient, last)
	// The held DB belongs to the fixture; it deliberately remains held until
	// after Close completes, so socket shutdown must cancel its own operations.
	closed := make(chan struct{})
	go func() { h.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(4 * time.Second):
		held.Close()
		<-closed
		t.Fatal("socket workers required external DB release to shut down")
	}
	recipient.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, e = recipient.ReadMessage(); e == nil {
		t.Fatal("recipient reader survived hub close")
	}
}

func TestRealNegotiationHistorySurvivesHubRestart(t *testing.T) {
	f := setup(t)
	f.announce(t, 0)
	h, server := socketServer(t, f)
	source := connect(t, server, f.sessions[0].AccessToken)
	recipient := connect(t, server, f.sessions[1].AccessToken)
	first := offer(f, f.sessions[0].DeviceID)
	writeFrame(t, recipient, first)
	readEvent(t, source)
	answer := first
	answer.Type = "peer.answer"
	answer.ToDeviceID = f.sessions[1].DeviceID
	writeFrame(t, source, answer)
	readEvent(t, recipient)
	closeFrame := first
	closeFrame.Type = "peer.close"
	closeFrame.Payload = Payload{}
	writeFrame(t, recipient, closeFrame)
	readEvent(t, source)
	h.Close()
	snapshot := func() string {
		t.Helper()
		var id, version, requester, owner, user, start, end, outcome string
		e := f.db.QueryRow(`SELECT connection_id,version_id,requester_device_id,source_device_id,requester_user_id,started_at,ended_at,outcome FROM peer_transfers WHERE connection_id=?`, first.ConnectionID).Scan(&id, &version, &requester, &owner, &user, &start, &end, &outcome)
		if e != nil {
			t.Fatal(e)
		}
		raw, _ := json.Marshal([]string{id, version, requester, owner, user, start, end, outcome})
		return string(raw)
	}
	before := snapshot()
	if e := f.groups.AddMember(f.contexts[0], f.sessions[0].User.ID, f.group.ID, f.sessions[2].User.ID, "member"); e != nil {
		t.Fatal(e)
	}
	data := []byte("verified peer bytes")
	next, e := f.files.CreateVersion(f.contexts[0], f.sessions[0].User.ID, f.version.FileID, files.StartUploadInput{GroupID: f.group.ID, Name: "second.bin", Size: int64(len(data)), SHA256: f.hash, Chunks: []files.Chunk{{Index: 0, Hash: f.hash, Size: int64(len(data))}}})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.files.PutChunk(f.contexts[0], f.sessions[0].User.ID, next.UploadID, 0, bytes.NewReader(data)); e != nil {
		t.Fatal(e)
	}
	if _, e = f.files.CompleteUpload(f.contexts[0], f.sessions[0].User.ID, next.UploadID); e != nil {
		t.Fatal(e)
	}
	if e = f.service.Announce(f.contexts[1], f.sessions[1].User.ID, AnnounceInput{VersionID: next.VersionID, Indexes: []int{0}}); e != nil {
		t.Fatal(e)
	}
	_, restarted := socketServer(t, f)
	connect(t, restarted, f.sessions[1].AccessToken)
	other := connect(t, restarted, f.sessions[2].AccessToken)
	reused := first
	reused.VersionID = next.VersionID
	reused.ToDeviceID = f.sessions[1].DeviceID
	writeFrame(t, other, reused)
	if got := readEvent(t, other); got.Code != "invalid_connection" {
		t.Fatalf("history reused by other pair/version %+v", got)
	}
	if after := snapshot(); after != before {
		t.Fatalf("original audit altered: %s -> %s", before, after)
	}
}
