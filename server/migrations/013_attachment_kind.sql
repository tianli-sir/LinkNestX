-- Chat file messages whose immutable version name marks a picture carry
-- 'image' here so clients render them inline; NULL keeps the file card. No
-- MIME column is introduced: the preview route re-sniffs actual content, so a
-- forged extension degrades to the ordinary file card client-side.
ALTER TABLE messages ADD COLUMN attachment_kind TEXT NULL;
