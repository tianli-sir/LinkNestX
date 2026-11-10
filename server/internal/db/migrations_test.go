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
	if err := conn.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&count); err != nil || count != 6 {
		t.Fatalf("migration history=%d err=%v", count, err)
	}
	if err := conn.QueryRow("SELECT count(*) FROM messages").Scan(&count); err != nil {
		t.Fatalf("chat table unavailable: %v", err)
	}
}

func TestAdministrationMigrationPreservesTransfersAndAuditAfterDeviceCascade(t *testing.T) {
	conn, err := Open(filepath.Join(t.TempDir(), "cascade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, name := range []string{"001_initial.sql", "002_groups_chat.sql", "003_files.sql", "004_transfer_audit.sql", "005_peers.sql"} {
		raw, err := migrations.Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	user, device, session := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO users(id,username,password_hash,created_at,updated_at) VALUES(?,'legacy','hash','2026-01-01','2026-01-01')`, []any{user}},
		{`INSERT INTO devices(id,user_id,created_at,last_seen_at) VALUES(?,?,'2026-01-01','2026-01-01')`, []any{device, user}},
		{`INSERT INTO sessions(id,user_id,device_id,refresh_token_hash,created_at,expires_at) VALUES(?,?,?,'token','2026-01-01','2027-01-01')`, []any{session, user, device}},
		{`INSERT INTO transfer_records(actor_id,version_id,task_id,node_id,action,bytes,created_at,updated_at) VALUES(?,'version','task','server','server.sent',17,'2026-01-01','2026-01-01')`, []any{user}},
		{`INSERT INTO audit_events(id,actor_id,action,created_at) VALUES('event',?,'legacy','2026-01-01')`, []any{user}},
	} {
		if _, err := conn.Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := migrations.Files.ReadFile("006_administration_audit.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(string(raw)); err != nil {
		t.Fatal(err)
	}
	var bytes int64
	if err := conn.QueryRow(`SELECT bytes FROM transfer_records WHERE actor_id=?`, user).Scan(&bytes); err != nil || bytes != 17 {
		t.Fatalf("legacy transfer=%d %v", bytes, err)
	}
	if _, err := conn.Exec(`DELETE FROM devices WHERE id=?`, device); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := conn.QueryRow(`SELECT count(*) FROM sessions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("sessions=%d %v", count, err)
	}
	if _, err := conn.Exec(`DELETE FROM users WHERE id=?`, user); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(`SELECT count(*) FROM audit_events WHERE actor_id=?`, user).Scan(&count); err != nil || count != 1 {
		t.Fatalf("audit attribution=%d %v", count, err)
	}
	rows, err := conn.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("cascade left invalid foreign keys")
	}
}
