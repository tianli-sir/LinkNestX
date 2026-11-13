package auth

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// DELETED_USER_ID is the shared tombstone account. Hard deletions re-point
// history at it so rows stay referable while the personal account is gone. The
// account is disabled and never issues sessions.
const DELETED_USER_ID = "e59ec6d2-8d67-4f14-9a1c-2b6d0f5a4c31"

var (
	ErrCannotDeleteSelf = errors.New("cannot delete the current administrator account")
	ErrAdminProtected   = errors.New("administrators must be removed from the admin role before deletion")
)

// UserDeletion summarizes a committed deletion so the router composer can
// refresh affected clients; the auth package stays free of the event hub.
type UserDeletion struct {
	DeletedUser   string
	Conversations []string
	Peers         []string
}

func (s *AuthService) SetUserDeletedHandler(handler func(context.Context, UserDeletion)) {
	s.userDeleted = handler
}

// DeleteUser hard-deletes one non-administrator account in a single
// transaction. History is anonymised onto the tombstone (client_message_id is
// rewritten alongside sender_id because the per-sender uniqueness key would
// otherwise collide across deleted users), groups owned by the target are
// dissolved — members, messages and file memberships are cleared while file
// and version records survive with tombstone attribution — friendships and
// direct conversations disappear for both sides, and the users row is removed
// with cascades taking devices, sessions, roles, reads and peer sources.
//
// The dissolved groups row itself is retained: messages and files hold
// RESTRICT foreign keys to it and the files CHECK constraint forbids moving
// group files elsewhere, so keeping the empty row is the only way to honour
// "files are never deleted".
func (s *AuthService) DeleteUser(ctx context.Context, userID string) error {
	if err := validateAccountID(userID); err != nil {
		return err
	}
	tx, actor, err := s.accountTransaction(ctx, true, false, true)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if userID == actor.user.ID {
		return ErrCannotDeleteSelf
	}
	if userID == DELETED_USER_ID {
		return ErrInvalidInput
	}
	var targetAdmin bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM user_roles ur JOIN roles r ON r.id=ur.role_id WHERE ur.user_id=users.id AND r.name='admin') FROM users WHERE id=?`, userID).Scan(&targetAdmin)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrUserNotFound
	}
	if err != nil {
		return err
	}
	if targetAdmin {
		return ErrAdminProtected
	}
	direct := map[string]bool{}
	if rows, e := tx.QueryContext(ctx, `SELECT group_id FROM direct_conversations WHERE pair_low=? OR pair_high=?`, userID, userID); e == nil {
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				direct[id] = true
			}
		}
		rows.Close()
	}
	dissolved := []string{}
	if rows, e := tx.QueryContext(ctx, `SELECT id FROM groups WHERE created_by=? AND id NOT IN(SELECT group_id FROM direct_conversations)`, userID); e == nil {
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				dissolved = append(dissolved, id)
			}
		}
		rows.Close()
	}
	dissolvedSet := map[string]bool{}
	for _, id := range dissolved {
		dissolvedSet[id] = true
	}
	shared := []string{}
	if rows, e := tx.QueryContext(ctx, `SELECT group_id FROM group_members WHERE user_id=?`, userID); e == nil {
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil && !direct[id] && !dissolvedSet[id] {
				shared = append(shared, id)
			}
		}
		rows.Close()
	}
	peers := []string{}
	seenPeer := map[string]bool{}
	if rows, e := tx.QueryContext(ctx, `SELECT CASE WHEN pair_low=? THEN pair_high ELSE pair_low END FROM friendships WHERE ? IN(pair_low,pair_high) UNION SELECT CASE WHEN pair_low=? THEN pair_high ELSE pair_low END FROM direct_conversations WHERE ? IN(pair_low,pair_high)`, userID, userID, userID, userID); e == nil {
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil && !seenPeer[id] {
				seenPeer[id] = true
				peers = append(peers, id)
			}
		}
		rows.Close()
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `INSERT INTO users(id,username,email,password_hash,disabled,must_change_password,created_at,updated_at) VALUES(?,'deleted-user','','',1,1,?,?) ON CONFLICT(id) DO NOTHING`, DELETED_USER_ID, now, now); err != nil {
		return err
	}
	// The user_profile AFTER INSERT trigger already created a profile row with
	// display_name=username when the tombstone user was inserted, so the
	// display name is pinned with an upsert. account_number is never written:
	// the account_number_format trigger rejects any UPDATE that mentions the
	// column, and the trigger-created row already carries NULL.
	if _, err = tx.ExecContext(ctx, `INSERT INTO profiles(user_id,display_name) VALUES(?,'已注销用户') ON CONFLICT(user_id) DO UPDATE SET display_name=excluded.display_name`, DELETED_USER_ID); err != nil {
		return err
	}
	for _, group := range dissolved {
		if _, err = tx.ExecContext(ctx, `DELETE FROM messages WHERE group_id=?`, group); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM file_memberships WHERE group_id=?`, group); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM group_members WHERE group_id=?`, group); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE messages SET sender_id=?,client_message_id='deleted:'||id WHERE sender_id=?`, DELETED_USER_ID, userID); err != nil {
		return err
	}
	for _, statement := range []string{
		`UPDATE files SET created_by=? WHERE created_by=?`,
		`UPDATE file_versions SET created_by=? WHERE created_by=?`,
		`UPDATE documents SET author_id=? WHERE author_id=?`,
		`UPDATE groups SET created_by=? WHERE created_by=?`,
	} {
		if _, err = tx.ExecContext(ctx, statement, DELETED_USER_ID, userID); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM upload_sessions WHERE user_id=?`, userID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM group_members WHERE user_id=?`, userID); err != nil {
		return err
	}
	// Direct conversations vanish entirely, so the peer's membership rows must
	// go too — otherwise the orphaned groups row resurfaces as an empty group
	// in the peer's conversation list.
	for id := range direct {
		if _, err = tx.ExecContext(ctx, `DELETE FROM group_members WHERE group_id=?`, id); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM friend_requests WHERE from_id=? OR to_id=?`, userID, userID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM friendships WHERE pair_low=? OR pair_high=?`, userID, userID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM direct_conversations WHERE pair_low=? OR pair_high=?`, userID, userID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM users WHERE id=?`, userID); err != nil {
		return err
	}
	if err = RecordAudit(ctx, tx, actor.user.ID, "account.deleted", userID, s.now()); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if s.userDeleted != nil {
		conversations := append(append([]string{}, dissolved...), shared...)
		s.userDeleted(ctx, UserDeletion{DeletedUser: userID, Conversations: conversations, Peers: peers})
	}
	return nil
}
