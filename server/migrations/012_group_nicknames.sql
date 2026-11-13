-- Group-scoped display names override the global profile name inside one group
-- only. NULL falls back to profiles.display_name; direct conversations never
-- read this column (enforced in application queries, not by the schema).
ALTER TABLE group_members ADD COLUMN nickname TEXT NULL;
