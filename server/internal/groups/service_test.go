package groups

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/linknestx/linknestx/server/internal/auth"
	"github.com/linknestx/linknestx/server/internal/db"
)

func groupFixture(t *testing.T) (*GroupService, *auth.AuthService, *sql.DB, auth.Session, context.Context) {
	t.Helper()
	conn, err := db.Open(filepath.Join(t.TempDir(), "groups.db"))
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
	session, err := a.Login(context.Background(), auth.LoginInput{Username: "admin", Password: "password123"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := a.AuthenticateContext(context.Background(), session.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	return NewGroupService(conn, a), a, conn, session, ctx
}
func groupUser(t *testing.T, a *auth.AuthService, ctx context.Context, name string) (auth.Session, context.Context) {
	t.Helper()
	if _, err := a.CreateUser(ctx, auth.CreateUserInput{Username: name, Password: "password123"}); err != nil {
		t.Fatal(err)
	}
	s, err := a.Login(context.Background(), auth.LoginInput{Username: name, Password: "password123"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := a.AuthenticateContext(context.Background(), s.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	return s, c
}
func TestGroupCreationAndMembershipACL(t *testing.T) {
	s, a, _, admin, ctx := groupFixture(t)
	owner, ownerCtx := groupUser(t, a, ctx, "owner")
	member, memberCtx := groupUser(t, a, ctx, "member")
	if _, err := s.Create(ownerCtx, owner.User.ID, CreateInput{Name: "forbidden"}); !errors.Is(err, auth.ErrAdminRequired) {
		t.Fatalf("nonadmin create: %v", err)
	}
	if _, err := s.Create(context.Background(), admin.User.ID, CreateInput{Name: "spoof"}); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("unverified create: %v", err)
	}
	if _, err := s.Create(memberCtx, admin.User.ID, CreateInput{Name: "spoof"}); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("mismatched actor: %v", err)
	}
	g, err := s.Create(ctx, admin.User.ID, CreateInput{Name: "Engineering", OwnerID: owner.User.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddMember(memberCtx, member.User.ID, g.ID, member.User.ID, "member"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("nonowner add: %v", err)
	}
	if err := s.AddMember(ownerCtx, owner.User.ID, g.ID, member.User.ID, "member"); err != nil {
		t.Fatal(err)
	}
	listed, err := s.List(memberCtx, member.User.ID)
	if err != nil || len(listed) != 1 || listed[0].ID != g.ID {
		t.Fatalf("groups=%v err=%v", listed, err)
	}
	members, err := s.ListMembers(memberCtx, member.User.ID, g.ID)
	if err != nil || len(members) != 2 {
		t.Fatalf("members=%v err=%v", members, err)
	}
	if err := s.RemoveMember(ownerCtx, owner.User.ID, g.ID, owner.User.ID); !errors.Is(err, ErrLastOwner) {
		t.Fatalf("last owner removal: %v", err)
	}
	if err := s.AddMember(ownerCtx, owner.User.ID, g.ID, owner.User.ID, "member"); !errors.Is(err, ErrLastOwner) {
		t.Fatalf("last owner demotion: %v", err)
	}
	if err := s.RemoveMember(ownerCtx, owner.User.ID, g.ID, member.User.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListMembers(memberCtx, member.User.ID, g.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("removed read: %v", err)
	}
	if err := a.RevokeSession(ctx, owner.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.AddMember(ownerCtx, owner.User.ID, g.ID, member.User.ID, "member"); !errors.Is(err, auth.ErrSessionRevoked) {
		t.Fatalf("revoked write: %v", err)
	}
}
