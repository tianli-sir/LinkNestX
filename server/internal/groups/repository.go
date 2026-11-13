package groups

import (
	"context"
	"database/sql"
	"time"

	"github.com/linknestx/linknestx/server/internal/auth"
)

type Repository struct{ db *sql.DB }

func NewRepository(conn *sql.DB) *Repository { return &Repository{conn} }

// Recheck session/account inside the same transaction as the membership write.
func activeActor(ctx context.Context, tx *sql.Tx, actorID string) error {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.id=? AND s.user_id=? AND s.revoked_at IS NULL AND u.disabled=0 AND u.must_change_password=0 AND julianday(s.expires_at)>julianday('now')`, auth.SessionIDFromContext(ctx), actorID).Scan(&count)
	if err != nil {
		return err
	}
	if count == 0 {
		return auth.ErrUnauthorized
	}
	return nil
}
func isAdmin(ctx context.Context, tx *sql.Tx, actorID string) (bool, error) {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM user_roles ur JOIN roles r ON r.id=ur.role_id WHERE ur.user_id=? AND r.name='admin'`, actorID).Scan(&count)
	return count > 0, err
}
func (r *Repository) create(ctx context.Context, actorID, ownerID string, g Group) (string, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if err := activeActor(ctx, tx, actorID); err != nil {
		return "", err
	}
	admin, err := isAdmin(ctx, tx, actorID)
	if err != nil {
		return "", err
	}
	if !admin {
		return "", auth.ErrAdminRequired
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE id=? AND disabled=0`, ownerID).Scan(&count); err != nil {
		return "", err
	}
	if count == 0 {
		return "", ErrNotFound
	}
	at := g.CreatedAt.Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO groups(id,name,created_by,created_at) VALUES(?,?,?,?)`, g.ID, g.Name, g.CreatedBy, at); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO group_members(group_id,user_id,role,joined_at) VALUES(?,?,'owner',?)`, g.ID, ownerID, at); err != nil {
		return "", err
	}
	if err := auth.RecordAudit(ctx, tx, actorID, "group.created", g.ID, g.CreatedAt); err != nil {
		return "", err
	}
	messageID, err := appendSystemMessage(ctx, tx, actorID, g.ID, ownerID, "group.created", g.CreatedAt)
	if err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return messageID, nil
}
func (r *Repository) list(ctx context.Context, actorID string) ([]Group, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT g.id,g.name,g.created_by,g.created_at,EXISTS(SELECT 1 FROM group_members m WHERE m.group_id=g.id AND m.user_id=?) FROM groups g WHERE NOT EXISTS(SELECT 1 FROM direct_conversations d WHERE d.group_id=g.id) AND (EXISTS(SELECT 1 FROM group_members m WHERE m.group_id=g.id AND m.user_id=?) OR EXISTS(SELECT 1 FROM user_roles ur JOIN roles r ON r.id=ur.role_id WHERE ur.user_id=? AND r.name='admin')) ORDER BY g.created_at,g.id`, actorID, actorID, actorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Group{}
	for rows.Next() {
		var g Group
		var at string
		if err := rows.Scan(&g.ID, &g.Name, &g.CreatedBy, &at, &g.IsMember); err != nil {
			return nil, err
		}
		g.CreatedAt, err = time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return nil, err
		}
		result = append(result, g)
	}
	return result, rows.Err()
}
func (r *Repository) members(ctx context.Context, groupID string) ([]Member, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT gm.user_id,gm.role,gm.joined_at,gm.nickname,COALESCE(NULLIF(gm.nickname,''),NULLIF(p.display_name,''),u.username),p.account_number,p.avatar IS NOT NULL,u.disabled=1 FROM group_members gm JOIN users u ON u.id=gm.user_id JOIN profiles p ON p.user_id=gm.user_id WHERE gm.group_id=? ORDER BY gm.joined_at,gm.user_id`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Member{}
	for rows.Next() {
		var m Member
		var at string
		var nickname, account sql.NullString
		if err := rows.Scan(&m.UserID, &m.Role, &at, &nickname, &m.DisplayName, &account, &m.HasAvatar, &m.Disabled); err != nil {
			return nil, err
		}
		if nickname.Valid {
			value := nickname.String
			m.Nickname = &value
		}
		m.AccountNumber = account.String
		m.JoinedAt, err = time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, rows.Err()
}
func (r *Repository) changeMember(ctx context.Context, actorID, groupID, userID, role string, remove bool) (string, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if err := activeActor(ctx, tx, actorID); err != nil {
		return "", err
	}
	if direct, e := IsDirect(ctx, tx, groupID); e != nil {
		return "", e
	} else if direct {
		return "", ErrForbidden
	}
	admin, err := isAdmin(ctx, tx, actorID)
	if err != nil {
		return "", err
	}
	var owners int
	if !admin {
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM group_members WHERE group_id=? AND user_id=? AND role='owner'`, groupID, actorID).Scan(&owners); err != nil {
			return "", err
		}
		if owners == 0 {
			return "", ErrForbidden
		}
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM groups WHERE id=?`, groupID).Scan(&exists); err != nil {
		return "", err
	}
	if exists == 0 {
		return "", ErrNotFound
	}
	var existingRole string
	err = tx.QueryRowContext(ctx, `SELECT role FROM group_members WHERE group_id=? AND user_id=?`, groupID, userID).Scan(&existingRole)
	if err != nil && err != sql.ErrNoRows {
		return "", err
	}
	if (remove && existingRole == "") || (!remove && existingRole == role) {
		return "", nil
	}
	if existingRole == "owner" && (remove || role != "owner") {
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM group_members WHERE group_id=? AND role='owner'`, groupID).Scan(&owners); err != nil {
			return "", err
		}
		if owners <= 1 {
			return "", ErrLastOwner
		}
	}
	if !remove {
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE id=? AND disabled=0`, userID).Scan(&exists); err != nil {
			return "", err
		}
		if exists == 0 {
			return "", ErrNotFound
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO group_members(group_id,user_id,role,joined_at) VALUES(?,?,?,?) ON CONFLICT(group_id,user_id) DO UPDATE SET role=excluded.role`, groupID, userID, role, time.Now().UTC().Format(time.RFC3339Nano))
		if err != nil {
			return "", err
		}
	}
	action := "group.member_updated"
	if remove {
		action = "group.member_removed"
	}
	if err := auth.RecordAudit(ctx, tx, actorID, action, groupID+"/"+userID, time.Now()); err != nil {
		return "", err
	}
	event := "group.member_added"
	if remove {
		event = "group.member_removed"
	} else if existingRole != "" {
		event = "group.owner_changed"
	}
	// The system message resolves the departing member's group nickname from
	// group_members, so remove the membership only after the body is written.
	messageID, err := appendSystemMessage(ctx, tx, actorID, groupID, userID, event, time.Now())
	if err != nil {
		return "", err
	}
	if remove {
		if _, err = tx.ExecContext(ctx, `DELETE FROM group_members WHERE group_id=? AND user_id=?`, groupID, userID); err != nil {
			return "", err
		}
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return messageID, nil
}
