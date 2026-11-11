package groups

import (
	"context"
	"database/sql"
	"github.com/google/uuid"
	"time"
)

// The notice is written in the membership transaction. Publication happens only
// after commit, and the chat hub rechecks current membership before delivery.
func appendSystemMessage(ctx context.Context, tx *sql.Tx, actor, group, target, event string, at time.Time) (string, error) {
	name := func(id string) (string, error) {
		var value string
		err := tx.QueryRowContext(ctx, "SELECT CASE WHEN p.display_name='' THEN u.username ELSE p.display_name END FROM users u JOIN profiles p ON p.user_id=u.id WHERE u.id=?", id).Scan(&value)
		return value, err
	}
	actorName, err := name(actor)
	if err != nil {
		return "", err
	}
	body := actorName + "创建了群聊"
	if event != "group.created" {
		targetName, err := name(target)
		if err != nil {
			return "", err
		}
		switch event {
		case "group.member_added":
			body = actorName + "邀请" + targetName + "加入了群聊"
		case "group.member_removed":
			body = actorName + "将" + targetName + "移出了群聊"
		case "group.owner_changed":
			var role string
			if err := tx.QueryRowContext(ctx, "SELECT role FROM group_members WHERE group_id=? AND user_id=?", group, target).Scan(&role); err != nil {
				return "", err
			}
			if role == "owner" {
				body = actorName + "将" + targetName + "设为群主"
			} else {
				body = actorName + "将" + targetName + "设为普通成员"
			}
		}
	}
	id := uuid.NewString()
	_, err = tx.ExecContext(ctx, "INSERT INTO messages(id,group_id,sender_id,client_message_id,body,created_at,kind,system_type) VALUES(?,?,?,?,?,?,'system',?)", id, group, actor, "system:"+id, body, at.UTC().Format("2006-01-02T15:04:05.000000000Z"), event)
	return id, err
}
