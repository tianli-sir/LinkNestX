package chat

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/linknestx/linknestx/server/internal/auth"
	"github.com/linknestx/linknestx/server/internal/groups"
)

var (
	ErrInvalidInput = errors.New("invalid message input or cursor")
	ErrConflict     = errors.New("client message ID already used for different content")
)

const timestampLayout = "2006-01-02T15:04:05.000000000Z"

type AppendInput struct {
	GroupID         string `json:"groupId"`
	ClientMessageID string `json:"clientMessageId"`
	Body            string `json:"body"`
}
type Message struct {
	Kind             string     `json:"kind"`
	SystemType       string     `json:"systemType,omitempty"`
	ConversationID   string     `json:"conversationId"`
	Sender           *auth.User `json:"sender,omitempty"`
	ReadCount        int        `json:"readCount"`
	ReadByMe         bool       `json:"readByMe"`
	ID               string     `json:"id"`
	Sequence         int64      `json:"sequence"`
	GroupID          string     `json:"groupId"`
	SenderID         string     `json:"senderId"`
	ClientMessageID  string     `json:"clientMessageId"`
	Body             *string    `json:"body"`
	CreatedAt        time.Time  `json:"createdAt"`
	ExpiredAt        *time.Time `json:"expiredAt,omitempty"`
	Cursor           string     `json:"cursor"`
	FileID           *string    `json:"fileId,omitempty"`
	FileVersionID    *string    `json:"fileVersionId,omitempty"`
	AttachmentName   *string    `json:"attachmentName,omitempty"`
	AttachmentSize   *int64     `json:"attachmentSize,omitempty"`
	AttachmentStatus *string    `json:"attachmentStatus,omitempty"`
}
type MessagePage struct {
	Messages   []Message `json:"messages"`
	NextCursor string    `json:"nextCursor"`
	HasMore    bool      `json:"hasMore"`
}
type ChatService struct {
	repo   *Repository
	auth   *auth.AuthService
	groups *groups.GroupService
	now    func() time.Time
	mu     sync.Mutex
	hub    *Hub
}

func NewChatService(conn *sql.DB, a *auth.AuthService, g *groups.GroupService) *ChatService {
	s := &ChatService{repo: NewRepository(conn), auth: a, groups: g, now: time.Now}
	g.SetSystemMessageObserver(s.publishGroupSystemMessage)
	return s
}
func (s *ChatService) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}
func (s *ChatService) Append(ctx context.Context, actorID string, in AppendInput) (Message, error) {
	if err := s.auth.ValidateActor(ctx, actorID); err != nil {
		return Message{}, err
	}
	if in.GroupID == "" || strings.TrimSpace(in.ClientMessageID) == "" || len(in.ClientMessageID) > 128 || strings.TrimSpace(in.Body) == "" || len(in.Body) > 16384 {
		return Message{}, ErrInvalidInput
	}
	// Serialize commit + publication so every connection observes persisted order.
	s.mu.Lock()
	defer s.mu.Unlock()
	m := Message{Kind: "text", ID: uuid.NewString(), GroupID: in.GroupID, SenderID: actorID, ClientMessageID: in.ClientMessageID, Body: &in.Body, CreatedAt: s.now().UTC()}
	saved, created, err := s.repo.append(ctx, m)
	if err == nil {
		err = s.project(ctx, actorID, &saved)
	}
	if err == nil && created && s.hub != nil {
		s.hub.Publish(saved)
	}
	return saved, err
}

type messageCursor struct {
	GroupID  string `json:"g"`
	Sequence int64  `json:"s"`
}

func encodeCursor(group string, sequence int64) string {
	b, _ := json.Marshal(messageCursor{group, sequence})
	return base64.RawURLEncoding.EncodeToString(b)
}
func decodeCursor(group, cursor string) (int64, error) {
	if cursor == "" {
		return 0, nil
	}
	if len(cursor) > 512 {
		return 0, ErrInvalidInput
	}
	b, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, ErrInvalidInput
	}
	var c messageCursor
	if err := json.Unmarshal(b, &c); err != nil || c.GroupID != group || c.Sequence < 0 {
		return 0, ErrInvalidInput
	}
	return c.Sequence, nil
}
func (s *ChatService) List(ctx context.Context, actorID, groupID, cursor string, limit int) (MessagePage, error) {
	if err := s.groups.RequireMember(ctx, actorID, groupID); err != nil {
		return MessagePage{}, err
	}
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 200 {
		return MessagePage{}, ErrInvalidInput
	}
	after, err := decodeCursor(groupID, cursor)
	if err != nil {
		return MessagePage{}, err
	}
	return s.repo.list(ctx, actorID, groupID, after, limit)
}

// ExpireBeforeTwoYears retains message IDs, ordering and attribution while
// removing content strictly older than the UTC calendar-year boundary.
func (s *ChatService) ExpireBeforeTwoYears(ctx context.Context, now time.Time) (int64, error) {
	return s.repo.expire(ctx, now.UTC().AddDate(-2, 0, 0), now.UTC())
}

func (s *ChatService) publishGroupSystemMessage(ctx context.Context, id string) {
	if s.hub == nil {
		return
	}
	m, err := scanMessage(s.repo.db.QueryRowContext(ctx, "SELECT "+messageColumns+" FROM messages WHERE id=? AND kind='system'", id))
	if err == nil && s.project(ctx, m.SenderID, &m) == nil {
		s.hub.Publish(m)
	}
}
