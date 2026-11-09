package chat

import (
	"context"
	"database/sql"
	"time"

	"github.com/linknestx/linknestx/server/internal/auth"
	"github.com/linknestx/linknestx/server/internal/groups"
)

type Repository struct{ db *sql.DB }

func NewRepository(conn *sql.DB) *Repository { return &Repository{conn} }

type scanner interface{ Scan(...any) error }

// file_versions.status retains the lifecycle cause when cleanup also marks the
// logical file unavailable; automatic expiry must not become explicit deletion.
const messageColumns = `id,sequence,group_id,sender_id,client_message_id,body,created_at,expired_at,file_id,file_version_id,(CASE WHEN file_version_id IS NULL THEN NULL WHEN expired_at IS NOT NULL THEN 'expired' ELSE COALESCE((SELECT CASE WHEN v.status='published' AND (v.expires_at IS NULL OR julianday(v.expires_at)>julianday('now')) AND f.deleted_at IS NULL THEN 'available' WHEN v.status='expired' THEN 'expired' WHEN v.status='deleted' OR f.deleted_at IS NOT NULL THEN 'deleted' ELSE 'expired' END FROM file_versions v JOIN files f ON f.id=v.file_id WHERE v.id=messages.file_version_id),'expired') END)`

func scanMessage(row scanner) (Message, error) {
	var m Message
	var created string
	var expired sql.NullString
	if err := row.Scan(&m.ID, &m.Sequence, &m.GroupID, &m.SenderID, &m.ClientMessageID, &m.Body, &created, &expired, &m.FileID, &m.FileVersionID, &m.AttachmentStatus); err != nil {
		return m, err
	}
	var err error
	m.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return m, err
	}
	if expired.Valid {
		at, e := time.Parse(time.RFC3339Nano, expired.String)
		if e != nil {
			return m, e
		}
		m.ExpiredAt = &at
	}
	m.Cursor = encodeCursor(m.GroupID, m.Sequence)
	return m, nil
}
func memberInTransaction(ctx context.Context, tx *sql.Tx, actorID, groupID string) error {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM group_members m JOIN users u ON u.id=m.user_id JOIN sessions s ON s.user_id=u.id WHERE m.group_id=? AND m.user_id=? AND u.disabled=0 AND s.id=? AND s.revoked_at IS NULL AND julianday(s.expires_at)>julianday('now')`, groupID, actorID, auth.SessionIDFromContext(ctx)).Scan(&count)
	if err != nil {
		return err
	}
	if count == 0 {
		return groups.ErrForbidden
	}
	return nil
}
func (r *Repository) append(ctx context.Context, m Message) (Message, bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, false, err
	}
	defer tx.Rollback()
	if err := memberInTransaction(ctx, tx, m.SenderID, m.GroupID); err != nil {
		return Message{}, false, err
	}
	existing, err := scanMessage(tx.QueryRowContext(ctx, `SELECT `+messageColumns+` FROM messages WHERE sender_id=? AND client_message_id=?`, m.SenderID, m.ClientMessageID))
	if err == nil {
		if existing.GroupID != m.GroupID || existing.FileVersionID != nil || (existing.Body != nil && *existing.Body != *m.Body) {
			return Message{}, false, ErrConflict
		}
		return existing, false, nil
	}
	if err != sql.ErrNoRows {
		return Message{}, false, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO messages(id,group_id,sender_id,client_message_id,body,created_at) VALUES(?,?,?,?,?,?)`, m.ID, m.GroupID, m.SenderID, m.ClientMessageID, *m.Body, m.CreatedAt.Format(timestampLayout))
	if err != nil {
		return Message{}, false, err
	}
	m.Sequence, err = result.LastInsertId()
	if err != nil {
		return Message{}, false, err
	}
	m.Cursor = encodeCursor(m.GroupID, m.Sequence)
	if err := tx.Commit(); err != nil {
		return Message{}, false, err
	}
	return m, true, nil
}
func (r *Repository) list(ctx context.Context, actorID, groupID string, after int64, limit int) (MessagePage, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return MessagePage{}, err
	}
	defer tx.Rollback()
	if err := memberInTransaction(ctx, tx, actorID, groupID); err != nil {
		return MessagePage{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+messageColumns+` FROM messages WHERE group_id=? AND sequence>? ORDER BY sequence LIMIT ?`, groupID, after, limit+1)
	if err != nil {
		return MessagePage{}, err
	}
	defer rows.Close()
	page := MessagePage{Messages: []Message{}, NextCursor: encodeCursor(groupID, after)}
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return MessagePage{}, err
		}
		if len(page.Messages) == limit {
			page.HasMore = true
			break
		}
		page.Messages = append(page.Messages, m)
		page.NextCursor = m.Cursor
	}
	return page, rows.Err()
}
func (r *Repository) expire(ctx context.Context, before, now time.Time) (int64, error) {
	result, err := r.db.ExecContext(ctx, `UPDATE messages SET body=NULL,file_id=NULL,file_version_id=NULL,expired_at=? WHERE created_at<? AND expired_at IS NULL`, now.Format(timestampLayout), before.Format(timestampLayout))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (r *Repository) sharedGroups(ctx context.Context, a, b string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT a.group_id FROM group_members a JOIN group_members b ON a.group_id=b.group_id WHERE a.user_id=? AND b.user_id=? ORDER BY a.group_id`, a, b)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
