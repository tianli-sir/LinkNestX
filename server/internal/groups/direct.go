package groups

import (
	"context"
	"database/sql"
)

type Querier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func IsDirect(ctx context.Context, q Querier, id string) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM direct_conversations WHERE group_id=?`, id).Scan(&n)
	return n > 0, err
}

// RequireDirectMember runs before every administrator or pool privilege fallback.
func RequireDirectMember(ctx context.Context, q Querier, actor, id string) (bool, error) {
	direct, err := IsDirect(ctx, q, id)
	if err != nil || !direct {
		return direct, err
	}
	var n int
	err = q.QueryRowContext(ctx, `SELECT count(*) FROM direct_conversations d JOIN group_members m ON m.group_id=d.group_id JOIN users u ON u.id=m.user_id WHERE d.group_id=? AND m.user_id=? AND m.user_id IN(d.pair_low,d.pair_high) AND u.disabled=0`, id, actor).Scan(&n)
	if err == nil && n == 0 {
		err = ErrForbidden
	}
	return true, err
}
func RequireSend(ctx context.Context, q Querier, actor, id string) error {
	direct, err := RequireDirectMember(ctx, q, actor, id)
	if err != nil || !direct {
		return err
	}
	var n int
	err = q.QueryRowContext(ctx, `SELECT count(*) FROM direct_conversations d JOIN friendships f ON f.pair_low=d.pair_low AND f.pair_high=d.pair_high JOIN users a ON a.id=d.pair_low JOIN users b ON b.id=d.pair_high WHERE d.group_id=? AND a.disabled=0 AND b.disabled=0`, id).Scan(&n)
	if err == nil && n == 0 {
		return ErrForbidden
	}
	return err
}
