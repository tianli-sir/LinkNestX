package db

import (
	"context"
	"database/sql"
	"fmt"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
)

// Open opens a SQLite database, enables foreign keys and applies conservative
// connection settings suitable for concurrent LAN requests.
func Open(path string) (*sql.DB, error) {
	if path == "" {
		return nil, fmt.Errorf("sqlite path is empty")
	}
	if path != ":memory:" && path != "file::memory:?cache=shared" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;`); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func Close(db *sql.DB) error {
	if db == nil {
		return nil
	}
	return db.Close()
}
func Ping(ctx context.Context, db *sql.DB) error { return db.PingContext(ctx) }

// OpenSQLite is an explicit alias for Open.
func OpenSQLite(path string) (*sql.DB, error) { return Open(path) }
