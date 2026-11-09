package retention

import (
	"context"
	"github.com/linknestx/linknestx/server/internal/audit"
	"github.com/linknestx/linknestx/server/internal/auth"
	"github.com/linknestx/linknestx/server/internal/chat"
	"github.com/linknestx/linknestx/server/internal/db"
	"github.com/linknestx/linknestx/server/internal/files"
	"github.com/linknestx/linknestx/server/internal/groups"
	"path/filepath"
	"testing"
	"time"
)

func TestCalendarYearAuditAndWorkerShutdown(t *testing.T) {
	conn, e := db.Open(filepath.Join(t.TempDir(), "retention.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	if e = db.ApplyMigrations(context.Background(), conn); e != nil {
		t.Fatal(e)
	}
	a := auth.NewAuthService(conn, "a", "r")
	g := groups.NewGroupService(conn, a)
	c := chat.NewChatService(conn, a, g)
	store, e := files.NewChunkStore(filepath.Join(t.TempDir(), "store"))
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	jobs := Jobs{Files: files.NewFileService(conn, a, store, "grant", c), Chat: c, Audit: audit.NewService(conn, a), Interval: time.Hour}
	now := time.Date(2028, 2, 29, 12, 0, 0, 0, time.UTC)
	boundary := now.AddDate(-2, 0, 0)
	for i, at := range []time.Time{boundary.Add(-time.Nanosecond), boundary, boundary.Add(time.Nanosecond)} {
		if _, e = conn.Exec(`INSERT INTO audit_events(id,action,created_at) VALUES(?,'test',?)`, i, at.Format("2006-01-02T15:04:05.000000000Z")); e != nil {
			t.Fatal(e)
		}
	}
	r, e := jobs.RunOnce(context.Background(), now)
	if e != nil || r.AuditDeleted != 1 {
		t.Fatalf("calendar report %+v %v", r, e)
	}
	r, e = jobs.RunOnce(context.Background(), now)
	if e != nil || r.AuditDeleted != 0 {
		t.Fatalf("idempotent %+v %v", r, e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { jobs.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop")
	}
}
