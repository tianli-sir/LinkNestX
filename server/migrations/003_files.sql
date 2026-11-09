CREATE TABLE files (
 id TEXT PRIMARY KEY,
 group_id TEXT NOT NULL REFERENCES groups(id),
 name TEXT NOT NULL,
 created_by TEXT NOT NULL REFERENCES users(id),
 visibility TEXT NOT NULL DEFAULT 'group' CHECK(visibility IN ('group','pool')),
 current_version_id TEXT,
 created_at TEXT NOT NULL,
 deleted_at TEXT
);
CREATE INDEX idx_files_group ON files(group_id,created_at);
CREATE TABLE file_versions (
 id TEXT PRIMARY KEY,
 file_id TEXT NOT NULL REFERENCES files(id),
 created_by TEXT NOT NULL REFERENCES users(id),
 name TEXT NOT NULL,
 size INTEGER NOT NULL CHECK(size>=0 AND size<=20000000000),
 sha256 TEXT NOT NULL,
 chunk_size INTEGER NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('pending','published','deleted','expired')),
 created_at TEXT NOT NULL,
 published_at TEXT,
 expires_at TEXT,
 deleted_at TEXT
);
CREATE INDEX idx_file_versions_file ON file_versions(file_id,created_at);
CREATE INDEX idx_file_versions_expiry ON file_versions(status,expires_at);
CREATE TABLE chunk_objects(hash TEXT PRIMARY KEY,created_at TEXT NOT NULL);
CREATE TABLE file_chunks (
 version_id TEXT NOT NULL REFERENCES file_versions(id) ON DELETE CASCADE,
 chunk_index INTEGER NOT NULL,
 hash TEXT NOT NULL,
 size INTEGER NOT NULL CHECK(size>0 AND size<=16777216),
 PRIMARY KEY(version_id,chunk_index)
);
CREATE INDEX idx_file_chunks_hash ON file_chunks(hash);
CREATE TABLE upload_sessions (
 id TEXT PRIMARY KEY,
 version_id TEXT NOT NULL UNIQUE REFERENCES file_versions(id) ON DELETE CASCADE,
 user_id TEXT NOT NULL REFERENCES users(id),
 created_at TEXT NOT NULL,
 expires_at TEXT NOT NULL,
 completed_at TEXT
);
CREATE TABLE upload_receipts (
 upload_id TEXT NOT NULL REFERENCES upload_sessions(id) ON DELETE CASCADE,
 chunk_index INTEGER NOT NULL,
 received_at TEXT NOT NULL,
 PRIMARY KEY(upload_id,chunk_index)
);
CREATE TABLE file_memberships (
 file_id TEXT NOT NULL REFERENCES files(id) ON DELETE CASCADE,
 group_id TEXT NOT NULL REFERENCES groups(id),
 PRIMARY KEY(file_id,group_id)
);
CREATE TABLE audit_events (
 id TEXT PRIMARY KEY,
 actor_id TEXT,
 action TEXT NOT NULL,
 file_id TEXT,
 version_id TEXT,
 task_id TEXT,
 node_id TEXT,
 bytes INTEGER NOT NULL DEFAULT 0 CHECK(bytes>=0),
 created_at TEXT NOT NULL
);
CREATE INDEX idx_audit_created ON audit_events(created_at);
CREATE INDEX idx_audit_file ON audit_events(file_id,created_at);
ALTER TABLE messages ADD COLUMN file_id TEXT;
ALTER TABLE messages ADD COLUMN file_version_id TEXT;
