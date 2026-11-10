package p2p

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/linknestx/linknestx/server/internal/auth"
	"github.com/linknestx/linknestx/server/internal/files"
)

type Payload struct {
	SDP           string  `json:"sdp,omitempty"`
	Candidate     string  `json:"candidate,omitempty"`
	SDPMid        *string `json:"sdpMid,omitempty"`
	SDPMLineIndex *uint16 `json:"sdpMLineIndex,omitempty"`
}
type ClientEvent struct {
	Type         string  `json:"type"`
	ToDeviceID   string  `json:"toDeviceId"`
	ConnectionID string  `json:"connectionId"`
	VersionID    string  `json:"versionId"`
	Payload      Payload `json:"payload"`
}
type Event struct {
	Type         string   `json:"type"`
	ToDeviceID   string   `json:"toDeviceId,omitempty"`
	ConnectionID string   `json:"connectionId,omitempty"`
	VersionID    string   `json:"versionId,omitempty"`
	Payload      *Payload `json:"payload,omitempty"`
	FromDeviceID string   `json:"fromDeviceId,omitempty"`
	FromUserID   string   `json:"fromUserId,omitempty"`
	DeviceID     string   `json:"deviceId,omitempty"`
	UserID       string   `json:"userId,omitempty"`
	Code         string   `json:"code,omitempty"`
}
type socketPeer struct {
	conn                  *websocket.Conn
	ctx                   context.Context
	user, device, session string
	out                   chan queuedEvent
	done                  chan struct{}
}
type connection struct {
	id, version       string
	requester, source *socketPeer
	expires           time.Time
	answered, closed  bool
}
type queuedEvent struct {
	event       Event
	pair        *connection
	sender      *socketPeer
	systemClose bool
}
type rateBucket struct {
	tokens  float64
	updated time.Time
}
type shutdownBudget struct {
	ctx    context.Context
	cancel context.CancelFunc
}
type Hub struct {
	service         PeerService
	mu              sync.Mutex
	peers           map[string]*socketPeer
	connections     map[string]*connection
	rates           map[string]rateBucket
	closed          bool
	active          sync.WaitGroup
	done            chan struct{}
	maintenanceDone chan struct{}
	closeOnce       sync.Once
	ctx             context.Context
	cancel          context.CancelFunc
	shutdown        atomic.Pointer[shutdownBudget]
}

func NewHub(s PeerService) *Hub {
	ctx, cancel := context.WithCancel(context.Background())
	h := &Hub{service: s, peers: map[string]*socketPeer{}, connections: map[string]*connection{}, rates: map[string]rateBucket{}, done: make(chan struct{}), maintenanceDone: make(chan struct{}), ctx: ctx, cancel: cancel}
	go h.maintain()
	return h
}
func (h *Hub) Close() {
	h.closeOnce.Do(func() {
		// Start one shared finalization budget, then cancel DB/auth operations
		// before taking mu: an operation holding mu may itself be waiting on DB.
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
		// A terminal writer may already own mu. Publish independently of that
		// mutex before cancellation so it can switch to this same total budget.
		h.shutdown.Store(&shutdownBudget{ctx: shutdownCtx, cancel: shutdownCancel})
		h.cancel()
		h.mu.Lock()
		h.closed = true
		close(h.done)
		for _, p := range h.peers {
			p.conn.Close()
		}
		h.mu.Unlock()
	})
	h.active.Wait()
	<-h.maintenanceDone
	h.shutdown.Load().cancel()
}

// Preserve the private authenticated principal while bounding each service
// operation and linking it to cancellation independent of the HTTP request.
func (h *Hub) operationContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	stop := context.AfterFunc(h.ctx, cancel)
	if h.ctx.Err() != nil {
		cancel()
	}
	return ctx, func() { stop(); cancel() }
}
func (h *Hub) validateActor(parent context.Context, user string) error {
	ctx, cancel := h.operationContext(parent)
	defer cancel()
	return h.service.ValidateActor(ctx, user)
}

var upgrader = websocket.Upgrader{ReadBufferSize: 4096, WriteBufferSize: 4096}

func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok || h.validateActor(r.Context(), user.ID) != nil {
		http.Error(w, "unauthorized", 401)
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	p := &socketPeer{conn: conn, ctx: r.Context(), user: user.ID, device: auth.DeviceIDFromContext(r.Context()), session: auth.SessionIDFromContext(r.Context()), out: make(chan queuedEvent, 64), done: make(chan struct{})}
	h.mu.Lock()
	if h.closed || len(h.peers) >= 1024 {
		h.mu.Unlock()
		conn.Close()
		return
	}
	if old := h.peers[p.device]; old != nil {
		old.conn.Close()
		h.removeConnectionsLocked(old, "reconnected")
	}
	h.peers[p.device] = p
	h.active.Add(1)
	h.queueLocked(p, queuedEvent{event: Event{Type: "session.ready", DeviceID: p.device, UserID: p.user}})
	h.mu.Unlock()
	writerDone := make(chan struct{})
	defer func() {
		conn.Close()
		close(p.done)
		h.mu.Lock()
		if h.peers[p.device] == p {
			delete(h.peers, p.device)
		}
		h.removeConnectionsLocked(p, "disconnected")
		h.mu.Unlock()
		<-writerDone
		h.active.Done()
	}()
	go func() { defer close(writerDone); h.write(p) }()
	h.read(p)
}
func (h *Hub) read(p *socketPeer) {
	p.conn.SetReadLimit(MaxBodyBytes)
	p.conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	p.conn.SetPongHandler(func(string) error { return p.conn.SetReadDeadline(time.Now().Add(90 * time.Second)) })
	for {
		kind, raw, err := p.conn.ReadMessage()
		if err != nil {
			return
		}
		if h.validateActor(p.ctx, p.user) != nil {
			return
		}
		p.conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		h.mu.Lock()
		if h.closed || h.peers[p.device] != p {
			h.mu.Unlock()
			return
		}
		if !h.allowRateLocked(p.session) {
			h.errorLocked(p, "rate_limited", "")
			h.mu.Unlock()
			continue
		}
		in, err := decodeClientEvent(raw)
		if kind != websocket.TextMessage || err != nil {
			h.errorLocked(p, "invalid_input", "")
			h.mu.Unlock()
			continue
		}
		h.handleLocked(p, in)
		h.mu.Unlock()
	}
}

func decodeClientEvent(raw []byte) (ClientEvent, error) {
	var in ClientEvent
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil || decoder.Decode(new(any)) != io.EOF {
		return in, ErrInvalidInput
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return in, ErrInvalidInput
	}
	payload := bytes.TrimSpace(fields["payload"])
	if len(payload) == 0 || payload[0] != '{' {
		return in, ErrInvalidInput
	}
	return in, nil
}
func (h *Hub) allowRateLocked(session string) bool {
	now := time.Now()
	r, exists := h.rates[session]
	if !exists {
		if len(h.rates) >= 8192 {
			return false
		}
		r = rateBucket{tokens: 60, updated: now}
	}
	r.tokens += now.Sub(r.updated).Seconds() * 2
	if r.tokens > 60 {
		r.tokens = 60
	}
	r.updated = now
	allowed := r.tokens >= 1
	if allowed {
		r.tokens--
	}
	h.rates[session] = r
	return allowed
}
func (h *Hub) handleLocked(p *socketPeer, in ClientEvent) {
	ctx, cancel := h.operationContext(p.ctx)
	defer cancel()
	if !validID(in.ConnectionID) || !validID(in.ToDeviceID) || !validID(in.VersionID) || in.ToDeviceID == p.device || !validPayload(in) {
		h.errorLocked(p, "invalid_input", in.ConnectionID)
		return
	}
	var pair *connection
	if in.Type != "peer.offer" {
		pair = h.connections[in.ConnectionID]
		if pair == nil || pair.version != in.VersionID || pair.closed || !time.Now().Before(pair.expires) || !((pair.requester == p && pair.source.device == in.ToDeviceID) || (pair.source == p && pair.requester.device == in.ToDeviceID)) {
			h.errorLocked(p, "invalid_connection", in.ConnectionID)
			return
		}
		if in.Type == "peer.answer" && (pair.source != p || pair.answered) {
			h.errorLocked(p, "invalid_connection", in.ConnectionID)
			return
		}
	} else if h.connections[in.ConnectionID] != nil {
		h.errorLocked(p, "invalid_connection", in.ConnectionID)
		return
	}
	if err := h.service.AuthorizeVersion(ctx, p.user, in.VersionID); err != nil {
		h.errorLocked(p, errorCode(err), in.ConnectionID)
		return
	}
	target := h.peers[in.ToDeviceID]
	if target == nil {
		h.errorLocked(p, "offline", in.ConnectionID)
		return
	}
	targetCtx, targetCancel := h.operationContext(target.ctx)
	defer targetCancel()
	if h.service.ValidateActor(targetCtx, target.user) != nil {
		target.conn.Close()
		h.errorLocked(p, "offline", in.ConnectionID)
		return
	}
	if err := h.service.AuthorizeVersion(targetCtx, target.user, in.VersionID); err != nil {
		h.errorLocked(p, "unavailable", in.ConnectionID)
		return
	}
	if in.Type == "peer.offer" {
		source, err := h.service.IsSource(ctx, target.device, target.session, in.VersionID)
		if err != nil {
			h.errorLocked(p, "internal_error", in.ConnectionID)
			return
		}
		if !source {
			h.errorLocked(p, "unavailable", in.ConnectionID)
			return
		}
		if len(h.connections) >= 2048 || h.connectionCountLocked(p) >= 16 || h.connectionCountLocked(target) >= 16 {
			h.errorLocked(p, "rate_limited", in.ConnectionID)
			return
		}
		pair = &connection{id: in.ConnectionID, version: in.VersionID, requester: p, source: target}
		if err = h.service.RecordConnection(ctx, ConnectionAudit{ConnectionID: pair.id, VersionID: pair.version, RequesterDeviceID: p.device, SourceDeviceID: target.device, RequesterUserID: p.user}); err != nil {
			code := "internal_error"
			if errors.Is(err, ErrConnectionExists) {
				code = "invalid_connection"
			}
			h.errorLocked(p, code, in.ConnectionID)
			return
		}
		h.connections[pair.id] = pair
	}
	pair.expires = time.Now().Add(2 * time.Minute)
	if in.Type == "peer.answer" {
		pair.answered = true
	}
	h.queueLocked(target, queuedEvent{event: Event{Type: in.Type, ToDeviceID: target.device, ConnectionID: pair.id, VersionID: pair.version, Payload: &in.Payload, FromDeviceID: p.device, FromUserID: p.user}, pair: pair, sender: p})
	if in.Type == "peer.close" {
		h.endLocked(pair, "closed")
	}
}
func validPayload(in ClientEvent) bool {
	p := in.Payload
	switch in.Type {
	case "peer.offer", "peer.answer":
		return p.SDP != "" && p.Candidate == "" && p.SDPMid == nil && p.SDPMLineIndex == nil
	case "peer.ice":
		return p.SDP == "" && len(p.Candidate) <= 8192 && (p.SDPMid == nil || len(*p.SDPMid) <= 256)
	case "peer.close":
		return p.SDP == "" && p.Candidate == "" && p.SDPMid == nil && p.SDPMLineIndex == nil
	}
	return false
}
func errorCode(err error) string {
	switch {
	case errors.Is(err, ErrInvalidInput):
		return "invalid_input"
	case errors.Is(err, files.ErrForbidden):
		return "forbidden"
	case errors.Is(err, files.ErrUnavailable):
		return "unavailable"
	}
	return "internal_error"
}
func (h *Hub) connectionCountLocked(p *socketPeer) int {
	n := 0
	for _, c := range h.connections {
		if c.requester == p || c.source == p {
			n++
		}
	}
	return n
}
func (h *Hub) queueLocked(p *socketPeer, e queuedEvent) {
	select {
	case p.out <- e:
	case <-p.done:
	default:
		p.conn.Close()
	}
}
func (h *Hub) errorLocked(p *socketPeer, code, id string) {
	if !validID(id) {
		id = ""
	}
	h.queueLocked(p, queuedEvent{event: Event{Type: "error", Code: code, ConnectionID: id}})
}
func (h *Hub) endLocked(c *connection, outcome string) {
	if c.closed {
		return
	}
	c.closed = true
	delete(h.connections, c.id)
	parent := h.ctx
	budget := h.shutdown.Load()
	if budget != nil {
		parent = budget.ctx
	}
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	audit := ConnectionAudit{ConnectionID: c.id, VersionID: c.version, RequesterDeviceID: c.requester.device, SourceDeviceID: c.source.device, RequesterUserID: c.requester.user, Outcome: outcome, Ended: true}
	err := h.service.RecordConnection(ctx, audit)
	cancel()
	// A write that began before Close published its budget can be interrupted
	// by ordinary cancellation. Retry once under the already-running shared
	// deadline; the repository's immutable identity/unfinished guards make even
	// an ambiguous first-write result safe without changing a finished record.
	if err != nil && budget == nil && h.ctx.Err() != nil {
		if current := h.shutdown.Load(); current != nil && current.ctx.Err() == nil {
			_ = h.service.RecordConnection(current.ctx, audit)
		}
	}
}
func (h *Hub) removeConnectionsLocked(p *socketPeer, outcome string) {
	for _, c := range h.connections {
		if c.requester == p || c.source == p {
			other := c.requester
			if other == p {
				other = c.source
			}
			h.queueLocked(other, queuedEvent{event: Event{Type: "peer.close", ConnectionID: c.id, VersionID: c.version, ToDeviceID: other.device, FromDeviceID: p.device, FromUserID: p.user, Payload: &Payload{}, Code: outcome}, pair: c, sender: p, systemClose: true})
			h.endLocked(c, outcome)
		}
	}
}

// Check both private auth contexts and ACLs again after queueing, immediately
// before writing, so revocation cannot leak previously queued SDP/candidates.
func (h *Hub) deliverable(p *socketPeer, q queuedEvent) bool {
	h.mu.Lock()
	allowed := h.deliveryStateLocked(p, q)
	h.mu.Unlock()
	if !allowed {
		return false
	}
	ctx, cancel := h.operationContext(p.ctx)
	defer cancel()
	if h.service.ValidateActor(ctx, p.user) != nil {
		p.conn.Close()
		return false
	}
	if q.pair == nil {
		return h.deliveryState(p, q)
	}
	if h.service.AuthorizeVersion(ctx, p.user, q.pair.version) != nil {
		return false
	}
	if q.systemClose {
		return h.deliveryState(p, q)
	}
	sender := q.sender
	senderCtx, senderCancel := h.operationContext(sender.ctx)
	defer senderCancel()
	if h.service.ValidateActor(senderCtx, sender.user) != nil {
		sender.conn.Close()
		return false
	}
	if h.service.AuthorizeVersion(senderCtx, sender.user, q.pair.version) != nil {
		return false
	}
	if q.event.Type == "peer.offer" {
		ok, err := h.service.IsSource(ctx, p.device, p.session, q.pair.version)
		if !ok || err != nil {
			return false
		}
	}
	return h.deliveryState(p, q)
}

func (h *Hub) deliveryState(p *socketPeer, q queuedEvent) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.deliveryStateLocked(p, q)
}
func (h *Hub) deliveryStateLocked(p *socketPeer, q queuedEvent) bool {
	if h.closed || h.peers[p.device] != p {
		return false
	}
	if q.pair == nil {
		return true
	}
	if q.pair.closed && q.event.Type != "peer.close" {
		return false
	}
	return q.systemClose || h.peers[q.sender.device] == q.sender
}
func (h *Hub) write(p *socketPeer) {
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-p.done:
			return
		case q := <-p.out:
			if !h.deliverable(p, q) {
				continue
			}
			p.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if p.conn.WriteJSON(q.event) != nil {
				p.conn.Close()
				return
			}
		case <-ticker.C:
			if h.validateActor(p.ctx, p.user) != nil {
				p.conn.Close()
				return
			}
			if p.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)) != nil {
				p.conn.Close()
				return
			}
		}
	}
}
func (h *Hub) maintain() {
	defer close(h.maintenanceDone)
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-h.done:
			return
		case now := <-ticker.C:
			h.mu.Lock()
			for _, c := range h.connections {
				if !now.Before(c.expires) {
					for _, p := range []*socketPeer{c.requester, c.source} {
						other := c.source
						if p == other {
							other = c.requester
						}
						h.queueLocked(p, queuedEvent{event: Event{Type: "peer.close", ConnectionID: c.id, VersionID: c.version, ToDeviceID: p.device, FromDeviceID: other.device, FromUserID: other.user, Payload: &Payload{}, Code: "expired"}, pair: c, systemClose: true})
					}
					h.endLocked(c, "expired")
				}
			}
			for id, r := range h.rates {
				if now.Sub(r.updated) > time.Minute {
					delete(h.rates, id)
				}
			}
			h.mu.Unlock()
		}
	}
}
