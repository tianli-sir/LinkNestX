package auth

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/linknestx/linknestx/server/internal/db"
)

func testService(t *testing.T) *AuthService {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "auth.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err = db.ApplyMigrations(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	return NewAuthService(NewRepository(d), "access-secret", "refresh-secret")
}
func TestPasswordHashAndVerify(t *testing.T) {
	h, e := HashPassword("correct horse battery staple")
	if e != nil || !VerifyPassword("correct horse battery staple", h) || VerifyPassword("wrong", h) {
		t.Fatalf("password verification failed: %v", e)
	}
}
func TestAuthSessionsRotateAndRevoke(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	u, e := s.BootstrapAdmin(ctx, CreateUserInput{Username: "admin", Password: "correct horse battery staple", Admin: true})
	if e != nil {
		t.Fatal(e)
	}
	if len(u.Roles) == 0 {
		t.Fatal("roles missing")
	}
	sess, e := s.Login(ctx, LoginInput{Username: "admin", Password: "correct horse battery staple"})
	if e != nil {
		t.Fatal(e)
	}
	rot, e := s.Refresh(ctx, sess.RefreshToken)
	if e != nil {
		t.Fatal(e)
	}
	if rot.RefreshToken == sess.RefreshToken {
		t.Fatal("refresh token did not rotate")
	}
	if _, e = s.Refresh(ctx, sess.RefreshToken); !errors.Is(e, ErrSessionRevoked) {
		t.Fatalf("old token error=%v", e)
	}
	if e = s.RevokeSession(ctx, rot.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Refresh(ctx, rot.RefreshToken); !errors.Is(e, ErrSessionRevoked) {
		t.Fatalf("revoked token error=%v", e)
	}
}
func TestDisabledAccount(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	_, e := s.BootstrapAdmin(ctx, CreateUserInput{Username: "admin", Password: "correct horse battery staple", Admin: true})
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.repo.db.Exec(`UPDATE users SET disabled=1 WHERE username='admin'`)
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.Login(ctx, LoginInput{Username: "admin", Password: "correct horse battery staple"})
	if !errors.Is(e, ErrDisabledAccount) {
		t.Fatalf("error=%v", e)
	}
}
func TestSessionExpiryUsesUTC(t *testing.T) {
	s := testService(t)
	now := time.Date(2040, 1, 1, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	s.SetClock(func() time.Time { return now })
	session, _ := adminSession(t, s)
	if !session.ExpiresAt.Equal(time.Date(2040, 1, 31, 4, 0, 0, 0, time.UTC)) || session.ExpiresAt.Location() != time.UTC {
		t.Fatalf("refresh expiry=%v", session.ExpiresAt)
	}
	now = now.Add(30 * 24 * time.Hour)
	if _, err := s.Refresh(context.Background(), session.RefreshToken); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("refresh at expiry: %v", err)
	}
}

func TestCreateUserRejectsUnverifiedActor(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	admin, err := s.BootstrapAdmin(ctx, CreateUserInput{Username: "admin", Password: "password123", Admin: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []CreateUserInput{
		{Username: "flag-bypass", Password: "password123", Admin: true},
		{Username: "actor-bypass", Password: "password123", ActorUserID: admin.ID},
		{Username: "both-bypass", Password: "password123", ActorUserID: "normal-user", Admin: true},
	} {
		if _, err := s.CreateUser(ctx, input); !errors.Is(err, ErrAdminRequired) {
			t.Errorf("%s: got %v, want admin required", input.Username, err)
		}
	}
}

func TestRefreshConcurrentSingleWinner(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	if _, err := s.BootstrapAdmin(ctx, CreateUserInput{Username: "admin", Password: "password123", Admin: true}); err != nil {
		t.Fatal(err)
	}
	session, err := s.Login(ctx, LoginInput{Username: "admin", Password: "password123"})
	if err != nil {
		t.Fatal(err)
	}
	const attempts = 16
	start := make(chan struct{})
	results := make(chan error, attempts)
	for i := 0; i < attempts; i++ {
		go func() { <-start; _, err := s.Refresh(ctx, session.RefreshToken); results <- err }()
	}
	close(start)
	successes := 0
	for i := 0; i < attempts; i++ {
		err := <-results
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrSessionRevoked) {
			t.Errorf("refresh: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful rotations=%d, want 1", successes)
	}
}

func TestDeviceIDMustBeUUID(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	if _, err := s.BootstrapAdmin(ctx, CreateUserInput{Username: "admin", Password: "password123", Admin: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Login(ctx, LoginInput{Username: "admin", Password: "password123", DeviceID: "not-a-uuid"}); err == nil {
		t.Fatal("invalid device UUID accepted")
	}
}

func TestAccessExpiryUsesServiceClock(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	now := time.Date(2040, 1, 1, 12, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return now })
	if _, err := s.BootstrapAdmin(ctx, CreateUserInput{Username: "admin", Password: "password123", Admin: true}); err != nil {
		t.Fatal(err)
	}
	session, err := s.Login(ctx, LoginInput{Username: "admin", Password: "password123"})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(15*time.Minute - time.Second)
	if _, _, _, _, err := s.ParseAccessToken(session.AccessToken); err != nil {
		t.Fatalf("access token rejected before expiry: %v", err)
	}
	now = now.Add(time.Second)
	if _, _, _, _, err := s.ParseAccessToken(session.AccessToken); err == nil {
		t.Fatal("access token accepted at expiry")
	}
}

func adminSession(t *testing.T, s *AuthService) (Session, context.Context) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.BootstrapAdmin(ctx, CreateUserInput{Username: "admin", Password: "password123"}); err != nil {
		t.Fatal(err)
	}
	session, err := s.Login(ctx, LoginInput{Username: "admin", Password: "password123"})
	if err != nil {
		t.Fatal(err)
	}
	authenticated, err := s.AuthenticateContext(ctx, session.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	return session, authenticated
}

func TestBootstrapExplicitAndOneTime(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	if _, err := s.CreateUser(ctx, CreateUserInput{Username: "first", Password: "password123", Admin: true}); !errors.Is(err, ErrAdminRequired) {
		t.Fatalf("ordinary create bootstrapped: %v", err)
	}
	admin, err := s.BootstrapAdmin(ctx, CreateUserInput{Username: "admin", Password: "password123"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.BootstrapAdmin(ctx, CreateUserInput{Username: "second", Password: "password123"}); !errors.Is(err, ErrBootstrapComplete) {
		t.Fatalf("second bootstrap: %v", err)
	}
	if _, err := s.repo.db.Exec("DELETE FROM users WHERE id=?", admin.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BootstrapAdmin(ctx, CreateUserInput{Username: "again", Password: "password123"}); !errors.Is(err, ErrBootstrapComplete) {
		t.Fatalf("bootstrap reopened after account deletion: %v", err)
	}
}

func TestBootstrapConcurrentSingleWinner(t *testing.T) {
	s := testService(t)
	start := make(chan struct{})
	results := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func(i int) {
			<-start
			_, err := s.BootstrapAdmin(context.Background(), CreateUserInput{Username: fmt.Sprintf("admin%d", i), Password: "password123"})
			results <- err
		}(i)
	}
	close(start)
	successes := 0
	for i := 0; i < 4; i++ {
		if err := <-results; err == nil {
			successes++
		} else if !errors.Is(err, ErrBootstrapComplete) {
			t.Errorf("bootstrap: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful bootstraps=%d", successes)
	}
}

func TestCreateUserRequiresCurrentAuthenticatedAdmin(t *testing.T) {
	s := testService(t)
	admin, ctx := adminSession(t, s)
	regular, err := s.CreateUser(ctx, CreateUserInput{Username: "regular", Password: "password123", Admin: true, ActorUserID: "untrusted"})
	if err != nil {
		t.Fatal(err)
	}
	if len(regular.Roles) != 1 || regular.Roles[0] != "user" {
		t.Fatalf("legacy admin flag granted roles: %v", regular.Roles)
	}
	session, err := s.Login(context.Background(), LoginInput{Username: "regular", Password: "password123"})
	if err != nil {
		t.Fatal(err)
	}
	regularCtx, err := s.AuthenticateContext(context.Background(), session.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(regularCtx, CreateUserInput{Username: "forged", Password: "password123", Admin: true, ActorUserID: admin.User.ID, Roles: []string{"admin"}}); !errors.Is(err, ErrAdminRequired) {
		t.Fatalf("non-admin forged actor: %v", err)
	}
	for _, state := range []string{"disabled", "revoked", "demoted", "expired"} {
		t.Run(state, func(t *testing.T) {
			service := testService(t)
			admin, verified := adminSession(t, service)
			switch state {
			case "disabled":
				_, err = service.repo.db.Exec("UPDATE users SET disabled=1 WHERE id=?", admin.User.ID)
			case "revoked":
				err = service.RevokeSession(context.Background(), admin.ID)
			case "demoted":
				_, err = service.repo.db.Exec("DELETE FROM user_roles WHERE user_id=? AND role_id=(SELECT id FROM roles WHERE name='admin')", admin.User.ID)
			case "expired":
				service.SetClock(func() time.Time { return time.Now().Add(time.Hour) })
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.CreateUser(verified, CreateUserInput{Username: "denied", Password: "password123"}); !errors.Is(err, ErrAdminRequired) {
				t.Fatalf("stale administrator allowed: %v", err)
			}
		})
	}
}

func TestRefreshFailureRollsBackConsumption(t *testing.T) {
	s := testService(t)
	session, _ := adminSession(t, s)
	ctx := context.Background()
	if _, err := s.repo.db.Exec(`CREATE TRIGGER reject_rotation BEFORE INSERT ON sessions WHEN NEW.rotated_from IS NOT NULL BEGIN SELECT RAISE(ABORT, 'test insert failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Refresh(ctx, session.RefreshToken); err == nil {
		t.Fatal("expected insert failure")
	}
	if _, err := s.repo.db.Exec("DROP TRIGGER reject_rotation"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Refresh(ctx, session.RefreshToken); err != nil {
		t.Fatalf("failed rotation consumed token: %v", err)
	}
}

func TestDeviceOwnershipAndUUIDs(t *testing.T) {
	s := testService(t)
	session, ctx := adminSession(t, s)
	if _, err := uuid.Parse(session.User.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := uuid.Parse(session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := uuid.Parse(session.DeviceID); err != nil {
		t.Fatal(err)
	}
	user, err := s.CreateUser(ctx, CreateUserInput{Username: "member", Password: "password123", Roles: []string{"custom"}})
	if err != nil {
		t.Fatal(err)
	}
	var roleID string
	if err := s.repo.db.QueryRow("SELECT id FROM roles WHERE name='custom'").Scan(&roleID); err != nil {
		t.Fatal(err)
	}
	if _, err := uuid.Parse(roleID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Login(context.Background(), LoginInput{Username: user.Username, Password: "password123", DeviceID: session.DeviceID}); err == nil {
		t.Fatal("another user's device accepted")
	}
	if _, err := s.Login(context.Background(), LoginInput{Username: "admin", Password: "password123", DeviceID: session.DeviceID}); err != nil {
		t.Fatalf("owner device reuse: %v", err)
	}
}
