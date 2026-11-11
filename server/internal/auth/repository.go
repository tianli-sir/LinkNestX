package auth

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

type User struct {
	AccountNumber      string    `json:"accountNumber"`
	DisplayName        string    `json:"displayName"`
	AvatarURL          string    `json:"avatarUrl,omitempty"`
	ID                 string    `json:"id"`
	Username           string    `json:"username"`
	Email              string    `json:"email,omitempty"`
	Roles              []string  `json:"roles,omitempty"`
	Disabled           bool      `json:"disabled"`
	CreatedAt          time.Time `json:"createdAt"`
	MustChangePassword bool      `json:"mustChangePassword"`
}

// Repository stores local account and session records.
type Repository struct{ db *sql.DB }

func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }
func NewRepo(db *sql.DB) *Repository       { return NewRepository(db) }
func (r *Repository) DB() *sql.DB          { return r.db }

func scanUser(ctx context.Context, r *Repository, row *sql.Row) (User, error) {
	var u User
	var disabled int
	var created string
	if err := row.Scan(&u.ID, &u.Username, &u.Email, &disabled, &u.MustChangePassword, &created); err != nil {
		return u, err
	}
	u.Disabled = disabled != 0
	u.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	if err := r.profile(ctx, &u); err != nil {
		return u, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT r.name FROM roles r JOIN user_roles ur ON ur.role_id=r.id WHERE ur.user_id=? ORDER BY r.name`, u.ID)
	if err != nil {
		return u, err
	}
	defer rows.Close()
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			return u, err
		}
		u.Roles = append(u.Roles, role)
	}
	return u, rows.Err()
}
func (r *Repository) userByID(ctx context.Context, id string) (User, error) {
	return scanUser(ctx, r, r.db.QueryRowContext(ctx, `SELECT id,username,email,disabled,must_change_password,created_at FROM users WHERE id=?`, id))
}
func (r *Repository) userByLogin(ctx context.Context, login string) (User, string, error) {
	var u User
	var disabled int
	var created, hash string
	err := r.db.QueryRowContext(ctx, `SELECT id,username,email,password_hash,disabled,must_change_password,created_at FROM users WHERE lower(username)=lower(?) OR (email<>'' AND lower(email)=lower(?)) OR id=(SELECT user_id FROM profiles WHERE account_number=?) LIMIT 1`, login, login, login).Scan(&u.ID, &u.Username, &u.Email, &hash, &disabled, &u.MustChangePassword, &created)
	if err != nil {
		return u, "", err
	}
	u.Disabled = disabled != 0
	u.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	if err := r.profile(ctx, &u); err != nil {
		return u, "", err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT r.name FROM roles r JOIN user_roles ur ON ur.role_id=r.id WHERE ur.user_id=? ORDER BY r.name`, u.ID)
	if err != nil {
		return u, "", err
	}
	defer rows.Close()
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			return u, "", err
		}
		u.Roles = append(u.Roles, role)
	}
	return u, hash, rows.Err()
}
func (r *Repository) createUser(ctx context.Context, in CreateUserInput, hash, id string, roles []string, at time.Time, actorID, actorSessionID string, bootstrap bool) (User, error) {
	now := at.UTC().Format(time.RFC3339Nano)
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	if bootstrap {
		result, err := tx.ExecContext(ctx, `INSERT INTO bootstrap_state(singleton, completed_at) SELECT 1, ? WHERE NOT EXISTS (SELECT 1 FROM users) ON CONFLICT(singleton) DO NOTHING`, now)
		if err != nil {
			return User{}, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return User{}, err
		}
		if count != 1 {
			return User{}, ErrBootstrapComplete
		}
	} else {
		// Acquire the write lock before rechecking current administrator state.
		if _, err := tx.ExecContext(ctx, "UPDATE sessions SET id=id WHERE id=?", actorSessionID); err != nil {
			return User{}, err
		}
		var authorized int
		err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users u
   JOIN user_roles ur ON ur.user_id=u.id JOIN roles r ON r.id=ur.role_id
   JOIN sessions s ON s.user_id=u.id
   WHERE u.id=? AND u.disabled=0 AND u.must_change_password=0 AND r.name='admin' AND s.id=?
    AND s.revoked_at IS NULL AND julianday(s.expires_at)>julianday(?)`, actorID, actorSessionID, now).Scan(&authorized)
		if err != nil {
			return User{}, err
		}
		if authorized == 0 {
			return User{}, ErrAdminRequired
		}
	}
	var loginCollision bool
	if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM profiles WHERE account_number=? OR (?<>'' AND account_number=?))", in.Username, in.Email, in.Email).Scan(&loginCollision); err != nil {
		return User{}, err
	}
	if loginCollision {
		return User{}, ErrAccountNumberConflict
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO users(id,username,email,password_hash,disabled,must_change_password,created_at,updated_at) VALUES(?,?,?,?,0,1,?,?)`, id, in.Username, in.Email, hash, now, now)
	if err != nil {
		return User{}, err
	}
	accountNumber, err := allocateAccountNumber(ctx, tx, id, in.AccountNumber)
	if err != nil {
		return User{}, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE profiles SET account_number=? WHERE user_id=?", accountNumber, id); err != nil {
		return User{}, err
	}
	for _, role := range roles {
		var roleID string
		err = tx.QueryRowContext(ctx, `SELECT id FROM roles WHERE name=?`, role).Scan(&roleID)
		if errors.Is(err, sql.ErrNoRows) {
			roleID = newID()
			_, err = tx.ExecContext(ctx, `INSERT INTO roles(id,name) VALUES(?,?)`, roleID, role)
		}
		if err != nil {
			return User{}, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO user_roles(user_id,role_id) VALUES(?,?)`, id, roleID); err != nil {
			return User{}, err
		}
	}
	if err = RecordAudit(ctx, tx, actorID, "account.created", id, at); err != nil {
		return User{}, err
	}
	if err = tx.Commit(); err != nil {
		return User{}, err
	}
	return r.userByID(ctx, id)
}
func (r *Repository) isAdmin(ctx context.Context, id string) (bool, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM user_roles ur JOIN roles ro ON ro.id=ur.role_id JOIN users u ON u.id=ur.user_id WHERE ur.user_id=? AND ro.name='admin' AND u.disabled=0`, id).Scan(&n)
	return n > 0, err
}
func (r *Repository) ensureDevice(ctx context.Context, userID, deviceID string, at time.Time) (string, error) {
	if strings.TrimSpace(deviceID) == "" {
		deviceID = newID()
	} else {
		id, err := uuid.Parse(deviceID)
		if err != nil || id == uuid.Nil {
			return "", ErrInvalidDevice
		}
		deviceID = id.String()
	}
	now := at.UTC().Format(time.RFC3339Nano)
	result, err := r.db.ExecContext(ctx, `INSERT INTO devices(id,user_id,name,created_at,last_seen_at) VALUES(?,?,'',?,?)
  ON CONFLICT(id) DO UPDATE SET last_seen_at=excluded.last_seen_at WHERE devices.user_id=excluded.user_id`, deviceID, userID, now, now)
	if err != nil {
		return "", err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return "", err
	}
	if count != 1 {
		return "", ErrInvalidDevice
	}
	return deviceID, nil
}

type sessionWriter interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func insertSession(ctx context.Context, writer sessionWriter, session Session, now time.Time, rotated string) error {
	_, err := writer.ExecContext(ctx, `INSERT INTO sessions(id,user_id,device_id,refresh_token_hash,created_at,expires_at,rotated_from,client_type,csrf_token) VALUES(?,?,?,?,?,?,?,?,?)`, session.ID, session.User.ID, session.DeviceID, hashToken(session.RefreshToken), now.UTC().Format(time.RFC3339Nano), session.ExpiresAt.UTC().Format(time.RFC3339Nano), nullString(rotated), session.ClientType, session.CSRFToken)
	return err
}
func (r *Repository) createSession(ctx context.Context, session Session, now time.Time, expectedHash string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE users SET id=id WHERE id=?`, session.User.ID); err != nil {
		return err
	}
	var hash string
	var disabled bool
	if err := tx.QueryRowContext(ctx, `SELECT password_hash,disabled FROM users WHERE id=?`, session.User.ID).Scan(&hash, &disabled); err != nil {
		return err
	}
	if disabled {
		return ErrDisabledAccount
	}
	if hash != expectedHash {
		return ErrInvalidCredentials
	}
	if err := insertSession(ctx, tx, session, now, ""); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_id,device_id,created_at) VALUES(?,?,'account.login',?,?,?)`, newID(), session.User.ID, session.ID, session.DeviceID, now.UTC().Format("2006-01-02T15:04:05.000000000Z")); err != nil {
		return err
	}
	return tx.Commit()
}

// The conditional update acquires the write lock before reading session state.
// Consumption and replacement commit together; concurrent callers can win once.
func (r *Repository) rotateSession(ctx context.Context, oldID, oldHash string, replacement Session, now time.Time) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked_at=? WHERE id=? AND refresh_token_hash=? AND revoked_at IS NULL
  AND julianday(expires_at)>julianday(?) AND EXISTS(SELECT 1 FROM users WHERE id=sessions.user_id AND disabled=0)`, now.UTC().Format(time.RFC3339Nano), oldID, oldHash, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrSessionRevoked
	}
	if err := insertSession(ctx, tx, replacement, now, oldID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE devices SET last_seen_at=? WHERE id=? AND user_id=?`, now.UTC().Format(time.RFC3339Nano), replacement.DeviceID, replacement.User.ID); err != nil {
		return err
	}
	return tx.Commit()
}
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func (r *Repository) findSession(ctx context.Context, hash string) (id, userID, deviceID string, expires time.Time, revoked bool, err error) {
	var exp sql.NullString
	var revAt sql.NullString
	err = r.db.QueryRowContext(ctx, `SELECT id,user_id,device_id,expires_at,revoked_at FROM sessions WHERE refresh_token_hash=?`, hash).Scan(&id, &userID, &deviceID, &exp, &revAt)
	if err != nil {
		return
	}
	expires, err = time.Parse(time.RFC3339Nano, exp.String)
	revoked = revAt.Valid
	return
}
func (r *Repository) revokeSession(ctx context.Context, id string, now time.Time) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked_at=? WHERE id=? AND revoked_at IS NULL`, now.UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n > 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_id,device_id,created_at) SELECT ?,user_id,'account.session_revoked',id,device_id,? FROM sessions WHERE id=?`, newID(), now.UTC().Format("2006-01-02T15:04:05.000000000Z"), id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *Repository) profile(ctx context.Context, u *User) error {
	var avatar bool
	err := r.db.QueryRowContext(ctx, `SELECT account_number,display_name,avatar IS NOT NULL FROM profiles WHERE user_id=?`, u.ID).Scan(&u.AccountNumber, &u.DisplayName, &avatar)
	if avatar {
		u.AvatarURL = "/api/v1/profiles/" + u.ID + "/avatar"
	}
	return err
}
