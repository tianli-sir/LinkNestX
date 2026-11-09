package db

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"sort"

	"github.com/linknestx/linknestx/server/migrations"
)

// ApplyMigrations applies the embedded schema. It is idempotent and needs no
// source files on disk when the server binary is deployed.
func ApplyMigrations(ctx context.Context, conn *sql.DB) error {
	if conn == nil {
		return fmt.Errorf("nil database")
	}
	// The history table is also the write-lock claim: concurrent startup cannot
	// run the same migration twice. Schema and history commit together.
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (name TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return err
	}
	names, err := fs.Glob(migrations.Files, "*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		result, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(name,applied_at) VALUES(?,strftime('%Y-%m-%dT%H:%M:%fZ','now')) ON CONFLICT(name) DO NOTHING`, name)
		if err != nil {
			return fmt.Errorf("claim migration %s: %w", name, err)
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count == 0 {
			continue
		}
		data, err := migrations.Files.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, string(data)); err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
	}
	return tx.Commit()
}
func Migrate(ctx context.Context, conn *sql.DB) error { return ApplyMigrations(ctx, conn) }
func Apply(ctx context.Context, conn *sql.DB) error   { return ApplyMigrations(ctx, conn) }
func MustApply(ctx context.Context, conn *sql.DB) {
	if err := ApplyMigrations(ctx, conn); err != nil {
		panic(err)
	}
}
func RunMigrations(ctx context.Context, conn *sql.DB) error { return ApplyMigrations(ctx, conn) }
