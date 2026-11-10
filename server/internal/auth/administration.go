package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const MaxPasswordBytes = 1024

var (
	ErrPasswordChangeRequired = errors.New("password change required")
	ErrLastAdmin              = errors.New("cannot disable the last enabled administrator")
	ErrUserNotFound           = errors.New("user not found")
	ErrDeviceNotFound         = errors.New("device not found")
)

type ChangePasswordInput struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

// DirectoryUser deliberately contains no account controls or session metadata.
type DirectoryUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
}

type Device struct {
	ID           string    `json:"id"`
	UserID       string    `json:"userId,omitempty"`
	Name         string    `json:"name,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	LastSeen     time.Time `json:"lastSeen"`
	Active       bool      `json:"active"`
	SessionCount int       `json:"sessionCount"`
	Current      bool      `json:"current"`
}

func validatePassword(password string) error {
	if !utf8.ValidString(password) || len(password) > MaxPasswordBytes || utf8.RuneCountInString(password) < 8 {
		return fmt.Errorf("%w: password must contain at least 8 characters and at most %d bytes", ErrInvalidInput, MaxPasswordBytes)
	}
	return nil
}

func validateAccountID(id string) error {
	parsed, err := uuid.Parse(id)
	if err != nil || parsed == uuid.Nil || parsed.String() != id {
		return ErrInvalidInput
	}
	return nil
}

// accountTransaction revalidates token, session and account state on the same
// transaction used by its caller. A harmless write takes SQLite's write lock
// before permission/last-admin reads so competing mutations cannot race them.
func (s *AuthService) accountTransaction(ctx context.Context, admin, allowInitialPassword, write bool) (*sql.Tx, principal, error) {
	actor, ok := ctx.Value(principalKey{}).(principal)
	deny := ErrUnauthorized
	if admin {
		deny = ErrAdminRequired
	}
	if !ok {
		return nil, principal{}, deny
	}
	uid, sid, did, _, err := s.ParseAccessToken(actor.accessToken)
	if err != nil || uid != actor.user.ID || sid != actor.sessionID || did != actor.deviceID {
		return nil, principal{}, deny
	}
	tx, err := s.repo.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, principal{}, err
	}
	fail := func(err error) (*sql.Tx, principal, error) { _ = tx.Rollback(); return nil, principal{}, err }
	if write {
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET id=id WHERE id=?`, sid); err != nil {
			return fail(err)
		}
	}
	// Waiting for the database may outlive the access token.
	if _, _, _, _, err := s.ParseAccessToken(actor.accessToken); err != nil {
		return fail(deny)
	}
	var disabled, mustChange, isAdmin bool
	var revoked sql.NullString
	var expires string
	err = tx.QueryRowContext(ctx, `SELECT u.disabled,u.must_change_password,s.revoked_at,s.expires_at,
 EXISTS(SELECT 1 FROM user_roles ur JOIN roles r ON r.id=ur.role_id WHERE ur.user_id=u.id AND r.name='admin')
 FROM users u JOIN sessions s ON s.user_id=u.id WHERE u.id=? AND s.id=? AND s.device_id=?`, uid, sid, did).Scan(&disabled, &mustChange, &revoked, &expires, &isAdmin)
	if errors.Is(err, sql.ErrNoRows) {
		return fail(deny)
	}
	if err != nil {
		return fail(err)
	}
	expiry, err := time.Parse(time.RFC3339Nano, expires)
	if err != nil || disabled || revoked.Valid || !s.now().Before(expiry) || (admin && !isAdmin) {
		return fail(deny)
	}
	if mustChange && !allowInitialPassword {
		return fail(ErrPasswordChangeRequired)
	}
	return tx, actor, nil
}

// ChangePassword keeps the submitting session valid and revokes every other
// session, including sessions on the same device. The old hash is rechecked
// inside the mutation transaction to reject concurrent password resets.
func (s *AuthService) ChangePassword(ctx context.Context, in ChangePasswordInput) (User, error) {
	if err := validatePassword(in.NewPassword); err != nil {
		return User{}, err
	}
	if len(in.CurrentPassword) > MaxPasswordBytes || !utf8.ValidString(in.CurrentPassword) {
		return User{}, ErrInvalidInput
	}
	if in.CurrentPassword == in.NewPassword {
		return User{}, fmt.Errorf("%w: new password must differ from current password", ErrInvalidInput)
	}
	tx, actor, err := s.accountTransaction(ctx, false, true, false)
	if err != nil {
		return User{}, err
	}
	var oldHash string
	err = tx.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id=?`, actor.user.ID).Scan(&oldHash)
	_ = tx.Rollback()
	if err != nil {
		return User{}, err
	}
	if !VerifyPassword(in.CurrentPassword, oldHash) {
		return User{}, ErrInvalidCredentials
	}
	newHash, err := HashPassword(in.NewPassword)
	if err != nil {
		return User{}, err
	}
	tx, actor, err = s.accountTransaction(ctx, false, true, true)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	now := s.now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE users SET password_hash=?,must_change_password=0,updated_at=? WHERE id=? AND password_hash=?`, newHash, now, actor.user.ID, oldHash)
	if err != nil {
		return User{}, err
	}
	if n, err := result.RowsAffected(); err != nil {
		return User{}, err
	} else if n != 1 {
		return User{}, ErrInvalidCredentials
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked_at=COALESCE(revoked_at,?) WHERE user_id=? AND id<>?`, now, actor.user.ID, actor.sessionID); err != nil {
		return User{}, err
	}
	if err := RecordAudit(ctx, tx, actor.user.ID, "account.password_changed", actor.user.ID, s.now()); err != nil {
		return User{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return s.repo.userByID(ctx, actor.user.ID)
}

func (s *AuthService) SetUserDisabled(ctx context.Context, userID string, disabled bool) error {
	if err := validateAccountID(userID); err != nil {
		return err
	}
	tx, actor, err := s.accountTransaction(ctx, true, false, true)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var alreadyDisabled, targetAdmin bool
	err = tx.QueryRowContext(ctx, `SELECT disabled,EXISTS(SELECT 1 FROM user_roles ur JOIN roles r ON r.id=ur.role_id WHERE ur.user_id=users.id AND r.name='admin') FROM users WHERE id=?`, userID).Scan(&alreadyDisabled, &targetAdmin)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrUserNotFound
	}
	if err != nil {
		return err
	}
	if disabled && !alreadyDisabled && targetAdmin {
		var remaining int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users u WHERE u.disabled=0 AND EXISTS(SELECT 1 FROM user_roles ur JOIN roles r ON r.id=ur.role_id WHERE ur.user_id=u.id AND r.name='admin')`).Scan(&remaining); err != nil {
			return err
		}
		if remaining <= 1 {
			return ErrLastAdmin
		}
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE users SET disabled=?,updated_at=? WHERE id=?`, disabled, now, userID); err != nil {
		return err
	}
	if disabled {
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked_at=COALESCE(revoked_at,?) WHERE user_id=?`, now, userID); err != nil {
			return err
		}
	}
	action := "account.enabled"
	if disabled {
		action = "account.disabled"
	}
	if err := RecordAudit(ctx, tx, actor.user.ID, action, userID, s.now()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *AuthService) ResetUserPassword(ctx context.Context, userID, password string) error {
	if err := validateAccountID(userID); err != nil {
		return err
	}
	if err := validatePassword(password); err != nil {
		return err
	}
	// Reject unauthorized requests before the expensive password hash.
	tx, actor, err := s.accountTransaction(ctx, true, false, false)
	if err != nil {
		return err
	}
	_ = tx.Rollback()
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	tx, actor, err = s.accountTransaction(ctx, true, false, true)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE users SET password_hash=?,must_change_password=1,updated_at=? WHERE id=?`, hash, now, userID)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return ErrUserNotFound
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked_at=COALESCE(revoked_at,?) WHERE user_id=?`, now, userID); err != nil {
		return err
	}
	if err := RecordAudit(ctx, tx, actor.user.ID, "account.password_reset", userID, s.now()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *AuthService) RevokeUserSessions(ctx context.Context, userID string) error {
	if err := validateAccountID(userID); err != nil {
		return err
	}
	tx, actor, err := s.accountTransaction(ctx, true, false, true)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE id=?`, userID).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return ErrUserNotFound
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked_at=COALESCE(revoked_at,?) WHERE user_id=?`, s.now().UTC().Format(time.RFC3339Nano), userID); err != nil {
		return err
	}
	if err := RecordAudit(ctx, tx, actor.user.ID, "account.sessions_revoked", userID, s.now()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *AuthService) Directory(ctx context.Context) ([]DirectoryUser, error) {
	tx, _, err := s.accountTransaction(ctx, false, false, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id,username,email FROM users WHERE disabled=0 ORDER BY lower(username),id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := []DirectoryUser{}
	for rows.Next() {
		var user DirectoryUser
		if err := rows.Scan(&user.ID, &user.Username, &user.Email); err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

func (s *AuthService) AdminUsers(ctx context.Context) ([]User, error) {
	tx, _, err := s.accountTransaction(ctx, true, false, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT u.id,u.username,u.email,u.disabled,u.must_change_password,u.created_at,r.name,p.account_number,p.display_name,p.avatar IS NOT NULL FROM users u JOIN profiles p ON p.user_id=u.id LEFT JOIN user_roles ur ON ur.user_id=u.id LEFT JOIN roles r ON r.id=ur.role_id ORDER BY lower(u.username),u.id,r.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := []User{}
	for rows.Next() {
		var user User
		var created string
		var role sql.NullString
		var avatar bool
		if err := rows.Scan(&user.ID, &user.Username, &user.Email, &user.Disabled, &user.MustChangePassword, &created, &role, &user.AccountNumber, &user.DisplayName, &avatar); err != nil {
			return nil, err
		}
		if avatar {
			user.AvatarURL = "/api/v1/profiles/" + user.ID + "/avatar"
		}
		if len(users) == 0 || users[len(users)-1].ID != user.ID {
			user.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
			if err != nil {
				return nil, err
			}
			user.CreatedAt = user.CreatedAt.UTC()
			user.Roles = []string{}
			users = append(users, user)
		}
		if role.Valid {
			users[len(users)-1].Roles = append(users[len(users)-1].Roles, role.String)
		}
	}
	return users, rows.Err()
}

func (s *AuthService) Devices(ctx context.Context) ([]Device, error) { return s.devices(ctx, false) }
func (s *AuthService) AdminDevices(ctx context.Context) ([]Device, error) {
	return s.devices(ctx, true)
}

func (s *AuthService) devices(ctx context.Context, admin bool) ([]Device, error) {
	tx, actor, err := s.accountTransaction(ctx, admin, false, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT d.id,d.user_id,d.name,d.created_at,d.last_seen_at,
 (SELECT count(*) FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.device_id=d.id AND s.user_id=d.user_id AND u.disabled=0 AND s.revoked_at IS NULL AND julianday(s.expires_at)>julianday(?))
 FROM devices d WHERE (? OR d.user_id=?) ORDER BY d.created_at,d.id`, s.now().UTC().Format(time.RFC3339Nano), admin, actor.user.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	devices := []Device{}
	for rows.Next() {
		var device Device
		var created, seen string
		if err := rows.Scan(&device.ID, &device.UserID, &device.Name, &created, &seen, &device.SessionCount); err != nil {
			return nil, err
		}
		device.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, err
		}
		device.LastSeen, err = time.Parse(time.RFC3339Nano, seen)
		if err != nil {
			return nil, err
		}
		device.CreatedAt, device.LastSeen = device.CreatedAt.UTC(), device.LastSeen.UTC()
		device.Active, device.Current = device.SessionCount > 0, device.ID == actor.deviceID
		if !admin {
			device.UserID = ""
		}
		devices = append(devices, device)
	}
	return devices, rows.Err()
}

func (s *AuthService) RevokeDevice(ctx context.Context, deviceID string) error {
	return s.revokeDevice(ctx, deviceID, false)
}
func (s *AuthService) AdminRevokeDevice(ctx context.Context, deviceID string) error {
	return s.revokeDevice(ctx, deviceID, true)
}

func (s *AuthService) revokeDevice(ctx context.Context, deviceID string, admin bool) error {
	if err := validateAccountID(deviceID); err != nil {
		return ErrInvalidDevice
	}
	tx, actor, err := s.accountTransaction(ctx, admin, false, true)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owner string
	err = tx.QueryRowContext(ctx, `SELECT user_id FROM devices WHERE id=?`, deviceID).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !admin && owner != actor.user.ID) {
		return ErrDeviceNotFound
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked_at=COALESCE(revoked_at,?) WHERE device_id=? AND user_id=?`, s.now().UTC().Format(time.RFC3339Nano), deviceID, owner); err != nil {
		return err
	}
	if err := RecordAudit(ctx, tx, actor.user.ID, "device.revoked", deviceID, s.now()); err != nil {
		return err
	}
	return tx.Commit()
}
