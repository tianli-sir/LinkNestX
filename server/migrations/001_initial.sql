PRAGMA foreign_keys = ON;
PRAGMA journal_mode = WAL;

CREATE TABLE IF NOT EXISTS users (
 id TEXT PRIMARY KEY,
 username TEXT NOT NULL UNIQUE,
 email TEXT NOT NULL DEFAULT '',
 password_hash TEXT NOT NULL,
 disabled INTEGER NOT NULL DEFAULT 0 CHECK (disabled IN (0,1)),
 must_change_password INTEGER NOT NULL DEFAULT 1 CHECK (must_change_password IN (0,1)),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS devices (
 id TEXT PRIMARY KEY,
 user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 name TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 last_seen_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
 id TEXT PRIMARY KEY,
 user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
 refresh_token_hash TEXT NOT NULL UNIQUE,
 created_at TEXT NOT NULL,
 expires_at TEXT NOT NULL,
 revoked_at TEXT,
 rotated_from TEXT REFERENCES sessions(id) ON DELETE SET NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_refresh_hash ON sessions(refresh_token_hash);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);
CREATE TABLE IF NOT EXISTS roles (
 id TEXT PRIMARY KEY,
 name TEXT NOT NULL UNIQUE
);
CREATE TABLE IF NOT EXISTS user_roles (
 user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 role_id TEXT NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
 PRIMARY KEY(user_id, role_id)
);
INSERT OR IGNORE INTO roles(id,name) VALUES ('8f4d0309-8d87-47d0-839b-6bcc2b294617','admin'), ('d58b62b3-e61b-420d-8cc3-a565fb6b7a17','user');

CREATE TABLE IF NOT EXISTS bootstrap_state (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),
 completed_at TEXT NOT NULL
);
