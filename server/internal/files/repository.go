package files

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"
	"github.com/linknestx/linknestx/server/internal/auth"
)

func recordAudit(ctx context.Context, tx *sql.Tx, actor, action, file, version string, at time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,file_id,version_id,created_at) VALUES(?,?,?,?,?,?)`, uuid.NewString(), actor, action, file, version, stamp(at))
	return err
}
func (s *FileService) List(ctx context.Context, actorID string, in ListInput) ([]File, error) {
	if err := s.auth.ValidateActor(ctx, actorID); err != nil {
		return nil, err
	}
	if (in.Scope != "pool" && in.GroupID == "") || (in.Scope != "" && in.Scope != "pool") || (in.Scope == "pool" && in.GroupID != "") {
		return nil, ErrInvalidInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = activeActor(ctx, tx, actorID); err != nil {
		return nil, err
	}
	if in.Scope != "pool" {
		if err = fileGroupAccess(ctx, tx, actorID, in.GroupID); err != nil {
			return nil, err
		}
	}
	where := "f.group_id=?"
	arg := in.GroupID
	if in.Scope == "pool" {
		where = "f.visibility=?"
		arg = "pool"
	}
	rows, err := tx.QueryContext(ctx, `SELECT f.id,f.group_id,f.name,f.created_by,f.visibility,f.current_version_id,f.created_at,v.expires_at,v.status,v.size,v.sha256 FROM files f JOIN file_versions v ON v.id=f.current_version_id WHERE `+where+` AND f.deleted_at IS NULL AND v.status='published' AND (v.expires_at IS NULL OR v.expires_at>?) ORDER BY f.created_at,f.id`, arg, stamp(s.now()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []File{}
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, f)
	}
	return items, rows.Err()
}

type scanner interface{ Scan(...any) error }

func scanFile(row scanner) (File, error) {
	var f File
	var at string
	var expires sql.NullString
	err := row.Scan(&f.ID, &f.GroupID, &f.Name, &f.CreatedBy, &f.Visibility, &f.CurrentVersionID, &at, &expires, &f.Status, &f.Size, &f.SHA256)
	if err != nil {
		return f, err
	}
	f.CreatedAt, err = time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return f, err
	}
	if expires.Valid {
		e, err := time.Parse(time.RFC3339Nano, expires.String)
		if err != nil {
			return f, err
		}
		f.ExpiresAt = &e
	}
	return f, nil
}
func (s *FileService) Manifest(ctx context.Context, actorID, versionID string) (FileVersion, error) {
	if err := s.auth.ValidateActor(ctx, actorID); err != nil {
		return FileVersion{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return FileVersion{}, err
	}
	defer tx.Rollback()
	if err = activeActor(ctx, tx, actorID); err != nil {
		return FileVersion{}, err
	}
	m, err := s.authorizedVersion(ctx, tx, actorID, versionID)
	if err != nil {
		return m, err
	}
	m.Chunks, err = loadChunks(ctx, tx, versionID)
	return m, err
}
func loadChunks(ctx context.Context, q queryer, versionID string) ([]Chunk, error) {
	rows, err := q.QueryContext(ctx, `SELECT chunk_index,hash,size FROM file_chunks WHERE version_id=? ORDER BY chunk_index`, versionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	chunks := []Chunk{}
	for rows.Next() {
		var c Chunk
		if err = rows.Scan(&c.Index, &c.Hash, &c.Size); err != nil {
			return nil, err
		}
		chunks = append(chunks, c)
	}
	return chunks, rows.Err()
}
func (s *FileService) Versions(ctx context.Context, actorID, fileID string) ([]FileVersion, error) {
	if err := s.auth.ValidateActor(ctx, actorID); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = activeActor(ctx, tx, actorID); err != nil {
		return nil, err
	}
	f, err := loadFile(ctx, tx, fileID)
	if err != nil {
		return nil, err
	}
	if err = access(ctx, tx, actorID, f); err != nil {
		return nil, err
	}
	if f.deleted {
		return nil, ErrUnavailable
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM file_versions WHERE file_id=? AND status='published' AND (expires_at IS NULL OR expires_at>?) ORDER BY created_at,id`, fileID, stamp(s.now()))
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	items := []FileVersion{}
	for _, id := range ids {
		m, err := s.authorizedVersion(ctx, tx, actorID, id)
		if err != nil {
			return nil, err
		}
		m.Chunks = []Chunk{}
		items = append(items, m)
	}
	return items, nil
}
func (s *FileService) PromoteToPool(ctx context.Context, actorID, fileID string) (File, error) {
	if err := s.auth.ValidateActor(ctx, actorID); err != nil {
		return File{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return File{}, err
	}
	defer tx.Rollback()
	if err = activeActor(ctx, tx, actorID); err != nil {
		return File{}, err
	}
	a, err := admin(ctx, tx, actorID)
	if err != nil {
		return File{}, err
	}
	if !a {
		return File{}, auth.ErrAdminRequired
	}
	f, err := loadFile(ctx, tx, fileID)
	if err != nil {
		return File{}, err
	}
	if _, err = s.authorizedVersion(ctx, tx, actorID, f.current); err != nil {
		return File{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE files SET visibility='pool' WHERE id=?`, fileID); err != nil {
		return File{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE file_versions SET expires_at=NULL WHERE file_id=? AND status='published' AND (expires_at IS NULL OR expires_at>?)`, fileID, stamp(s.now())); err != nil {
		return File{}, err
	}
	if err = recordAudit(ctx, tx, actorID, "file.promoted", fileID, f.current, s.now()); err != nil {
		return File{}, err
	}
	result, err := scanFile(tx.QueryRowContext(ctx, `SELECT f.id,f.group_id,f.name,f.created_by,f.visibility,f.current_version_id,f.created_at,v.expires_at,v.status,v.size,v.sha256 FROM files f JOIN file_versions v ON v.id=f.current_version_id WHERE f.id=?`, fileID))
	if err != nil {
		return File{}, err
	}
	return result, tx.Commit()
}
func (s *FileService) Delete(ctx context.Context, actorID, fileID string) error {
	if err := s.auth.ValidateActor(ctx, actorID); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = activeActor(ctx, tx, actorID); err != nil {
		return err
	}
	f, err := loadFile(ctx, tx, fileID)
	if err != nil {
		return err
	}
	if err = manage(ctx, tx, actorID, f); err != nil {
		return err
	}
	if f.deleted {
		return nil
	}
	at := s.now()
	if _, err = tx.ExecContext(ctx, `UPDATE files SET deleted_at=? WHERE id=?`, stamp(at), fileID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE file_versions SET status='deleted',deleted_at=? WHERE file_id=? AND status IN ('pending','published')`, stamp(at), fileID); err != nil {
		return err
	}
	if err = recordAudit(ctx, tx, actorID, "file.deleted", fileID, f.current, at); err != nil {
		return err
	}
	return tx.Commit()
}
