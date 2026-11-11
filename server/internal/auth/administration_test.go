package auth

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func accountSession(t *testing.T, s *AuthService, admin context.Context, username string, roles ...string) (Session, context.Context) {
	t.Helper()
	if _, err := s.CreateUser(admin, CreateUserInput{Username: username, Email: username + "@example.test", Password: "initial-password", Roles: roles}); err != nil {
		t.Fatal(err)
	}
	session, err := s.Login(context.Background(), LoginInput{Username: username, Password: "initial-password"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := s.AuthenticateContext(context.Background(), session.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	return session, ctx
}

func initializeAccount(t *testing.T, s *AuthService, ctx context.Context) {
	t.Helper()
	if _, err := s.ChangePassword(ctx, ChangePasswordInput{CurrentPassword: "initial-password", NewPassword: "changed-password"}); err != nil {
		t.Fatal(err)
	}
}

func TestChangePasswordPreservesCurrentSessionAndRevokesOthers(t *testing.T) {
	s := testService(t)
	_, admin := adminSession(t, s)
	current, ctx := accountSession(t, s, admin, "employee")
	other, err := s.Login(context.Background(), LoginInput{Username: "employee", Password: "initial-password"})
	if err != nil {
		t.Fatal(err)
	}
	sameDevice, err := s.Login(context.Background(), LoginInput{Username: "employee", Password: "initial-password", DeviceID: current.DeviceID})
	if err != nil {
		t.Fatal(err)
	}
	user, err := s.ChangePassword(ctx, ChangePasswordInput{CurrentPassword: "initial-password", NewPassword: "一二三四五六七八"})
	if err != nil {
		t.Fatal(err)
	}
	if user.ID != current.User.ID || user.MustChangePassword || user.Username != "employee" || len(user.Roles) != 1 {
		t.Fatalf("returned user = %+v", user)
	}
	if _, _, _, _, err := s.AuthenticateAccessToken(context.Background(), current.AccessToken); err != nil {
		t.Fatalf("current access session revoked: %v", err)
	}
	if _, err := s.Refresh(context.Background(), other.RefreshToken); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("other session survived: %v", err)
	}
	if _, err := s.Refresh(context.Background(), sameDevice.RefreshToken); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("same-device session survived: %v", err)
	}
	if _, err := s.Login(context.Background(), LoginInput{Username: "employee", Password: "initial-password"}); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("old password accepted: %v", err)
	}
	if _, err := s.Login(context.Background(), LoginInput{Username: "employee", Password: "一二三四五六七八"}); err != nil {
		t.Fatalf("new Unicode password rejected: %v", err)
	}
	if _, err := s.Refresh(context.Background(), current.RefreshToken); err != nil {
		t.Fatalf("current refresh session revoked: %v", err)
	}
}

func TestChangePasswordRejectsInvalidChangesWithoutConsumingSession(t *testing.T) {
	s := testService(t)
	_, admin := adminSession(t, s)
	current, ctx := accountSession(t, s, admin, "employee")
	for _, tc := range []struct {
		name, current, next string
		want                error
	}{
		{"wrong_current", "incorrect", "next-password", ErrInvalidCredentials},
		{"same_password", "initial-password", "initial-password", ErrInvalidInput},
		{"short_unicode", "initial-password", "一二三", ErrInvalidInput},
		{"large_new", "initial-password", strings.Repeat("x", 1025), ErrInvalidInput},
		{"large_current", strings.Repeat("x", 1025), "next-password", ErrInvalidInput},
		{"invalid_utf8", "initial-password", "12345678\xff", ErrInvalidInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.ChangePassword(ctx, ChangePasswordInput{CurrentPassword: tc.current, NewPassword: tc.next})
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
	user, err := s.User(context.Background(), current.User.ID)
	if err != nil || !user.MustChangePassword {
		t.Fatalf("password gate cleared: %+v, %v", user, err)
	}
	if _, _, _, _, err := s.AuthenticateAccessToken(context.Background(), current.AccessToken); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChangePassword(context.Background(), ChangePasswordInput{CurrentPassword: "initial-password", NewPassword: "next-password"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("anonymous password change: %v", err)
	}
}

func TestAccountDisableEnableResetAndRevokeSessions(t *testing.T) {
	s := testService(t)
	_, admin := adminSession(t, s)
	user, ctx := accountSession(t, s, admin, "employee")
	initializeAccount(t, s, ctx)
	if err := s.SetUserDisabled(admin, user.User.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Login(context.Background(), LoginInput{Username: "employee", Password: "changed-password"}); !errors.Is(err, ErrDisabledAccount) {
		t.Fatalf("disabled login: %v", err)
	}
	if err := s.SetUserDisabled(admin, user.User.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Refresh(context.Background(), user.RefreshToken); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("enable restored old session: %v", err)
	}
	active, err := s.Login(context.Background(), LoginInput{Username: "employee", Password: "changed-password"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ResetUserPassword(admin, user.User.ID, "reset-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Refresh(context.Background(), active.RefreshToken); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("reset did not revoke: %v", err)
	}
	reset, err := s.Login(context.Background(), LoginInput{Username: "employee", Password: "reset-password"})
	if err != nil || !reset.User.MustChangePassword {
		t.Fatalf("reset login = %+v, %v", reset.User, err)
	}
	if err := s.RevokeUserSessions(admin, user.User.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := s.AuthenticateAccessToken(context.Background(), reset.AccessToken); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("admin revoke did not invalidate access: %v", err)
	}
}

func TestLastEnabledAdminCannotBeDisabledConcurrently(t *testing.T) {
	s := testService(t)
	first, admin := adminSession(t, s)
	second, secondCtx := accountSession(t, s, admin, "second", "admin", "user")
	initializeAccount(t, s, secondCtx)
	start, results := make(chan struct{}), make(chan error, 2)
	for _, actor := range []struct {
		id  string
		ctx context.Context
	}{{first.User.ID, admin}, {second.User.ID, secondCtx}} {
		go func(id string, ctx context.Context) { <-start; results <- s.SetUserDisabled(ctx, id, true) }(actor.id, actor.ctx)
	}
	close(start)
	successes := 0
	for range 2 {
		if err := <-results; err == nil {
			successes++
		} else if !errors.Is(err, ErrLastAdmin) {
			t.Fatalf("disable: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful disable operations = %d", successes)
	}
	var enabled int
	if err := s.repo.db.QueryRow(`SELECT count(*) FROM users u JOIN user_roles ur ON ur.user_id=u.id JOIN roles r ON r.id=ur.role_id WHERE u.disabled=0 AND r.name='admin'`).Scan(&enabled); err != nil || enabled != 1 {
		t.Fatalf("enabled admins = %d, %v", enabled, err)
	}
}

func TestAccountMutationRollsBackWhenSessionRevocationFails(t *testing.T) {
	for _, operation := range []string{"change", "disable", "reset"} {
		t.Run(operation, func(t *testing.T) {
			s := testService(t)
			_, admin := adminSession(t, s)
			user, ctx := accountSession(t, s, admin, "employee")
			if _, err := s.Login(context.Background(), LoginInput{Username: "employee", Password: "initial-password"}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.repo.db.Exec(`CREATE TRIGGER reject_revoke BEFORE UPDATE OF revoked_at ON sessions BEGIN SELECT RAISE(ABORT, 'test revocation failure'); END`); err != nil {
				t.Fatal(err)
			}
			var err error
			switch operation {
			case "change":
				_, err = s.ChangePassword(ctx, ChangePasswordInput{CurrentPassword: "initial-password", NewPassword: "next-password"})
			case "disable":
				err = s.SetUserDisabled(admin, user.User.ID, true)
			case "reset":
				err = s.ResetUserPassword(admin, user.User.ID, "reset-password")
			}
			if err == nil {
				t.Fatal("mutation succeeded despite revocation failure")
			}
			persisted, err := s.User(context.Background(), user.User.ID)
			if err != nil || persisted.Disabled || !persisted.MustChangePassword {
				t.Fatalf("partial mutation persisted: %+v, %v", persisted, err)
			}
			if _, err := s.Login(context.Background(), LoginInput{Username: "employee", Password: "initial-password"}); err != nil {
				t.Fatalf("password mutation did not roll back: %v", err)
			}
		})
	}
}

func TestInitialPasswordBlocksDirectoryAndDeviceOperations(t *testing.T) {
	s := testService(t)
	_, admin := adminSession(t, s)
	user, ctx := accountSession(t, s, admin, "employee")
	checks := []func() error{
		func() error { _, err := s.Directory(ctx); return err },
		func() error { _, err := s.Devices(ctx); return err },
		func() error { return s.RevokeDevice(ctx, user.DeviceID) },
	}
	for i, check := range checks {
		if err := check(); !errors.Is(err, ErrPasswordChangeRequired) {
			t.Fatalf("operation %d: %v", i, err)
		}
	}
}

func TestAdminOperationsRejectStaleOrUninitializedPrincipals(t *testing.T) {
	for _, state := range []string{"regular", "password_required", "revoked", "disabled", "demoted", "expired"} {
		t.Run(state, func(t *testing.T) {
			s := testService(t)
			_, admin := adminSession(t, s)
			roles := []string{"admin", "user"}
			if state == "regular" {
				roles = []string{"user"}
			}
			actor, ctx := accountSession(t, s, admin, "actor", roles...)
			if state != "password_required" {
				initializeAccount(t, s, ctx)
			}
			target, _ := accountSession(t, s, admin, "target")
			switch state {
			case "revoked":
				if err := s.RevokeSession(admin, actor.ID); err != nil {
					t.Fatal(err)
				}
			case "disabled":
				if _, err := s.repo.db.Exec(`UPDATE users SET disabled=1 WHERE id=?`, actor.User.ID); err != nil {
					t.Fatal(err)
				}
			case "demoted":
				if _, err := s.repo.db.Exec(`DELETE FROM user_roles WHERE user_id=? AND role_id=(SELECT id FROM roles WHERE name='admin')`, actor.User.ID); err != nil {
					t.Fatal(err)
				}
			case "expired":
				s.SetClock(func() time.Time { return time.Now().Add(time.Hour) })
			}
			checks := []func() error{
				func() error { _, err := s.UpdateAccountNumber(ctx, target.User.ID, "987654"); return err },
				func() error { return s.SetUserDisabled(ctx, target.User.ID, true) },
				func() error { return s.ResetUserPassword(ctx, target.User.ID, "reset-password") },
				func() error { return s.RevokeUserSessions(ctx, target.User.ID) },
				func() error { return s.AdminRevokeDevice(ctx, target.DeviceID) },
				func() error { _, err := s.AdminUsers(ctx); return err },
			}
			for i, check := range checks {
				if err := check(); !errors.Is(err, ErrAdminRequired) && !errors.Is(err, ErrPasswordChangeRequired) {
					t.Fatalf("operation %d accepted %s: %v", i, state, err)
				}
			}
		})
	}
}

func TestDirectoryExcludesDisabledAndPrivilegedFields(t *testing.T) {
	s := testService(t)
	_, admin := adminSession(t, s)
	_, ctx := accountSession(t, s, admin, "active")
	initializeAccount(t, s, ctx)
	disabled, _ := accountSession(t, s, admin, "disabled")
	if err := s.SetUserDisabled(admin, disabled.User.ID, true); err != nil {
		t.Fatal(err)
	}
	users, err := s.Directory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 || users[0].Username != "active" || users[1].Username != "admin" {
		t.Fatalf("directory = %+v", users)
	}
	encoded, err := json.Marshal(users)
	if err != nil {
		t.Fatal(err)
	}
	var fields []map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	for _, entry := range fields {
		if len(entry) != 3 || entry["id"] == nil || entry["username"] == nil || entry["email"] == nil {
			t.Fatalf("directory leaked account metadata: %s", encoded)
		}
	}
	all, err := s.AdminUsers(admin)
	if err != nil || len(all) != 3 || !all[2].Disabled {
		t.Fatalf("admin users = %+v, %v", all, err)
	}
	if _, err := s.Directory(context.Background()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("anonymous directory: %v", err)
	}
}

func TestDeviceRevocationChecksOwnershipAndOnlyRevokesTargetDevice(t *testing.T) {
	s := testService(t)
	adminSession, admin := adminSession(t, s)
	current, ctx := accountSession(t, s, admin, "employee")
	initializeAccount(t, s, ctx)
	other, err := s.Login(context.Background(), LoginInput{Username: "employee", Password: "changed-password"})
	if err != nil {
		t.Fatal(err)
	}
	devices, err := s.Devices(ctx)
	if err != nil || len(devices) != 2 {
		t.Fatalf("devices = %+v, %v", devices, err)
	}
	for _, d := range devices {
		if !d.Active || d.SessionCount != 1 || d.CreatedAt.IsZero() || d.LastSeen.IsZero() || d.Current != (d.ID == current.DeviceID) {
			t.Fatalf("device metadata = %+v", d)
		}
	}
	if err := s.RevokeDevice(ctx, adminSession.DeviceID); !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("cross-user revoke = %v", err)
	}
	if err := s.RevokeDevice(ctx, other.DeviceID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := s.AuthenticateAccessToken(context.Background(), other.AccessToken); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("device access remains valid: %v", err)
	}
	if _, _, _, _, err := s.AuthenticateAccessToken(context.Background(), current.AccessToken); err != nil {
		t.Fatalf("wrong device revoked: %v", err)
	}
	devices, err = s.Devices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range devices {
		if d.ID == other.DeviceID && (d.Active || d.SessionCount != 0) {
			t.Fatalf("revoked device marked active: %+v", d)
		}
	}
	all, err := s.AdminDevices(admin)
	if err != nil || len(all) != 3 {
		t.Fatalf("admin devices = %+v, %v", all, err)
	}
	if err := s.AdminRevokeDevice(admin, current.DeviceID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := s.AuthenticateAccessToken(context.Background(), current.AccessToken); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("admin device revoke = %v", err)
	}
}

func TestAdminCanRevokeOwnSessionsWithoutDisablingLastAdmin(t *testing.T) {
	s := testService(t)
	current, ctx := adminSession(t, s)
	if err := s.SetUserDisabled(ctx, current.User.ID, true); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("last admin disable = %v", err)
	}
	if err := s.RevokeUserSessions(ctx, current.User.ID); err != nil {
		t.Fatalf("own session revoke = %v", err)
	}
	if _, _, _, _, err := s.AuthenticateAccessToken(context.Background(), current.AccessToken); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("own session still valid: %v", err)
	}
	if _, err := s.Login(context.Background(), LoginInput{Username: "admin", Password: "password123"}); err != nil {
		t.Fatalf("last admin could not log in again: %v", err)
	}
}
