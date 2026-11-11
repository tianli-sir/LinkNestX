package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"time"
)

var ErrAccountNumberConflict = errors.New("account number is already in use")
var ErrAccountNumbersExhausted = errors.New("six-digit account numbers are unavailable")

func validateAccountNumber(number string) error {
	if len(number) < 6 || len(number) > 10 || number[0] < '1' || number[0] > '9' {
		return fmt.Errorf("%w: account number must contain 6 to 10 digits without a leading zero", ErrInvalidInput)
	}
	for _, digit := range number {
		if digit < '0' || digit > '9' {
			return fmt.Errorf("%w: account number must contain 6 to 10 digits without a leading zero", ErrInvalidInput)
		}
	}
	return nil
}

func accountNumberAvailable(ctx context.Context, tx *sql.Tx, userID, number string) (bool, error) {
	var occupied bool
	err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM profiles WHERE account_number=? AND user_id<>?) OR EXISTS(SELECT 1 FROM users WHERE (username=? OR email=?) AND id<>?)", number, userID, number, number, userID).Scan(&occupied)
	return !occupied, err
}

// The caller holds SQLite's write lock. Cryptographic sampling and the unique
// profile constraint keep allocation atomic without changing existing numbers.
func allocateAccountNumber(ctx context.Context, tx *sql.Tx, userID, requested string) (string, error) {
	if requested != "" {
		available, err := accountNumberAvailable(ctx, tx, userID, requested)
		if err != nil {
			return "", err
		}
		if !available {
			return "", ErrAccountNumberConflict
		}
		return requested, nil
	}
	for attempt := 0; attempt < 128; attempt++ {
		value, err := rand.Int(rand.Reader, big.NewInt(900000))
		if err != nil {
			return "", err
		}
		candidate := strconv.FormatInt(value.Int64()+100000, 10)
		available, err := accountNumberAvailable(ctx, tx, userID, candidate)
		if err != nil {
			return "", err
		}
		if available {
			return candidate, nil
		}
	}
	return "", ErrAccountNumbersExhausted
}

// SetProfileChangedHandler connects account edits to the shared profile event
// publisher during server composition, before requests are served.
func (s *AuthService) SetProfileChangedHandler(handler func(context.Context, string)) {
	s.profileChanged = handler
}

func (s *AuthService) UpdateAccountNumber(ctx context.Context, userID, number string) (User, error) {
	if err := validateAccountID(userID); err != nil {
		return User{}, err
	}
	if err := validateAccountNumber(number); err != nil {
		return User{}, err
	}
	tx, actor, err := s.accountTransaction(ctx, true, false, true)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	var previous string
	err = tx.QueryRowContext(ctx, "SELECT account_number FROM profiles WHERE user_id=?", userID).Scan(&previous)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, err
	}
	if previous == number {
		if err = tx.Commit(); err != nil {
			return User{}, err
		}
		return s.repo.userByID(ctx, userID)
	}
	number, err = allocateAccountNumber(ctx, tx, userID, number)
	if err != nil {
		return User{}, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE profiles SET account_number=? WHERE user_id=?", number, userID); err != nil {
		return User{}, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE users SET updated_at=? WHERE id=?", s.now().UTC().Format(time.RFC3339Nano), userID); err != nil {
		return User{}, err
	}
	if err = RecordAudit(ctx, tx, actor.user.ID, "account.number_changed", userID, s.now()); err != nil {
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
