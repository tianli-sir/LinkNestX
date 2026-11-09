package files

import (
	"context"
	"time"
)

type DeletionReport struct {
	VersionsExpired int64 `json:"versionsExpired"`
	UploadsExpired  int64 `json:"uploadsExpired"`
	ChunksDeleted   int64 `json:"chunksDeleted"`
	MetadataDeleted int64 `json:"metadataDeleted"`
}

// DeleteExpired serializes physical deletion against every CAS writer/opener.
// Each final reference check and unlink is also enclosed in a database
// transaction, so a concurrent new manifest cannot claim a hash mid-deletion.
func (s *FileService) DeleteExpired(ctx context.Context, now time.Time) (DeletionReport, error) {
	s.storageMu.Lock()
	defer s.storageMu.Unlock()
	report := DeletionReport{}
	now = now.UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return report, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id,file_id FROM file_versions WHERE status='published' AND expires_at IS NOT NULL AND expires_at<=?`, stamp(now))
	if err != nil {
		return report, err
	}
	type expired struct{ id, file string }
	versions := []expired{}
	for rows.Next() {
		var v expired
		if err = rows.Scan(&v.id, &v.file); err != nil {
			rows.Close()
			return report, err
		}
		versions = append(versions, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return report, err
	}
	for _, v := range versions {
		if err = recordAudit(ctx, tx, "", "file.expired", v.file, v.id, now); err != nil {
			return report, err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE file_versions SET status='expired',deleted_at=? WHERE status='published' AND expires_at IS NOT NULL AND expires_at<=?`, stamp(now), stamp(now))
	if err != nil {
		return report, err
	}
	report.VersionsExpired, _ = result.RowsAffected()
	result, err = tx.ExecContext(ctx, `UPDATE file_versions SET status='expired',deleted_at=? WHERE status='pending' AND id IN (SELECT version_id FROM upload_sessions WHERE completed_at IS NULL AND expires_at<=?)`, stamp(now), stamp(now))
	if err != nil {
		return report, err
	}
	report.UploadsExpired, _ = result.RowsAffected()
	if _, err = tx.ExecContext(ctx, `UPDATE files SET deleted_at=? WHERE deleted_at IS NULL AND NOT EXISTS(SELECT 1 FROM file_versions v WHERE v.file_id=files.id AND v.status IN ('pending','published'))`, stamp(now)); err != nil {
		return report, err
	}
	// Retain tombstones for two calendar years after lifecycle removal. Chat
	// attachment references are plain IDs and continue to display expired status.
	result, err = tx.ExecContext(ctx, `DELETE FROM file_versions WHERE deleted_at<? AND status IN ('deleted','expired')`, stamp(now.AddDate(-2, 0, 0)))
	if err != nil {
		return report, err
	}
	report.MetadataDeleted, _ = result.RowsAffected()
	if _, err = tx.ExecContext(ctx, `DELETE FROM files WHERE deleted_at<? AND NOT EXISTS(SELECT 1 FROM file_versions v WHERE v.file_id=files.id)`, stamp(now.AddDate(-2, 0, 0))); err != nil {
		return report, err
	}
	if err = tx.Commit(); err != nil {
		return report, err
	}
	rows, err = s.db.QueryContext(ctx, `SELECT hash FROM chunk_objects ORDER BY hash`)
	if err != nil {
		return report, err
	}
	hashes := []string{}
	for rows.Next() {
		var h string
		if err = rows.Scan(&h); err != nil {
			rows.Close()
			return report, err
		}
		hashes = append(hashes, h)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return report, err
	}
	for _, hash := range hashes {
		deleted, e := s.deleteUnreferenced(ctx, hash, now)
		if e != nil {
			return report, e
		}
		if deleted {
			report.ChunksDeleted++
		}
	}
	return report, nil
}
func (s *FileService) deleteUnreferenced(ctx context.Context, hash string, now time.Time) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var n int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM file_chunks c JOIN file_versions v ON v.id=c.version_id JOIN files f ON f.id=v.file_id LEFT JOIN upload_sessions u ON u.version_id=v.id WHERE c.hash=? AND f.deleted_at IS NULL AND ((v.status='published' AND (v.expires_at IS NULL OR v.expires_at>?)) OR (v.status='pending' AND u.completed_at IS NULL AND u.expires_at>?))`, hash, stamp(now), stamp(now)).Scan(&n)
	if err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil
	}
	if err = s.store.Delete(ctx, hash); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM chunk_objects WHERE hash=?`, hash); err != nil {
		return false, err
	}
	return true, tx.Commit()
}
