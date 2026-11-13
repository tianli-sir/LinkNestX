package db

import (
	"context"
	"database/sql"
	"io/fs"
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
	packaged, err := fs.Glob(migrations.Files, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&count); err != nil || count != len(packaged) {
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

func TestPoolUpgradeDefaultsExistingFilesListedAndPreservesVersions(t *testing.T) {
	conn, err := Open(filepath.Join(t.TempDir(), "pool-upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, name := range []string{"001_initial.sql", "002_groups_chat.sql", "003_files.sql", "004_transfer_audit.sql", "005_peers.sql", "006_administration_audit.sql"} {
		raw, err := migrations.Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = conn.Exec(string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{`INSERT INTO users(id,username,password_hash,created_at,updated_at)VALUES('u','u','h','x','x')`, `INSERT INTO groups(id,name,created_by,created_at)VALUES('g','g','u','x')`, `INSERT INTO files(id,group_id,name,created_by,visibility,created_at)VALUES('f','g','display','u','pool','x')`, `INSERT INTO file_versions(id,file_id,created_by,name,size,sha256,chunk_size,status,created_at)VALUES('v','f','u','original',4,'hash',4,'published','x')`} {
		if _, err = conn.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := migrations.Files.ReadFile("007_pool_sharing.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(string(raw)); err != nil {
		t.Fatal(err)
	}
	var listed bool
	var name string
	var expiry sql.NullString
	if err = conn.QueryRow(`SELECT f.listed,v.name,v.expires_at FROM files f JOIN file_versions v ON v.file_id=f.id WHERE f.id='f'`).Scan(&listed, &name, &expiry); err != nil || !listed || name != "original" || expiry.Valid {
		t.Fatalf("upgrade %v %s %v %v", listed, name, expiry, err)
	}
}

func TestProfilesUpgradeNumbersAvoidLegacyNumericNames(t *testing.T) {
	conn, e := Open(filepath.Join(t.TempDir(), "profiles.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	conn.Exec(`CREATE TABLE schema_migrations(name TEXT PRIMARY KEY,applied_at TEXT NOT NULL)`)
	for _, name := range []string{"001_initial.sql", "002_groups_chat.sql", "003_files.sql", "004_transfer_audit.sql", "005_peers.sql", "006_administration_audit.sql", "007_pool_sharing.sql"} {
		raw, e := migrations.Files.ReadFile(name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = conn.Exec(string(raw)); e != nil {
			t.Fatal(e)
		}
		conn.Exec(`INSERT INTO schema_migrations VALUES(?,'x')`, name)
	}
	for _, name := range []string{"10000000", "10000001", "alice"} {
		if _, e = conn.Exec(`INSERT INTO users(id,username,password_hash,created_at,updated_at) VALUES(?,?,'h','2026-01-01','2026-01-01')`, name, name); e != nil {
			t.Fatal(e)
		}
	}
	if e = ApplyMigrations(context.Background(), conn); e != nil {
		t.Fatal(e)
	}
	var n int
	if e = conn.QueryRow(`SELECT count(*) FROM profiles WHERE length(account_number)>=8 AND display_name<>'' AND account_number NOT IN (SELECT username FROM users)`).Scan(&n); e != nil || n != 3 {
		t.Fatalf("number allocation n=%d err=%v", n, e)
	}
	var number string
	conn.QueryRow(`SELECT account_number FROM profiles WHERE user_id='alice'`).Scan(&number)
	if _, e = conn.Exec(`INSERT INTO users(id,username,password_hash,created_at,updated_at) VALUES('collision',?,'h','x','x')`, number); e == nil {
		t.Fatal("numeric username stole an allocated login")
	}
	if e = ApplyMigrations(context.Background(), conn); e != nil {
		t.Fatal(e)
	}
	var after string
	conn.QueryRow(`SELECT account_number FROM profiles WHERE user_id='alice'`).Scan(&after)
	if after != number {
		t.Fatal("number changed")
	}
	// Authorized application transactions may now edit numbers; schema guards
	// still reject malformed numbers and collisions with legacy logins.
	for _, invalid := range []string{"012345", "12345", "12345678901", "12345x", "10000000"} {
		if _, e = conn.Exec("UPDATE profiles SET account_number=? WHERE user_id='alice'", invalid); e == nil {
			t.Fatalf("invalid account number accepted: %s", invalid)
		}
	}
	if _, e = conn.Exec("UPDATE profiles SET account_number='123456789' WHERE user_id='alice'"); e != nil {
		t.Fatalf("valid account-number update rejected: %v", e)
	}
}

func TestDocumentMigrationPreservesFileChildrenAndEnforcesOwnership(t *testing.T) {
	conn, e := Open(filepath.Join(t.TempDir(), "document-upgrade.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	if _, e = conn.Exec(`CREATE TABLE schema_migrations(name TEXT PRIMARY KEY,applied_at TEXT NOT NULL)`); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"001_initial.sql", "002_groups_chat.sql", "003_files.sql", "004_transfer_audit.sql", "005_peers.sql", "006_administration_audit.sql", "007_pool_sharing.sql", "008_collaboration.sql"} {
		raw, e := migrations.Files.ReadFile(name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = conn.Exec(string(raw)); e != nil {
			t.Fatal(e)
		}
		if _, e = conn.Exec(`INSERT INTO schema_migrations VALUES(?,'x')`, name); e != nil {
			t.Fatal(e)
		}
	}
	for _, q := range []string{
		`INSERT INTO users(id,username,password_hash,created_at,updated_at) VALUES('u','legacy','h','x','x')`,
		`INSERT INTO groups(id,name,created_by,created_at) VALUES('g','group','u','x')`,
		`INSERT INTO files(id,group_id,name,created_by,current_version_id,created_at) VALUES('f','g','old','u','v','x')`,
		`INSERT INTO file_versions(id,file_id,created_by,name,size,sha256,chunk_size,status,created_at)VALUES('v','f','u','old',4,'hash',4,'published','x')`,
		`INSERT INTO file_memberships VALUES('f','g')`,
		`INSERT INTO file_chunks VALUES('v',0,'hash',4)`,
		`INSERT INTO upload_sessions(id,version_id,user_id,created_at,expires_at)VALUES('upload','v','u','x','y')`,
		`INSERT INTO upload_receipts VALUES('upload',0,'x')`,
		`INSERT INTO group_members VALUES('g','u','owner','x')`,
		`INSERT INTO messages(id,group_id,sender_id,client_message_id,body,created_at,file_id,file_version_id)VALUES('m','g','u','client','old','x','f','v')`,
	} {
		if _, e = conn.Exec(q); e != nil {
			t.Fatal(e)
		}
	}
	if e = ApplyMigrations(context.Background(), conn); e != nil {
		t.Fatal(e)
	}
	for _, table := range []string{"files", "file_versions", "file_memberships", "file_chunks", "upload_sessions", "upload_receipts", "messages"} {
		var n int
		if e = conn.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); e != nil || n != 1 {
			t.Fatalf("%s=%d %v", table, n, e)
		}
	}
	var version string
	if e = conn.QueryRow(`SELECT file_version_id FROM messages WHERE id='m'`).Scan(&version); e != nil || version != "v" {
		t.Fatalf("message attachment %s %v", version, e)
	}
	if _, e = conn.Exec(`INSERT INTO files(id,name,created_by,created_at)VALUES('orphan','orphan','u','x')`); e == nil {
		t.Fatal("ownerless file accepted")
	}
	if _, e = conn.Exec(`INSERT INTO files(id,group_id,name,created_by,created_at)VALUES('bad','absent','bad','u','x')`); e == nil {
		t.Fatal("foreign key not restored")
	}
	if e = ApplyMigrations(context.Background(), conn); e != nil {
		t.Fatal(e)
	}
}

func TestGroupNicknameColumnMigration(t *testing.T) {
	conn, err := Open(filepath.Join(t.TempDir(), "nickname.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for i := 0; i < 2; i++ {
		if err := ApplyMigrations(context.Background(), conn); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := conn.QueryRow(`SELECT count(*) FROM pragma_table_info('group_members') WHERE name='nickname' AND "notnull"=0`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("nullable nickname column missing: n=%d err=%v", n, err)
	}
}

func TestMigrationFailureRestoresForeignKeysAndRollsBack(t *testing.T) {
	conn, e := Open(filepath.Join(t.TempDir(), "invalid.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	if e = ApplyMigrations(context.Background(), conn); e != nil {
		t.Fatal(e)
	}
	if _, e = conn.Exec(`PRAGMA foreign_keys=OFF;INSERT INTO files(id,group_id,name,created_by,created_at) VALUES('bad','missing','bad','missing','x');PRAGMA foreign_keys=ON;`); e != nil {
		t.Fatal(e)
	}
	if e = ApplyMigrations(context.Background(), conn); e == nil {
		t.Fatal("invalid foreign keys accepted")
	}
	var enabled int
	if e = conn.QueryRow(`PRAGMA foreign_keys`).Scan(&enabled); e != nil || enabled != 1 {
		t.Fatalf("foreign keys %d %v", enabled, e)
	}
	var n int
	if e = conn.QueryRow(`SELECT count(*) FROM files WHERE id='bad'`).Scan(&n); e != nil || n != 1 {
		t.Fatal("migration silently erased prior data")
	}
}
