package chat

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/linknestx/linknestx/server/internal/auth"
	"github.com/linknestx/linknestx/server/internal/db"
	"github.com/linknestx/linknestx/server/internal/groups"
)

type fixture struct {
	service *ChatService
	groups  *groups.GroupService
	auth    *auth.AuthService
	db      *sql.DB
	session auth.Session
	ctx     context.Context
	group   groups.Group
}

func chatFixture(t *testing.T) fixture {
	t.Helper()
	conn, err := db.Open(filepath.Join(t.TempDir(), "chat.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := db.ApplyMigrations(context.Background(), conn); err != nil {
		t.Fatal(err)
	}
	a := auth.NewAuthService(conn, "secret", "refresh")
	if _, err := a.BootstrapAdmin(context.Background(), auth.CreateUserInput{Username: "admin", Password: "password123"}); err != nil {
		t.Fatal(err)
	}
	// This fixture models accounts whose initial password was already changed.
	if _, err := a.Database().Exec(`UPDATE users SET must_change_password=0`); err != nil {
		t.Fatal(err)
	}
	sess, err := a.Login(context.Background(), auth.LoginInput{Username: "admin", Password: "password123"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := a.AuthenticateContext(context.Background(), sess.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	g := groups.NewGroupService(conn, a)
	group, err := g.Create(ctx, sess.User.ID, groups.CreateInput{Name: "team"})
	if err != nil {
		t.Fatal(err)
	}
	return fixture{NewChatService(conn, a, g), g, a, conn, sess, ctx, group}
}
func (f fixture) user(t *testing.T, name string) (auth.Session, context.Context) {
	t.Helper()
	if _, err := f.auth.CreateUser(f.ctx, auth.CreateUserInput{Username: name, Password: "password123"}); err != nil {
		t.Fatal(err)
	}
	// This fixture models accounts whose initial password was already changed.
	if _, err := f.auth.Database().Exec(`UPDATE users SET must_change_password=0`); err != nil {
		t.Fatal(err)
	}
	sess, err := f.auth.Login(context.Background(), auth.LoginInput{Username: name, Password: "password123"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := f.auth.AuthenticateContext(context.Background(), sess.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	return sess, ctx
}
func TestChatIdempotentOrderingAndOfflinePagination(t *testing.T) {
	f := chatFixture(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f.service.SetClock(func() time.Time { return now })
	input := AppendInput{GroupID: f.group.ID, ClientMessageID: "retry-1", Body: "first"}
	first, err := f.service.Append(f.ctx, f.session.User.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m, e := f.service.Append(f.ctx, f.session.User.ID, input)
			if e != nil || m.ID != first.ID {
				t.Errorf("retry=%v err=%v", m, e)
			}
		}()
	}
	wg.Wait()
	input.Body = "changed"
	if _, err := f.service.Append(f.ctx, f.session.User.ID, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting retry: %v", err)
	}
	now = now.Add(-time.Hour)
	second, err := f.service.Append(f.ctx, f.session.User.ID, AppendInput{GroupID: f.group.ID, ClientMessageID: "second", Body: "second"})
	if err != nil {
		t.Fatal(err)
	}
	page, err := f.service.List(f.ctx, f.session.User.ID, f.group.ID, "", 1)
	if err != nil || len(page.Messages) != 1 || page.Messages[0].ID != first.ID || !page.HasMore {
		t.Fatalf("first page=%+v err=%v", page, err)
	}
	page, err = f.service.List(f.ctx, f.session.User.ID, f.group.ID, page.NextCursor, 1)
	if err != nil || len(page.Messages) != 1 || page.Messages[0].ID != second.ID || page.HasMore {
		t.Fatalf("second page=%+v err=%v", page, err)
	}
	third, err := f.service.Append(f.ctx, f.session.User.ID, AppendInput{GroupID: f.group.ID, ClientMessageID: "third", Body: "offline"})
	if err != nil {
		t.Fatal(err)
	}
	page, err = f.service.List(f.ctx, f.session.User.ID, f.group.ID, page.NextCursor, 100)
	if err != nil || len(page.Messages) != 1 || page.Messages[0].ID != third.ID {
		t.Fatalf("catchup=%+v err=%v", page, err)
	}
	other, err := f.groups.Create(f.ctx, f.session.User.ID, groups.CreateInput{Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.List(f.ctx, f.session.User.ID, other.ID, page.NextCursor, 10); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("cross-group cursor: %v", err)
	}
}
func TestChatACLAndRevocation(t *testing.T) {
	f := chatFixture(t)
	member, ctx := f.user(t, "member")
	input := AppendInput{GroupID: f.group.ID, ClientMessageID: "one", Body: "hello"}
	if _, err := f.service.List(ctx, member.User.ID, f.group.ID, "", 10); !errors.Is(err, groups.ErrForbidden) {
		t.Fatalf("cross group read: %v", err)
	}
	if _, err := f.service.Append(ctx, f.session.User.ID, input); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("actor spoof: %v", err)
	}
	if err := f.groups.AddMember(f.ctx, f.session.User.ID, f.group.ID, member.User.ID, "member"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Append(ctx, member.User.ID, input); err != nil {
		t.Fatal(err)
	}
	if err := f.groups.RemoveMember(f.ctx, f.session.User.ID, f.group.ID, member.User.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.List(ctx, member.User.ID, f.group.ID, "", 10); !errors.Is(err, groups.ErrForbidden) {
		t.Fatalf("removed read: %v", err)
	}
	if _, err := f.service.Append(ctx, member.User.ID, input); !errors.Is(err, groups.ErrForbidden) {
		t.Fatalf("removed retry: %v", err)
	}
	page, err := f.service.List(f.ctx, f.session.User.ID, f.group.ID, "", 10)
	if err != nil || len(page.Messages) != 1 {
		t.Fatalf("remaining history=%+v err=%v", page, err)
	}
	if err := f.auth.RevokeSession(f.ctx, f.session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Append(f.ctx, f.session.User.ID, input); !errors.Is(err, auth.ErrSessionRevoked) {
		t.Fatalf("revoked append: %v", err)
	}
}
func TestRetentionUsesUTCCalendarYearsAndKeepsTombstones(t *testing.T) {
	f := chatFixture(t)
	for i, at := range []string{"2024-02-28T23:59:59.999999999Z", "2024-02-29T00:00:00.000000000Z", "2024-03-01T00:00:00.000000000Z"} {
		parsed, _ := time.Parse(time.RFC3339Nano, at)
		f.service.SetClock(func() time.Time { return parsed })
		if _, err := f.service.Append(f.ctx, f.session.User.ID, AppendInput{GroupID: f.group.ID, ClientMessageID: at, Body: "retain me"}); err != nil {
			t.Fatalf("%d: %v", i, err)
		}
	}
	// 2026-03-01 08:00 UTC+8 is 2026-03-01 UTC; two calendar years starts March 1 2024.
	count, err := f.service.ExpireBeforeTwoYears(context.Background(), time.Date(2026, 3, 1, 8, 0, 0, 0, time.FixedZone("CST", 8*3600)))
	if err != nil || count != 2 {
		t.Fatalf("expired=%d err=%v", count, err)
	}
	count, err = f.service.ExpireBeforeTwoYears(context.Background(), time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || count != 0 {
		t.Fatalf("repeat expired=%d err=%v", count, err)
	}
	page, err := f.service.List(f.ctx, f.session.User.ID, f.group.ID, "", 10)
	if err != nil || len(page.Messages) != 3 {
		t.Fatalf("history=%+v err=%v", page, err)
	}
	if page.Messages[0].Body != nil || page.Messages[1].Body != nil || page.Messages[2].Body == nil || page.Messages[0].ExpiredAt == nil {
		t.Fatalf("retained content=%+v", page.Messages)
	}
}
