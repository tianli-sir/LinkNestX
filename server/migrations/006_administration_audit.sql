ALTER TABLE audit_events ADD COLUMN target_id TEXT NOT NULL DEFAULT '';
ALTER TABLE audit_events ADD COLUMN device_id TEXT NOT NULL DEFAULT '';
ALTER TABLE audit_events ADD COLUMN source TEXT NOT NULL DEFAULT 'server-observed';
ALTER TABLE transfer_records RENAME TO transfer_records_legacy;
CREATE TABLE transfer_records (
 actor_id TEXT NOT NULL,
 device_id TEXT NOT NULL DEFAULT '',
 version_id TEXT NOT NULL,
 task_id TEXT NOT NULL,
 node_id TEXT NOT NULL,
 action TEXT NOT NULL CHECK(action IN ('server.sent','server.received')),
 bytes INTEGER NOT NULL DEFAULT 0 CHECK(bytes>=0),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 PRIMARY KEY(actor_id,device_id,version_id,task_id,node_id,action)
);
INSERT INTO transfer_records(actor_id,version_id,task_id,node_id,action,bytes,created_at,updated_at)
 SELECT actor_id,version_id,task_id,node_id,action,bytes,created_at,updated_at FROM transfer_records_legacy;
DROP TABLE transfer_records_legacy;
CREATE INDEX idx_transfer_updated ON transfer_records(updated_at);
CREATE INDEX idx_audit_actor_device ON audit_events(actor_id,device_id,created_at,id);
CREATE INDEX idx_audit_action_created ON audit_events(action,created_at,id);

-- These are cumulative client claims, distinct from observed server writes.
-- Attribution survives account/device/file deletion for the audit retention period.
CREATE TABLE client_transfer_records (
 id TEXT PRIMARY KEY,
 actor_id TEXT NOT NULL,
 device_id TEXT NOT NULL,
 task_id TEXT NOT NULL,
 version_id TEXT NOT NULL,
 file_id TEXT NOT NULL,
 node_id TEXT NOT NULL,
 action TEXT NOT NULL CHECK(action IN ('peer.sent','peer.received','transfer.completed')),
 bytes INTEGER NOT NULL CHECK(bytes>=0),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 UNIQUE(actor_id,device_id,task_id,version_id,node_id,action)
);
CREATE INDEX idx_client_transfer_updated ON client_transfer_records(updated_at,id);
CREATE TRIGGER audit_events_no_update BEFORE UPDATE ON audit_events
BEGIN SELECT RAISE(ABORT,'audit events are append-only'); END;
