package groups

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/linknestx/linknestx/server/internal/auth"
)

var (
	ErrForbidden    = errors.New("group access denied")
	ErrInvalidInput = errors.New("invalid group input")
	ErrLastOwner    = errors.New("group must retain an owner")
	ErrNotFound     = errors.New("group or account not found")
)

type Group struct {
	IsMember  bool      `json:"isMember"`
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedBy string    `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
}
type CreateInput struct {
	Name    string `json:"name"`
	OwnerID string `json:"ownerId,omitempty"`
}
type Member struct {
	UserID        string     `json:"userId"`
	Role          string     `json:"role"`
	JoinedAt      time.Time  `json:"joinedAt"`
	Nickname      *string    `json:"nickname"`
	DisplayName   string     `json:"displayName"`
	AccountNumber string     `json:"accountNumber"`
	HasAvatar     bool       `json:"hasAvatar"`
	Disabled      bool       `json:"disabled"`
}
type GroupService struct {
	repo           *Repository
	auth           *auth.AuthService
	observer       func(string, string)
	systemObserver func(context.Context, string)
}

func NewGroupService(conn *sql.DB, a *auth.AuthService) *GroupService {
	return &GroupService{repo: NewRepository(conn), auth: a}
}
func (s *GroupService) Create(ctx context.Context, actorID string, in CreateInput) (Group, error) {
	if err := s.auth.ValidateActor(ctx, actorID); err != nil {
		return Group{}, err
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || utf8.RuneCountInString(in.Name) > 120 {
		return Group{}, ErrInvalidInput
	}
	if in.OwnerID == "" {
		in.OwnerID = actorID
	}
	g := Group{IsMember: in.OwnerID == actorID, ID: uuid.NewString(), Name: in.Name, CreatedBy: actorID, CreatedAt: time.Now().UTC()}
	messageID, err := s.repo.create(ctx, actorID, in.OwnerID, g)
	if err == nil && s.systemObserver != nil {
		s.systemObserver(ctx, messageID)
	}
	return g, err
}
func (s *GroupService) List(ctx context.Context, actorID string) ([]Group, error) {
	if err := s.auth.ValidateActor(ctx, actorID); err != nil {
		return nil, err
	}
	return s.repo.list(ctx, actorID)
}
func (s *GroupService) ListMembers(ctx context.Context, actorID, groupID string) ([]Member, error) {
	if err := s.auth.ValidateActor(ctx, actorID); err != nil {
		return nil, err
	}
	if direct, err := IsDirect(ctx, s.repo.db, groupID); err != nil {
		return nil, err
	} else if direct {
		return nil, ErrForbidden
	}
	var allowed int
	if err := s.repo.db.QueryRowContext(ctx, `SELECT count(*) FROM groups g WHERE g.id=? AND (EXISTS(SELECT 1 FROM group_members m WHERE m.group_id=g.id AND m.user_id=?) OR EXISTS(SELECT 1 FROM user_roles ur JOIN roles r ON r.id=ur.role_id WHERE ur.user_id=? AND r.name='admin'))`, groupID, actorID, actorID).Scan(&allowed); err != nil {
		return nil, err
	}
	if allowed == 0 {
		return nil, ErrForbidden
	}
	return s.repo.members(ctx, groupID)
}
func (s *GroupService) RequireMember(ctx context.Context, actorID, groupID string) error {
	if err := s.auth.ValidateActor(ctx, actorID); err != nil {
		return err
	}
	if _, err := RequireDirectMember(ctx, s.repo.db, actorID, groupID); err != nil {
		return err
	}
	var count int
	err := s.repo.db.QueryRowContext(ctx, `SELECT count(*) FROM group_members WHERE group_id=? AND user_id=?`, groupID, actorID).Scan(&count)
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrForbidden
	}
	return nil
}
func (s *GroupService) AddMember(ctx context.Context, actorID, groupID, userID, role string) error {
	if err := s.auth.ValidateActor(ctx, actorID); err != nil {
		return err
	}
	if role != "member" && role != "owner" {
		return ErrInvalidInput
	}
	messageID, err := s.repo.changeMember(ctx, actorID, groupID, userID, role, false)
	if err == nil && messageID != "" && s.systemObserver != nil {
		s.systemObserver(ctx, messageID)
	}
	if err == nil && messageID != "" && s.observer != nil {
		s.observer(groupID, userID)
	}
	return err
}
func (s *GroupService) RemoveMember(ctx context.Context, actorID, groupID, userID string) error {
	if err := s.auth.ValidateActor(ctx, actorID); err != nil {
		return err
	}
	messageID, err := s.repo.changeMember(ctx, actorID, groupID, userID, "", true)
	if err == nil && messageID != "" && s.systemObserver != nil {
		s.systemObserver(ctx, messageID)
	}
	if err == nil && messageID != "" && s.observer != nil {
		s.observer(groupID, userID)
	}
	return err
}

func (s *GroupService) SetMemberObserver(observer func(string, string)) { s.observer = observer }

func (s *GroupService) SetSystemMessageObserver(observer func(context.Context, string)) {
	s.systemObserver = observer
}
