package chat

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
	"github.com/linknestx/linknestx/server/internal/auth"
)

type AttachmentInput struct{ GroupID, ClientMessageID, FileID, FileVersionID, Name string }

// AppendAttachment commits verified file publication and its immutable message
// association together. publish runs inside this service's transaction; the
// caller must not call database services from inside that callback.
func (s *ChatService) AppendAttachment(ctx context.Context, actorID string, in AttachmentInput, publish func(*sql.Tx) error) (Message, error) {
	if err := s.auth.ValidateActor(ctx, actorID); err != nil {
		return Message{}, err
	}
	if in.GroupID == "" || in.ClientMessageID == "" || len(in.ClientMessageID) > 128 || in.FileID == "" || in.FileVersionID == "" || publish == nil {
		return Message{}, ErrInvalidInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.repo.db.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, err
	}
	defer tx.Rollback()
	if err = attachmentPublisherInTransaction(ctx, tx, actorID, in.GroupID); err != nil {
		return Message{}, err
	}
	if err = publish(tx); err != nil {
		return Message{}, err
	}
	existing, err := scanMessage(tx.QueryRowContext(ctx, `SELECT `+messageColumns+` FROM messages WHERE sender_id=? AND client_message_id=?`, actorID, in.ClientMessageID))
	if err == nil {
		if existing.GroupID != in.GroupID || existing.FileID == nil || existing.FileVersionID == nil || *existing.FileID != in.FileID || *existing.FileVersionID != in.FileVersionID {
			return Message{}, ErrConflict
		}
		return existing, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Message{}, err
	}
	var n int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM file_versions v JOIN files f ON f.id=v.file_id WHERE v.id=? AND f.id=? AND f.group_id=? AND v.status='published' AND f.deleted_at IS NULL AND (v.expires_at IS NULL OR julianday(v.expires_at)>julianday('now'))`, in.FileVersionID, in.FileID, in.GroupID).Scan(&n); err != nil {
		return Message{}, err
	}
	if n == 0 {
		return Message{}, ErrInvalidInput
	}
	m := Message{ID: uuid.NewString(), GroupID: in.GroupID, SenderID: actorID, ClientMessageID: in.ClientMessageID, Body: &in.Name, CreatedAt: s.now().UTC(), FileID: &in.FileID, FileVersionID: &in.FileVersionID}
	status := "available"
	m.AttachmentStatus = &status
	result, err := tx.ExecContext(ctx, `INSERT INTO messages(id,group_id,sender_id,client_message_id,body,created_at,file_id,file_version_id) VALUES(?,?,?,?,?,?,?,?)`, m.ID, m.GroupID, m.SenderID, m.ClientMessageID, in.Name, m.CreatedAt.Format(timestampLayout), in.FileID, in.FileVersionID)
	if err != nil {
		return Message{}, err
	}
	m.Sequence, err = result.LastInsertId()
	if err != nil {
		return Message{}, err
	}
	m.Cursor = encodeCursor(m.GroupID, m.Sequence)
	if err = tx.Commit(); err != nil {
		return Message{}, err
	}
	if s.hub != nil {
		s.hub.Publish(m)
	}
	return m, nil
}

// This permission is limited to server-authored file-publication attachments.
// Ordinary text and history continue to require group membership. The caller's
// publication callback rechecks file management, and the immutable file/version
// and original group association are verified before any message is committed.
func attachmentPublisherInTransaction(ctx context.Context, tx *sql.Tx, actorID, groupID string) error {
	err := memberInTransaction(ctx, tx, actorID, groupID)
	if err == nil {
		return nil
	}
	var count int
	adminErr := tx.QueryRowContext(ctx, `SELECT count(*) FROM user_roles ur JOIN roles r ON r.id=ur.role_id JOIN users u ON u.id=ur.user_id JOIN sessions s ON s.user_id=u.id WHERE ur.user_id=? AND r.name='admin' AND u.disabled=0 AND u.must_change_password=0 AND s.id=? AND s.device_id=? AND s.revoked_at IS NULL AND julianday(s.expires_at)>julianday('now')`, actorID, auth.SessionIDFromContext(ctx), auth.DeviceIDFromContext(ctx)).Scan(&count)
	if adminErr != nil {
		return adminErr
	}
	if count == 0 {
		return err
	}
	return nil
}
