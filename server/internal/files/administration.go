package files

import (
	"context"
	"database/sql"
	"errors"
	"github.com/linknestx/linknestx/server/internal/auth"
	"strings"
	"unicode"
	"unicode/utf8"
)

var ErrSharingDisabled = errors.New("peer sharing disabled by administrator")

type PoolEdit struct {
	Name   *string `json:"name,omitempty"`
	Listed *bool   `json:"listed,omitempty"`
}
type SharingPolicy struct {
	Enabled           bool  `json:"enabled"`
	MaxConcurrent     int   `json:"maxConcurrent"`
	BandwidthLimitBps int64 `json:"bandwidthLimitBps"`
}

func loadSharingPolicy(ctx context.Context, q queryer, f fileState) (SharingPolicy, error) {
	scope, id := "group", f.groupID
	if f.visibility == "pool" {
		scope, id = "pool", f.id
	}
	p := SharingPolicy{Enabled: true}
	err := q.QueryRowContext(ctx, `SELECT enabled,max_concurrent,bandwidth_limit_bps FROM sharing_policies WHERE scope=? AND target_id=?`, scope, id).Scan(&p.Enabled, &p.MaxConcurrent, &p.BandwidthLimitBps)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return p, err
}
func (s *FileService) RequirePeerSharing(ctx context.Context, actor, version string) error {
	m, e := s.Manifest(ctx, actor, version)
	if e != nil {
		return e
	}
	if !m.SharingPolicy.Enabled {
		return ErrSharingDisabled
	}
	return nil
}
func (s *FileService) EditPool(ctx context.Context, actor, id string, in PoolEdit) error {
	if in.Name == nil && in.Listed == nil {
		return ErrInvalidInput
	}
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" || !utf8.ValidString(n) || utf8.RuneCountInString(n) > 255 || strings.ContainsAny(n, "/\\") {
			return ErrInvalidInput
		}
		for _, r := range n {
			if unicode.IsControl(r) {
				return ErrInvalidInput
			}
		}
		in.Name = &n
	}
	if e := s.auth.ValidateActor(ctx, actor); e != nil {
		return e
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = activeActor(ctx, tx, actor); e != nil {
		return e
	}
	a, e := admin(ctx, tx, actor)
	if e != nil {
		return e
	}
	if !a {
		return auth.ErrAdminRequired
	}
	f, e := loadFile(ctx, tx, id)
	if e != nil {
		return e
	}
	if f.deleted {
		return ErrUnavailable
	}
	if f.visibility != "pool" {
		return ErrInvalidInput
	}
	if in.Name != nil {
		if _, e = tx.ExecContext(ctx, `UPDATE files SET name=? WHERE id=?`, *in.Name, id); e != nil {
			return e
		}
		if e = recordAudit(ctx, tx, actor, "file.metadata_updated", id, f.current, s.now()); e != nil {
			return e
		}
	}
	if in.Listed != nil {
		if _, e = tx.ExecContext(ctx, `UPDATE files SET listed=? WHERE id=?`, *in.Listed, id); e != nil {
			return e
		}
		action := "file.unlisted"
		if *in.Listed {
			action = "file.relisted"
		}
		if e = recordAudit(ctx, tx, actor, action, id, f.current, s.now()); e != nil {
			return e
		}
	}
	return s.commitChange(tx)
}
func (s *FileService) policyTarget(ctx context.Context, tx *sql.Tx, scope, id string) error {
	var n int
	switch scope {
	case "group":
		if e := tx.QueryRowContext(ctx, `SELECT count(*) FROM groups WHERE id=? AND NOT EXISTS(SELECT 1 FROM direct_conversations WHERE group_id=groups.id)`, id).Scan(&n); e != nil {
			return e
		}
	case "pool":
		if e := tx.QueryRowContext(ctx, `SELECT count(*) FROM files WHERE id=? AND visibility='pool' AND deleted_at IS NULL`, id).Scan(&n); e != nil {
			return e
		}
	default:
		return ErrInvalidInput
	}
	if n == 0 {
		return ErrUnavailable
	}
	return nil
}
func (s *FileService) SetSharingPolicy(ctx context.Context, actor, scope, id string, p SharingPolicy) error {
	if p.MaxConcurrent < 0 || p.MaxConcurrent > 32 || p.BandwidthLimitBps < 0 {
		return ErrInvalidInput
	}
	if e := s.auth.ValidateActor(ctx, actor); e != nil {
		return e
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = activeActor(ctx, tx, actor); e != nil {
		return e
	}
	a, e := admin(ctx, tx, actor)
	if e != nil {
		return e
	}
	if !a {
		return auth.ErrAdminRequired
	}
	if e = s.policyTarget(ctx, tx, scope, id); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO sharing_policies(scope,target_id,enabled,max_concurrent,bandwidth_limit_bps) VALUES(?,?,?,?,?) ON CONFLICT(scope,target_id) DO UPDATE SET enabled=excluded.enabled,max_concurrent=excluded.max_concurrent,bandwidth_limit_bps=excluded.bandwidth_limit_bps`, scope, id, p.Enabled, p.MaxConcurrent, p.BandwidthLimitBps); e != nil {
		return e
	}
	if e = auth.RecordAudit(ctx, tx, actor, "sharing.policy_updated", scope+"/"+id, s.now()); e != nil {
		return e
	}
	return s.commitChange(tx)
}
func (s *FileService) GetSharingPolicy(ctx context.Context, actor, scope, id string) (SharingPolicy, error) {
	p := SharingPolicy{Enabled: true}
	if e := s.auth.ValidateActor(ctx, actor); e != nil {
		return p, e
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return p, e
	}
	defer tx.Rollback()
	if e = activeActor(ctx, tx, actor); e != nil {
		return p, e
	}
	a, e := admin(ctx, tx, actor)
	if e != nil {
		return p, e
	}
	if !a {
		return p, auth.ErrAdminRequired
	}
	if e = s.policyTarget(ctx, tx, scope, id); e != nil {
		return p, e
	}
	e = tx.QueryRowContext(ctx, `SELECT enabled,max_concurrent,bandwidth_limit_bps FROM sharing_policies WHERE scope=? AND target_id=?`, scope, id).Scan(&p.Enabled, &p.MaxConcurrent, &p.BandwidthLimitBps)
	if errors.Is(e, sql.ErrNoRows) {
		e = nil
	}
	return p, e
}
