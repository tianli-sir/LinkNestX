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
	UserID   string    `json:"userId"`
	Role     string    `json:"role"`
	JoinedAt time.Time `json:"joinedAt"`
}
type GroupService struct {
	repo *Repository
	auth *auth.AuthService
}

func NewGroupService(conn *sql.DB, a *auth.AuthService) *GroupService {
	return &GroupService{NewRepository(conn), a}
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
	g := Group{ID: uuid.NewString(), Name: in.Name, CreatedBy: actorID, CreatedAt: time.Now().UTC()}
	err := s.repo.create(ctx, actorID, in.OwnerID, g)
	return g, err
}
func (s *GroupService) List(ctx context.Context, actorID string) ([]Group, error) {
	if err := s.auth.ValidateActor(ctx, actorID); err != nil {
		return nil, err
	}
	return s.repo.list(ctx, actorID)
}
func (s *GroupService) ListMembers(ctx context.Context, actorID, groupID string) ([]Member, error) {
	if err := s.RequireMember(ctx, actorID, groupID); err != nil {
		return nil, err
	}
	return s.repo.members(ctx, groupID)
}
func (s *GroupService) RequireMember(ctx context.Context, actorID, groupID string) error {
	if err := s.auth.ValidateActor(ctx, actorID); err != nil {
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
	return s.repo.changeMember(ctx, actorID, groupID, userID, role, false)
}
func (s *GroupService) RemoveMember(ctx context.Context, actorID, groupID, userID string) error {
	if err := s.auth.ValidateActor(ctx, actorID); err != nil {
		return err
	}
	return s.repo.changeMember(ctx, actorID, groupID, userID, "", true)
}
