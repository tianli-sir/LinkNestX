package db

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/linknestx/linknestx/server/migrations"
)

func TestMigrationsPackagedAndIdempotent(t *testing.T) {
	t.Chdir(t.TempDir())
	conn, err := Open(filepath.Join(t.TempDir(), "test.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	for i := 0; i < 2; i++ {
		if err := ApplyMigrations(context.Background(), conn); err != nil {
			t.Fatal(err)
		}
	}
	var mode string
	if err := conn.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal mode=%s, err=%v", mode, err)
	}
	var enabled int
	if err := conn.QueryRow("PRAGMA foreign_keys").Scan(&enabled); err != nil || enabled != 1 {
		t.Fatalf("foreign keys=%d, err=%v", enabled, err)
	}
	rows, err := conn.Query("SELECT id FROM roles")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		if _, err := uuid.Parse(id); err != nil {
			t.Errorf("role identifier %q is not a UUID", id)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("roles=%d, want 2", count)
	}
}

func TestMigrationHistoryAndExistingDatabaseUpgrade(t *testing.T) {
	conn, err := Open(filepath.Join(t.TempDir(), "upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	initial, err := migrations.Files.ReadFile("001_initial.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(string(initial)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := ApplyMigrations(context.Background(), conn); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := conn.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&count); err != nil || count != 4 {
		t.Fatalf("migration history=%d err=%v", count, err)
	}
	if err := conn.QueryRow("SELECT count(*) FROM messages").Scan(&count); err != nil {
		t.Fatalf("chat table unavailable: %v", err)
	}
}
