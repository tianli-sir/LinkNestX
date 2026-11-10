package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestBootstrapPasswordGateRechecksLiveContext(t *testing.T) {
	s := testService(t)
	user, err := s.BootstrapAdmin(context.Background(), CreateUserInput{Username: "fresh", Password: "initial-password"})
	if err != nil {
		t.Fatal(err)
	}
	if !user.MustChangePassword {
		t.Fatal("bootstrap bypassed initial password change")
	}
	session, err := s.Login(context.Background(), LoginInput{Username: "fresh", Password: "initial-password"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := s.AuthenticateContext(context.Background(), session.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateActor(ctx, user.ID); !errors.Is(err, ErrPasswordChangeRequired) {
		t.Fatalf("initial actor: %v", err)
	}
	if _, err := s.CreateUser(ctx, CreateUserInput{Username: "denied", Password: "initial-password"}); !errors.Is(err, ErrPasswordChangeRequired) {
		t.Fatalf("initial admin: %v", err)
	}
	if _, err := s.ChangePassword(ctx, ChangePasswordInput{CurrentPassword: "initial-password", NewPassword: "changed-password"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateActor(ctx, user.ID); err != nil {
		t.Fatalf("stale context failed to see password change: %v", err)
	}
	if _, err := s.repo.db.Exec(`UPDATE users SET must_change_password=1 WHERE id=?`, user.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateActor(ctx, user.ID); !errors.Is(err, ErrPasswordChangeRequired) {
		t.Fatalf("live context bypassed gate: %v", err)
	}
}

func TestAuthenticationInputsAreBoundedBeforePasswordWork(t *testing.T) {
	s := testService(t)
	for _, in := range []LoginInput{{Username: strings.Repeat("x", 257), Password: "password123"}, {Username: "user", Password: strings.Repeat("x", 1025)}, {Username: "user", Password: "password123", DeviceID: strings.Repeat("x", 65)}} {
		if _, err := s.Login(context.Background(), in); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("login bound: %v", err)
		}
	}
	if _, err := s.Refresh(context.Background(), strings.Repeat("x", 1025)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("refresh bound: %v", err)
	}
	if _, err := s.BootstrapAdmin(context.Background(), CreateUserInput{Username: "admin", Password: strings.Repeat("x", 1025)}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("create bound: %v", err)
	}
}
