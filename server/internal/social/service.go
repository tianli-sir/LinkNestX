// Package social owns employee profiles, accepted contacts and closed direct pairs.
package social

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"github.com/google/uuid"
	"github.com/linknestx/linknestx/server/internal/auth"
	"github.com/linknestx/linknestx/server/internal/chat"
	"github.com/linknestx/linknestx/server/internal/groups"
	"image"
	_ "image/jpeg"
	"image/png"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var ErrInvalid = errors.New("invalid social input")
var ErrNotFound = errors.New("profile or request not found")

type Service struct {
	db   *sql.DB
	auth *auth.AuthService
	chat *chat.ChatService
	hub  *chat.Hub
}

func New(a *auth.AuthService, c *chat.ChatService, h *chat.Hub) *Service {
	return &Service{a.Database(), a, c, h}
}
func pair(a, b string) (string, string) {
	if a > b {
		return b, a
	}
	return a, b
}
func (s *Service) Profile(ctx context.Context, actor, id string) (auth.User, error) {
	if e := s.auth.ValidateActor(ctx, actor); e != nil {
		return auth.User{}, e
	}
	u, e := s.auth.User(ctx, id)
	if errors.Is(e, sql.ErrNoRows) || u.Disabled {
		return auth.User{}, ErrNotFound
	}
	return u, e
}
func (s *Service) Search(ctx context.Context, actor, number string) (auth.User, error) {
	var id string
	if e := s.db.QueryRowContext(ctx, `SELECT user_id FROM profiles WHERE account_number=?`, number).Scan(&id); e != nil {
		return auth.User{}, ErrNotFound
	}
	return s.Profile(ctx, actor, id)
}
func (s *Service) UpdateProfile(ctx context.Context, actor, name string) (auth.User, error) {
	if e := s.auth.ValidateActor(ctx, actor); e != nil {
		return auth.User{}, e
	}
	name = strings.TrimSpace(name)
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 80 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return auth.User{}, ErrInvalid
	}
	if _, e := s.db.ExecContext(ctx, `UPDATE profiles SET display_name=? WHERE user_id=?`, name, actor); e != nil {
		return auth.User{}, e
	}
	s.profileChanged(ctx, actor)
	return s.Profile(ctx, actor, actor)
}
func (s *Service) Avatar(ctx context.Context, actor, id string) ([]byte, error) {
	if _, e := s.Profile(ctx, actor, id); e != nil {
		return nil, e
	}
	var b []byte
	e := s.db.QueryRowContext(ctx, `SELECT avatar FROM profiles WHERE user_id=?`, id).Scan(&b)
	if len(b) == 0 && e == nil {
		e = ErrNotFound
	}
	return b, e
}
func (s *Service) SetAvatar(ctx context.Context, actor string, data []byte) (auth.User, error) {
	if e := s.auth.ValidateActor(ctx, actor); e != nil {
		return auth.User{}, e
	}
	if len(data) == 0 || len(data) > 2*1024*1024 {
		return auth.User{}, ErrInvalid
	}
	cfg, kind, e := image.DecodeConfig(bytes.NewReader(data))
	if e != nil || (kind != "png" && kind != "jpeg") || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 4096 || cfg.Height > 4096 || int64(cfg.Width)*int64(cfg.Height) > 16000000 {
		return auth.User{}, ErrInvalid
	}
	source, _, e := image.Decode(bytes.NewReader(data))
	if e != nil {
		return auth.User{}, ErrInvalid
	}
	width, height := cfg.Width, cfg.Height
	if width > 256 || height > 256 {
		if width >= height {
			height = height * 256 / width
			width = 256
		} else {
			width = width * 256 / height
			height = 256
		}
	}
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	normalized := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			normalized.Set(x, y, source.At(x*cfg.Width/width, y*cfg.Height/height))
		}
	}
	var out bytes.Buffer
	if e = png.Encode(&out, normalized); e != nil {
		return auth.User{}, e
	}
	if _, e = s.db.ExecContext(ctx, `UPDATE profiles SET avatar=? WHERE user_id=?`, out.Bytes(), actor); e != nil {
		return auth.User{}, e
	}
	s.profileChanged(ctx, actor)
	return s.Profile(ctx, actor, actor)
}
func (s *Service) profileChanged(ctx context.Context, user string) {
	if s.hub == nil {
		return
	}
	ids := []string{user}
	rows, e := s.db.QueryContext(ctx, `SELECT DISTINCT b.user_id FROM group_members a JOIN group_members b ON b.group_id=a.group_id WHERE a.user_id=? UNION SELECT CASE WHEN pair_low=? THEN pair_high ELSE pair_low END FROM friendships WHERE ? IN(pair_low,pair_high)`, user, user, user)
	if e == nil {
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		rows.Close()
	}
	s.hub.NotifyUsers(ids, chat.Event{Type: "profile.changed", UserID: user})
}

type FriendRequest struct {
	ID        string    `json:"id"`
	From      auth.User `json:"from"`
	To        auth.User `json:"to"`
	Status    string    `json:"status"`
	CreatedAt string    `json:"createdAt"`
}
type Contacts struct {
	Friends  []auth.User     `json:"friends"`
	Incoming []FriendRequest `json:"incoming"`
	Outgoing []FriendRequest `json:"outgoing"`
}

func (s *Service) request(ctx context.Context, id string) (FriendRequest, error) {
	r := FriendRequest{ID: id}
	var a, b string
	e := s.db.QueryRowContext(ctx, `SELECT from_id,to_id,status,created_at FROM friend_requests WHERE id=?`, id).Scan(&a, &b, &r.Status, &r.CreatedAt)
	if errors.Is(e, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if e != nil {
		return r, e
	}
	r.From, e = s.auth.User(ctx, a)
	if e == nil {
		r.To, e = s.auth.User(ctx, b)
	}
	return r, e
}
func (s *Service) Contacts(ctx context.Context, actor string) (Contacts, error) {
	result := Contacts{[]auth.User{}, []FriendRequest{}, []FriendRequest{}}
	if e := s.auth.ValidateActor(ctx, actor); e != nil {
		return result, e
	}
	rows, e := s.db.QueryContext(ctx, `SELECT CASE WHEN pair_low=? THEN pair_high ELSE pair_low END FROM friendships WHERE ? IN(pair_low,pair_high)`, actor, actor)
	if e != nil {
		return result, e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return result, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return result, e
	}
	for _, id := range ids {
		p, e := s.Profile(ctx, actor, id)
		if e == nil {
			result.Friends = append(result.Friends, p)
		}
	}
	rows, e = s.db.QueryContext(ctx, `SELECT id FROM friend_requests WHERE status='pending' AND ? IN(from_id,to_id) ORDER BY created_at,id`, actor)
	if e != nil {
		return result, e
	}
	ids = nil
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return result, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return result, e
	}
	for _, id := range ids {
		r, e := s.request(ctx, id)
		if e != nil {
			return result, e
		}
		if r.From.Disabled || r.To.Disabled {
			continue
		}
		if r.From.ID == actor {
			result.Outgoing = append(result.Outgoing, r)
		} else {
			result.Incoming = append(result.Incoming, r)
		}
	}
	return result, nil
}
func active(ctx context.Context, tx *sql.Tx, actor string) error {
	var n int
	e := tx.QueryRowContext(ctx, `SELECT count(*) FROM users u JOIN sessions s ON s.user_id=u.id WHERE u.id=? AND u.disabled=0 AND u.must_change_password=0 AND s.id=? AND s.revoked_at IS NULL AND julianday(s.expires_at)>julianday('now')`, actor, auth.SessionIDFromContext(ctx)).Scan(&n)
	if e == nil && n == 0 {
		return auth.ErrUnauthorized
	}
	return e
}
func (s *Service) Request(ctx context.Context, actor, number string) (FriendRequest, error) {
	target, e := s.Search(ctx, actor, number)
	if e != nil {
		return FriendRequest{}, e
	}
	if target.ID == actor {
		return FriendRequest{}, ErrInvalid
	}
	lo, hi := pair(actor, target.ID)
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return FriendRequest{}, e
	}
	defer tx.Rollback()
	if e = active(ctx, tx, actor); e != nil {
		return FriendRequest{}, e
	}
	id := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	// Acquire a write reservation before checking pair state. Concurrent reversed requests share one row.
	if _, e = tx.ExecContext(ctx, `UPDATE users SET id=id WHERE id=?`, actor); e != nil {
		return FriendRequest{}, e
	}
	var existing string
	e = tx.QueryRowContext(ctx, `SELECT id FROM friend_requests WHERE pair_low=? AND pair_high=? AND (status='pending' OR (status='accepted' AND EXISTS(SELECT 1 FROM friendships WHERE pair_low=? AND pair_high=?))) ORDER BY created_at DESC LIMIT 1`, lo, hi, lo, hi).Scan(&existing)
	if e != nil && e != sql.ErrNoRows {
		return FriendRequest{}, e
	}
	if existing != "" {
		id = existing
	} else {
		var enabled int
		if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE id=? AND disabled=0`, target.ID).Scan(&enabled); e != nil {
			return FriendRequest{}, e
		}
		if enabled == 0 {
			return FriendRequest{}, ErrNotFound
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO friend_requests(id,from_id,to_id,pair_low,pair_high,status,created_at) VALUES(?,?,?,?,?,'pending',?)`, id, actor, target.ID, lo, hi, now)
		if e != nil {
			return FriendRequest{}, e
		}
	}
	if e = tx.Commit(); e != nil {
		return FriendRequest{}, e
	}
	s.contactsChanged(lo, hi)
	return s.request(ctx, id)
}
func (s *Service) Act(ctx context.Context, actor, id, action string) (FriendRequest, error) {
	if e := s.auth.ValidateActor(ctx, actor); e != nil {
		return FriendRequest{}, e
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return FriendRequest{}, e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `UPDATE friend_requests SET id=id WHERE id=?`, id); e != nil {
		return FriendRequest{}, e
	}
	if e = active(ctx, tx, actor); e != nil {
		return FriendRequest{}, e
	}
	var from, to, status, lo, hi string
	if e = tx.QueryRowContext(ctx, `SELECT from_id,to_id,status,pair_low,pair_high FROM friend_requests WHERE id=?`, id).Scan(&from, &to, &status, &lo, &hi); e != nil {
		return FriendRequest{}, ErrNotFound
	}
	next := ""
	switch action {
	case "accept":
		next = "accepted"
	case "reject":
		next = "rejected"
	case "cancel":
		next = "cancelled"
	default:
		return FriendRequest{}, ErrInvalid
	}
	if (action == "cancel" && actor != from) || (action != "cancel" && actor != to) {
		return FriendRequest{}, groups.ErrForbidden
	}
	if status != next && status != "pending" {
		return FriendRequest{}, ErrInvalid
	}
	if next == "accepted" {
		if status == "accepted" {
			var existing int
			if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM friendships WHERE pair_low=? AND pair_high=?`, lo, hi).Scan(&existing); e != nil {
				return FriendRequest{}, e
			}
			if existing == 0 {
				return FriendRequest{}, ErrInvalid
			}
		}
		var enabled int
		if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE id IN(?,?) AND disabled=0`, lo, hi).Scan(&enabled); e != nil {
			return FriendRequest{}, e
		}
		if enabled != 2 {
			return FriendRequest{}, groups.ErrForbidden
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO friendships VALUES(?,?,?) ON CONFLICT DO NOTHING`, lo, hi, time.Now().UTC().Format(time.RFC3339Nano)); e != nil {
			return FriendRequest{}, e
		}
	}
	if _, e = tx.ExecContext(ctx, `UPDATE friend_requests SET status=? WHERE id=?`, next, id); e != nil {
		return FriendRequest{}, e
	}
	if e = tx.Commit(); e != nil {
		return FriendRequest{}, e
	}
	s.contactsChanged(lo, hi)
	return s.request(ctx, id)
}
func (s *Service) Delete(ctx context.Context, actor, other string) error {
	if e := s.auth.ValidateActor(ctx, actor); e != nil {
		return e
	}
	lo, hi := pair(actor, other)
	if _, e := s.db.ExecContext(ctx, `DELETE FROM friendships WHERE pair_low=? AND pair_high=?`, lo, hi); e != nil {
		return e
	}
	s.contactsChanged(lo, hi)
	return nil
}
func (s *Service) contactsChanged(lo, hi string) {
	if s.hub != nil {
		s.hub.NotifyUsers([]string{lo, hi}, chat.Event{Type: "contacts.changed"})
		s.hub.NotifyUsers([]string{lo, hi}, chat.Event{Type: "conversation.changed"})
	}
}

type Conversation struct {
	ID          string        `json:"id"`
	Kind        string        `json:"kind"`
	Title       string        `json:"title"`
	AvatarURL   string        `json:"avatarUrl,omitempty"`
	Peer        *auth.User    `json:"peer,omitempty"`
	MemberCount int           `json:"memberCount"`
	UnreadCount int           `json:"unreadCount"`
	LastMessage *chat.Message `json:"lastMessage,omitempty"`
	CanSend     bool          `json:"canSend"`
}

func (s *Service) OpenDirect(ctx context.Context, actor, other string) (Conversation, error) {
	if e := s.auth.ValidateActor(ctx, actor); e != nil {
		return Conversation{}, e
	}
	if other == actor {
		return Conversation{}, ErrInvalid
	}
	lo, hi := pair(actor, other)
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return Conversation{}, e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `UPDATE users SET id=id WHERE id=?`, actor); e != nil {
		return Conversation{}, e
	}
	if e = active(ctx, tx, actor); e != nil {
		return Conversation{}, e
	}
	var n int
	e = tx.QueryRowContext(ctx, `SELECT count(*) FROM friendships f JOIN users a ON a.id=f.pair_low JOIN users b ON b.id=f.pair_high WHERE pair_low=? AND pair_high=? AND a.disabled=0 AND b.disabled=0`, lo, hi).Scan(&n)
	if e != nil {
		return Conversation{}, e
	}
	if n != 1 {
		return Conversation{}, groups.ErrForbidden
	}
	var id string
	e = tx.QueryRowContext(ctx, `SELECT group_id FROM direct_conversations WHERE pair_low=? AND pair_high=?`, lo, hi).Scan(&id)
	if e == sql.ErrNoRows {
		id = uuid.NewString()
		now := time.Now().UTC().Format(time.RFC3339Nano)
		for _, q := range []struct {
			sql  string
			args []any
		}{{`INSERT INTO groups(id,name,created_by,created_at) VALUES(?,'',?,?)`, []any{id, actor, now}}, {`INSERT INTO group_members VALUES(?,?,'member',?),(?,?,'member',?)`, []any{id, lo, now, id, hi, now}}, {`INSERT INTO direct_conversations VALUES(?,?,?)`, []any{id, lo, hi}}} {
			if _, e = tx.ExecContext(ctx, q.sql, q.args...); e != nil {
				return Conversation{}, e
			}
		}
	} else if e != nil {
		return Conversation{}, e
	}
	if e = tx.Commit(); e != nil {
		return Conversation{}, e
	}
	s.contactsChanged(lo, hi)
	return s.conversation(ctx, actor, id)
}
func (s *Service) Conversations(ctx context.Context, actor string) ([]Conversation, error) {
	if e := s.auth.ValidateActor(ctx, actor); e != nil {
		return nil, e
	}
	rows, e := s.db.QueryContext(ctx, `SELECT g.id FROM groups g JOIN group_members m ON m.group_id=g.id LEFT JOIN direct_conversations d ON d.group_id=g.id WHERE m.user_id=? AND (d.group_id IS NULL OR ? IN(d.pair_low,d.pair_high)) ORDER BY COALESCE((SELECT max(sequence) FROM messages WHERE group_id=g.id),0) DESC,g.id`, actor, actor)
	if e != nil {
		return nil, e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return nil, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	out := []Conversation{}
	for _, id := range ids {
		v, e := s.conversation(ctx, actor, id)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, nil
}
func (s *Service) conversation(ctx context.Context, actor, id string) (Conversation, error) {
	v := Conversation{ID: id, Kind: "group", CanSend: true}
	if e := s.db.QueryRowContext(ctx, `SELECT name FROM groups WHERE id=?`, id).Scan(&v.Title); e != nil {
		return v, e
	}
	var other string
	e := s.db.QueryRowContext(ctx, `SELECT CASE WHEN pair_low=? THEN pair_high ELSE pair_low END FROM direct_conversations WHERE group_id=?`, actor, id).Scan(&other)
	if e != nil && e != sql.ErrNoRows {
		return v, e
	}
	if other != "" {
		v.Kind = "direct"
		p, e := s.auth.User(ctx, other)
		if e != nil {
			return v, e
		}
		v.Peer = &p
		v.Title = p.DisplayName
		v.AvatarURL = p.AvatarURL
		v.CanSend = groups.RequireSend(ctx, s.db, actor, id) == nil
	}
	if e = s.db.QueryRowContext(ctx, `SELECT count(*) FROM group_members m JOIN users u ON u.id=m.user_id WHERE m.group_id=? AND u.disabled=0`, id).Scan(&v.MemberCount); e != nil {
		return v, e
	}
	if e = s.db.QueryRowContext(ctx, `SELECT count(*) FROM messages m WHERE m.group_id=? AND m.sender_id<>? AND m.expired_at IS NULL AND NOT EXISTS(SELECT 1 FROM message_reads r WHERE r.message_id=m.id AND r.user_id=?)`, id, actor, actor).Scan(&v.UnreadCount); e != nil {
		return v, e
	}
	page, e := s.chat.ConversationPage(ctx, actor, id, "", "", 1)
	if e != nil {
		return v, e
	}
	if len(page.Messages) > 0 {
		v.LastMessage = &page.Messages[0]
	}
	return v, nil
}
