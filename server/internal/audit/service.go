package audit

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/linknestx/linknestx/server/internal/auth"
)

const timestampLayout = "2006-01-02T15:04:05.000000000Z"

var ErrInvalidInput = errors.New("invalid audit filter or transfer record")
var ErrTransferForbidden = errors.New("transfer version or source access denied")

type Service struct {
	db   *sql.DB
	auth *auth.AuthService
}

func NewService(conn *sql.DB, a *auth.AuthService) *Service { return &Service{conn, a} }

type Event struct {
	ID        string    `json:"id"`
	ActorID   string    `json:"actorId"`
	Action    string    `json:"action"`
	TargetID  string    `json:"targetId"`
	DeviceID  string    `json:"deviceId"`
	FileID    string    `json:"fileId"`
	VersionID string    `json:"versionId"`
	TaskID    string    `json:"taskId"`
	NodeID    string    `json:"nodeId"`
	Bytes     int64     `json:"bytes"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"createdAt"`
}
type Filter struct {
	cursorAt, cursorID                           string
	ActorID, DeviceID, FileID, VersionID, Action string
	From, Before                                 time.Time
	Limit, Offset                                int
}
type Page struct {
	Events []Event `json:"events"`
	Total  int64   `json:"total"`
}

const eventUnion = `WITH events AS (
 SELECT id,COALESCE(actor_id,'') actor_id,action,target_id,device_id,COALESCE(file_id,'') file_id,COALESCE(version_id,'') version_id,COALESCE(task_id,'') task_id,COALESCE(node_id,'') node_id,bytes,source,created_at FROM audit_events
 UNION ALL SELECT 'server:'||actor_id||':'||device_id||':'||version_id||':'||task_id||':'||node_id||':'||action,actor_id,action,'',device_id,COALESCE((SELECT file_id FROM file_versions WHERE id=version_id),''),version_id,task_id,node_id,bytes,'server-observed',updated_at FROM transfer_records
 UNION ALL SELECT 'client:'||id,actor_id,action,'',device_id,file_id,version_id,task_id,node_id,bytes,'client-reported',updated_at FROM client_transfer_records
 UNION ALL SELECT 'connection:'||connection_id,requester_user_id,'peer.connection.'||outcome,'',requester_device_id,COALESCE((SELECT file_id FROM file_versions WHERE id=version_id),''),version_id,connection_id,source_device_id,0,'server-observed',COALESCE(ended_at,started_at) FROM peer_transfers
) `
const eventColumns = `id,actor_id,action,target_id,device_id,file_id,version_id,task_id,node_id,bytes,source,created_at`

func validID(id string) bool {
	u, e := uuid.Parse(id)
	return e == nil && u != uuid.Nil && u.String() == id
}
func validateFilter(f Filter) (Filter, error) {
	if f.Limit == 0 {
		f.Limit = 50
	}
	if f.Limit < 1 || f.Limit > 1000 || f.Offset < 0 || f.Offset > 10000000 || len(f.Action) > 128 || !utf8.ValidString(f.Action) || strings.ContainsAny(f.Action, "\r\n\x00") {
		return f, ErrInvalidInput
	}
	for _, id := range []string{f.ActorID, f.DeviceID, f.FileID, f.VersionID} {
		if id != "" && !validID(id) {
			return f, ErrInvalidInput
		}
	}
	if !f.From.IsZero() && !f.Before.IsZero() && !f.From.Before(f.Before) {
		return f, ErrInvalidInput
	}
	return f, nil
}
func filterSQL(f Filter) (string, []any) {
	query := " WHERE 1=1"
	args := []any{}
	for _, item := range []struct{ col, value string }{{"actor_id", f.ActorID}, {"device_id", f.DeviceID}, {"file_id", f.FileID}, {"version_id", f.VersionID}, {"action", f.Action}} {
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
	if f.cursorAt != "" {
		query += " AND (created_at<? OR (created_at=? AND id<?))"
		args = append(args, f.cursorAt, f.cursorAt, f.cursorID)
	}
	return query, args
}
func (s *Service) adminSnapshot(ctx context.Context) (*sql.Tx, error) {
	actor, ok := auth.UserFromContext(ctx)
	if !ok {
		return nil, auth.ErrAdminRequired
	}
	if err := s.auth.ValidateActor(ctx, actor.ID); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	if _, err := s.auth.AccessExpiryFromContext(ctx); err != nil {
		tx.Rollback()
		return nil, err
	}
	var allowed int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM users u JOIN sessions s ON s.user_id=u.id WHERE u.id=? AND u.disabled=0 AND u.must_change_password=0 AND s.id=? AND s.device_id=? AND s.revoked_at IS NULL AND julianday(s.expires_at)>julianday('now') AND EXISTS(SELECT 1 FROM user_roles ur JOIN roles r ON r.id=ur.role_id WHERE ur.user_id=u.id AND r.name='admin')`, actor.ID, auth.SessionIDFromContext(ctx), auth.DeviceIDFromContext(ctx)).Scan(&allowed)
	if err != nil || allowed != 1 {
		tx.Rollback()
		if err != nil {
			return nil, err
		}
		return nil, auth.ErrAdminRequired
	}
	return tx, nil
}
func readEvents(ctx context.Context, tx *sql.Tx, f Filter) ([]Event, error) {
	where, args := filterSQL(f)
	args = append(args, f.Limit, f.Offset)
	rows, err := tx.QueryContext(ctx, eventUnion+`SELECT `+eventColumns+` FROM events`+where+` ORDER BY created_at DESC,id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Event{}
	for rows.Next() {
		var e Event
		var at string
		if err := rows.Scan(&e.ID, &e.ActorID, &e.Action, &e.TargetID, &e.DeviceID, &e.FileID, &e.VersionID, &e.TaskID, &e.NodeID, &e.Bytes, &e.Source, &at); err != nil {
			return nil, err
		}
		e.CreatedAt, err = time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return nil, err
		}
		e.CreatedAt = e.CreatedAt.UTC()
		result = append(result, e)
	}
	return result, rows.Err()
}
func (s *Service) Page(ctx context.Context, f Filter) (Page, error) {
	f, err := validateFilter(f)
	if err != nil {
		return Page{}, err
	}
	tx, err := s.adminSnapshot(ctx)
	if err != nil {
		return Page{}, err
	}
	defer tx.Rollback()
	where, args := filterSQL(f)
	var page Page
	if err := tx.QueryRowContext(ctx, eventUnion+`SELECT count(*) FROM events`+where, args...).Scan(&page.Total); err != nil {
		return Page{}, err
	}
	page.Events, err = readEvents(ctx, tx, f)
	return page, err
}
func (s *Service) List(ctx context.Context, actorID string, f Filter) ([]Event, error) {
	actor, ok := auth.UserFromContext(ctx)
	if !ok || actor.ID != actorID {
		return nil, auth.ErrAdminRequired
	}
	page, err := s.Page(ctx, f)
	return page.Events, err
}

// Export fixes a UTC cutoff and follows a stable timestamp/ID cursor. Every
// page releases its rows and transaction before network writes and rechecks
// administrator/session state before reading another page. Later aggregate
// changes are excluded by the cutoff; no database lock follows a slow client.
func (s *Service) Export(ctx context.Context, f Filter, format string, out io.Writer) error {
	if format != "csv" && format != "json" {
		return ErrInvalidInput
	}
	var err error
	f, err = validateFilter(f)
	if err != nil {
		return err
	}
	cutoff := time.Now().UTC()
	if f.Before.IsZero() || cutoff.Before(f.Before) {
		f.Before = cutoff
	}
	f.Limit = 200
	f.Offset = 0
	tx, err := s.adminSnapshot(ctx)
	if err != nil {
		return err
	}
	_ = tx.Rollback()
	var csvWriter *csv.Writer
	if format == "csv" {
		csvWriter = csv.NewWriter(out)
		if err := csvWriter.Write([]string{"id", "action", "actorId", "targetId", "deviceId", "fileId", "versionId", "taskId", "nodeId", "bytes", "source", "createdAt"}); err != nil {
			return err
		}
	} else {
		if _, err := io.WriteString(out, `{"events":[`); err != nil {
			return err
		}
	}
	count := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		tx, err := s.adminSnapshot(ctx)
		if err != nil {
			return err
		}
		events, err := readEvents(ctx, tx, f)
		_ = tx.Rollback()
		if err != nil {
			return err
		}
		if len(events) == 0 {
			break
		}
		for _, e := range events {
			if csvWriter != nil {
				values := []string{e.ID, e.Action, e.ActorID, e.TargetID, e.DeviceID, e.FileID, e.VersionID, e.TaskID, e.NodeID, strconv.FormatInt(e.Bytes, 10), e.Source, e.CreatedAt.Format(timestampLayout)}
				for i, value := range values {
					values[i] = spreadsheetSafe(value)
				}
				if err := csvWriter.Write(values); err != nil {
					return err
				}
			} else {
				if count > 0 {
					if _, err := io.WriteString(out, ","); err != nil {
						return err
					}
				}
				if err := json.NewEncoder(out).Encode(e); err != nil {
					return err
				}
			}
			count++
		}
		last := events[len(events)-1]
		f.cursorAt = last.CreatedAt.Format(timestampLayout)
		f.cursorID = last.ID
		if csvWriter != nil {
			csvWriter.Flush()
			if err := csvWriter.Error(); err != nil {
				return err
			}
		}
	}
	if csvWriter != nil {
		csvWriter.Flush()
		return csvWriter.Error()
	}
	_, err = fmt.Fprintf(out, "],\"total\":%d}\n", count)
	return err
}

func spreadsheetSafe(value string) string {
	trimmed := strings.TrimLeft(value, " \t\r\n")
	if len(trimmed) > 0 && strings.ContainsRune("=+-@", rune(trimmed[0])) {
		return "'" + value
	}
	if strings.HasPrefix(value, "\t") || strings.HasPrefix(value, "\r") {
		return "'" + value
	}
	return value
}

type TransferInput struct {
	TaskID    string `json:"taskId"`
	VersionID string `json:"versionId"`
	NodeID    string `json:"nodeId"`
	Action    string `json:"action"`
	Bytes     int64  `json:"bytes"`
}

func (s *Service) RecordClientTransfer(ctx context.Context, in TransferInput) error {
	if !validID(in.TaskID) || !validID(in.VersionID) || (in.NodeID != "server" && !validID(in.NodeID)) || in.Bytes < 0 || in.Bytes > 20000000000 {
		return ErrInvalidInput
	}
	if in.Action != "peer.sent" && in.Action != "peer.received" && in.Action != "transfer.completed" {
		return ErrInvalidInput
	}
	if in.NodeID == "server" && in.Action != "transfer.completed" {
		return ErrInvalidInput
	}
	actor, ok := auth.UserFromContext(ctx)
	if !ok {
		return auth.ErrUnauthorized
	}
	if err := s.auth.ValidateActor(ctx, actor.ID); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET id=id WHERE id=?`, auth.SessionIDFromContext(ctx)); err != nil {
		return err
	}
	var file string
	var size int64
	err = tx.QueryRowContext(ctx, `SELECT f.id,v.size FROM file_versions v JOIN files f ON f.id=v.file_id JOIN users u ON u.id=? JOIN sessions s ON s.user_id=u.id WHERE v.id=? AND s.id=? AND s.device_id=? AND s.revoked_at IS NULL AND u.disabled=0 AND u.must_change_password=0 AND julianday(s.expires_at)>julianday('now') AND f.deleted_at IS NULL AND v.status='published' AND (v.expires_at IS NULL OR julianday(v.expires_at)>julianday('now')) AND (f.visibility='pool' OR EXISTS(SELECT 1 FROM group_members gm WHERE gm.group_id=f.group_id AND gm.user_id=u.id) OR EXISTS(SELECT 1 FROM user_roles ur JOIN roles r ON r.id=ur.role_id WHERE ur.user_id=u.id AND r.name='admin'))`, actor.ID, in.VersionID, auth.SessionIDFromContext(ctx), auth.DeviceIDFromContext(ctx)).Scan(&file, &size)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrTransferForbidden
	}
	if err != nil {
		return err
	}
	if in.Bytes > size {
		return ErrInvalidInput
	}
	if in.NodeID != "server" {
		if in.NodeID == auth.DeviceIDFromContext(ctx) {
			return ErrInvalidInput
		}
		var peers int
		// This is client-reported history, not a request to authorize new bytes.
		// A durable device identity remains attributable after logout, disable,
		// revocation or membership removal. If that device was deleted, retain
		// attribution only when a recorded negotiation binds this device pair
		// and version. Neither branch establishes that the claimed bytes flowed.
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM devices WHERE id=?) OR EXISTS(
 SELECT 1 FROM peer_transfers WHERE version_id=? AND (
 (requester_device_id=? AND requester_user_id=? AND source_device_id=?) OR
 (source_device_id=? AND requester_device_id=?)))`, in.NodeID, in.VersionID,
			auth.DeviceIDFromContext(ctx), actor.ID, in.NodeID,
			auth.DeviceIDFromContext(ctx), in.NodeID).Scan(&peers)
		if err != nil {
			return err
		}
		if peers == 0 {
			return ErrTransferForbidden
		}
	}
	at := time.Now().UTC().Format(timestampLayout)
	_, err = tx.ExecContext(ctx, `INSERT INTO client_transfer_records(id,actor_id,device_id,task_id,version_id,file_id,node_id,action,bytes,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(actor_id,device_id,task_id,version_id,node_id,action) DO UPDATE SET bytes=max(bytes,excluded.bytes),updated_at=CASE WHEN excluded.bytes>bytes THEN excluded.updated_at ELSE updated_at END`, uuid.NewString(), actor.ID, auth.DeviceIDFromContext(ctx), in.TaskID, in.VersionID, file, in.NodeID, in.Action, in.Bytes, at, at)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// RecordServerDelivery records actual completed response writes, without
// claiming that the recipient verified or persisted a complete file.
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
	_, err := s.db.ExecContext(ctx, `INSERT INTO transfer_records(actor_id,version_id,task_id,node_id,action,bytes,created_at,updated_at,device_id) VALUES(?,?,?,'server','server.sent',?,?,?,?) ON CONFLICT(actor_id,device_id,version_id,task_id,node_id,action) DO UPDATE SET bytes=bytes+excluded.bytes,updated_at=excluded.updated_at,device_id=excluded.device_id`, actorID, versionID, taskID, n, at, at, auth.DeviceIDFromContext(ctx))
	return err
}
func (s *Service) DeleteBeforeTwoYears(ctx context.Context, now time.Time) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	cutoff := now.UTC().AddDate(-2, 0, 0).Format(timestampLayout)
	var count int64
	for _, item := range []struct{ table, column string }{{"audit_events", "created_at"}, {"transfer_records", "updated_at"}, {"client_transfer_records", "updated_at"}, {"peer_transfers", "started_at"}} {
		result, err := tx.ExecContext(ctx, "DELETE FROM "+item.table+" WHERE "+item.column+"<?", cutoff)
		if err != nil {
			return 0, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		count += n
	}
	return count, tx.Commit()
}
