CREATE TABLE peer_sources (
 device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
 version_id TEXT NOT NULL REFERENCES file_versions(id) ON DELETE CASCADE,
 user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 indexes_json TEXT NOT NULL,
 last_seen TEXT NOT NULL,
 PRIMARY KEY(device_id,version_id)
);
CREATE INDEX idx_peer_sources_version ON peer_sources(version_id,last_seen);

-- Aggregates negotiation lifecycle, never SDP/candidates or per-frame rows.
CREATE TABLE peer_transfers (
 connection_id TEXT PRIMARY KEY,
 version_id TEXT NOT NULL REFERENCES file_versions(id) ON DELETE CASCADE,
 requester_device_id TEXT NOT NULL,
 source_device_id TEXT NOT NULL,
 requester_user_id TEXT NOT NULL,
 started_at TEXT NOT NULL,
 ended_at TEXT,
 outcome TEXT NOT NULL DEFAULT 'negotiating'
);
CREATE INDEX idx_peer_transfers_started ON peer_transfers(started_at);
