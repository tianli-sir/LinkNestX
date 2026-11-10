ALTER TABLE files ADD COLUMN listed INTEGER NOT NULL DEFAULT 1 CHECK(listed IN (0,1));
CREATE TABLE sharing_policies (
 scope TEXT NOT NULL CHECK(scope IN ('group','pool')),
 target_id TEXT NOT NULL,
 enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)),
 max_concurrent INTEGER NOT NULL DEFAULT 0 CHECK(max_concurrent BETWEEN 0 AND 32),
 bandwidth_limit_bps INTEGER NOT NULL DEFAULT 0 CHECK(bandwidth_limit_bps >= 0),
 PRIMARY KEY(scope,target_id)
);
-- Successful local saves are client claims, independent of per-source contribution.
ALTER TABLE client_transfer_records RENAME TO client_transfer_records_before_completion;
CREATE TABLE client_transfer_records (
 id TEXT PRIMARY KEY,
 actor_id TEXT NOT NULL,
 device_id TEXT NOT NULL,
 task_id TEXT NOT NULL,
 version_id TEXT NOT NULL,
 file_id TEXT NOT NULL,
 node_id TEXT NOT NULL,
 action TEXT NOT NULL CHECK(action IN ('peer.sent','peer.received','transfer.completed','download.completed')),
 bytes INTEGER NOT NULL CHECK(bytes>=0),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 UNIQUE(actor_id,device_id,task_id,version_id,node_id,action)
);
INSERT INTO client_transfer_records SELECT * FROM client_transfer_records_before_completion;
DROP TABLE client_transfer_records_before_completion;
CREATE INDEX idx_client_transfer_updated ON client_transfer_records(updated_at,id);
