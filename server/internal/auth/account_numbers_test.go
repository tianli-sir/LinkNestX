package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"testing"

	"github.com/google/uuid"
)

func TestNewAccountNumbersAreUniqueRandomSixDigits(t *testing.T) {
	s := testService(t)
	_, admin := adminSession(t, s)
	seen := map[string]bool{}
	for i := 0; i < 12; i++ {
		user, err := s.CreateUser(admin, CreateUserInput{Username: fmt.Sprintf("member-%d", i), Password: "password123"})
		if err != nil {
			t.Fatal(err)
		}
		if !regexp.MustCompile("^[1-9][0-9]{5}$").MatchString(user.AccountNumber) {
			t.Fatalf("default account number %q is not six digits", user.AccountNumber)
		}
		if seen[user.AccountNumber] {
			t.Fatalf("reused account number %s", user.AccountNumber)
		}
		if _, err = uuid.Parse(user.ID); err != nil {
			t.Fatalf("internal id changed shape: %s", user.ID)
		}
		seen[user.AccountNumber] = true
	}
}

func TestAdministratorCanProvideAccountNumberAtCreation(t *testing.T) {
	s := testService(t)
	_, admin := adminSession(t, s)
	var in CreateUserInput
	raw, _ := json.Marshal(map[string]string{"username": "chosen", "password": "password123", "accountNumber": "9876543210"})
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	user, err := s.CreateUser(admin, in)
	if err != nil {
		t.Fatal(err)
	}
	if user.AccountNumber != "9876543210" {
		t.Fatalf("provided number ignored: %s", user.AccountNumber)
	}
	login, err := s.Login(context.Background(), LoginInput{Username: "9876543210", Password: "password123"})
	if err != nil || login.User.ID != user.ID {
		t.Fatalf("account-number login=%+v, %v", login.User, err)
	}
}

func TestCreateRejectsInvalidOrConflictingAccountNumber(t *testing.T) {
	s := testService(t)
	root, admin := adminSession(t, s)
	for i, number := range []string{"12345", "012345", "12345678901", "12345x", " 123456", root.User.AccountNumber} {
		var in CreateUserInput
		raw, _ := json.Marshal(map[string]string{"username": fmt.Sprintf("bad-%d", i), "password": "password123", "accountNumber": number})
		json.Unmarshal(raw, &in)
		_, err := s.CreateUser(admin, in)
		if err == nil {
			t.Fatalf("created account with invalid/conflicting number %q", number)
		}
	}
	var count int
	if err := s.repo.db.QueryRow("SELECT count(*) FROM users WHERE username LIKE 'bad-%'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed create persisted users count=%d err=%v", count, err)
	}
}

// This assertion allows the first test to fail on missing behavior, not compilation.
type accountNumberUpdater interface {
	UpdateAccountNumber(context.Context, string, string) (User, error)
}

func numberUpdater(t *testing.T, s *AuthService) accountNumberUpdater {
	t.Helper()
	updater, ok := any(s).(accountNumberUpdater)
	if !ok {
		t.Fatal("administrator account-number update is unavailable")
	}
	return updater
}

func TestAdminNumberUpdatePreservesIdentityAndLogin(t *testing.T) {
	s := testService(t)
	_, admin := adminSession(t, s)
	user, _ := accountSession(t, s, admin, "employee")
	updater := numberUpdater(t, s)
	next, err := updater.UpdateAccountNumber(admin, user.User.ID, "7654321")
	if err != nil {
		t.Fatal(err)
	}
	if next.ID != user.User.ID || next.AccountNumber != "7654321" {
		t.Fatalf("updated user=%+v", next)
	}
	if _, _, _, _, err = s.AuthenticateAccessToken(context.Background(), user.AccessToken); err != nil {
		t.Fatalf("edit invalidated session: %v", err)
	}
	for _, login := range []string{"employee", "7654321"} {
		got, err := s.Login(context.Background(), LoginInput{Username: login, Password: "initial-password"})
		if err != nil || got.User.ID != user.User.ID {
			t.Fatalf("login %q user=%s err=%v", login, got.User.ID, err)
		}
	}
	if _, err = s.Login(context.Background(), LoginInput{Username: user.User.AccountNumber, Password: "initial-password"}); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("old number still authenticates: %v", err)
	}
	var audit int
	if err = s.repo.db.QueryRow("SELECT count(*) FROM audit_events WHERE action='account.number_changed' AND target_id=?", user.User.ID).Scan(&audit); err != nil || audit != 1 {
		t.Fatalf("audit count=%d err=%v", audit, err)
	}
}

func TestAccountNumberUpdateRejectsUnauthorizedAndConflict(t *testing.T) {
	s := testService(t)
	root, admin := adminSession(t, s)
	user, ctx := accountSession(t, s, admin, "employee")
	initializeAccount(t, s, ctx)
	updater := numberUpdater(t, s)
	for _, ctx := range []context.Context{context.Background(), ctx} {
		if _, err := updater.UpdateAccountNumber(ctx, user.User.ID, "765432"); !errors.Is(err, ErrAdminRequired) {
			t.Fatalf("unauthorized edit=%v", err)
		}
	}
	for _, number := range []string{"12345", "012345", "12345678901", "1234a6", root.User.AccountNumber} {
		if _, err := updater.UpdateAccountNumber(admin, user.User.ID, number); err == nil {
			t.Fatalf("accepted number=%q", number)
		}
	}
	if _, err := s.CreateUser(admin, CreateUserInput{Username: "654321", Password: "password123"}); err != nil {
		t.Fatal(err)
	}
	if _, err := updater.UpdateAccountNumber(admin, user.User.ID, "654321"); err == nil {
		t.Fatal("numeric username collision accepted")
	}
	got, err := s.User(context.Background(), user.User.ID)
	if err != nil || got.AccountNumber != user.User.AccountNumber {
		t.Fatalf("failed edit changed number=%s err=%v", got.AccountNumber, err)
	}
	if _, err := s.repo.db.Exec("DELETE FROM user_roles WHERE user_id=? AND role_id=(SELECT id FROM roles WHERE name='admin')", root.User.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := updater.UpdateAccountNumber(admin, user.User.ID, "765432"); !errors.Is(err, ErrAdminRequired) {
		t.Fatalf("stale administrator accepted: %v", err)
	}
}

func TestConcurrentAccountNumberUpdatesHaveOneWinner(t *testing.T) {
	s := testService(t)
	_, admin := adminSession(t, s)
	a, _ := accountSession(t, s, admin, "alice")
	b, _ := accountSession(t, s, admin, "bob")
	updater := numberUpdater(t, s)
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, id := range []string{a.User.ID, b.User.ID} {
		go func(id string) { <-start; _, err := updater.UpdateAccountNumber(admin, id, "765432"); results <- err }(id)
	}
	close(start)
	success := 0
	for range 2 {
		if err := <-results; err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("successful concurrent updates=%d", success)
	}
}

func TestAccountNumberChangePublishesOnlyAfterCommitAndRollsBackWithAudit(t *testing.T) {
	s := testService(t)
	_, admin := adminSession(t, s)
	user, _ := accountSession(t, s, admin, "employee")
	var published []string
	s.SetProfileChangedHandler(func(ctx context.Context, id string) {
		stored, err := s.User(ctx, id)
		if err != nil || stored.AccountNumber != "7654321" {
			t.Fatalf("event preceded commit: %+v, %v", stored, err)
		}
		published = append(published, id)
	})
	if _, err := s.repo.db.Exec("CREATE TRIGGER reject_number_audit BEFORE INSERT ON audit_events WHEN NEW.action='account.number_changed' BEGIN SELECT RAISE(ABORT,'test audit failure'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateAccountNumber(admin, user.User.ID, "7654321"); err == nil {
		t.Fatal("audit failure accepted")
	}
	stored, err := s.User(context.Background(), user.User.ID)
	if err != nil || stored.AccountNumber != user.User.AccountNumber || len(published) != 0 {
		t.Fatalf("partial update=%+v event=%v err=%v", stored, published, err)
	}
	if _, err := s.repo.db.Exec("DROP TRIGGER reject_number_audit"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateAccountNumber(admin, user.User.ID, "7654321"); err != nil {
		t.Fatal(err)
	}
	if len(published) != 1 || published[0] != user.User.ID {
		t.Fatalf("notifications=%v", published)
	}
	if _, err := s.UpdateAccountNumber(admin, user.User.ID, "7654321"); err != nil {
		t.Fatal(err)
	}
	if len(published) != 1 {
		t.Fatalf("no-op update published %d events", len(published))
	}
}

func TestNewAccountCannotUseAnotherNumberAsEmailLogin(t *testing.T) {
	s := testService(t)
	root, admin := adminSession(t, s)
	_, err := s.CreateUser(admin, CreateUserInput{Username: "numeric-email", Email: root.User.AccountNumber, Password: "password123"})
	if !errors.Is(err, ErrAccountNumberConflict) {
		t.Fatalf("numeric email stole existing account login: %v", err)
	}
}

func TestRandomAccountAllocatorRetriesOccupiedNumber(t *testing.T) {
	s := testService(t)
	_, admin := adminSession(t, s)
	occupied, err := s.CreateUser(admin, CreateUserInput{Username: "occupied", Password: "password123"})
	if err != nil {
		t.Fatal(err)
	}
	blocked := new(big.Int)
	if _, ok := blocked.SetString(occupied.AccountNumber, 10); !ok {
		t.Fatal("invalid fixture number")
	}
	blocked.Sub(blocked, big.NewInt(100000))
	var available string
	for candidate := 100000; candidate < 100010; candidate++ {
		var count int
		if err := s.repo.db.QueryRow("SELECT count(*) FROM profiles WHERE account_number=?", fmt.Sprint(candidate)).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			available = fmt.Sprint(candidate)
			break
		}
	}
	if available == "" {
		t.Fatal("no available fixture candidate")
	}
	value := new(big.Int)
	value.SetString(available, 10)
	value.Sub(value, big.NewInt(100000))
	stream := append(blocked.FillBytes(make([]byte, 3)), value.FillBytes(make([]byte, 3))...)
	targetID := uuid.NewString()
	previous := rand.Reader
	rand.Reader = bytes.NewReader(stream)
	defer func() { rand.Reader = previous }()
	tx, err := s.repo.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	number, err := allocateAccountNumber(context.Background(), tx, targetID, "")
	if err != nil || number != available {
		t.Fatalf("collision retry number=%s want=%s err=%v", number, available, err)
	}
}
