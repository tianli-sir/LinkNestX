package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/linknestx/linknestx/server/migrations"
)

// ApplyMigrations applies the embedded schema. It is idempotent and needs no
// source files on disk when the server binary is deployed.
func ApplyMigrations(ctx context.Context, conn *sql.DB) error {
	if conn == nil {
		return fmt.Errorf("nil database")
	}
	data, err := migrations.Files.ReadFile("001_initial.sql")
	if err != nil {
		return fmt.Errorf("read packaged migration: %w", err)
	}
	if _, err := conn.ExecContext(ctx, string(data)); err != nil {
		return fmt.Errorf("apply initial migration: %w", err)
	}
	return nil
}
func Migrate(ctx context.Context, conn *sql.DB) error { return ApplyMigrations(ctx, conn) }
func Apply(ctx context.Context, conn *sql.DB) error   { return ApplyMigrations(ctx, conn) }
func MustApply(ctx context.Context, conn *sql.DB) {
	if err := ApplyMigrations(ctx, conn); err != nil {
		panic(err)
	}
}
func RunMigrations(ctx context.Context, conn *sql.DB) error { return ApplyMigrations(ctx, conn) }
