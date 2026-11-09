CREATE TABLE groups (
 id TEXT PRIMARY KEY,
 name TEXT NOT NULL,
 created_by TEXT NOT NULL REFERENCES users(id),
 created_at TEXT NOT NULL
);
CREATE TABLE group_members (
 group_id TEXT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
 user_id TEXT NOT NULL REFERENCES users(id),
 role TEXT NOT NULL CHECK(role IN ('owner','member')),
 joined_at TEXT NOT NULL,
 PRIMARY KEY(group_id,user_id)
);
CREATE INDEX idx_group_members_user ON group_members(user_id,group_id);
CREATE TABLE messages (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,
 id TEXT NOT NULL UNIQUE,
 group_id TEXT NOT NULL REFERENCES groups(id),
 sender_id TEXT NOT NULL REFERENCES users(id),
 client_message_id TEXT NOT NULL,
 body TEXT,
 created_at TEXT NOT NULL,
 expired_at TEXT,
 UNIQUE(sender_id,client_message_id)
);
CREATE INDEX idx_messages_group_created_id ON messages(group_id,created_at,id);
CREATE INDEX idx_messages_group_sequence ON messages(group_id,sequence);
