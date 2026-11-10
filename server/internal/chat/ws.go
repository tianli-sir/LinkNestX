package chat

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/linknestx/linknestx/server/internal/auth"
	"github.com/linknestx/linknestx/server/internal/groups"
)

// ClientEvent and Event are the v1 socket envelopes. requestId correlates a
// request and its reply; clientMessageId is the durable message deduplication key.
type ClientEvent struct {
	Type            string `json:"type"`
	RequestID       string `json:"requestId,omitempty"`
	GroupID         string `json:"groupId,omitempty"`
	ClientMessageID string `json:"clientMessageId,omitempty"`
	Body            string `json:"body,omitempty"`
}
type Event struct {
	ConversationID  string   `json:"conversationId,omitempty"`
	MessageIDs      []string `json:"messageIds,omitempty"`
	recipient       string
	EventID         string   `json:"eventId,omitempty"`
	Type            string   `json:"type"`
	RequestID       string   `json:"requestId,omitempty"`
	Code            string   `json:"code,omitempty"`
	GroupID         string   `json:"groupId,omitempty"`
	UserID          string   `json:"userId,omitempty"`
	Online          bool     `json:"online"`
	ClientMessageID string   `json:"clientMessageId,omitempty"`
	Message         *Message `json:"message,omitempty"`
}
type peer struct {
	conn   *websocket.Conn
	ctx    context.Context
	userID string
	out    chan Event
	done   chan struct{}
}
type Hub struct {
	service *ChatService
	mu      sync.RWMutex
	peers   map[*peer]struct{}
	closed  bool
	active  sync.WaitGroup
}

func NewHub(service *ChatService) *Hub {
	h := &Hub{service: service, peers: map[*peer]struct{}{}}
	service.hub = h
	return h
}

// Close closes upgraded sockets as HTTP Shutdown does not own them.
func (h *Hub) Close() {
	h.mu.Lock()
	h.closed = true
	for p := range h.peers {
		_ = p.conn.Close()
	}
	h.mu.Unlock()
	h.active.Wait()
}

var upgrader = websocket.Upgrader{ReadBufferSize: 4096, WriteBufferSize: 4096}

func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok || h.service.auth.ValidateActor(r.Context(), user.ID) != nil {
		http.Error(w, "unauthorized", 401)
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	p := &peer{conn: conn, ctx: r.Context(), userID: user.ID, out: make(chan Event, 128), done: make(chan struct{})}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		_ = conn.Close()
		return
	}
	h.peers[p] = struct{}{}
	h.active.Add(1)
	p.out <- Event{Type: "session.ready"}
	h.mu.Unlock()
	writerDone := make(chan struct{})
	defer func() {
		h.mu.Lock()
		delete(h.peers, p)
		h.mu.Unlock()
		close(p.done)
		_ = conn.Close()
		<-writerDone
		h.broadcastPresence(p, false)
		h.active.Done()
	}()
	go func() { defer close(writerDone); h.write(p) }()
	h.broadcastPresence(p, true)
	// Keep the request alive while the socket is active: its verified context is
	// canceled when ServeHTTP returns. Never return with a detached reader.
	h.read(p)
	_ = conn.Close()
}
func (h *Hub) snapshot() []*peer {
	h.mu.RLock()
	defer h.mu.RUnlock()
	peers := make([]*peer, 0, len(h.peers))
	for p := range h.peers {
		peers = append(peers, p)
	}
	return peers
}
func (h *Hub) read(p *peer) {
	p.conn.SetReadLimit(65536)
	_ = p.conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	p.conn.SetPongHandler(func(string) error { return p.conn.SetReadDeadline(time.Now().Add(90 * time.Second)) })
	for {
		var in ClientEvent
		if err := p.conn.ReadJSON(&in); err != nil {
			return
		}
		if err := h.service.auth.ValidateActor(p.ctx, p.userID); err != nil {
			return
		}
		_ = p.conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		if len(in.RequestID) > 128 {
			h.send(p, Event{Type: "error", Code: "invalid_input"})
			continue
		}
		switch in.Type {
		case "ping":
			h.send(p, Event{Type: "pong", RequestID: in.RequestID})
		case "message.send":
			h.sendMessage(p, in)
		default:
			h.send(p, Event{Type: "error", RequestID: in.RequestID, Code: "invalid_event"})
		}
	}
}
func (h *Hub) sendMessage(p *peer, in ClientEvent) {
	m, err := h.service.Append(p.ctx, p.userID, AppendInput{GroupID: in.GroupID, ClientMessageID: in.ClientMessageID, Body: in.Body})
	if err != nil {
		code := "internal_error"
		switch {
		case errors.Is(err, groups.ErrForbidden):
			code = "forbidden"
		case errors.Is(err, ErrInvalidInput):
			code = "invalid_input"
		case errors.Is(err, ErrConflict):
			code = "conflict"
		}
		h.send(p, Event{Type: "error", RequestID: in.RequestID, Code: code})
		return
	}
	h.send(p, Event{Type: "message.ack", RequestID: in.RequestID, GroupID: in.GroupID, ClientMessageID: m.ClientMessageID, Message: &m})
}
func (h *Hub) Publish(m Message) {
	for _, p := range h.snapshot() {
		h.send(p, Event{Type: "message.created", GroupID: m.GroupID, Message: &m})
	}
}
func (h *Hub) broadcastPresence(changed *peer, online bool) {
	peers := h.snapshot()
	if !online {
		for _, p := range peers {
			if p.userID == changed.userID && h.service.auth.ValidateActor(p.ctx, p.userID) == nil {
				return
			}
		}
	}
	for _, p := range peers {
		ids, err := h.service.repo.sharedGroups(p.ctx, p.userID, changed.userID)
		if err != nil {
			continue
		}
		for _, id := range ids {
			h.send(p, Event{Type: "presence.changed", GroupID: id, UserID: changed.userID, Online: online})
		}
	}
}
func (h *Hub) allowed(p *peer, e Event) bool {
	if err := h.service.auth.ValidateActor(p.ctx, p.userID); err != nil {
		_ = p.conn.Close()
		return false
	}
	if e.recipient != "" && e.recipient != p.userID {
		return false
	}
	if e.GroupID != "" && h.service.groups.RequireMember(p.ctx, p.userID, e.GroupID) != nil {
		return false
	}
	if e.Type == "presence.changed" {
		// A membership change between queuing and delivery must not expose presence.
		shared, err := h.service.repo.sharedGroups(p.ctx, p.userID, e.UserID)
		if err != nil {
			return false
		}
		for _, id := range shared {
			if id == e.GroupID {
				return true
			}
		}
		return false
	}
	return true
}
func (h *Hub) send(p *peer, e Event) {
	if !h.allowed(p, e) {
		return
	}
	select {
	case p.out <- e:
	case <-p.done:
	default:
		// Disconnect slow consumers instead of silently dropping durable events.
		// Reconnect uses REST cursor catch-up, so committed messages remain available.
		_ = p.conn.Close()
	}
}
func (h *Hub) write(p *peer) {
	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-p.done:
			return
		case e := <-p.out:
			if !h.allowed(p, e) {
				continue
			}
			_ = p.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := p.conn.WriteJSON(e); err != nil {
				_ = p.conn.Close()
				return
			}
		case <-heartbeat.C:
			if err := h.service.auth.ValidateActor(p.ctx, p.userID); err != nil {
				_ = p.conn.Close()
				return
			}
			if err := p.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
				_ = p.conn.Close()
				return
			}
		}
	}
}

// A removed member receives only their own permission-change notice, never chat content.
func (h *Hub) MembershipChanged(group, user string) {
	id := uuid.NewString()
	for _, p := range h.snapshot() {
		e := Event{Type: "membership.changed", EventID: id, GroupID: group, UserID: user}
		if p.userID == user {
			e.GroupID = ""
		}
		h.send(p, e)
	}
}
func (h *Hub) FilesChanged() {
	id := uuid.NewString()
	for _, p := range h.snapshot() {
		h.send(p, Event{Type: "files.changed", EventID: id})
	}
}

func (h *Hub) NotifyUsers(ids []string, e Event) {
	for _, p := range h.snapshot() {
		for _, id := range ids {
			if p.userID == id {
				e.recipient = id
				h.send(p, e)
				break
			}
		}
	}
}
func (h *Hub) NotifyConversation(id string, e Event) {
	e.GroupID = id
	e.ConversationID = id
	for _, p := range h.snapshot() {
		h.send(p, e)
	}
}
