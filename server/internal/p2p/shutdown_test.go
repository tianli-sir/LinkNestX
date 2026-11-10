package p2p

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Exercises the real forwarding/ACL database path without opening a listener.
// A held SQL connection must not make Close wait for that connection to free.
func TestHubCloseCancelsContendedDatabaseAndMultiplePairAudits(t *testing.T) {
	f := setup(t)
	h := NewHub(f.service)
	defer h.Close()
	p := &socketPeer{ctx: f.contexts[1], user: f.sessions[1].User.ID, device: f.sessions[1].DeviceID, session: f.sessions[1].ID, out: make(chan queuedEvent, 64), done: make(chan struct{})}
	q := &socketPeer{ctx: f.contexts[0], user: f.sessions[0].User.ID, device: f.sessions[0].DeviceID, session: f.sessions[0].ID, out: make(chan queuedEvent, 64), done: make(chan struct{})}
	for i := 0; i < 4; i++ {
		id := uuid.NewString()
		h.connections[id] = &connection{id: id, version: f.version.VersionID, requester: p, source: q, expires: time.Now().Add(time.Minute)}
	}
	held, e := f.db.Conn(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer held.Close()
	routed := make(chan struct{})
	started := make(chan struct{})
	h.active.Add(1)
	go func() {
		defer h.active.Done()
		defer close(routed)
		h.mu.Lock()
		close(started)
		h.handleLocked(p, offer(f, q.device))
		h.removeConnectionsLocked(p, "disconnected")
		h.mu.Unlock()
	}()
	<-started
	closed := make(chan struct{})
	go func() { h.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(4 * time.Second):
		held.Close()
		<-closed
		t.Fatal("Close waited for occupied DB instead of canceling operations")
	}
	select {
	case <-routed:
	default:
		t.Fatal("forwarding worker still alive after close")
	}
	select {
	case <-h.maintenanceDone:
	default:
		t.Fatal("maintenance worker still alive after close")
	}
}

func TestShutdownAuditFinalizationUsesOneTotalBudget(t *testing.T) {
	f := setup(t)
	h := NewHub(f.service)
	defer h.Close()
	p := &socketPeer{ctx: f.contexts[1], user: f.sessions[1].User.ID, device: f.sessions[1].DeviceID, out: make(chan queuedEvent, 64), done: make(chan struct{})}
	q := &socketPeer{ctx: f.contexts[0], user: f.sessions[0].User.ID, device: f.sessions[0].DeviceID, out: make(chan queuedEvent, 64), done: make(chan struct{})}
	for i := 0; i < 4; i++ {
		id := uuid.NewString()
		h.connections[id] = &connection{id: id, version: f.version.VersionID, requester: p, source: q}
		if e := f.service.RecordConnection(context.Background(), ConnectionAudit{ConnectionID: id, VersionID: f.version.VersionID, RequesterDeviceID: p.device, SourceDeviceID: q.device, RequesterUserID: p.user}); e != nil {
			t.Fatal(e)
		}
	}
	held, e := f.db.Conn(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer held.Close()
	h.active.Add(1)
	go func() {
		defer h.active.Done()
		<-h.done
		h.mu.Lock()
		h.removeConnectionsLocked(p, "disconnected")
		h.mu.Unlock()
	}()
	closed := make(chan struct{})
	go func() { h.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(4 * time.Second):
		held.Close()
		<-closed
		t.Fatal("shutdown audit budget accumulated per pair")
	}
	if len(h.connections) != 0 {
		t.Fatal("pair state remained after shutdown")
	}
}

func persistedAuditPair(t *testing.T, f *fixture, h *Hub) *connection {
	t.Helper()
	p := &socketPeer{user: f.sessions[1].User.ID, device: f.sessions[1].DeviceID}
	q := &socketPeer{user: f.sessions[0].User.ID, device: f.sessions[0].DeviceID}
	c := &connection{id: uuid.NewString(), version: f.version.VersionID, requester: p, source: q}
	if err := f.service.RecordConnection(context.Background(), ConnectionAudit{ConnectionID: c.id, VersionID: c.version, RequesterDeviceID: p.device, SourceDeviceID: q.device, RequesterUserID: p.user}); err != nil {
		t.Fatal(err)
	}
	h.connections[c.id] = c
	return c
}
func assertEndedAudit(t *testing.T, f *fixture, c *connection) {
	t.Helper()
	var ended sql.NullString
	var outcome string
	if err := f.db.QueryRow(`SELECT ended_at,outcome FROM peer_transfers WHERE connection_id=?`, c.id).Scan(&ended, &outcome); err != nil {
		t.Fatal(err)
	}
	if !ended.Valid || outcome != "disconnected" {
		t.Fatalf("terminal audit lost during shutdown: ended=%v outcome=%s", ended, outcome)
	}
}

func TestShutdownPublishesAuditBudgetBeforeCancelUnderHeldMutex(t *testing.T) {
	f := setup(t)
	h := NewHub(f.service)
	defer h.Close()
	c := persistedAuditPair(t, f, h)
	h.mu.Lock()
	closed := make(chan struct{})
	go func() { h.Close(); close(closed) }()
	select {
	case <-h.ctx.Done():
	case <-time.After(time.Second):
		h.mu.Unlock()
		t.Fatal("hub cancellation waited for the hub mutex")
	}
	// Close has canceled ordinary work but cannot acquire mu yet. A terminal
	// write in this exact transition must already have a usable shared budget.
	h.endLocked(c, "disconnected")
	h.mu.Unlock()
	<-closed
	assertEndedAudit(t, f, c)
}

// This observer delegates every write to the real SQL repository. Channels
// expose only the first write's cancellation boundary for deterministic timing.
type observedAuditService struct {
	PeerService
	first    sync.Once
	started  chan context.Context
	finished chan error
}

func (s *observedAuditService) RecordConnection(ctx context.Context, a ConnectionAudit) error {
	observe := false
	if a.Ended {
		s.first.Do(func() { observe = true })
	}
	if observe {
		s.started <- ctx
	}
	err := s.PeerService.RecordConnection(ctx, a)
	if observe {
		s.finished <- err
	}
	return err
}

func TestShutdownRetriesInterruptedTerminalAuditWithinSharedBudget(t *testing.T) {
	f := setup(t)
	observer := &observedAuditService{PeerService: f.service, started: make(chan context.Context, 1), finished: make(chan error, 1)}
	h := NewHub(observer)
	defer h.Close()
	c := persistedAuditPair(t, f, h)
	held, err := f.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	h.active.Add(1)
	go func() { defer h.active.Done(); h.mu.Lock(); h.endLocked(c, "disconnected"); h.mu.Unlock() }()
	firstCtx := <-observer.started
	closed := make(chan struct{})
	go func() { h.Close(); close(closed) }()
	select {
	case <-firstCtx.Done():
	case <-time.After(time.Second):
		held.Close()
		t.Fatal("ordinary terminal write was not canceled")
	}
	if err = <-observer.finished; !errors.Is(err, context.Canceled) {
		held.Close()
		t.Fatalf("expected actual SQL cancellation, got %v", err)
	}
	// Only now release the DB. The original write has definitely failed, so a
	// terminal row here proves the hub retried under the published shared budget.
	held.Close()
	select {
	case <-closed:
	case <-time.After(4 * time.Second):
		t.Fatal("retry exceeded total shutdown budget")
	}
	assertEndedAudit(t, f, c)
}
