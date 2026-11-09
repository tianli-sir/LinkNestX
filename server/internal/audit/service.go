package audit

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/linknestx/linknestx/server/internal/auth"
)

const timestampLayout = "2006-01-02T15:04:05.000000000Z"

var ErrInvalidInput = errors.New("invalid audit filter or transfer record")

type Service struct {
	db   *sql.DB
	auth *auth.AuthService
}

func NewService(conn *sql.DB, a *auth.AuthService) *Service { return &Service{conn, a} }

type Event struct {
	ID        string    `json:"id"`
	ActorID   string    `json:"actorId"`
	Action    string    `json:"action"`
	FileID    string    `json:"fileId"`
	VersionID string    `json:"versionId"`
	Bytes     int64     `json:"bytes"`
	CreatedAt time.Time `json:"createdAt"`
}
type Filter struct {
	ActorID, FileID, VersionID, Action string
	From, Before                       time.Time
	Limit                              int
}

func (s *Service) List(ctx context.Context, actorID string, f Filter) ([]Event, error) {
	if err := s.auth.ValidateActor(ctx, actorID); err != nil {
		return nil, err
	}
	isAdmin, err := s.auth.IsAdmin(ctx, actorID)
	if err != nil {
		return nil, err
	}
	if !isAdmin {
		return nil, auth.ErrAdminRequired
	}
	if f.Limit == 0 {
		f.Limit = 100
	}
	if f.Limit < 1 || f.Limit > 1000 {
		return nil, ErrInvalidInput
	}
	query := `SELECT id,COALESCE(actor_id,''),action,COALESCE(file_id,''),COALESCE(version_id,''),bytes,created_at FROM audit_events WHERE 1=1`
	args := []any{}
	for _, item := range []struct{ col, value string }{{"actor_id", f.ActorID}, {"file_id", f.FileID}, {"version_id", f.VersionID}, {"action", f.Action}} {
		if item.value != "" {
			query += " AND " + item.col + "=?"
			args = append(args, item.value)
		}
	}
	if !f.From.IsZero() {
		query += " AND created_at>=?"
		args = append(args, f.From.UTC().Format(timestampLayout))
	}
	if !f.Before.IsZero() {
		query += " AND created_at<?"
		args = append(args, f.Before.UTC().Format(timestampLayout))
	}
	query += " ORDER BY created_at DESC,id DESC LIMIT ?"
	args = append(args, f.Limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []Event{}
	for rows.Next() {
		var e Event
		var at string
		if err = rows.Scan(&e.ID, &e.ActorID, &e.Action, &e.FileID, &e.VersionID, &e.Bytes, &at); err != nil {
			return nil, err
		}
		e.CreatedAt, err = time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// RecordServerDelivery records completed server writes, not a claim that a
// recipient verified an entire file. Transfers are aggregated per task/node.
func (s *Service) RecordServerDelivery(ctx context.Context, actorID, versionID, taskID string, n int64) error {
	if err := s.auth.ValidateActor(ctx, actorID); err != nil {
		return err
	}
	if taskID == "" {
		taskID = versionID
	}
	if len(taskID) > 128 || versionID == "" || n < 0 {
		return ErrInvalidInput
	}
	at := time.Now().UTC().Format(timestampLayout)
	_, err := s.db.ExecContext(ctx, `INSERT INTO transfer_records(actor_id,version_id,task_id,node_id,action,bytes,created_at,updated_at) VALUES(?,?,?,'server','server.sent',?,?,?) ON CONFLICT(actor_id,version_id,task_id,node_id,action) DO UPDATE SET bytes=bytes+excluded.bytes,updated_at=excluded.updated_at`, actorID, versionID, taskID, n, at, at)
	return err
}
func (s *Service) DeleteBeforeTwoYears(ctx context.Context, now time.Time) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	cutoff := now.UTC().AddDate(-2, 0, 0).Format(timestampLayout)
	result, err := tx.ExecContext(ctx, `DELETE FROM audit_events WHERE created_at<?`, cutoff)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	result, err = tx.ExecContext(ctx, `DELETE FROM transfer_records WHERE updated_at<?`, cutoff)
	if err != nil {
		return 0, err
	}
	m, _ := result.RowsAffected()
	return n + m, tx.Commit()
}
