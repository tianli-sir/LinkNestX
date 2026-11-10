package p2p

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/linknestx/linknestx/server/internal/auth"
)

// eligibility mirrors the file service's current read ACL (pool, group member,
// or administrator), and additionally requires the precise announced session.
const eligibility = ` FROM peer_sources p
 JOIN sessions s ON s.id=p.session_id AND s.user_id=p.user_id AND s.device_id=p.device_id
 JOIN users u ON u.id=p.user_id
 JOIN file_versions v ON v.id=p.version_id JOIN files f ON f.id=v.file_id
 WHERE u.disabled=0 AND u.must_change_password=0 AND s.revoked_at IS NULL AND julianday(s.expires_at)>julianday(?)
 AND p.last_seen>=? AND f.deleted_at IS NULL AND v.status='published'
 AND (v.expires_at IS NULL OR julianday(v.expires_at)>julianday(?))
 AND (f.visibility='pool' OR EXISTS(SELECT 1 FROM group_members gm WHERE gm.group_id=f.group_id AND gm.user_id=p.user_id)
 OR EXISTS(SELECT 1 FROM user_roles ur JOIN roles r ON r.id=ur.role_id WHERE ur.user_id=p.user_id AND r.name='admin'))`

func (s *Service) Announce(ctx context.Context, actor string, in AnnounceInput) error {
	if len(in.Indexes) > 5000 || !validID(in.VersionID) {
		return ErrInvalidInput
	}
	manifest, err := s.files.Manifest(ctx, actor, in.VersionID)
	if err != nil {
		return err
	}
	seen := map[int]bool{}
	indexes := append([]int{}, in.Indexes...)
	for _, index := range indexes {
		if index < 0 || index >= len(manifest.Chunks) || seen[index] {
			return ErrInvalidInput
		}
		seen[index] = true
	}
	if len(indexes) == 0 {
		return s.Withdraw(ctx, actor, in.VersionID)
	}
	sort.Ints(indexes)
	encoded, _ := json.Marshal(indexes)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM peer_sources WHERE device_id=? AND version_id<>?`, auth.DeviceIDFromContext(ctx), in.VersionID).Scan(&count); err != nil {
		return err
	}
	if count >= 128 {
		return ErrLimit
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO peer_sources(device_id,version_id,user_id,session_id,indexes_json,last_seen) VALUES(?,?,?,?,?,?)
 ON CONFLICT(device_id,version_id) DO UPDATE SET session_id=excluded.session_id,indexes_json=excluded.indexes_json,last_seen=excluded.last_seen`, auth.DeviceIDFromContext(ctx), in.VersionID, actor, auth.SessionIDFromContext(ctx), string(encoded), stamp(s.now()))
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Service) Heartbeat(ctx context.Context, actor, version string) error {
	if err := s.ValidateActor(ctx, actor); err != nil {
		return err
	}
	if version != "" && !validID(version) {
		return ErrInvalidInput
	}
	rows, err := s.db.QueryContext(ctx, `SELECT version_id FROM peer_sources WHERE device_id=? AND user_id=? AND (?='' OR version_id=?)`, auth.DeviceIDFromContext(ctx), actor, version, version)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = s.AuthorizeVersion(ctx, actor, id); err != nil {
			_ = s.Withdraw(ctx, actor, id)
			if version != "" {
				return err
			}
		}
	}
	_, err = s.db.ExecContext(ctx, `UPDATE peer_sources SET last_seen=?,session_id=? WHERE device_id=? AND user_id=? AND (?='' OR version_id=?)`, stamp(s.now()), auth.SessionIDFromContext(ctx), auth.DeviceIDFromContext(ctx), actor, version, version)
	return err
}
func (s *Service) Withdraw(ctx context.Context, actor, version string) error {
	if err := s.ValidateActor(ctx, actor); err != nil {
		return err
	}
	if version != "" && !validID(version) {
		return ErrInvalidInput
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM peer_sources WHERE device_id=? AND user_id=? AND (?='' OR version_id=?)`, auth.DeviceIDFromContext(ctx), actor, version, version)
	return err
}
func (s *Service) ListSources(ctx context.Context, actor, version string) (SourceResult, error) {
	result := SourceResult{Sources: []PeerSource{}, ICEServers: s.iceServers(auth.DeviceIDFromContext(ctx))}
	if err := s.AuthorizeVersion(ctx, actor, version); err != nil {
		return result, err
	}
	now := s.now()
	rows, err := s.db.QueryContext(ctx, `SELECT p.device_id,p.user_id,p.indexes_json,p.last_seen`+eligibility+` AND p.version_id=? AND p.device_id<>? ORDER BY p.last_seen DESC,p.device_id LIMIT 64`, stamp(now), stamp(now.Add(-SourceLease)), stamp(now), version, auth.DeviceIDFromContext(ctx))
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var source PeerSource
		var indexes, at string
		if err = rows.Scan(&source.DeviceID, &source.UserID, &indexes, &at); err != nil {
			return result, err
		}
		if err = json.Unmarshal([]byte(indexes), &source.Indexes); err != nil {
			return result, err
		}
		if source.LastSeen, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return result, err
		}
		result.Sources = append(result.Sources, source)
	}
	return result, rows.Err()
}
func (s *Service) IsSource(ctx context.Context, device, session, version string) (bool, error) {
	now := s.now()
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT count(*)`+eligibility+` AND p.device_id=? AND p.session_id=? AND p.version_id=?`, stamp(now), stamp(now.Add(-SourceLease)), stamp(now), device, session, version).Scan(&count)
	return count > 0, err
}

type ConnectionAudit struct {
	ConnectionID, VersionID, RequesterDeviceID, SourceDeviceID, RequesterUserID, Outcome string
	Ended                                                                                bool
}

func (s *Service) RecordConnection(ctx context.Context, a ConnectionAudit) error {
	if a.Ended {
		_, err := s.db.ExecContext(ctx, `UPDATE peer_transfers SET ended_at=?,outcome=? WHERE connection_id=? AND version_id=? AND requester_device_id=? AND source_device_id=? AND requester_user_id=? AND ended_at IS NULL`, stamp(s.now()), a.Outcome, a.ConnectionID, a.VersionID, a.RequesterDeviceID, a.SourceDeviceID, a.RequesterUserID)
		return err
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO peer_transfers(connection_id,version_id,requester_device_id,source_device_id,requester_user_id,started_at) VALUES(?,?,?,?,?,?) ON CONFLICT(connection_id) DO NOTHING`, a.ConnectionID, a.VersionID, a.RequesterDeviceID, a.SourceDeviceID, a.RequesterUserID, stamp(s.now()))
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrConnectionExists
	}
	return nil
}
func (s *Service) Cleanup(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM peer_sources WHERE last_seen<? OR NOT EXISTS(SELECT 1 FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.id=peer_sources.session_id AND s.revoked_at IS NULL AND u.disabled=0 AND u.must_change_password=0 AND julianday(s.expires_at)>julianday(?)) OR EXISTS(SELECT 1 FROM file_versions v JOIN files f ON f.id=v.file_id WHERE v.id=peer_sources.version_id AND (f.deleted_at IS NOT NULL OR v.status<>'published' OR (v.expires_at IS NOT NULL AND julianday(v.expires_at)<=julianday(?))))`, stamp(s.now().Add(-SourceLease)), stamp(s.now()), stamp(s.now()))
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM peer_transfers WHERE started_at<?`, stamp(s.now().AddDate(-2, 0, 0)))
	return err
}
