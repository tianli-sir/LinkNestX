package auth

import (
	"context"
	"database/sql"
	"time"
)

// RecordAudit appends operation attribution in the caller's mutation transaction.
// Identity is taken from the verified context; targets never establish identity.
func RecordAudit(ctx context.Context, tx *sql.Tx, actor, action, target string, at time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_id,device_id,created_at) VALUES(?,?,?,?,?,?)`, newID(), actor, action, target, DeviceIDFromContext(ctx), at.UTC().Format("2006-01-02T15:04:05.000000000Z"))
	return err
}
