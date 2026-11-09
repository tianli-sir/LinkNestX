CREATE TABLE transfer_records (
 actor_id TEXT NOT NULL,
 version_id TEXT NOT NULL,
 task_id TEXT NOT NULL,
 node_id TEXT NOT NULL,
 action TEXT NOT NULL CHECK(action IN ('server.sent','server.received')),
 bytes INTEGER NOT NULL DEFAULT 0 CHECK(bytes>=0),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 PRIMARY KEY(actor_id,version_id,task_id,node_id,action)
);
CREATE INDEX idx_transfer_updated ON transfer_records(updated_at);
