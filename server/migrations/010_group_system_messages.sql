ALTER TABLE messages ADD COLUMN kind TEXT NOT NULL DEFAULT 'text' CHECK(kind IN ('text','system'));
ALTER TABLE messages ADD COLUMN system_type TEXT NOT NULL DEFAULT '';
