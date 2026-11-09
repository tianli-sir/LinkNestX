package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrDisabledAccount    = errors.New("account is disabled")
	ErrUnauthorized       = errors.New("unauthorized")
	ErrSessionRevoked     = errors.New("session is revoked")
	ErrSessionExpired     = errors.New("session is expired")
	ErrAdminRequired      = errors.New("administrator role required")
	ErrBootstrapComplete  = errors.New("administrator bootstrap is already complete")
	ErrInvalidDevice      = errors.New("invalid device identifier")
	ErrInvalidInput       = errors.New("invalid account input")
)

type CreateUserInput struct {
	Username string   `json:"username"`
	Email    string   `json:"email"`
	Password string   `json:"password"`
	DeviceID string   `json:"deviceId,omitempty"`
	Roles    []string `json:"roles,omitempty"`
	// Deprecated: caller assertions never establish authorization or target roles.
	ActorUserID string `json:"-"`
	Admin       bool   `json:"-"`
}
type LoginInput struct {
	Username string `json:"username"`
	Email    string `json:"email,omitempty"`
	Password string `json:"password"`
	DeviceID string `json:"deviceId,omitempty"`
}
type Session struct {
	ID           string    `json:"id,omitempty"`
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken"`
	ExpiresAt    time.Time `json:"expiresAt"`
	User         User      `json:"user"`
	DeviceID     string    `json:"deviceId"`
}

type AuthService struct {
	repo       *Repository
	signingKey []byte
	refreshKey []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
	now        func() time.Time
}

func NewAuthService(source any, signingKey, refreshKey string) *AuthService {
	var repo *Repository
	switch value := source.(type) {
	case *Repository:
		repo = value
	case *sql.DB:
		repo = NewRepository(value)
	case nil:
		repo = nil
	}
	if refreshKey == "" {
		refreshKey = signingKey
	}
	return &AuthService{repo: repo, signingKey: []byte(signingKey), refreshKey: []byte(refreshKey), accessTTL: 15 * time.Minute, refreshTTL: 30 * 24 * time.Hour, now: time.Now}
}
func NewService(source any, signingKey, refreshKey string) *AuthService {
	return NewAuthService(source, signingKey, refreshKey)
}
func (s *AuthService) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}
func newID() string { return uuid.NewString() }
func hashToken(token string) string {
	b := sha512.Sum512([]byte(token))
	return hex.EncodeToString(b[:])
}
func randomToken() (string, error) {
	b := make([]byte, 48)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// CreateUser requires a context issued by AuthenticateContext for a current
// administrator. Caller-provided actor IDs and legacy Admin flags are ignored.
func (s *AuthService) CreateUser(ctx context.Context, in CreateUserInput) (User, error) {
	actor, ok := ctx.Value(principalKey{}).(principal)
	if !ok {
		return User{}, ErrAdminRequired
	}
	uid, sid, _, _, err := s.AuthenticateAccessToken(ctx, actor.accessToken)
	if err != nil {
		return User{}, ErrAdminRequired
	}
	admin, err := s.repo.isAdmin(ctx, uid)
	if err != nil {
		return User{}, err
	}
	if !admin {
		return User{}, ErrAdminRequired
	}
	in, err = validateCreateUser(in)
	if err != nil {
		return User{}, err
	}
	hash, err := HashPassword(in.Password)
	if err != nil {
		return User{}, err
	}
	// Check token expiry again after the password hash, before writing the account.
	if _, _, _, _, err := s.ParseAccessToken(actor.accessToken); err != nil {
		return User{}, ErrAdminRequired
	}
	roles := in.Roles
	if len(roles) == 0 {
		roles = []string{"user"}
	}
	return s.repo.createUser(ctx, in, hash, newID(), roles, s.now().UTC(), uid, sid, false)
}

// BootstrapAdmin is an explicit local setup operation. It is not exposed over
// HTTP, and its durable database claim allows it to succeed only once.
func (s *AuthService) BootstrapAdmin(ctx context.Context, in CreateUserInput) (User, error) {
	var err error
	in, err = validateCreateUser(in)
	if err != nil {
		return User{}, err
	}
	hash, err := HashPassword(in.Password)
	if err != nil {
		return User{}, err
	}
	return s.repo.createUser(ctx, in, hash, newID(), []string{"admin", "user"}, s.now().UTC(), "", "", true)
}

func validateCreateUser(in CreateUserInput) (CreateUserInput, error) {
	in.Username = strings.TrimSpace(in.Username)
	in.Email = strings.TrimSpace(in.Email)
	if in.Username == "" || in.Password == "" {
		return in, fmt.Errorf("%w: username and password are required", ErrInvalidInput)
	}
	if len(in.Password) < 8 {
		return in, fmt.Errorf("%w: password must be at least 8 characters", ErrInvalidInput)
	}
	return in, nil
}

func (s *AuthService) Login(ctx context.Context, in LoginInput) (Session, error) {
	login := strings.TrimSpace(in.Username)
	if login == "" {
		login = strings.TrimSpace(in.Email)
	}
	u, hash, err := s.repo.userByLogin(ctx, login)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrInvalidCredentials
	}
	if err != nil {
		return Session{}, err
	}
	if u.Disabled {
		return Session{}, ErrDisabledAccount
	}
	if !VerifyPassword(in.Password, hash) {
		return Session{}, ErrInvalidCredentials
	}
	device, err := s.repo.ensureDevice(ctx, u.ID, in.DeviceID, s.now().UTC())
	if err != nil {
		return Session{}, err
	}
	return s.newSession(ctx, u, device, "")
}

func (s *AuthService) prepareSession(u User, device string) (Session, error) {
	refresh, err := randomToken()
	if err != nil {
		return Session{}, err
	}
	id := newID()
	access, err := s.issueAccess(u, id, device)
	if err != nil {
		return Session{}, err
	}
	return Session{ID: id, AccessToken: access, RefreshToken: refresh, ExpiresAt: s.now().UTC().Add(s.refreshTTL), User: u, DeviceID: device}, nil
}

func (s *AuthService) newSession(ctx context.Context, u User, device, rotated string) (Session, error) {
	session, err := s.prepareSession(u, device)
	if err != nil {
		return Session{}, err
	}
	if err := s.repo.createSession(ctx, session, s.now().UTC(), rotated); err != nil {
		return Session{}, err
	}
	return session, nil
}

func (s *AuthService) Refresh(ctx context.Context, refreshToken string) (Session, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return Session{}, ErrInvalidCredentials
	}
	id, uid, device, expires, revoked, err := s.repo.findSession(ctx, hashToken(refreshToken))
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrInvalidCredentials
	}
	if err != nil {
		return Session{}, err
	}
	if revoked {
		return Session{}, ErrSessionRevoked
	}
	if !s.now().Before(expires) {
		return Session{}, ErrSessionExpired
	}
	u, err := s.repo.userByID(ctx, uid)
	if err != nil {
		return Session{}, err
	}
	if u.Disabled {
		return Session{}, ErrDisabledAccount
	}
	session, err := s.prepareSession(u, device)
	if err != nil {
		return Session{}, err
	}
	if err := s.repo.rotateSession(ctx, id, hashToken(refreshToken), session, s.now().UTC()); err != nil {
		return Session{}, err
	}
	return session, nil
}
func (s *AuthService) RevokeSession(ctx context.Context, id string) error {
	if strings.TrimSpace(id) == "" {
		return ErrUnauthorized
	}
	return s.repo.revokeSession(ctx, id, s.now().UTC())
}
func (s *AuthService) User(ctx context.Context, id string) (User, error) {
	return s.repo.userByID(ctx, id)
}
func (s *AuthService) IsAdmin(ctx context.Context, id string) (bool, error) {
	return s.repo.isAdmin(ctx, id)
}

type accessClaims struct {
	Sub      string `json:"sub"`
	SID      string `json:"sid"`
	DeviceID string `json:"deviceId,omitempty"`
	Exp      int64  `json:"exp"`
	Iat      int64  `json:"iat"`
	Typ      string `json:"typ"`
}

func (s *AuthService) issueAccess(u User, sid, device string) (string, error) {
	now := s.now().UTC()
	c := accessClaims{Sub: u.ID, SID: sid, DeviceID: device, Exp: now.Add(s.accessTTL).Unix(), Iat: now.Unix(), Typ: "access"}
	return signJWT(c, s.signingKey)
}
func signJWT(payload any, key []byte) (string, error) {
	h := map[string]string{"alg": "HS256", "typ": "JWT"}
	hb, _ := json.Marshal(h)
	pb, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	raw := enc.EncodeToString(hb) + "." + enc.EncodeToString(pb)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(raw))
	return raw + "." + enc.EncodeToString(mac.Sum(nil)), nil
}
func ParseAccessToken(token string, key []byte) (userID, sessionID, deviceID string, exp time.Time, err error) {
	return parseAccessTokenAt(token, key, time.Now())
}

func parseAccessTokenAt(token string, key []byte, now time.Time) (userID, sessionID, deviceID string, exp time.Time, err error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", "", "", time.Time{}, ErrUnauthorized
	}
	enc := base64.RawURLEncoding
	sig, e := enc.DecodeString(parts[2])
	if e != nil {
		return "", "", "", time.Time{}, ErrUnauthorized
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	if subtle.ConstantTimeCompare(sig, mac.Sum(nil)) != 1 {
		return "", "", "", time.Time{}, ErrUnauthorized
	}
	pb, e := enc.DecodeString(parts[1])
	if e != nil {
		return "", "", "", time.Time{}, ErrUnauthorized
	}
	var c accessClaims
	if e = json.Unmarshal(pb, &c); e != nil || c.Typ != "access" || c.Sub == "" || c.SID == "" || c.Exp == 0 {
		return "", "", "", time.Time{}, ErrUnauthorized
	}
	exp = time.Unix(c.Exp, 0)
	if !now.Before(exp) {
		return "", "", "", exp, ErrUnauthorized
	}
	return c.Sub, c.SID, c.DeviceID, exp, nil
}

// ParseAccessToken validates an access token issued by this service.
func (s *AuthService) ParseAccessToken(token string) (string, string, string, time.Time, error) {
	return parseAccessTokenAt(token, s.signingKey, s.now())
}

// AuthenticateAccessToken validates the token signature, lifetime and backing
// session state, returning identifiers suitable for request context.
func (s *AuthService) AuthenticateAccessToken(ctx context.Context, token string) (userID, sessionID, deviceID string, exp time.Time, err error) {
	userID, sessionID, deviceID, exp, err = s.ParseAccessToken(token)
	if err != nil {
		return
	}
	var revoked sql.NullString
	var sessionExp string
	var disabled bool
	err = s.repo.db.QueryRowContext(ctx, `SELECT s.expires_at, s.revoked_at, u.disabled FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.id=? AND s.user_id=? AND s.device_id=?`, sessionID, userID, deviceID).Scan(&sessionExp, &revoked, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrUnauthorized
		return
	}
	if err != nil {
		return "", "", "", time.Time{}, err
	}
	if disabled {
		return "", "", "", time.Time{}, ErrDisabledAccount
	}
	if revoked.Valid {
		err = ErrSessionRevoked
		return "", "", "", time.Time{}, err
	}
	var refreshExpiry time.Time
	refreshExpiry, err = time.Parse(time.RFC3339Nano, sessionExp)
	if err != nil || !s.now().UTC().Before(refreshExpiry) {
		err = ErrSessionExpired
		return "", "", "", time.Time{}, err
	}
	return
}

// principalKey and principal are private: external packages cannot create a
// trusted actor by putting an arbitrary user ID in a context.
type principalKey struct{}
type principal struct {
	user        User
	sessionID   string
	deviceID    string
	accessToken string
}

func (s *AuthService) AuthenticateContext(ctx context.Context, token string) (context.Context, error) {
	uid, sid, did, _, err := s.AuthenticateAccessToken(ctx, token)
	if err != nil {
		return nil, err
	}
	user, err := s.User(ctx, uid)
	if err != nil {
		return nil, err
	}
	if user.Disabled {
		return nil, ErrDisabledAccount
	}
	return context.WithValue(ctx, principalKey{}, principal{user: user, sessionID: sid, deviceID: did, accessToken: token}), nil
}

func UserFromContext(ctx context.Context) (User, bool) {
	actor, ok := ctx.Value(principalKey{}).(principal)
	return actor.user, ok
}
func SessionIDFromContext(ctx context.Context) string {
	actor, _ := ctx.Value(principalKey{}).(principal)
	return actor.sessionID
}
func DeviceIDFromContext(ctx context.Context) string {
	actor, _ := ctx.Value(principalKey{}).(principal)
	return actor.deviceID
}
