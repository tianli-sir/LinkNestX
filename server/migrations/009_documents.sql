CREATE TABLE documents (
 id TEXT PRIMARY KEY,
 author_id TEXT NOT NULL REFERENCES users(id),
 title TEXT NOT NULL,
 body TEXT NOT NULL,
 visibility TEXT NOT NULL CHECK(visibility IN ('private','friends','team')),
 status TEXT NOT NULL CHECK(status IN ('draft','published')),
 curated INTEGER NOT NULL DEFAULT 0 CHECK(curated IN(0,1)),
 revision INTEGER NOT NULL DEFAULT 1 CHECK(revision>0),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 published_at TEXT,
 deleted_at TEXT,
 CHECK(curated=0 OR (visibility='team' AND status='published'))
);
CREATE INDEX idx_documents_feed ON documents(updated_at DESC,id DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_documents_author ON documents(author_id,updated_at DESC);
CREATE TABLE files_with_documents (
 id TEXT PRIMARY KEY,
 group_id TEXT REFERENCES groups(id),
 document_id TEXT REFERENCES documents(id),
 name TEXT NOT NULL,
 created_by TEXT NOT NULL REFERENCES users(id),
 visibility TEXT NOT NULL DEFAULT 'group' CHECK(visibility IN ('group','pool','document')),
 current_version_id TEXT,
 created_at TEXT NOT NULL,
 deleted_at TEXT,
 listed INTEGER NOT NULL DEFAULT 1 CHECK(listed IN(0,1)),
 CHECK((document_id IS NULL AND group_id IS NOT NULL AND visibility IN('group','pool')) OR (document_id IS NOT NULL AND group_id IS NULL AND visibility='document'))
);
INSERT INTO files_with_documents(id,group_id,name,created_by,visibility,current_version_id,created_at,deleted_at,listed) SELECT id,group_id,name,created_by,visibility,current_version_id,created_at,deleted_at,listed FROM files;
DROP TABLE files;
ALTER TABLE files_with_documents RENAME TO files;
CREATE INDEX idx_files_group ON files(group_id,created_at);
CREATE INDEX idx_files_document ON files(document_id,created_at);
