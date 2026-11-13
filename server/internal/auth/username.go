package auth

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrUsernameConflict = errors.New("username is already in use")

// usernameConflict reports SQLite guard failures that mean the requested name
// is taken: the users UNIQUE constraint or the username/account-number
// collision trigger installed by migrations 008/011.
func usernameConflict(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "UNIQUE constraint failed: users.username") ||
		strings.Contains(message, "username collides with account number")
}

// UpdateUsername renames any account. Case-insensitive duplicates are rejected
// alongside the schema guards; sessions stay valid and contacts receive
// profile.changed only after commit.
func (s *AuthService) UpdateUsername(ctx context.Context, userID, username string) (User, error) {
	if err := validateAccountID(userID); err != nil {
		return User{}, err
	}
	username = strings.TrimSpace(username)
	if username == "" || len(username) > 256 || !utf8.ValidString(username) {
		return User{}, ErrInvalidInput
	}
	tx, actor, err := s.accountTransaction(ctx, true, false, true)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	var current string
	err = tx.QueryRowContext(ctx, `SELECT username FROM users WHERE id=?`, userID).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, err
	}
	if current == username {
		if err = tx.Commit(); err != nil {
			return User{}, err
		}
		return s.repo.userByID(ctx, userID)
	}
	var duplicate int
	if err = tx.QueryRowContext(ctx, `SELECT 1 FROM users WHERE lower(username)=lower(?) AND id<>?`, username, userID).Scan(&duplicate); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return User{}, err
	}
	if duplicate == 1 {
		return User{}, ErrUsernameConflict
	}
	if _, err = tx.ExecContext(ctx, `UPDATE users SET username=?,updated_at=? WHERE id=?`, username, s.now().UTC().Format(time.RFC3339Nano), userID); err != nil {
		if usernameConflict(err) {
			return User{}, ErrUsernameConflict
		}
		return User{}, err
	}
	if err = RecordAudit(ctx, tx, actor.user.ID, "account.username_changed", userID, s.now()); err != nil {
		return User{}, err
	}
	if err = tx.Commit(); err != nil {
		return User{}, err
	}
	if s.profileChanged != nil {
		s.profileChanged(ctx, userID)
	}
	return s.repo.userByID(ctx, userID)
}
