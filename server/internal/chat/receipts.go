package chat

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"time"

	"github.com/linknestx/linknestx/server/internal/auth"
)

// The same entitlement predicate drives the public readers list and count.
const entitledReaders = ` FROM message_reads r JOIN messages m ON m.id=r.message_id JOIN users u ON u.id=r.user_id JOIN profiles p ON p.user_id=u.id JOIN group_members gm ON gm.group_id=m.group_id AND gm.user_id=r.user_id WHERE r.message_id=? AND r.user_id<>m.sender_id AND u.disabled=0 AND m.expired_at IS NULL AND (NOT EXISTS(SELECT 1 FROM direct_conversations d WHERE d.group_id=m.group_id) OR EXISTS(SELECT 1 FROM direct_conversations d WHERE d.group_id=m.group_id AND r.user_id IN(d.pair_low,d.pair_high)))`

func (s *ChatService) project(ctx context.Context, actor string, m *Message) error {
	return s.projectWith(ctx, actor, m, nil)
}

// projectWith resolves the sender and read state. nicknames is nil for
// single-message paths (one extra lookup per message) or a prefetched map for
// page projection (missing key = no nickname, no extra query).
func (s *ChatService) projectWith(ctx context.Context, actor string, m *Message, nicknames map[string]string) error {
	m.ConversationID = m.GroupID
	u, e := s.auth.User(ctx, m.SenderID)
	if e != nil {
		return e
	}
	m.Sender = &u
	if e = s.applyGroupNickname(ctx, m, nicknames); e != nil {
		return e
	}
	if e = s.repo.db.QueryRowContext(ctx, `SELECT count(*)`+entitledReaders, m.ID).Scan(&m.ReadCount); e != nil {
		return e
	}
	return s.repo.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM message_reads WHERE message_id=? AND user_id=?)`, m.ID, actor).Scan(&m.ReadByMe)
}

// applyGroupNickname overlays the sender's group nickname on the global profile
// name. Direct conversations never carry nicknames.
func (s *ChatService) applyGroupNickname(ctx context.Context, m *Message, nicknames map[string]string) error {
	if m.Sender == nil {
		return nil
	}
	nick, present := nicknames[m.SenderID]
	if !present && nicknames == nil {
		var value string
		e := s.repo.db.QueryRowContext(ctx, `SELECT COALESCE(NULLIF(gm.nickname,''),'') FROM group_members gm WHERE gm.group_id=? AND gm.user_id=? AND NOT EXISTS(SELECT 1 FROM direct_conversations d WHERE d.group_id=gm.group_id)`, m.GroupID, m.SenderID).Scan(&value)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		nick = value
	}
	if nick != "" {
		m.Sender.DisplayName = nick
	}
	return nil
}

// prefetchNicknames loads a group's non-empty nicknames in one query so page
// projection avoids a per-message lookup.
func (s *ChatService) prefetchNicknames(ctx context.Context, groupID string) (map[string]string, error) {
	rows, e := s.repo.db.QueryContext(ctx, `SELECT gm.user_id,gm.nickname FROM group_members gm WHERE gm.group_id=? AND COALESCE(gm.nickname,'')<>'' AND NOT EXISTS(SELECT 1 FROM direct_conversations d WHERE d.group_id=gm.group_id)`, groupID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	nicknames := map[string]string{}
	for rows.Next() {
		var id, nickname string
		if e = rows.Scan(&id, &nickname); e != nil {
			return nil, e
		}
		nicknames[id] = nickname
	}
	return nicknames, rows.Err()
}
func (s *ChatService) ConversationPage(ctx context.Context, actor, id, before, after string, limit int) (MessagePage, error) {
	if e := s.groups.RequireMember(ctx, actor, id); e != nil {
		return MessagePage{}, e
	}
	if before != "" && after != "" {
		return MessagePage{}, ErrInvalidInput
	}
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 200 {
		return MessagePage{}, ErrInvalidInput
	}
	cursor := before
	if after != "" {
		cursor = after
	}
	seq, e := decodeCursor(id, cursor)
	if e != nil {
		return MessagePage{}, e
	}
	tx, e := s.repo.db.BeginTx(ctx, nil)
	if e != nil {
		return MessagePage{}, e
	}
	defer tx.Rollback()
	if e = memberInTransaction(ctx, tx, actor, id); e != nil {
		return MessagePage{}, e
	}
	query := `SELECT ` + messageColumns + ` FROM messages WHERE group_id=?`
	args := []any{id}
	order := " DESC"
	if after != "" {
		query += ` AND sequence>?`
		args = append(args, seq)
		order = " ASC"
	} else if before != "" {
		query += ` AND sequence<?`
		args = append(args, seq)
	}
	query += ` ORDER BY sequence` + order + ` LIMIT ?`
	args = append(args, limit+1)
	rows, e := tx.QueryContext(ctx, query, args...)
	if e != nil {
		return MessagePage{}, e
	}
	page := MessagePage{Messages: []Message{}}
	for rows.Next() {
		m, e := scanMessage(rows)
		if e != nil {
			rows.Close()
			return page, e
		}
		if len(page.Messages) == limit {
			page.HasMore = true
			break
		}
		page.Messages = append(page.Messages, m)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return page, e
	}
	if e = tx.Commit(); e != nil {
		return page, e
	}
	sort.Slice(page.Messages, func(i, j int) bool { return page.Messages[i].Sequence < page.Messages[j].Sequence })
	nicknames, e := s.prefetchNicknames(ctx, id)
	if e != nil {
		return page, e
	}
	for i := range page.Messages {
		if e = s.projectWith(ctx, actor, &page.Messages[i], nicknames); e != nil {
			return page, e
		}
	}
	if len(page.Messages) > 0 {
		page.NextCursor = page.Messages[0].Cursor
		if after != "" {
			page.NextCursor = page.Messages[len(page.Messages)-1].Cursor
		}
	}
	return page, nil
}
func (s *ChatService) Read(ctx context.Context, actor, id string, ids []string) ([]string, error) {
	if e := s.groups.RequireMember(ctx, actor, id); e != nil {
		return nil, e
	}
	if len(ids) == 0 || len(ids) > 100 {
		return nil, ErrInvalidInput
	}
	unique := []string{}
	seen := map[string]bool{}
	for _, v := range ids {
		if v == "" || len(v) > 128 {
			return nil, ErrInvalidInput
		}
		if !seen[v] {
			seen[v] = true
			unique = append(unique, v)
		}
	}
	tx, e := s.repo.db.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `UPDATE groups SET id=id WHERE id=?`, id); e != nil {
		return nil, e
	}
	if e = memberInTransaction(ctx, tx, actor, id); e != nil {
		return nil, e
	}
	for _, message := range unique {
		var n int
		if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE id=? AND group_id=? AND expired_at IS NULL`, message, id).Scan(&n); e != nil {
			return nil, e
		}
		if n != 1 {
			return nil, ErrInvalidInput
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, message := range unique {
		if _, e = tx.ExecContext(ctx, `INSERT INTO message_reads VALUES(?,?,?) ON CONFLICT DO NOTHING`, message, actor, now); e != nil {
			return nil, e
		}
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	if s.hub != nil {
		s.hub.NotifyConversation(id, Event{Type: "read.changed", UserID: actor, MessageIDs: unique})
		s.hub.NotifyConversation(id, Event{Type: "conversation.changed"})
	}
	return unique, nil
}

type Reader struct {
	auth.User
	ReadAt string `json:"readAt"`
}
type Readers struct {
	Readers []Reader `json:"readers"`
	Count   int      `json:"count"`
}

func (s *ChatService) Readers(ctx context.Context, actor, id, message string) (Readers, error) {
	out := Readers{Readers: []Reader{}}
	if e := s.groups.RequireMember(ctx, actor, id); e != nil {
		return out, e
	}
	tx, e := s.repo.db.BeginTx(ctx, nil)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	if e = memberInTransaction(ctx, tx, actor, id); e != nil {
		return out, e
	}
	var n int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE id=? AND group_id=? AND expired_at IS NULL`, message, id).Scan(&n); e != nil {
		return out, e
	}
	if n != 1 {
		return out, ErrInvalidInput
	}
	rows, e := tx.QueryContext(ctx, `SELECT u.id,u.username,u.email,u.disabled,u.must_change_password,u.created_at,p.account_number,CASE WHEN EXISTS(SELECT 1 FROM direct_conversations d WHERE d.group_id=gm.group_id) THEN COALESCE(NULLIF(p.display_name,''),u.username) ELSE COALESCE(NULLIF(gm.nickname,''),NULLIF(p.display_name,''),u.username) END,p.avatar IS NOT NULL,r.read_at`+entitledReaders+` ORDER BY r.read_at,u.id`, message)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		var r Reader
		var at string
		var avatar bool
		if e = rows.Scan(&r.ID, &r.Username, &r.Email, &r.Disabled, &r.MustChangePassword, &at, &r.AccountNumber, &r.DisplayName, &avatar, &r.ReadAt); e != nil {
			return out, e
		}
		r.CreatedAt, _ = time.Parse(time.RFC3339Nano, at)
		if avatar {
			r.AvatarURL = "/api/v1/profiles/" + r.ID + "/avatar"
		}
		out.Readers = append(out.Readers, r)
	}
	out.Count = len(out.Readers)
	return out, rows.Err()
}
