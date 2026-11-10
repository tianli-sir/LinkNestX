package audit

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/linknestx/linknestx/server/internal/auth"
	"github.com/linknestx/linknestx/server/internal/db"
	"path/filepath"
	"testing"
	"time"
)

func fixture(t *testing.T) (*Service, context.Context) {
	t.Helper()
	conn, err := db.Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err = db.ApplyMigrations(context.Background(), conn); err != nil {
		t.Fatal(err)
	}
	a := auth.NewAuthService(conn, "sign", "refresh")
	if _, err = a.BootstrapAdmin(context.Background(), auth.CreateUserInput{Username: "admin", Password: "initial-password"}); err != nil {
		t.Fatal(err)
	}
	session, err := a.Login(context.Background(), auth.LoginInput{Username: "admin", Password: "initial-password"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := a.AuthenticateContext(context.Background(), session.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.ChangePassword(ctx, auth.ChangePasswordInput{CurrentPassword: "initial-password", NewPassword: "changed-password"}); err != nil {
		t.Fatal(err)
	}
	return NewService(conn, a), ctx
}

func TestAuditPaginationFiltersAndFullExports(t *testing.T) {
	s, ctx := fixture(t)
	actor, _ := auth.UserFromContext(ctx)
	device := auth.DeviceIDFromContext(ctx)
	for i := 0; i < 451; i++ {
		_, err := s.db.Exec(`INSERT INTO audit_events(id,actor_id,action,target_id,device_id,created_at) VALUES(?,?,?,?,?,?)`, fmt.Sprintf("event-%04d", i), actor.ID, "sample", "=SUM(1,2)\nline", device, "2025-11-10T10:00:00.000000000Z")
		if err != nil {
			t.Fatal(err)
		}
	}
	f := Filter{Action: "sample", ActorID: actor.ID, DeviceID: device, Limit: 50, Offset: 50, From: time.Date(2025, 11, 10, 0, 0, 0, 0, time.UTC), Before: time.Date(2025, 11, 11, 0, 0, 0, 0, time.UTC)}
	page, err := s.Page(ctx, f)
	if err != nil || page.Total != 451 || len(page.Events) != 50 || page.Events[0].ID != "event-0400" || page.Events[49].ID != "event-0351" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	for _, format := range []string{"json", "csv"} {
		var out bytes.Buffer
		if err := s.Export(ctx, f, format, &out); err != nil {
			t.Fatal(err)
		}
		if format == "json" {
			var got Page
			if err := json.Unmarshal(out.Bytes(), &got); err != nil || len(got.Events) != 451 || got.Total != 451 {
				t.Fatalf("full JSON events=%d total=%d err=%v", len(got.Events), got.Total, err)
			}
		} else {
			rows, err := csv.NewReader(&out).ReadAll()
			if err != nil || len(rows) != 452 || rows[1][3] != "'=SUM(1,2)\nline" {
				t.Fatalf("CSV count=%d err=%v", len(rows), err)
			}
		}
	}
	if _, err := s.Page(context.Background(), f); !errors.Is(err, auth.ErrAdminRequired) {
		t.Fatalf("anonymous audit: %v", err)
	}
	if _, err := s.Page(ctx, Filter{Limit: 1001}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bound: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE audit_events SET action='tampered' WHERE action='sample'`); err == nil {
		t.Fatal("audit events are mutable")
	}
}

func TestClientReportsAreDeviceBoundCumulativeAndACLChecked(t *testing.T) {
	s, ctx := fixture(t)
	actor, _ := auth.UserFromContext(ctx)
	group, file, version := uuid.NewString(), uuid.NewString(), uuid.NewString()
	at := "2025-11-10T10:00:00.000000000Z"
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO groups(id,name,created_by,created_at) VALUES(?,'group',?,?)`, []any{group, actor.ID, at}},
		{`INSERT INTO files(id,group_id,name,created_by,visibility,current_version_id,created_at) VALUES(?,?,'file',?,'pool',?,?)`, []any{file, group, actor.ID, version, at}},
		{`INSERT INTO file_versions(id,file_id,created_by,name,size,sha256,chunk_size,status,created_at) VALUES(?,?,?,'file',100,'hash',100,'published',?)`, []any{version, file, actor.ID, at}},
	} {
		if _, err := s.db.Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	in := TransferInput{TaskID: uuid.NewString(), VersionID: version, NodeID: "server", Action: "transfer.completed", Bytes: 100}
	for _, n := range []int64{80, 100, 90, 100} {
		in.Bytes = n
		if err := s.RecordClientTransfer(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.Page(ctx, Filter{Action: "transfer.completed"})
	if err != nil || page.Total != 1 || page.Events[0].Bytes != 100 || page.Events[0].Source != "client-reported" || page.Events[0].DeviceID != auth.DeviceIDFromContext(ctx) {
		t.Fatalf("client aggregate=%+v %v", page, err)
	}
	in.Bytes = -1
	if err := s.RecordClientTransfer(ctx, in); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("negative bytes: %v", err)
	}
	in.Bytes = 100
	in.NodeID = uuid.NewString()
	if err := s.RecordClientTransfer(ctx, in); err == nil {
		t.Fatal("unknown peer accepted")
	}
	in.NodeID = "server"
	if _, err := s.db.Exec(`UPDATE file_versions SET status='expired' WHERE id=?`, version); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordClientTransfer(ctx, in); err == nil {
		t.Fatal("expired version accepted")
	}
}

func TestAuditCalendarYearBoundaryAndReferenceSurvival(t *testing.T) {
	s, ctx := fixture(t)
	now := time.Date(2028, 2, 29, 0, 0, 0, 0, time.UTC)
	boundary := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	for i, at := range []time.Time{boundary.Add(-time.Nanosecond), boundary, boundary.Add(time.Nanosecond)} {
		if _, err := s.db.Exec(`INSERT INTO audit_events(id,action,created_at) VALUES(?,'calendar',?)`, fmt.Sprint(i), at.Format(timestampLayout)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DeleteBeforeTwoYears(ctx, now); err != nil {
		t.Fatal(err)
	}
	page, err := s.Page(ctx, Filter{Action: "calendar"})
	if err != nil || page.Total != 2 {
		t.Fatalf("calendar total=%d err=%v", page.Total, err)
	}
}

type callbackWriter struct {
	bytes.Buffer
	once     bool
	callback func() error
}

func (w *callbackWriter) Write(b []byte) (int, error) {
	if !w.once {
		w.once = true
		if err := w.callback(); err != nil {
			return 0, err
		}
	}
	return w.Buffer.Write(b)
}

func TestAuditExportReleasesDatabaseBeforeWritingAndRechecksRevocation(t *testing.T) {
	s, ctx := fixture(t)
	for i := 0; i < 401; i++ {
		if _, err := s.db.Exec(`INSERT INTO audit_events(id,action,created_at) VALUES(?,'stream','2026-01-01T00:00:00.000000000Z')`, fmt.Sprintf("stream-%04d", i)); err != nil {
			t.Fatal(err)
		}
	}
	blocked := errors.New("export held database while writing response")
	writer := &callbackWriter{callback: func() error {
		done := make(chan error, 1)
		go func() { done <- s.auth.RevokeSession(context.Background(), auth.SessionIDFromContext(ctx)) }()
		select {
		case err := <-done:
			return err
		case <-time.After(time.Second):
			return blocked
		}
	}}
	err := s.Export(ctx, Filter{Action: "stream"}, "json", writer)
	if errors.Is(err, blocked) {
		t.Fatal(err)
	}
	if !errors.Is(err, auth.ErrSessionRevoked) {
		t.Fatalf("revoked export completed: %v", err)
	}
	if bytes.HasSuffix(writer.Bytes(), []byte("]}\n")) {
		t.Fatal("revoked export emitted successful completion")
	}
}
