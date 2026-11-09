package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linknestx/linknestx/server/internal/auth"
	"github.com/linknestx/linknestx/server/internal/db"
)

func TestRunReportsDatabaseOpenError(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(parent, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LINKNESTX_SQLITE_PATH", filepath.Join(parent, "server.sqlite3"))
	if err := run(); err == nil || !strings.Contains(err.Error(), "open database") {
		t.Fatalf("startup error=%v", err)
	}
}

func TestRunReportsMigrationError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.sqlite3")
	conn, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), "CREATE TABLE roles(id TEXT PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LINKNESTX_SQLITE_PATH", path)
	if err := run(); err == nil || !strings.Contains(err.Error(), "apply initial migration") {
		t.Fatalf("migration error=%v", err)
	}
}

func TestRunBootstrapsAdministratorFromEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bootstrap.sqlite3")
	t.Setenv("LINKNESTX_SQLITE_PATH", path)
	t.Setenv("LINKNESTX_BIND_ADDRESS", "invalid-address")
	t.Setenv("LINKNESTX_BOOTSTRAP_ADMIN_USERNAME", "operator")
	t.Setenv("LINKNESTX_BOOTSTRAP_ADMIN_PASSWORD", "  local password  ")
	// A deliberately invalid listener ends startup after setup without opening a port.
	if err := run(); err == nil || !strings.Contains(err.Error(), "missing port") {
		t.Fatalf("listener error=%v", err)
	}
	conn, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	service := auth.NewAuthService(conn, "test", "test")
	session, err := service.Login(context.Background(), auth.LoginInput{Username: "operator", Password: "  local password  "})
	if err != nil {
		t.Fatalf("bootstrap login: %v", err)
	}
	admin, err := service.IsAdmin(context.Background(), session.User.ID)
	if err != nil || !admin {
		t.Fatalf("bootstrap admin=%v, err=%v", admin, err)
	}
	t.Setenv("LINKNESTX_BOOTSTRAP_ADMIN_PASSWORD", "replacement-password")
	if err := run(); err == nil || !strings.Contains(err.Error(), "missing port") {
		t.Fatalf("restart error=%v", err)
	}
	if _, err := service.Login(context.Background(), auth.LoginInput{Username: "operator", Password: "  local password  "}); err != nil {
		t.Fatalf("restart replaced administrator password: %v", err)
	}
}
